package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/claudioed/slotting-optimization/internal/application/ports"
	"github.com/claudioed/slotting-optimization/internal/application/repository"
	"github.com/claudioed/slotting-optimization/internal/domain/planning"
	"github.com/claudioed/slotting-optimization/internal/domain/slotplan"
)

// Demand line states as stored.
const (
	stateActive  = "ACTIVE"
	stateRemoved = "REMOVED"
)

// DemandLedger is the pgxpool-backed demand copy (ports.DemandLedger).
type DemandLedger struct {
	pool *pgxpool.Pool
}

var _ ports.DemandLedger = (*DemandLedger)(nil)

// NewDemandLedger constructs a DemandLedger over pool.
func NewDemandLedger(pool *pgxpool.Pool) *DemandLedger { return &DemandLedger{pool: pool} }

// Apply implements ports.DemandLedger: an upsert, last writer wins.
func (d *DemandLedger) Apply(ctx context.Context, l repository.DemandLine) error {
	state := stateRemoved
	if l.Active {
		state = stateActive
	}
	_, err := queryFor(ctx, d.pool).Exec(ctx, `
		INSERT INTO demand_lines (source_order_id, line_no, site_id, sku, units, due_at, state)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (source_order_id, line_no) DO UPDATE
		SET site_id = EXCLUDED.site_id, sku = EXCLUDED.sku, units = EXCLUDED.units,
		    due_at = EXCLUDED.due_at, state = EXCLUDED.state, updated_at = now()
	`, l.SourceOrderID, l.LineNo, string(l.Site), string(l.SKU), l.Units, l.DueAt, state)
	if err != nil {
		return fmt.Errorf("upsert demand line: %w", err)
	}
	return nil
}

// Velocity implements ports.DemandLedger.
func (d *DemandLedger) Velocity(ctx context.Context, site slotplan.SiteID, from, to time.Time) ([]planning.SkuVelocity, error) {
	rows, err := queryFor(ctx, d.pool).Query(ctx, `
		SELECT sku, COUNT(*)::bigint, COALESCE(SUM(units), 0)::bigint
		FROM demand_lines
		WHERE site_id = $1 AND state = 'ACTIVE' AND due_at >= $2 AND due_at < $3
		GROUP BY sku
		ORDER BY COUNT(*) DESC, SUM(units) DESC, sku ASC
	`, string(site), from, to)
	if err != nil {
		return nil, fmt.Errorf("sku velocity: %w", err)
	}
	defer rows.Close()
	var out []planning.SkuVelocity
	for rows.Next() {
		var sku string
		var v planning.SkuVelocity
		if err := rows.Scan(&sku, &v.Picks, &v.Units); err != nil {
			return nil, fmt.Errorf("scan sku velocity: %w", err)
		}
		v.SKU = slotplan.SKU(sku)
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read sku velocity: %w", err)
	}
	return out, nil
}

// ProfileDirectory is the pgxpool-backed product copy (ports.ProfileDirectory).
type ProfileDirectory struct {
	pool *pgxpool.Pool
}

var _ ports.ProfileDirectory = (*ProfileDirectory)(nil)

// NewProfileDirectory constructs a ProfileDirectory over pool.
func NewProfileDirectory(pool *pgxpool.Pool) *ProfileDirectory { return &ProfileDirectory{pool: pool} }

const profileColumns = `sku, handling_tags, temperature_class, volume_mm3, weight_g, version`

// Get implements ports.ProfileDirectory.
func (p *ProfileDirectory) Get(ctx context.Context, sku slotplan.SKU) (repository.ProductProfile, error) {
	profiles, err := p.query(ctx, `SELECT `+profileColumns+` FROM product_profiles WHERE sku = $1`, string(sku))
	if err != nil {
		return repository.ProductProfile{}, err
	}
	if len(profiles) == 0 {
		return repository.ProductProfile{}, repository.ErrProfileNotFound
	}
	return profiles[0], nil
}

// Many implements ports.ProfileDirectory.
func (p *ProfileDirectory) Many(ctx context.Context, skus []slotplan.SKU) (map[slotplan.SKU]planning.Profile, error) {
	keys := make([]string, len(skus))
	for i, s := range skus {
		keys[i] = string(s)
	}
	profiles, err := p.query(ctx, `SELECT `+profileColumns+` FROM product_profiles WHERE sku = ANY($1)`, keys)
	if err != nil {
		return nil, err
	}
	out := make(map[slotplan.SKU]planning.Profile, len(profiles))
	for _, r := range profiles {
		out[r.SKU] = planning.Profile{HandlingTags: r.HandlingTags, TemperatureClass: r.TemperatureClass, Unit: r.Unit}
	}
	return out, nil
}

func (p *ProfileDirectory) query(ctx context.Context, sql string, args ...any) ([]repository.ProductProfile, error) {
	rows, err := queryFor(ctx, p.pool).Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("read product profiles: %w", err)
	}
	defer rows.Close()
	var out []repository.ProductProfile
	for rows.Next() {
		var (
			sku, temp      string
			tags           []string
			volume, weight *int64
			r              repository.ProductProfile
		)
		if err := rows.Scan(&sku, &tags, &temp, &volume, &weight, &r.Version); err != nil {
			return nil, fmt.Errorf("scan product profile: %w", err)
		}
		r.SKU, r.HandlingTags, r.TemperatureClass = slotplan.SKU(sku), tags, planning.TemperatureClass(temp)
		if volume != nil && weight != nil {
			r.Unit = &planning.UnitSize{VolumeMM3: *volume, WeightG: *weight}
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read product profiles: %w", err)
	}
	return out, nil
}

