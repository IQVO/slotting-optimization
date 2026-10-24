package slotplan

import (
	"errors"
	"testing"
)

func TestPickUnitValid(t *testing.T) {
	for u, want := range map[PickUnit]bool{PickEach: true, PickCase: true, "": false, "PALLET": false, "each": false} {
		if got := u.Valid(); got != want {
			t.Errorf("%q.Valid() = %v, want %v", u, got, want)
		}
	}
}

func TestGenerateValidatesPickUnitAndCasesCapacity(t *testing.T) {
	for _, tc := range []struct {
		name string
		unit PickUnit
		cap  int64
		want error
	}{
		{"zero unit is EACH", "", 0, nil},
		{"explicit EACH", PickEach, 0, nil},
		{"CASE with capacity", PickCase, 1, nil},
		{"CASE with a big capacity", PickCase, 40, nil},
		{"CASE without capacity", PickCase, 0, ErrInvalidPickUnit},
		{"CASE with negative capacity", PickCase, -1, ErrInvalidPickUnit},
		{"EACH with capacity", PickEach, 1, ErrInvalidPickUnit},
		{"zero unit with capacity", "", 3, ErrInvalidPickUnit},
		{"unknown unit", "PALLET", 0, ErrInvalidPickUnit},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := validNew()
			in.Assignments[0].PickUnit, in.Assignments[0].CasesCapacity = tc.unit, tc.cap
			_, _, err := Generate(in, t0)
			if !errors.Is(err, tc.want) && (tc.want != nil || err != nil) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
		})
	}
}

func TestPickUnitIsSpelledEachAndSurvivesTheRoundTrip(t *testing.T) {
	in := validNew()
	in.Assignments[0].PickUnit, in.Assignments[0].CasesCapacity = PickCase, 6
	p, _, err := Generate(in, t0)
	if err != nil {
		t.Fatal(err)
	}
	var sawCase, sawEach int
	for _, a := range p.Snapshot().Assignments {
		switch a.PickUnit {
		case PickCase:
			sawCase++
			if a.CasesCapacity != 6 {
				t.Fatalf("capacity lost: %+v", a)
			}
		case PickEach:
			sawEach++
		default:
			t.Fatalf("pick unit %q: the zero value must read as EACH", a.PickUnit)
		}
	}
	if sawCase != 1 || sawEach != len(in.Assignments)-1 {
		t.Fatalf("case %d each %d", sawCase, sawEach)
	}
	again, err := Restore(p.Snapshot())
	if err != nil {
		t.Fatal(err)
	}
	if len(again.Snapshot().Assignments) != len(in.Assignments) {
		t.Fatal("restore lost assignments")
	}
	if in.Assignments[1].PickUnit != "" {
		t.Fatal("Generate must not rewrite the caller's slice")
	}
}
