package usecases

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/claudioed/slotting-optimization/internal/application/ports"
	"github.com/claudioed/slotting-optimization/internal/application/repository"
	"github.com/claudioed/slotting-optimization/internal/domain/planning"
	"github.com/claudioed/slotting-optimization/internal/domain/slotplan"
)

// Consumer names namespace the claims of the three local-copy consumers in
// ports.ProcessedEvents.
const (
	DemandConsumer  = "slotting-demand"
	ProductConsumer = "slotting-product"
	LayoutConsumer  = "slotting-layout"
)

// ErrInvalidEvent marks a consumed message that can never be applied (bad
// SKU, bad quantity, unknown state). It wraps the cause. The consumer logs it
// and commits past the message; it is claimed by nothing.
var ErrInvalidEvent = errors.New("invalid event")

// Outcome reports what a consumer use case did.
type Outcome string

// The outcomes.
const (
	// OutcomeApplied: the copy was updated.
	OutcomeApplied Outcome = "applied"
	// OutcomeDuplicate: this CloudEvents id was already processed.
	OutcomeDuplicate Outcome = "duplicate"
	// OutcomeStale: the message version is not newer than the stored one.
	OutcomeStale Outcome = "stale"
	// OutcomeIgnored: the message is for a site this deployment does not plan.
	OutcomeIgnored Outcome = "ignored"
)

func invalid(err error) error { return fmt.Errorf("%w: %w", ErrInvalidEvent, err) }

// Intake is the claim-and-apply skeleton every consumer use case shares: the
// claim of the CloudEvents id and the effect run in ONE unit of work, so a
// rollback un-claims the id and a redelivery is processed, not skipped.
type Intake struct {
	UoW       ports.UnitOfWork
	Processed ports.ProcessedEvents
}

func (in Intake) apply(ctx context.Context, consumer, eventID string, effect func(ctx context.Context) (Outcome, error)) (Outcome, error) {
	var outcome Outcome
	err := in.UoW.Do(ctx, func(ctx context.Context) error {
		claimed, err := in.Processed.Claim(ctx, consumer, eventID)
		if err != nil {
			return fmt.Errorf("claim processed event: %w", err)
		}
		if !claimed {
			outcome = OutcomeDuplicate
			return nil
		}
		out, err := effect(ctx)
		outcome = out
		return err
	})
	if err != nil {
		return "", err
	}
	return outcome, nil
}

// DemandState is the state of a SiteSkuDemandChanged line.
const (
	DemandActive  = "ACTIVE"
	DemandRemoved = "REMOVED"
)

// DemandChange is a decoded SiteSkuDemandChanged payload.
type DemandChange struct {
	SourceOrderID string
	LineNo        int
	SiteID        string
	SKU           string
	Units         int64
	DueAt         time.Time
	State         string
}

// ApplyDemandChanged upserts one source order line into the demand copy
// (last writer wins; REMOVED deactivates the row).
type ApplyDemandChanged struct {
	Intake
	Demand ports.DemandLedger
	// OnlySite, when set, drops lines of any other site (DEMAND_SITE_ID).
	OnlySite string
}

// Handle runs the use case. A payload that can never be applied is wrapped
// in ErrInvalidEvent before anything is claimed.
func (uc *ApplyDemandChanged) Handle(ctx context.Context, eventID string, c DemandChange) (Outcome, error) {
	line, err := demandLine(c)
	if err != nil {
		return "", invalid(err)
	}
	if uc.OnlySite != "" && string(line.Site) != uc.OnlySite {
		return OutcomeIgnored, nil
	}
	return uc.apply(ctx, DemandConsumer, eventID, func(ctx context.Context) (Outcome, error) {
		if err := uc.Demand.Apply(ctx, line); err != nil {
			return "", fmt.Errorf("apply demand line: %w", err)
		}
		return OutcomeApplied, nil
	})
}