// Save implements ports.ProfileDirectory: an equal or older version never
// replaces the stored row (the WHERE of the upsert).
func (p *ProfileDirectory) Save(ctx context.Context, r repository.ProductProfile) error {
	var volume, weight *int64
	if r.Unit != nil {
		volume, weight = &r.Unit.VolumeMM3, &r.Unit.WeightG
	}
	tags := r.HandlingTags
	if tags == nil {
		tags = []string{}
	}
	_, err := queryFor(ctx, p.pool).Exec(ctx, `
		INSERT INTO product_profiles (sku, handling_tags, temperature_class, volume_mm3, weight_g, version)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (sku) DO UPDATE
		SET handling_tags = EXCLUDED.handling_tags, temperature_class = EXCLUDED.temperature_class,
		    volume_mm3 = EXCLUDED.volume_mm3, weight_g = EXCLUDED.weight_g,
		    version = EXCLUDED.version, updated_at = now()
		WHERE product_profiles.version < EXCLUDED.version
	`, string(r.SKU), tags, string(r.TemperatureClass), volume, weight, r.Version)
	if err != nil {
		return fmt.Errorf("upsert product profile: %w", err)
	}
	return nil
}

// SlotCatalogue is the pgxpool-backed layout copy (ports.SlotCatalogue).
type SlotCatalogue struct {
	pool *pgxpool.Pool
}

var _ ports.SlotCatalogue = (*SlotCatalogue)(nil)

// NewSlotCatalogue constructs a SlotCatalogue over pool.
func NewSlotCatalogue(pool *pgxpool.Pool) *SlotCatalogue { return &SlotCatalogue{pool: pool} }

// ForwardSlots implements ports.SlotCatalogue.
func (c *SlotCatalogue) ForwardSlots(ctx context.Context, site slotplan.SiteID, codes []string) ([]planning.Slot, error) {
	if codes == nil {
		codes = []string{}
	}
	rows, err := queryFor(ctx, c.pool).Query(ctx, `
		SELECT s.location_code, z.zone_code, z.temperature_class, z.hazmat, s.max_weight_kg, s.max_volume_m3
		FROM slots s
		JOIN zones z ON z.zone_id = s.zone_id
		WHERE s.active AND s.role = 'Storage' AND z.site_code = $1 AND z.zone_code = ANY($2)
		ORDER BY s.location_code
	`, string(site), codes)
	if err != nil {
		return nil, fmt.Errorf("forward slots: %w", err)
	}
	defer rows.Close()
	var out []planning.Slot
	for rows.Next() {
		var code, temp string
		var s planning.Slot
		if err := rows.Scan(&code, &s.ZoneCode, &temp, &s.Hazmat, &s.MaxWeightKg, &s.MaxVolumeM3); err != nil {
			return nil, fmt.Errorf("scan forward slot: %w", err)
		}
		s.Code, s.TemperatureClass = slotplan.SlotCode(code), planning.TemperatureClass(temp)
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read forward slots: %w", err)
	}
	return out, nil
}

// SaveZone implements ports.SlotCatalogue.
func (c *SlotCatalogue) SaveZone(ctx context.Context, z repository.Zone) error {
	_, err := queryFor(ctx, c.pool).Exec(ctx, `
		INSERT INTO zones (zone_id, site_code, area_code, zone_code, temperature_class, hazmat)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (zone_id) DO UPDATE
		SET site_code = EXCLUDED.site_code, area_code = EXCLUDED.area_code, zone_code = EXCLUDED.zone_code,
		    temperature_class = EXCLUDED.temperature_class, hazmat = EXCLUDED.hazmat, updated_at = now()
	`, z.ID, z.SiteCode, z.AreaCode, z.ZoneCode, string(z.TemperatureClass), z.Hazmat)
	if err != nil {
		return fmt.Errorf("upsert zone: %w", err)
	}
	return nil
}

// SaveSlot implements ports.SlotCatalogue: a decommissioned slot stays so
// (the WHERE of the upsert).
func (c *SlotCatalogue) SaveSlot(ctx context.Context, s repository.Slot) error {
	_, err := queryFor(ctx, c.pool).Exec(ctx, `
		INSERT INTO slots (location_code, zone_id, role, active, max_weight_kg, max_volume_m3)
		VALUES ($1, $2, $3, true, $4, $5)
		ON CONFLICT (location_code) DO UPDATE
		SET zone_id = EXCLUDED.zone_id, role = EXCLUDED.role,
		    max_weight_kg = EXCLUDED.max_weight_kg, max_volume_m3 = EXCLUDED.max_volume_m3, updated_at = now()
		WHERE slots.active
	`, string(s.Code), s.ZoneID, s.Role, s.MaxWeightKg, s.MaxVolumeM3)
	if err != nil {
		return fmt.Errorf("upsert slot: %w", err)
	}
	return nil
}

// Decommission implements ports.SlotCatalogue.
func (c *SlotCatalogue) Decommission(ctx context.Context, code slotplan.SlotCode) error {
	_, err := queryFor(ctx, c.pool).Exec(ctx, `
		INSERT INTO slots (location_code, active) VALUES ($1, false)
		ON CONFLICT (location_code) DO UPDATE SET active = false, updated_at = now()
	`, string(code))
	if err != nil {
		return fmt.Errorf("decommission slot: %w", err)
	}
	return nil
}
