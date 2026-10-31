// Package packlevel holds the PackLevel value of the local pack-level view
// (ADR 0012): one packaging level (EACH, INNER, CASE or PALLET) of a SKU as
// product-master declared and measured it. It is pure: no I/O, no clock
// reads, standard library only.
package packlevel

import (
	"errors"
	"strings"
	"time"
)

// Level is a packaging level of a SKU, spelled exactly as on product-master's
// wire.
type Level string

// The levels. Only Case drives slotting in v1.
const (
	Each   Level = "EACH"
	Inner  Level = "INNER"
	Case   Level = "CASE"
	Pallet Level = "PALLET"
)

// MaxDimMM and MaxWeightG bound a dimension set so that length * width *
// height always fits an int64 (1e6 mm per side = 1e18 mm3).
const (
	MaxDimMM   int64 = 1_000_000
	MaxWeightG int64 = 1_000_000_000
)

// Domain errors, as sentinel values (compare with errors.Is).
var (
	// ErrBlankSKU is returned when the SKU is empty or only white space.
	ErrBlankSKU = errors.New("packlevel: sku must not be blank")
	// ErrInvalidLevel is returned for a level that is not EACH, INNER, CASE or PALLET.
	ErrInvalidLevel = errors.New("packlevel: level must be EACH, INNER, CASE or PALLET")
	// ErrBlankGTIN is returned when the GTIN is empty or only white space.
	ErrBlankGTIN = errors.New("packlevel: gtin must not be blank")
	// ErrInvalidUnits is returned when units per pack is below 1.
	ErrInvalidUnits = errors.New("packlevel: units per pack must be at least 1")
	// ErrInvalidVersion is returned when the product version is below 1.
	ErrInvalidVersion = errors.New("packlevel: version must be at least 1")
	// ErrInvalidDims is returned for a dimension set with a side or a weight
	// outside 1..MaxDimMM / 1..MaxWeightG.
	ErrInvalidDims = errors.New("packlevel: dimensions and weight must be positive and within bounds")
)

// Dims are the physical dimensions of one pack of a level, in millimetres and
// grams.
type Dims struct {
	LengthMM int64
	WidthMM  int64
	HeightMM int64
	WeightG  int64
}

// Validate reports ErrInvalidDims unless every side and the weight are within
// bounds.
func (d Dims) Validate() error {
	for _, side := range []int64{d.LengthMM, d.WidthMM, d.HeightMM} {
		if side < 1 || side > MaxDimMM {
			return ErrInvalidDims
		}
	}
	if d.WeightG < 1 || d.WeightG > MaxWeightG {
		return ErrInvalidDims
	}
	return nil
}

// VolumeMM3 is the bounding-box cube of one pack.
func (d Dims) VolumeMM3() int64 { return d.LengthMM * d.WidthMM * d.HeightMM }

// PackLevel is one packaging level of a SKU. UnitsPerPack is the direct factor
// to eaches. Declared are the dimensions product-master declared; Measured the
// latest measurement of THIS GTIN (cleared when the GTIN changes). Version is
// the product-master aggregate version that last changed the level: a message
// is applied only when its version is greater. EffectiveTo is nil while the
// level is open-ended.
type PackLevel struct {
	SKU           string
	Level         Level
	GTIN          string
	UnitsPerPack  int64
	Declared      *Dims
	Measured      *Dims
	EffectiveFrom time.Time
	EffectiveTo   *time.Time
	Version       int64
}

// New builds a level from a ProductPackLevelDeclared payload. declared may be
// nil (product-master omits dims when none were declared).
func New(sku, level, gtin string, unitsPerPack int64, declared *Dims, effectiveFrom time.Time, version int64) (PackLevel, error) {
	if isBlank(sku) {
		return PackLevel{}, ErrBlankSKU
	}
	if !Level(level).valid() {
		return PackLevel{}, ErrInvalidLevel
	}
	if isBlank(gtin) {
		return PackLevel{}, ErrBlankGTIN
	}
	if unitsPerPack < 1 {
		return PackLevel{}, ErrInvalidUnits
	}
	if version < 1 {
		return PackLevel{}, ErrInvalidVersion
	}
	if declared != nil {
		if err := declared.Validate(); err != nil {
			return PackLevel{}, err
		}
		d := *declared
		declared = &d
	}
	return PackLevel{
		SKU: sku, Level: Level(level), GTIN: gtin, UnitsPerPack: unitsPerPack,
		Declared: declared, EffectiveFrom: effectiveFrom.UTC(), Version: version,
	}, nil
}

func isBlank(s string) bool { return strings.TrimSpace(s) == "" }

func (l Level) valid() bool {
	if l == Each {
		return true
	}
	if l == Inner {
		return true
	}
	if l == Case {
		return true
	}
	return l == Pallet
}

// ActiveAt reports whether the level is in force at t: effective from its
// start and until, not including, its end date.
func (p PackLevel) ActiveAt(t time.Time) bool {
	if t.Before(p.EffectiveFrom) {
		return false
	}
	return p.EffectiveTo == nil || t.Before(*p.EffectiveTo)
}

// Supersedes reports whether p should replace cur in the view: only a strictly
// greater version does, so a replay or a late message changes nothing. The zero
// PackLevel (nothing stored) is superseded by anything.
func (p PackLevel) Supersedes(cur PackLevel) bool {
	return p.Version > cur.Version
}

// Carry returns p with the measurement of cur kept when both are the same
// GTIN: a re-declaration of the same GTIN (new dims, flags) does not erase a
// measurement, a new GTIN starts unmeasured.
func (p PackLevel) Carry(cur PackLevel) PackLevel {
	if cur.GTIN == p.GTIN && cur.Measured != nil {
		m := *cur.Measured
		p.Measured = &m
	}
	return p
}

// Effective returns the dimensions slotting acts on: the measurement if there
// is one, else the declaration; false when the level has neither.
func (p PackLevel) Effective() (Dims, bool) {
	if p.Measured != nil {
		return *p.Measured, true
	}
	if p.Declared != nil {
		return *p.Declared, true
	}
	return Dims{}, false
}

// Measure records a measurement of the level with the given GTIN. It applies
// only to the stored GTIN (a late measurement of a superseded GTIN must not
// land on the GTIN that replaced it), only with valid dims and only with a
// strictly greater version; otherwise it reports false and returns the level
// unchanged.
func (p PackLevel) Measure(gtin string, dims Dims, version int64) (PackLevel, bool) {
	if gtin != p.GTIN || version <= p.Version || dims.Validate() != nil {
		return p, false
	}
	m := dims
	p.Measured = &m
	p.Version = version
	return p, true
}

// Retire end-dates the level at effectiveTo and records version. It applies
// only to the level with the given GTIN (a late retirement of a superseded
// GTIN must not end the GTIN that replaced it) and only with a strictly greater
// version; otherwise it reports false and returns the level unchanged.
func (p PackLevel) Retire(gtin string, effectiveTo time.Time, version int64) (PackLevel, bool) {
	if gtin != p.GTIN || version <= p.Version {
		return p, false
	}
	end := effectiveTo.UTC()
	p.EffectiveTo = &end
	p.Version = version
	return p, true
}
