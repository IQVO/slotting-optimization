package packlevel_test

import (
	"errors"
	"testing"
	"time"

	"github.com/claudioed/slotting-optimization/internal/domain/packlevel"
)

var t0 = time.Date(2026, 10, 6, 20, 0, 0, 0, time.UTC)

func caseDims() *packlevel.Dims {
	return &packlevel.Dims{LengthMM: 400, WidthMM: 300, HeightMM: 200, WeightG: 6500}
}

func newCase(t *testing.T) packlevel.PackLevel {
	t.Helper()
	l, err := packlevel.New("SKU-1", "CASE", "24006381333935", 12, caseDims(), t0, 3)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return l
}

func TestNewRejectsInvalidInput(t *testing.T) {
	bad := packlevel.Dims{LengthMM: 0, WidthMM: 1, HeightMM: 1, WeightG: 1}
	for _, tc := range []struct {
		name  string
		sku   string
		level string
		gtin  string
		upp   int64
		dims  *packlevel.Dims
		ver   int64
		want  error
	}{
		{"blank sku", "  ", "CASE", "g", 12, nil, 1, packlevel.ErrBlankSKU},
		{"bad level", "S", "BOX", "g", 12, nil, 1, packlevel.ErrInvalidLevel},
		{"blank gtin", "S", "CASE", " ", 12, nil, 1, packlevel.ErrBlankGTIN},
		{"zero units", "S", "CASE", "g", 0, nil, 1, packlevel.ErrInvalidUnits},
		{"zero version", "S", "CASE", "g", 12, nil, 0, packlevel.ErrInvalidVersion},
		{"zero side", "S", "CASE", "g", 12, &bad, 1, packlevel.ErrInvalidDims},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := packlevel.New(tc.sku, tc.level, tc.gtin, tc.upp, tc.dims, t0, tc.ver)
			if !errors.Is(err, tc.want) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
		})
	}
}

func TestNewAcceptsEveryLevelAndBoundaryValues(t *testing.T) {
	for _, lvl := range []string{"EACH", "INNER", "CASE", "PALLET"} {
		if _, err := packlevel.New("S", lvl, "g", 1, nil, t0, 1); err != nil {
			t.Fatalf("%s: %v", lvl, err)
		}
	}
	max := packlevel.Dims{LengthMM: packlevel.MaxDimMM, WidthMM: packlevel.MaxDimMM, HeightMM: packlevel.MaxDimMM, WeightG: packlevel.MaxWeightG}
	if err := max.Validate(); err != nil {
		t.Fatalf("max dims: %v", err)
	}
	min := packlevel.Dims{LengthMM: 1, WidthMM: 1, HeightMM: 1, WeightG: 1}
	if err := min.Validate(); err != nil {
		t.Fatalf("min dims: %v", err)
	}
}

func TestDimsValidateBounds(t *testing.T) {
	base := packlevel.Dims{LengthMM: 10, WidthMM: 10, HeightMM: 10, WeightG: 10}
	for name, mutate := range map[string]func(d *packlevel.Dims){
		"length over": func(d *packlevel.Dims) { d.LengthMM = packlevel.MaxDimMM + 1 },
		"width over":  func(d *packlevel.Dims) { d.WidthMM = packlevel.MaxDimMM + 1 },
		"height over": func(d *packlevel.Dims) { d.HeightMM = packlevel.MaxDimMM + 1 },
		"weight over": func(d *packlevel.Dims) { d.WeightG = packlevel.MaxWeightG + 1 },
		"width zero":  func(d *packlevel.Dims) { d.WidthMM = 0 },
		"height zero": func(d *packlevel.Dims) { d.HeightMM = 0 },
		"weight zero": func(d *packlevel.Dims) { d.WeightG = 0 },
		"negative":    func(d *packlevel.Dims) { d.LengthMM = -5 },
	} {
		d := base
		mutate(&d)
		if err := d.Validate(); !errors.Is(err, packlevel.ErrInvalidDims) {
			t.Errorf("%s: got %v", name, err)
		}
	}
	if got := (packlevel.Dims{LengthMM: 400, WidthMM: 300, HeightMM: 200, WeightG: 1}).VolumeMM3(); got != 24_000_000 {
		t.Fatalf("volume = %d", got)
	}
}

