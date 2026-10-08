package memory

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/claudioed/slotting-optimization/internal/application/ports"
	"github.com/claudioed/slotting-optimization/internal/application/repository"
	"github.com/claudioed/slotting-optimization/internal/domain/planning"
	"github.com/claudioed/slotting-optimization/internal/domain/slotplan"
)

// DemandLedger is the in-memory demand copy (ports.DemandLedger).
type DemandLedger struct {
	mu    sync.Mutex
	lines map[lineKey]repository.DemandLine
}

type lineKey struct {
	order string
	line  int
}

var _ ports.DemandLedger = (*DemandLedger)(nil)

// NewDemandLedger constructs an empty DemandLedger.
func NewDemandLedger() *DemandLedger {
	return &DemandLedger{lines: make(map[lineKey]repository.DemandLine)}
}

// Apply implements ports.DemandLedger: last writer wins per (order, line).
func (d *DemandLedger) Apply(_ context.Context, l repository.DemandLine) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.lines[lineKey{l.SourceOrderID, l.LineNo}] = l
	return nil
}

// Velocity implements ports.DemandLedger.
func (d *DemandLedger) Velocity(_ context.Context, site slotplan.SiteID, from, to time.Time) ([]planning.SkuVelocity, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	bySKU := map[slotplan.SKU]*planning.SkuVelocity{}
	for _, l := range d.lines {
		if !l.Active || l.Site != site || l.DueAt.Before(from) || !l.DueAt.Before(to) {
			continue
		}
		v := bySKU[l.SKU]
		if v == nil {
			v = &planning.SkuVelocity{SKU: l.SKU}
			bySKU[l.SKU] = v
		}
		v.Picks++
		v.Units += l.Units
	}
	out := make([]planning.SkuVelocity, 0, len(bySKU))
	for _, v := range bySKU {
		out = append(out, *v)
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Picks != b.Picks {
			return a.Picks > b.Picks
		}
		if a.Units != b.Units {
			return a.Units > b.Units
		}
		return a.SKU < b.SKU
	})
	return out, nil
}

// Snapshot implements Snapshotter.
func (d *DemandLedger) Snapshot() func() {
	d.mu.Lock()
	defer d.mu.Unlock()
	saved := make(map[lineKey]repository.DemandLine, len(d.lines))
	for k, v := range d.lines {
		saved[k] = v
	}
	return func() {
		d.mu.Lock()
		defer d.mu.Unlock()
		d.lines = saved
	}
}

// ProfileDirectory is the in-memory product copy (ports.ProfileDirectory).
type ProfileDirectory struct {
	mu   sync.Mutex
	rows map[slotplan.SKU]repository.ProductProfile
}

var _ ports.ProfileDirectory = (*ProfileDirectory)(nil)

// NewProfileDirectory constructs an empty ProfileDirectory.
func NewProfileDirectory() *ProfileDirectory {
	return &ProfileDirectory{rows: make(map[slotplan.SKU]repository.ProductProfile)}
}

// Get implements ports.ProfileDirectory.
func (p *ProfileDirectory) Get(_ context.Context, sku slotplan.SKU) (repository.ProductProfile, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	row, ok := p.rows[sku]
	if !ok {
		return repository.ProductProfile{}, repository.ErrProfileNotFound
	}
	return copyProfile(row), nil
}

// Many implements ports.ProfileDirectory.
func (p *ProfileDirectory) Many(_ context.Context, skus []slotplan.SKU) (map[slotplan.SKU]planning.Profile, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make(map[slotplan.SKU]planning.Profile, len(skus))
	for _, sku := range skus {
		row, ok := p.rows[sku]
		if !ok {
			continue
		}
		row = copyProfile(row)
		out[sku] = planning.Profile{HandlingTags: row.HandlingTags, TemperatureClass: row.TemperatureClass, Unit: row.Unit}
	}
	return out, nil
}

