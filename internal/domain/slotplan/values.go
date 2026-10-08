// Package slotplan holds the SlotPlan aggregate: a proposal that assigns SKUs
// to forward pick slots for one site and demand window, and the human
// decision (approve / reject) taken on it. The plan is computed elsewhere
// (package planning); this package only enforces what a valid plan is and
// which transitions it may go through.
package slotplan

import (
	"errors"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// Validation errors. The application layer maps them to problem slugs.
var (
	ErrInvalidPlanID       = errors.New("slotplan: invalid plan id")
	ErrInvalidSiteID       = errors.New("slotplan: invalid site id")
	ErrInvalidSKU          = errors.New("slotplan: invalid sku")
	ErrInvalidSlotCode     = errors.New("slotplan: invalid slot code")
	ErrInvalidPolicy       = errors.New("slotplan: invalid policy name")
	ErrInvalidWindow       = errors.New("slotplan: window must have from before to")
	ErrInvalidABCClass     = errors.New("slotplan: unknown abc class")
	ErrInvalidMoveKind     = errors.New("slotplan: unknown move kind")
	ErrInvalidReason       = errors.New("slotplan: unknown unassigned reason")
	ErrInvalidState        = errors.New("slotplan: unknown state")
	ErrInvalidMetric       = errors.New("slotplan: picks must be at least 1 and units at least 0")
	ErrDuplicateSKU        = errors.New("slotplan: a sku appears twice")
	ErrDuplicateSlot       = errors.New("slotplan: a slot holds more than one sku")
	ErrInvalidMove         = errors.New("slotplan: move is malformed or contradicts the assignments")
	ErrInvalidUnassigned   = errors.New("slotplan: unassigned entry is malformed or contradicts the assignments")
	ErrInvalidSupersedes   = errors.New("slotplan: supersedes_plan_id is invalid")
	ErrInvalidSnapshot     = errors.New("slotplan: persisted state is inconsistent")
	ErrNotDraft            = errors.New("slotplan: only a Draft plan can be approved or rejected")
	ErrNotApproved         = errors.New("slotplan: only an Approved plan can be superseded")
	ErrRejectReasonTooLong = errors.New("slotplan: reject reason is too long")
)

const (
	maxTokenLen     = 64
	planIDPrefix    = "plan-"
	maxRejectReason = 500
)

// PlanID identifies a SlotPlan: "plan-" followed by a unique suffix.
type PlanID string

// SiteID identifies the site a plan is for.
type SiteID string

// SKU identifies a product.
type SKU string

// SlotCode is the facility-layout location code of a forward slot.
type SlotCode string

// PolicyName names the planning policy that produced a plan.
type PolicyName string

// PolicyABCVelocityV1 is the v1 policy: rank by picks, ABC classes, sticky.
const PolicyABCVelocityV1 PolicyName = "abc-velocity-v1"

// validToken reports whether s is 1..64 characters with no whitespace,
// control characters or '/'.
func validToken(s string) bool {
	if s == "" || utf8.RuneCountInString(s) > maxTokenLen {
		return false
	}
	for _, r := range s {
		if r == '/' || unicode.IsSpace(r) || unicode.IsControl(r) {
			return false
		}
	}
	return true
}

// NewPlanID validates a plan id: the "plan-" prefix, a non-empty suffix, and
// the token rules.
func NewPlanID(s string) (PlanID, error) {
	if !validToken(s) || !strings.HasPrefix(s, planIDPrefix) || len(s) == len(planIDPrefix) {
		return "", ErrInvalidPlanID
	}
	return PlanID(s), nil
}

// NewSiteID validates a site id.
func NewSiteID(s string) (SiteID, error) {
	if !validToken(s) {
		return "", ErrInvalidSiteID
	}
	return SiteID(s), nil
}

// NewSKU validates a SKU.
func NewSKU(s string) (SKU, error) {
	if !validToken(s) {
		return "", ErrInvalidSKU
	}
	return SKU(s), nil
}

// NewSlotCode validates a slot code.
func NewSlotCode(s string) (SlotCode, error) {
	if !validToken(s) {
		return "", ErrInvalidSlotCode
	}
	return SlotCode(s), nil
}

// NewPolicyName validates a policy name.
func NewPolicyName(s string) (PolicyName, error) {
	if !validToken(s) {
		return "", ErrInvalidPolicy
	}
	return PolicyName(s), nil
}

// Window is the half-open demand window [From, To) a plan was computed over.
type Window struct {
	From time.Time
	To   time.Time
}

// NewWindow validates a window and normalises it to UTC.
func NewWindow(from, to time.Time) (Window, error) {
	if from.IsZero() || to.IsZero() || !from.Before(to) {
		return Window{}, ErrInvalidWindow
	}
	return Window{From: from.UTC(), To: to.UTC()}, nil
}

// State is the lifecycle state of a plan.
type State string

// Plan states.
const (
	StateDraft      State = "Draft"
	StateApproved   State = "Approved"
	StateRejected   State = "Rejected"
	StateSuperseded State = "Superseded"
)

// Valid reports whether s is a known state.
func (s State) Valid() bool {
	if s == StateDraft || s == StateApproved {
		return true
	}
	return s == StateRejected || s == StateSuperseded
}

// ABCClass is the velocity class of an assigned SKU.
type ABCClass string

// ABC classes.
const (
	ClassA ABCClass = "A"
	ClassB ABCClass = "B"
	ClassC ABCClass = "C"
)

// Valid reports whether c is a known class.
func (c ABCClass) Valid() bool {
	if c == ClassA || c == ClassB {
		return true
	}
	return c == ClassC
}

// MoveKind says what a move does to a SKU's forward slot.
type MoveKind string

// Move kinds.
const (
	MoveAssign   MoveKind = "Assign"
	MoveRelocate MoveKind = "Relocate"
	MoveVacate   MoveKind = "Vacate"
)

// Valid reports whether k is a known kind.
func (k MoveKind) Valid() bool {
	if k == MoveAssign || k == MoveRelocate {
		return true
	}
	return k == MoveVacate
}

// UnassignedReason says why a candidate SKU got no forward slot.
type UnassignedReason string

// Unassigned reasons.
const (
	ReasonNoEligibleSlot    UnassignedReason = "NoEligibleSlot"
	ReasonNoPhysicalProfile UnassignedReason = "NoPhysicalProfile"
	ReasonNoCapacityFit     UnassignedReason = "NoCapacityFit"
)

// Valid reports whether r is a known reason.
func (r UnassignedReason) Valid() bool {
	if r == ReasonNoEligibleSlot || r == ReasonNoPhysicalProfile {
		return true
	}
	return r == ReasonNoCapacityFit
}

// Assignment puts one SKU into one forward slot.
type Assignment struct {
	SKU   SKU
	Slot  SlotCode
	Class ABCClass
	Picks int64
	Units int64
}

// Move is a physical change implied by the plan against the previous state.
// Assign has no From; Vacate has no To; Relocate has both and they differ.
type Move struct {
	SKU  SKU
	From SlotCode
	To   SlotCode
	Kind MoveKind
}

// Unassigned records a candidate SKU that received no slot, and why.
type Unassigned struct {
	SKU    SKU
	Reason UnassignedReason
}