func TestActiveAtIsHalfOpen(t *testing.T) {
	l := newCase(t)
	end := t0.Add(time.Hour)
	l.EffectiveTo = &end
	for _, tc := range []struct {
		at   time.Time
		want bool
	}{
		{t0.Add(-time.Nanosecond), false},
		{t0, true},
		{end.Add(-time.Nanosecond), true},
		{end, false},
	} {
		if got := l.ActiveAt(tc.at); got != tc.want {
			t.Errorf("ActiveAt(%v) = %v, want %v", tc.at, got, tc.want)
		}
	}
	if !newCase(t).ActiveAt(t0.AddDate(50, 0, 0)) {
		t.Fatal("an open-ended level is active forever")
	}
}

func TestSupersedesIsStrictlyGreaterVersion(t *testing.T) {
	cur := newCase(t)
	next, same, older := cur, cur, cur
	next.Version, older.Version = 4, 2
	if !next.Supersedes(cur) || same.Supersedes(cur) || older.Supersedes(cur) {
		t.Fatal("only a strictly greater version supersedes")
	}
	if !cur.Supersedes(packlevel.PackLevel{}) {
		t.Fatal("anything supersedes nothing")
	}
}

func TestEffectivePrefersMeasuredOverDeclared(t *testing.T) {
	l := newCase(t)
	d, ok := l.Effective()
	if !ok || d != *caseDims() {
		t.Fatalf("declared effective = %+v %v", d, ok)
	}
	measured := packlevel.Dims{LengthMM: 410, WidthMM: 310, HeightMM: 210, WeightG: 6900}
	m, ok := l.Measure("24006381333935", measured, 5)
	if !ok || m.Version != 5 {
		t.Fatalf("Measure = %+v %v", m, ok)
	}
	if d, _ := m.Effective(); d != measured {
		t.Fatalf("effective = %+v, want the measurement", d)
	}
	none := packlevel.PackLevel{}
	if _, ok := none.Effective(); ok {
		t.Fatal("a level with no dims has no effective dims")
	}
}

func TestMeasureGuards(t *testing.T) {
	l := newCase(t)
	dims := packlevel.Dims{LengthMM: 1, WidthMM: 1, HeightMM: 1, WeightG: 1}
	if _, ok := l.Measure("OTHER-GTIN", dims, 9); ok {
		t.Fatal("a measurement of another GTIN must not apply")
	}
	if _, ok := l.Measure(l.GTIN, dims, l.Version); ok {
		t.Fatal("an equal version must not apply")
	}
	if _, ok := l.Measure(l.GTIN, packlevel.Dims{}, 9); ok {
		t.Fatal("invalid dims must not apply")
	}
	if _, ok := l.Measure(l.GTIN, dims, l.Version+1); !ok {
		t.Fatal("the next version applies")
	}
}

func TestCarryKeepsTheMeasurementOfTheSameGTINOnly(t *testing.T) {
	cur, _ := newCase(t).Measure("24006381333935", packlevel.Dims{LengthMM: 5, WidthMM: 5, HeightMM: 5, WeightG: 5}, 4)
	same := newCase(t)
	same.Version = 6
	if got := same.Carry(cur); got.Measured == nil || got.Measured.LengthMM != 5 {
		t.Fatalf("same GTIN must keep the measurement: %+v", got.Measured)
	}
	other := same
	other.GTIN = "NEW-GTIN"
	if got := other.Carry(cur); got.Measured != nil {
		t.Fatalf("a new GTIN starts unmeasured: %+v", got.Measured)
	}
	if got := same.Carry(newCase(t)); got.Measured != nil {
		t.Fatal("nothing to carry")
	}
}

func TestRetireGuards(t *testing.T) {
	l := newCase(t)
	end := t0.Add(time.Hour)
	if _, ok := l.Retire("OTHER", end, 9); ok {
		t.Fatal("another GTIN must not retire the level")
	}
	if _, ok := l.Retire(l.GTIN, end, l.Version); ok {
		t.Fatal("an equal version must not retire the level")
	}
	r, ok := l.Retire(l.GTIN, end, l.Version+1)
	if !ok || r.EffectiveTo == nil || !r.EffectiveTo.Equal(end) || r.Version != l.Version+1 {
		t.Fatalf("Retire = %+v %v", r, ok)
	}
	if l.EffectiveTo != nil {
		t.Fatal("Retire must not mutate the receiver")
	}
}

func TestNewCopiesDeclaredDims(t *testing.T) {
	d := caseDims()
	l, err := packlevel.New("S", "CASE", "g", 12, d, t0, 1)
	if err != nil {
		t.Fatal(err)
	}
	d.LengthMM = 1
	if l.Declared.LengthMM != 400 {
		t.Fatal("the level must own a copy of the dims")
	}
}
