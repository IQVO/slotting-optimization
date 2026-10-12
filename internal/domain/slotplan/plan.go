package slotplan

import (
	"cmp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

// SlotPlan is the aggregate root: a proposal of SKU -> forward slot
// assignments for one site and window, produced by a planning policy, and
// the decision taken on it.
//
// Invariants: a slot holds at most one SKU and a SKU is in at most one slot
// (v1); moves and unassigned entries are consistent with the assignments;
// only a Draft plan can be approved or rejected; only an Approved plan can
// be superseded; Approved, Rejected and Superseded plans never change their
// content. At most one plan per site is Approved: that rule spans plans, so
// the use case enforces it (supersede the previous one in the same unit of
// work) and a partial unique index backs it (ADR 0002).
type SlotPlan struct {
	id           PlanID
	site         SiteID
	window       Window
	policy       PolicyName
	state        State
	assignments  []Assignment
	moves        []Move
	unassigned   []Unassigned
	generatedAt  time.Time
	approvedAt   time.Time
	rejectedAt   time.Time
	supersededAt time.Time
	rejectReason string
	supersedes   PlanID
	version      int64
}

// Snapshot is the persisted form of a plan. Restore validates it.
type Snapshot struct {
	ID               PlanID
	Site             SiteID
	Window           Window
	Policy           PolicyName
	State            State
	Assignments      []Assignment
	Moves            []Move
	Unassigned       []Unassigned
	GeneratedAt      time.Time
	ApprovedAt       time.Time
	RejectedAt       time.Time
	SupersededAt     time.Time
	RejectReason     string
	SupersedesPlanID PlanID
	Version          int64
}

// NewPlan is the input of Generate: the computed proposal.
type NewPlan struct {
	ID          PlanID
	Site        SiteID
	Window      Window
	Policy      PolicyName
	Assignments []Assignment
	Moves       []Move
	Unassigned  []Unassigned
}

// Generate creates a Draft plan at version 1 and raises SlotPlanGenerated.
// Assignments, moves and unassigned entries are stored sorted by SKU so the
// same proposal always yields the same plan.
func Generate(in NewPlan, now time.Time) (*SlotPlan, []Event, error) {
	snap := Snapshot{
		ID:          in.ID,
		Site:        in.Site,
		Window:      in.Window,
		Policy:      in.Policy,
		State:       StateDraft,
		Assignments: in.Assignments,
		Moves:       in.Moves,
		Unassigned:  in.Unassigned,
		GeneratedAt: now.UTC(),
		Version:     1,
	}
	p, err := Restore(snap)
	if err != nil {
		return nil, nil, err
	}
	ev := SlotPlanGenerated{
		Header:          p.header(now),
		Window:          p.window,
		Policy:          p.policy,
		AssignmentCount: len(p.assignments),
		MoveCount:       len(p.moves),
		UnassignedCount: len(p.unassigned),
	}
	return p, []Event{ev}, nil
}

// Restore rebuilds a persisted plan, validating every invariant, so a
// corrupt row can never become a live aggregate.
func Restore(s Snapshot) (*SlotPlan, error) {
	if err := validateIdentity(s); err != nil {
		return nil, err
	}
	if err := validateState(s); err != nil {
		return nil, err
	}
	assigned, err := validateAssignments(s.Assignments)
	if err != nil {
		return nil, err
	}
	if err := validateMoves(s.Moves, assigned); err != nil {
		return nil, err
	}
	if err := validateUnassigned(s.Unassigned, assigned); err != nil {
		return nil, err
	}
	return &SlotPlan{
		id:           s.ID,
		site:         s.Site,
		window:       Window{From: s.Window.From.UTC(), To: s.Window.To.UTC()},
		policy:       s.Policy,
		state:        s.State,
		assignments:  sortedCopy(s.Assignments, func(a Assignment) SKU { return a.SKU }),
		moves:        sortedCopy(s.Moves, func(m Move) SKU { return m.SKU }),
		unassigned:   sortedCopy(s.Unassigned, func(u Unassigned) SKU { return u.SKU }),
		generatedAt:  s.GeneratedAt.UTC(),
		approvedAt:   utcOrZero(s.ApprovedAt),
		rejectedAt:   utcOrZero(s.RejectedAt),
		supersededAt: utcOrZero(s.SupersededAt),
		rejectReason: s.RejectReason,
		supersedes:   s.SupersedesPlanID,
		version:      s.Version,
	}, nil
}

// ID returns the plan id.
func (p *SlotPlan) ID() PlanID { return p.id }

// State returns the lifecycle state.
func (p *SlotPlan) State() State { return p.state }

// Version returns the aggregate version.
func (p *SlotPlan) Version() int64 { return p.version }

// Site returns the site the plan is for.
func (p *SlotPlan) Site() SiteID { return p.site }

// Snapshot returns the persisted form (slices are copies).
func (p *SlotPlan) Snapshot() Snapshot {
	return Snapshot{
		ID:               p.id,
		Site:             p.site,
		Window:           p.window,
		Policy:           p.policy,
		State:            p.state,
		Assignments:      slices.Clone(p.assignments),
		Moves:            slices.Clone(p.moves),
		Unassigned:       slices.Clone(p.unassigned),
		GeneratedAt:      p.generatedAt,
		ApprovedAt:       p.approvedAt,
		RejectedAt:       p.rejectedAt,
		SupersededAt:     p.supersededAt,
		RejectReason:     p.rejectReason,
		SupersedesPlanID: p.supersedes,
		Version:          p.version,
	}
}

// Approve moves a Draft plan to Approved and raises SlotPlanApproved with
// the full assignment map and the moves. supersedes is the site's previously
// Approved plan (empty if none); the caller must Supersede that plan in the
// same unit of work.
func (p *SlotPlan) Approve(supersedes PlanID, now time.Time) ([]Event, error) {
	if p.state != StateDraft {
		return nil, ErrNotDraft
	}
	if err := validateSupersedes(supersedes, p.id); err != nil {
		return nil, err
	}
	p.state = StateApproved
	p.approvedAt = now.UTC()
	p.supersedes = supersedes
	p.version++
	return []Event{SlotPlanApproved{
		Header:           p.header(now),
		SupersedesPlanID: supersedes,
		Assignments:      slices.Clone(p.assignments),
		Moves:            slices.Clone(p.moves),
	}}, nil
}

// Reject moves a Draft plan to Rejected and raises SlotPlanRejected. The
// reason is optional, trimmed, and at most 500 characters.
func (p *SlotPlan) Reject(reason string, now time.Time) ([]Event, error) {
	if p.state != StateDraft {
		return nil, ErrNotDraft
	}
	reason = strings.TrimSpace(reason)
	if utf8.RuneCountInString(reason) > maxRejectReason {
		return nil, ErrRejectReasonTooLong
	}
	p.state = StateRejected
	p.rejectedAt = now.UTC()
	p.rejectReason = reason
	p.version++
	return []Event{SlotPlanRejected{Header: p.header(now), Reason: reason}}, nil
}

// Supersede marks an Approved plan as replaced by a newer approval. It
// raises no event: consumers learn of it from supersedes_plan_id on the
// newer SlotPlanApproved.
func (p *SlotPlan) Supersede(now time.Time) error {
	if p.state != StateApproved {
		return ErrNotApproved
	}
	p.state = StateSuperseded
	p.supersededAt = now.UTC()
	p.version++
	return nil
}

func (p *SlotPlan) header(now time.Time) Header {
	return Header{Plan: p.id, Site: p.site, Version: p.version, At: now.UTC()}
}

func validateSupersedes(supersedes, self PlanID) error {
	if supersedes == "" {
		return nil
	}
	if _, err := NewPlanID(string(supersedes)); err != nil {
		return ErrInvalidSupersedes
	}
	if supersedes == self {
		return ErrInvalidSupersedes
	}
	return nil
}

func validateIdentity(s Snapshot) error {
	if _, err := NewPlanID(string(s.ID)); err != nil {
		return err
	}
	if _, err := NewSiteID(string(s.Site)); err != nil {
		return err
	}
	if _, err := NewPolicyName(string(s.Policy)); err != nil {
		return err
	}
	if _, err := NewWindow(s.Window.From, s.Window.To); err != nil {
		return err
	}
	if s.GeneratedAt.IsZero() || s.Version < 1 {
		return ErrInvalidSnapshot
	}
	return nil
}

// Each state has exactly one legal combination of decision timestamps.
const (
	stampApproved = 1 << iota
	stampRejected
	stampSuperseded
)

func wantStamps(st State) (int, bool) {
	if st == StateDraft {
		return 0, true
	}
	if st == StateApproved {
		return stampApproved, true
	}
	if st == StateRejected {
		return stampRejected, true
	}
	if st == StateSuperseded {
		return stampApproved | stampSuperseded, true
	}
	return 0, false
}

func gotStamps(s Snapshot) int {
	got := 0
	if !s.ApprovedAt.IsZero() {
		got |= stampApproved
	}
	if !s.RejectedAt.IsZero() {
		got |= stampRejected
	}
	if !s.SupersededAt.IsZero() {
		got |= stampSuperseded
	}
	return got
}

func validateState(s Snapshot) error {
	want, ok := wantStamps(s.State)
	if !ok {
		return ErrInvalidState
	}
	if gotStamps(s) != want {
		return ErrInvalidSnapshot
	}
	if s.RejectReason != "" && s.State != StateRejected {
		return ErrInvalidSnapshot
	}
	if s.SupersedesPlanID != "" && s.State != StateApproved && s.State != StateSuperseded {
		return ErrInvalidSnapshot
	}
	return validateSupersedes(s.SupersedesPlanID, s.ID)
}

func validateAssignments(as []Assignment) (map[SKU]SlotCode, error) {
	bySKU := make(map[SKU]SlotCode, len(as))
	bySlot := make(map[SlotCode]struct{}, len(as))
	for _, a := range as {
		if err := validateAssignment(a); err != nil {
			return nil, err
		}
		if _, dup := bySKU[a.SKU]; dup {
			return nil, ErrDuplicateSKU
		}
		if _, dup := bySlot[a.Slot]; dup {
			return nil, ErrDuplicateSlot
		}
		bySKU[a.SKU] = a.Slot
		bySlot[a.Slot] = struct{}{}
	}
	return bySKU, nil
}

func validateAssignment(a Assignment) error {
	if _, err := NewSKU(string(a.SKU)); err != nil {
		return err
	}
	if _, err := NewSlotCode(string(a.Slot)); err != nil {
		return err
	}
	if !a.Class.Valid() {
		return ErrInvalidABCClass
	}
	if a.Picks < 1 || a.Units < 0 {
		return ErrInvalidMetric
	}
	return nil
}

func validateMoves(moves []Move, assigned map[SKU]SlotCode) error {
	seen := make(map[SKU]struct{}, len(moves))
	for _, m := range moves {
		if _, err := NewSKU(string(m.SKU)); err != nil {
			return err
		}
		if !m.Kind.Valid() {
			return ErrInvalidMoveKind
		}
		if _, dup := seen[m.SKU]; dup {
			return ErrInvalidMove
		}
		seen[m.SKU] = struct{}{}
		if !moveConsistent(m, assigned) {
			return ErrInvalidMove
		}
	}
	return nil
}

// moveConsistent checks the shape of a move and that it agrees with the
// assignments: Assign and Relocate land on the SKU's assigned slot, Vacate
// is for a SKU that has none.
func moveConsistent(m Move, assigned map[SKU]SlotCode) bool {
	slot, isAssigned := assigned[m.SKU]
	if m.Kind == MoveAssign {
		return m.From == "" && m.To != "" && isAssigned && slot == m.To
	}
	if m.Kind == MoveRelocate {
		return m.From != "" && m.To != "" && m.From != m.To && isAssigned && slot == m.To
	}
	return m.From != "" && m.To == "" && !isAssigned
}

func validateUnassigned(us []Unassigned, assigned map[SKU]SlotCode) error {
	seen := make(map[SKU]struct{}, len(us))
	for _, u := range us {
		if _, err := NewSKU(string(u.SKU)); err != nil {
			return err
		}
		if !u.Reason.Valid() {
			return ErrInvalidReason
		}
		if _, dup := seen[u.SKU]; dup {
			return ErrInvalidUnassigned
		}
		seen[u.SKU] = struct{}{}
		if _, isAssigned := assigned[u.SKU]; isAssigned {
			return ErrInvalidUnassigned
		}
	}
	return nil
}

func sortedCopy[T any](in []T, key func(T) SKU) []T {
	out := slices.Clone(in)
	slices.SortFunc(out, func(a, b T) int { return cmp.Compare(key(a), key(b)) })
	return out
}

func utcOrZero(t time.Time) time.Time {
	if t.IsZero() {
		return time.Time{}
	}
	return t.UTC()
}