func demandLine(c DemandChange) (repository.DemandLine, error) {
	site, err := slotplan.NewSiteID(c.SiteID)
	if err != nil {
		return repository.DemandLine{}, err
	}
	sku, err := slotplan.NewSKU(c.SKU)
	if err != nil {
		return repository.DemandLine{}, err
	}
	if c.SourceOrderID == "" || c.LineNo < 1 {
		return repository.DemandLine{}, errors.New("source_order_id and line_no are required")
	}
	if c.DueAt.IsZero() {
		return repository.DemandLine{}, errors.New("due_at is required")
	}
	if c.State != DemandActive && c.State != DemandRemoved {
		return repository.DemandLine{}, fmt.Errorf("unknown state %q", c.State)
	}
	if c.State == DemandActive && c.Units < 1 {
		return repository.DemandLine{}, errors.New("demanded_units must be at least 1")
	}
	units := c.Units
	if units < 0 {
		units = 0
	}
	return repository.DemandLine{
		SourceOrderID: c.SourceOrderID, LineNo: c.LineNo, Site: site, SKU: sku,
		Units: units, DueAt: c.DueAt.UTC(), Active: c.State == DemandActive,
	}, nil
}

// ProductClassification is a decoded ProductClassified payload.
type ProductClassification struct {
	SKU              string
	HandlingTags     []string
	TemperatureClass string
	Version          int64
}

// ApplyProductClassified replaces the classification of a SKU in the product
// copy when the message version is newer than the stored one.
type ApplyProductClassified struct {
	Intake
	Profiles ports.ProfileDirectory
}

// Handle runs the use case.
func (uc *ApplyProductClassified) Handle(ctx context.Context, eventID string, c ProductClassification) (Outcome, error) {
	sku, err := slotplan.NewSKU(c.SKU)
	if err != nil {
		return "", invalid(err)
	}
	temp, err := temperatureClass(c.TemperatureClass)
	if err != nil {
		return "", invalid(err)
	}
	if c.Version < 1 {
		return "", invalid(errors.New("version must be at least 1"))
	}
	return uc.apply(ctx, ProductConsumer, eventID, func(ctx context.Context) (Outcome, error) {
		cur, err := currentProfile(ctx, uc.Profiles, sku)
		if err != nil {
			return "", err
		}
		if c.Version <= cur.Version {
			return OutcomeStale, nil
		}
		cur.HandlingTags = append([]string(nil), c.HandlingTags...)
		cur.TemperatureClass = temp
		cur.Version = c.Version
		if err := uc.Profiles.Save(ctx, cur); err != nil {
			return "", fmt.Errorf("save product profile: %w", err)
		}
		return OutcomeApplied, nil
	})
}

// PhysicalChange is a decoded ProductDimensionsDeclared / ProductMeasured
// payload, reduced to what slotting acts on: the effective unit size.
// HasEffective is false when the message carries no effective dimensions.
type PhysicalChange struct {
	SKU          string
	HasEffective bool
	VolumeMM3    int64
	WeightG      int64
	Version      int64
}

// ApplyPhysicalProfile replaces the effective physical profile of a SKU in
// the product copy when the message version is newer than the stored one.
type ApplyPhysicalProfile struct {
	Intake
	Profiles ports.ProfileDirectory
}

// Handle runs the use case.
func (uc *ApplyPhysicalProfile) Handle(ctx context.Context, eventID string, c PhysicalChange) (Outcome, error) {
	sku, err := slotplan.NewSKU(c.SKU)
	if err != nil {
		return "", invalid(err)
	}
	if c.Version < 1 {
		return "", invalid(errors.New("version must be at least 1"))
	}
	if c.HasEffective && (c.VolumeMM3 < 1 || c.WeightG < 1) {
		return "", invalid(errors.New("effective volume_mm3 and weight_g must be at least 1"))
	}
	return uc.apply(ctx, ProductConsumer, eventID, func(ctx context.Context) (Outcome, error) {
		cur, err := currentProfile(ctx, uc.Profiles, sku)
		if err != nil {
			return "", err
		}
		if c.Version <= cur.Version {
			return OutcomeStale, nil
		}
		cur.Unit = nil
		if c.HasEffective {
			cur.Unit = &planning.UnitSize{VolumeMM3: c.VolumeMM3, WeightG: c.WeightG}
		}
		cur.Version = c.Version
		if err := uc.Profiles.Save(ctx, cur); err != nil {
			return "", fmt.Errorf("save product profile: %w", err)
		}
		return OutcomeApplied, nil
	})
}

