package slotplan

import "time"

// Event is a domain event raised by the SlotPlan aggregate. Every event
// carries the plan id, the site, the aggregate version AFTER the change and
// when it happened (from the caller's clock).
type Event interface {
	EventName() string
	PlanID() PlanID
	SiteID() SiteID
	PlanVersion() int64
	OccurredAt() time.Time
}

// Header is embedded in every event.
type Header struct {
	Plan    PlanID
	Site    SiteID
	Version int64
	At      time.Time
}

// PlanID returns the plan the event is about.
func (h Header) PlanID() PlanID { return h.Plan }

// SiteID returns the site the plan is for.
func (h Header) SiteID() SiteID { return h.Site }

// PlanVersion returns the aggregate version after the change.
func (h Header) PlanVersion() int64 { return h.Version }

// OccurredAt returns when the change happened.
func (h Header) OccurredAt() time.Time { return h.At }

// SlotPlanGenerated is raised by Generate with the size of the proposal.
type SlotPlanGenerated struct {
	Header
	Window          Window
	Policy          PolicyName
	AssignmentCount int
	MoveCount       int
	UnassignedCount int
}

// EventName returns "SlotPlanGenerated".
func (SlotPlanGenerated) EventName() string { return "SlotPlanGenerated" }

// SlotPlanApproved is raised when a Draft plan is approved. It carries the
// FULL assignment map and the moves so a consumer needs no lookup.
// SupersedesPlanID is the previously Approved plan of the site, if any.
type SlotPlanApproved struct {
	Header
	SupersedesPlanID PlanID
	Assignments      []Assignment
	Moves            []Move
}

// EventName returns "SlotPlanApproved".
func (SlotPlanApproved) EventName() string { return "SlotPlanApproved" }

// SlotPlanRejected is raised when a Draft plan is rejected.
type SlotPlanRejected struct {
	Header
	Reason string
}

// EventName returns "SlotPlanRejected".
func (SlotPlanRejected) EventName() string { return "SlotPlanRejected" }
