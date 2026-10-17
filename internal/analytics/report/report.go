// Package report is the self-contained read-model region of the analytics
// read side (ADR 0006): the shape of the slot-plan funnel report, the query
// rules (date range, site scope, day windows, granularity), the pure
// percentile and freshness logic, the validation of a projectable event, and
// the writer/reader ports the analytical store implements. It imports no
// other internal package, so the OLTP domain can never leak into the
// projection and the arch test keeps this region isolated (mirroring
// inbound-receiving ADR 0006 and process-path-management ADR 0007).
package report

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// Kind is which of the three published events a fact came from. The names
// are the CloudEvents event names (the last segment of `type`).
type Kind string

// The three events the analytics topic carries.
const (
	KindSlotPlanGenerated Kind = "SlotPlanGenerated"
	KindSlotPlanApproved  Kind = "SlotPlanApproved"
	KindSlotPlanRejected  Kind = "SlotPlanRejected"
)

// Kinds returns every projected kind, in catalogue order.
func Kinds() []Kind {
	return []Kind{KindSlotPlanGenerated, KindSlotPlanApproved, KindSlotPlanRejected}
}

// Known reports whether k is one of the three published kinds.
func (k Kind) Known() bool {
	for _, c := range Kinds() {
		if k == c {
			return true
		}
	}
	return false
}

// Event is one decoded analytics event ready to project. Every kind carries
// the plan id (the CloudEvents subject) and the site, because all three
// payloads do.
type Event struct {
	Kind    Kind
	EventID string    // CloudEvents id: the idempotency key
	At      time.Time // CloudEvents time: when the event occurred
	PlanID  string
	SiteID  string
	// SupersedesPlanID is set exactly on a SlotPlanApproved that replaces a
	// previous Approved plan ("" when the site had none).
	SupersedesPlanID string
}

// ErrInvalidEvent marks an event that can never be projected (the consumer
// dead-letters it).
var ErrInvalidEvent = errors.New("report: event cannot be projected")

// Validate checks the invariants every store relies on. Every failure wraps
// ErrInvalidEvent and is deterministic.
func (e Event) Validate() error {
	switch {
	case !e.Kind.Known():
		return fmt.Errorf("%w: unknown kind %q", ErrInvalidEvent, e.Kind)
	case e.EventID == "":
		return fmt.Errorf("%w: %s without an id", ErrInvalidEvent, e.Kind)
	case e.At.IsZero():
		return fmt.Errorf("%w: %s %s without a time", ErrInvalidEvent, e.Kind, e.EventID)
	case e.PlanID == "":
		return fmt.Errorf("%w: %s %s without a plan_id", ErrInvalidEvent, e.Kind, e.EventID)
	case e.SiteID == "":
		return fmt.Errorf("%w: %s %s without a site_id", ErrInvalidEvent, e.Kind, e.EventID)
	case e.Kind != KindSlotPlanApproved && e.SupersedesPlanID != "":
		return fmt.Errorf("%w: %s %s supersedes another plan", ErrInvalidEvent, e.Kind, e.EventID)
	}
	return nil
}

// Milestones is the "this happened at" candidate an event offers for each
// column of slot_plan_facts (nil where it offers none). A store keeps the
// earliest candidate per milestone, so arrival order never matters.
type Milestones struct {
	Generated *time.Time
	Approved  *time.Time
	Rejected  *time.Time
	// Superseded is offered to the SUPERSEDED plan's row, not the event's
	// own: it is set exactly on a SlotPlanApproved that names another plan.
	Superseded *time.Time
}

// MilestonesOf returns the milestones an event marks on its own plan row
// (none for any other kind than its own) and, through Superseded, on the row
// of the plan it supersedes.
func MilestonesOf(e Event) Milestones {
	at := e.At.UTC()
	var m Milestones
	switch e.Kind {
	case KindSlotPlanGenerated:
		m.Generated = &at
	case KindSlotPlanApproved:
		m.Approved = &at
		if e.SupersedesPlanID != "" {
			m.Superseded = &at
		}
	case KindSlotPlanRejected:
		m.Rejected = &at
	}
	return m
}

// Projection is the WRITER port: Apply records the event id and folds the
// event into the model in ONE transaction. applied is false when the id was
// already recorded (a replay): nothing changed. An error wrapping
// ErrRejected is deterministic (the store can never accept this event); any
// other error is transient and the same event may be retried.
type Projection interface {
	Apply(ctx context.Context, e Event) (applied bool, err error)
}

// ErrRejected marks an event the analytical store deterministically refuses
// (a data-exception or integrity-violation class error).
var ErrRejected = errors.New("report: event rejected by the analytical store")

// Reader is the READER port.
type Reader interface {
	// FunnelDays returns one row per DayWindow of the query, in day order,
	// zeros included.
	FunnelDays(ctx context.Context, q FunnelQuery) ([]FunnelDay, error)
	// LastEventAt is the CloudEvents time of the newest event the projection
	// has applied (nil while it has applied none): the freshness basis.
	LastEventAt(ctx context.Context) (*time.Time, error)
}

// FunnelQuery selects the funnel rows: the time range, the optional site
// scope ("" = every site) and the bucket granularity (day only today).
type FunnelQuery struct {
	Range       Range
	SiteID      string
	Granularity Granularity
}

// FunnelDay is one UTC day of the slot-plan funnel report, counted over the
// day's window (the part of the day inside the requested range).
type FunnelDay struct {
	// Day is the UTC calendar day, at midnight UTC.
	Day time.Time
	// Generated counts the SlotPlanGenerated events whose `time` fell in the
	// window.
	Generated int
	// Approved counts the SlotPlanApproved events that fell in the window.
	Approved int
	// Rejected counts the SlotPlanRejected events that fell in the window.
	Rejected int
	// ApprovalLatency is the generated-to-approved time of the plans
	// approved in the window whose generation the projection also saw.
	// A plan approved (or still pending) without a seen generation counts in
	// Approved (or nowhere) but has no latency.
	ApprovalLatency Timing
	// Supersessions counts the plans whose superseded_at fell in the window:
	// a supersession is recorded when a later approval names the plan.
	Supersessions int
}