// Save implements ports.ProfileDirectory: an older or equal version never
// replaces the stored one.
func (p *ProfileDirectory) Save(_ context.Context, row repository.ProductProfile) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if cur, ok := p.rows[row.SKU]; ok && cur.Version >= row.Version {
		return nil
	}
	p.rows[row.SKU] = copyProfile(row)
	return nil
}

func copyProfile(r repository.ProductProfile) repository.ProductProfile {
	r.HandlingTags = append([]string(nil), r.HandlingTags...)
	if r.Unit != nil {
		u := *r.Unit
		r.Unit = &u
	}
	return r
}

// Snapshot implements Snapshotter.
func (p *ProfileDirectory) Snapshot() func() {
	p.mu.Lock()
	defer p.mu.Unlock()
	saved := make(map[slotplan.SKU]repository.ProductProfile, len(p.rows))
	for k, v := range p.rows {
		saved[k] = copyProfile(v)
	}
	return func() {
		p.mu.Lock()
		defer p.mu.Unlock()
		p.rows = saved
	}
}

// SlotCatalogue is the in-memory layout copy (ports.SlotCatalogue).
type SlotCatalogue struct {
	mu    sync.Mutex
	zones map[string]repository.Zone
	slots map[slotplan.SlotCode]slotRow
}

type slotRow struct {
	repository.Slot
	decommissioned bool
}

var _ ports.SlotCatalogue = (*SlotCatalogue)(nil)

// NewSlotCatalogue constructs an empty SlotCatalogue.
func NewSlotCatalogue() *SlotCatalogue {
	return &SlotCatalogue{zones: make(map[string]repository.Zone), slots: make(map[slotplan.SlotCode]slotRow)}
}

// ForwardSlots implements ports.SlotCatalogue.
func (c *SlotCatalogue) ForwardSlots(_ context.Context, site slotplan.SiteID, codes []string) ([]planning.Slot, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []planning.Slot
	for _, s := range c.slots {
		if s.decommissioned || s.Role != repository.RoleStorage {
			continue
		}
		z, ok := c.zones[s.ZoneID]
		if !ok || z.SiteCode != string(site) || !contains(codes, z.ZoneCode) {
			continue
		}
		out = append(out, planning.Slot{
			Code: s.Code, ZoneCode: z.ZoneCode, TemperatureClass: z.TemperatureClass, Hazmat: z.Hazmat,
			MaxWeightKg: s.MaxWeightKg, MaxVolumeM3: s.MaxVolumeM3,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Code < out[j].Code })
	return out, nil
}

func contains(codes []string, code string) bool {
	for _, c := range codes {
		if c == code {
			return true
		}
	}
	return false
}

// SaveZone implements ports.SlotCatalogue.
func (c *SlotCatalogue) SaveZone(_ context.Context, z repository.Zone) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.zones[z.ID] = z
	return nil
}

// SaveSlot implements ports.SlotCatalogue: a decommissioned slot stays so.
func (c *SlotCatalogue) SaveSlot(_ context.Context, s repository.Slot) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if cur, ok := c.slots[s.Code]; ok && cur.decommissioned {
		return nil
	}
	c.slots[s.Code] = slotRow{Slot: s}
	return nil
}

// Decommission implements ports.SlotCatalogue.
func (c *SlotCatalogue) Decommission(_ context.Context, code slotplan.SlotCode) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	row := c.slots[code]
	row.Code = code
	row.decommissioned = true
	c.slots[code] = row
	return nil
}

// Snapshot implements Snapshotter.
func (c *SlotCatalogue) Snapshot() func() {
	c.mu.Lock()
	defer c.mu.Unlock()
	zones := make(map[string]repository.Zone, len(c.zones))
	for k, v := range c.zones {
		zones[k] = v
	}
	slots := make(map[slotplan.SlotCode]slotRow, len(c.slots))
	for k, v := range c.slots {
		slots[k] = v
	}
	return func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		c.zones, c.slots = zones, slots
	}
}
