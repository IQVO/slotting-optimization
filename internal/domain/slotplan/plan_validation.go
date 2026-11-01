package slotplan

import (
	"cmp"
	"slices"
	"time"
)

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
	return validatePickUnit(a)
}

// validatePickUnit: the zero unit means EACH; a CASE face holds at least one
// case and an EACH face has no case capacity.
func validatePickUnit(a Assignment) error {
	if a.PickUnit != "" && !a.PickUnit.Valid() {
		return ErrInvalidPickUnit
	}
	if a.PickUnit == PickCase {
		if a.CasesCapacity < 1 {
			return ErrInvalidPickUnit
		}
		return nil
	}
	if a.CasesCapacity != 0 {
		return ErrInvalidPickUnit
	}
	return nil
}

// withPickUnit returns the assignments with the zero pick unit spelled EACH,
// so a plan reads the same whether it was just generated or restored.
func withPickUnit(as []Assignment) []Assignment {
	out := make([]Assignment, len(as))
	for i, a := range as {
		if a.PickUnit == "" {
			a.PickUnit = PickEach
		}
		out[i] = a
	}
	return out
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
