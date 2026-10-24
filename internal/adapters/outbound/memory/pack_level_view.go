package memory

import (
	"context"
	"sync"
	"time"

	"github.com/claudioed/slotting-optimization/internal/application/ports"
	"github.com/claudioed/slotting-optimization/internal/domain/packlevel"
	"github.com/claudioed/slotting-optimization/internal/domain/planning"
	"github.com/claudioed/slotting-optimization/internal/domain/slotplan"
)

type packKey struct {
	sku   string
	level packlevel.Level
}

// PackLevelView is the in-memory pack-level copy (ports.PackLevelView).
type PackLevelView struct {
	mu   sync.Mutex
	rows map[packKey]packlevel.PackLevel
}

var (
	_ ports.PackLevelView = (*PackLevelView)(nil)
	_ Snapshotter         = (*PackLevelView)(nil)
)

// NewPackLevelView constructs an empty PackLevelView.
func NewPackLevelView() *PackLevelView {
	return &PackLevelView{rows: make(map[packKey]packlevel.PackLevel)}
}

// Get implements ports.PackLevelView.
func (v *PackLevelView) Get(_ context.Context, sku string, level packlevel.Level) (packlevel.PackLevel, bool, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	l, ok := v.rows[packKey{sku, level}]
	return copyLevel(l), ok, nil
}

// Save implements ports.PackLevelView.
func (v *PackLevelView) Save(_ context.Context, l packlevel.PackLevel) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.rows[packKey{l.SKU, l.Level}] = copyLevel(l)
	return nil
}

// ActiveCases implements ports.PackLevelView.
func (v *PackLevelView) ActiveCases(_ context.Context, skus []slotplan.SKU, at time.Time) (map[slotplan.SKU]planning.CaseSize, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	out := make(map[slotplan.SKU]planning.CaseSize, len(skus))
	for _, sku := range skus {
		l, ok := v.rows[packKey{string(sku), packlevel.Case}]
		if !ok || !l.ActiveAt(at) {
			continue
		}
		d, ok := l.Effective()
		if !ok {
			continue
		}
		out[sku] = planning.CaseSize{VolumeMM3: d.VolumeMM3(), WeightG: d.WeightG, UnitsPerPack: l.UnitsPerPack}
	}
	return out, nil
}

// Snapshot implements Snapshotter.
func (v *PackLevelView) Snapshot() func() {
	v.mu.Lock()
	defer v.mu.Unlock()
	saved := make(map[packKey]packlevel.PackLevel, len(v.rows))
	for k, l := range v.rows {
		saved[k] = copyLevel(l)
	}
	return func() {
		v.mu.Lock()
		defer v.mu.Unlock()
		v.rows = saved
	}
}

func copyLevel(l packlevel.PackLevel) packlevel.PackLevel {
	if l.Declared != nil {
		d := *l.Declared
		l.Declared = &d
	}
	if l.Measured != nil {
		m := *l.Measured
		l.Measured = &m
	}
	if l.EffectiveTo != nil {
		e := *l.EffectiveTo
		l.EffectiveTo = &e
	}
	return l
}
