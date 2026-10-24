package usecases_test

import (
	"context"
	"time"

	"github.com/claudioed/slotting-optimization/internal/domain/packlevel"
	"github.com/claudioed/slotting-optimization/internal/domain/planning"
	"github.com/claudioed/slotting-optimization/internal/domain/slotplan"
)

// fakePacks is an in-test PackLevelView.
type fakePacks struct {
	rows      map[string]packlevel.PackLevel
	getErr    error
	saveErr   error
	activeErr error
	saves     int
	askedAt   time.Time
	askedFor  []slotplan.SKU
	cases     map[slotplan.SKU]planning.CaseSize
}

func newFakePacks() *fakePacks { return &fakePacks{rows: map[string]packlevel.PackLevel{}} }

func packKey(sku string, level packlevel.Level) string { return sku + "|" + string(level) }

func (f *fakePacks) Get(_ context.Context, sku string, level packlevel.Level) (packlevel.PackLevel, bool, error) {
	if f.getErr != nil {
		return packlevel.PackLevel{}, false, f.getErr
	}
	l, ok := f.rows[packKey(sku, level)]
	return l, ok, nil
}

func (f *fakePacks) Save(_ context.Context, l packlevel.PackLevel) error {
	if f.saveErr != nil {
		return f.saveErr
	}
	f.saves++
	f.rows[packKey(l.SKU, l.Level)] = l
	return nil
}

func (f *fakePacks) ActiveCases(_ context.Context, skus []slotplan.SKU, at time.Time) (map[slotplan.SKU]planning.CaseSize, error) {
	f.askedAt, f.askedFor = at, skus
	return f.cases, f.activeErr
}
