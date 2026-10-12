// Package repository holds the non-interface vocabulary of the application's
// ports: typed errors, list filters and the value types of the event-fed local
// copies (ADR 0003). It is separate from package ports because ports may
// contain interfaces only (internal/architecture's "ports package only
// contains interfaces" rule).
package repository

import (
	"errors"
	"time"

	"github.com/claudioed/slotting-optimization/internal/domain/planning"
	"github.com/claudioed/slotting-optimization/internal/domain/slotplan"
)

var (
	// ErrPlanNotFound is returned when a plan id is unknown, or by Current
	// when the site has no Approved plan.
	ErrPlanNotFound = errors.New("slot plan not found")
	// ErrConcurrentModification is returned by Save when the stored version
	// is not the version the caller loaded (or, for an insert, when the plan
	// already exists): another writer won the race.
	ErrConcurrentModification = errors.New("concurrent modification")
	// ErrApprovedPlanConflict is returned by Save when it would leave the
	// site with two Approved plans (the partial unique index): another
	// approval of the same site won the race.
	ErrApprovedPlanConflict = errors.New("another plan of this site is already approved")
	// ErrProfileNotFound is returned by ProfileDirectory.Get for an unknown SKU.
	ErrProfileNotFound = errors.New("product profile not found")
)

// PlanFilter narrows SlotPlanRepository.List. Zero values mean "no filter".
type PlanFilter struct {
	Site  slotplan.SiteID
	State slotplan.State
}

// DemandLine is one source order line of the demand copy. Active=false is a
// line the producer said is REMOVED: the row stays, so a replay cannot
// resurrect it, but it no longer counts as a pick.
type DemandLine struct {
	SourceOrderID string
	LineNo        int
	Site          slotplan.SiteID
	SKU           slotplan.SKU
	Units         int64
	DueAt         time.Time
	Active        bool
}

// ProductProfile is the product copy of one SKU: the classification and the
// effective physical profile, guarded by the product-master aggregate
// version of the last message applied. Unit is nil while the SKU has no
// effective dimensions.
type ProductProfile struct {
	SKU              slotplan.SKU
	HandlingTags     []string
	TemperatureClass planning.TemperatureClass
	Unit             *planning.UnitSize
	Version          int64
}

// Zone is a facility-layout zone of the layout copy.
type Zone struct {
	ID               string
	SiteCode         string
	AreaCode         string
	ZoneCode         string
	TemperatureClass planning.TemperatureClass
	Hazmat           bool
}

// Slot is a facility-layout location slot of the layout copy. It carries the
// id of its zone; the zone attributes live on the zone row.
type Slot struct {
	Code        slotplan.SlotCode
	ZoneID      string
	Role        string
	MaxWeightKg float64
	MaxVolumeM3 float64
}

// RoleStorage is the slot role forward slots must have; an absent role on
// the wire means Storage.
const RoleStorage = "Storage"
