// Package ports declares the application's OUT ports: interfaces only
// (enforced by internal/architecture). Their non-interface vocabulary
// (errors, filters, value types, messages) lives in
// internal/application/repository and internal/application/outbox.
package ports

import (
	"context"
	"time"

	"github.com/claudioed/slotting-optimization/internal/application/outbox"
	"github.com/claudioed/slotting-optimization/internal/application/repository"
	"github.com/claudioed/slotting-optimization/internal/domain/planning"
	"github.com/claudioed/slotting-optimization/internal/domain/slotplan"
)

// SlotPlanRepository persists SlotPlan aggregates by plan id. Inside a
// UnitOfWork it joins the transaction carried in ctx.
type SlotPlanRepository interface {
	// Get returns the plan, or repository.ErrPlanNotFound.
	Get(ctx context.Context, id slotplan.PlanID) (*slotplan.SlotPlan, error)
	// Save persists p guarded by the version the caller loaded: loadedVersion
	// 0 inserts (an existing id is repository.ErrConcurrentModification); any
	// other value updates only when the stored version still equals
	// loadedVersion (else repository.ErrConcurrentModification). Saving a
	// plan as Approved while another plan of the site is Approved returns
	// repository.ErrApprovedPlanConflict.
	Save(ctx context.Context, p *slotplan.SlotPlan, loadedVersion int64) error
	// Current returns the site's Approved plan, or repository.ErrPlanNotFound.
	Current(ctx context.Context, site slotplan.SiteID) (*slotplan.SlotPlan, error)
	// List returns up to limit plans newest first (generated-at, then id,
	// descending) that come after the plan `after` in that order ("" = from
	// the start), narrowed by filter.
	List(ctx context.Context, filter repository.PlanFilter, after slotplan.PlanID, limit int) ([]*slotplan.SlotPlan, error)
}

// DemandLedger is the demand copy: one row per source order line.
type DemandLedger interface {
	// Apply upserts the line (last writer wins per (order, line)).
	Apply(ctx context.Context, line repository.DemandLine) error
	// Velocity counts the ACTIVE lines of the site due in [from, to) per SKU:
	// picks = lines, units = sum of units. Ordered picks desc, units desc,
	// SKU asc.
	Velocity(ctx context.Context, site slotplan.SiteID, from, to time.Time) ([]planning.SkuVelocity, error)
}

// ProfileDirectory is the product copy.
type ProfileDirectory interface {
	// Get returns the SKU's profile, or repository.ErrProfileNotFound.
	Get(ctx context.Context, sku slotplan.SKU) (repository.ProductProfile, error)
	// Many returns the planner view of the known SKUs among skus.
	Many(ctx context.Context, skus []slotplan.SKU) (map[slotplan.SKU]planning.Profile, error)
	// Save upserts the profile; an older version never replaces a newer one.
	Save(ctx context.Context, p repository.ProductProfile) error
}

// SlotCatalogue is the layout copy: zones and location slots.
type SlotCatalogue interface {
	// ForwardSlots returns the active Storage slots of the site whose zone
	// code is one of forwardZoneCodes, each joined with its zone attributes.
	ForwardSlots(ctx context.Context, site slotplan.SiteID, forwardZoneCodes []string) ([]planning.Slot, error)
	// SaveZone upserts a zone.
	SaveZone(ctx context.Context, z repository.Zone) error
	// SaveSlot upserts a slot; a decommissioned slot stays decommissioned.
	SaveSlot(ctx context.Context, s repository.Slot) error
	// Decommission retires a slot for good (also when it was never seen).
	Decommission(ctx context.Context, code slotplan.SlotCode) error
}

// IDGenerator mints plan ids.
type IDGenerator interface {
	// NewPlanID returns a fresh "plan-" id.
	NewPlanID() slotplan.PlanID
}

// OutboxRepository is the write side of the transactional outbox. Called
// with the ctx handed to UnitOfWork.Do it joins the SAME database
// transaction as the aggregate's Save, so the plan and its integration
// events commit or roll back together. A relay adapter later drains the
// stored rows to Kafka (internal/adapters/outbound/outbox).
type OutboxRepository interface {
	Insert(ctx context.Context, msgs ...outbox.Message) error
}

// EventEncoder turns SlotPlan domain events into wire-ready outbox messages
// (CloudEvents 1.0 structured mode, each with a freshly minted id). It is
// pure, so the use case can call it inside its UnitOfWork.
type EventEncoder interface {
	Encode(events ...slotplan.Event) ([]outbox.Message, error)
}

// ProcessedEvents is the inbound-consumer idempotency guard. Claim records
// (consumer, CloudEvents id) INSIDE the same UnitOfWork as the event's
// effect and reports false when an earlier committed handling already
// claimed it (the caller then skips).
type ProcessedEvents interface {
	Claim(ctx context.Context, consumer, eventID string) (claimed bool, err error)
}