// currentProfile returns the stored profile, or an empty one at version 0.
func currentProfile(ctx context.Context, d ports.ProfileDirectory, sku slotplan.SKU) (repository.ProductProfile, error) {
	cur, err := d.Get(ctx, sku)
	if errors.Is(err, repository.ErrProfileNotFound) {
		return repository.ProductProfile{SKU: sku}, nil
	}
	if err != nil {
		return repository.ProductProfile{}, fmt.Errorf("read product profile: %w", err)
	}
	return cur, nil
}

func temperatureClass(raw string) (planning.TemperatureClass, error) {
	t := planning.TemperatureClass(raw)
	if t == "" || t == planning.Ambient || t == planning.Chilled || t == planning.Frozen {
		return t, nil
	}
	return "", fmt.Errorf("unknown temperature class %q", raw)
}

// ZoneChange is a decoded ZoneRegistered payload.
type ZoneChange struct {
	ZoneID           string
	SiteCode         string
	AreaCode         string
	ZoneCode         string
	TemperatureClass string
	Hazmat           bool
}

// ApplyZoneRegistered upserts a zone into the layout copy.
type ApplyZoneRegistered struct {
	Intake
	Catalogue ports.SlotCatalogue
}

// Handle runs the use case.
func (uc *ApplyZoneRegistered) Handle(ctx context.Context, eventID string, c ZoneChange) (Outcome, error) {
	temp, err := temperatureClass(c.TemperatureClass)
	if err != nil {
		return "", invalid(err)
	}
	if c.ZoneID == "" || c.SiteCode == "" || c.ZoneCode == "" {
		return "", invalid(errors.New("zoneId, siteCode and zoneCode are required"))
	}
	z := repository.Zone{
		ID: c.ZoneID, SiteCode: c.SiteCode, AreaCode: c.AreaCode, ZoneCode: c.ZoneCode,
		TemperatureClass: temp, Hazmat: c.Hazmat,
	}
	return uc.apply(ctx, LayoutConsumer, eventID, func(ctx context.Context) (Outcome, error) {
		if err := uc.Catalogue.SaveZone(ctx, z); err != nil {
			return "", fmt.Errorf("save zone: %w", err)
		}
		return OutcomeApplied, nil
	})
}

// SlotChange is a decoded LocationSlotRegistered payload. An empty Role means
// Storage.
type SlotChange struct {
	LocationCode string
	ZoneID       string
	Role         string
	MaxWeightKg  float64
	MaxVolumeM3  float64
}

// ApplyLocationSlotRegistered upserts a location slot into the layout copy.
type ApplyLocationSlotRegistered struct {
	Intake
	Catalogue ports.SlotCatalogue
}

// Handle runs the use case.
func (uc *ApplyLocationSlotRegistered) Handle(ctx context.Context, eventID string, c SlotChange) (Outcome, error) {
	code, err := slotplan.NewSlotCode(c.LocationCode)
	if err != nil {
		return "", invalid(err)
	}
	if c.ZoneID == "" {
		return "", invalid(errors.New("zoneId is required"))
	}
	if c.MaxWeightKg < 0 || c.MaxVolumeM3 < 0 {
		return "", invalid(errors.New("capacity must not be negative"))
	}
	role := c.Role
	if role == "" {
		role = repository.RoleStorage
	}
	s := repository.Slot{Code: code, ZoneID: c.ZoneID, Role: role, MaxWeightKg: c.MaxWeightKg, MaxVolumeM3: c.MaxVolumeM3}
	return uc.apply(ctx, LayoutConsumer, eventID, func(ctx context.Context) (Outcome, error) {
		if err := uc.Catalogue.SaveSlot(ctx, s); err != nil {
			return "", fmt.Errorf("save slot: %w", err)
		}
		return OutcomeApplied, nil
	})
}

// ApplyLocationSlotDecommissioned retires a location slot in the layout copy.
type ApplyLocationSlotDecommissioned struct {
	Intake
	Catalogue ports.SlotCatalogue
}

// Handle runs the use case.
func (uc *ApplyLocationSlotDecommissioned) Handle(ctx context.Context, eventID, locationCode string) (Outcome, error) {
	code, err := slotplan.NewSlotCode(locationCode)
	if err != nil {
		return "", invalid(err)
	}
	return uc.apply(ctx, LayoutConsumer, eventID, func(ctx context.Context) (Outcome, error) {
		if err := uc.Catalogue.Decommission(ctx, code); err != nil {
			return "", fmt.Errorf("decommission slot: %w", err)
		}
		return OutcomeApplied, nil
	})
}
