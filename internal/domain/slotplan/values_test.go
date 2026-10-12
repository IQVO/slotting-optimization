package slotplan

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestValidToken(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want bool
	}{
		{"one char", "a", true},
		{"max length", strings.Repeat("a", 64), true},
		{"too long", strings.Repeat("a", 65), false},
		{"multi-byte at the limit counts runes", strings.Repeat("é", 64), true},
		{"multi-byte over the limit", strings.Repeat("é", 65), false},
		{"empty", "", false},
		{"space", "a b", false},
		{"tab", "a\tb", false},
		{"unicode space", "a\u00a0b", false},
		{"control", "a\x00b", false},
		{"slash", "a/b", false},
		{"dash and dot", "A-1.b_2", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := validToken(tt.in); got != tt.want {
				t.Fatalf("validToken(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

func TestNewPlanID(t *testing.T) {
	tests := []struct {
		in      string
		wantErr bool
	}{
		{"plan-1", false},
		{"plan-" + strings.Repeat("a", 59), false},
		{"plan-" + strings.Repeat("a", 60), true},
		{"plan-", true},
		{"plan", true},
		{"xplan-1", true},
		{"PLAN-1", true},
		{"", true},
		{"plan-a b", true},
		{"plan-a/b", true},
	}
	for _, tt := range tests {
		got, err := NewPlanID(tt.in)
		if tt.wantErr {
			if !errors.Is(err, ErrInvalidPlanID) || got != "" {
				t.Errorf("NewPlanID(%q) = %q, %v; want ErrInvalidPlanID", tt.in, got, err)
			}
			continue
		}
		if err != nil || string(got) != tt.in {
			t.Errorf("NewPlanID(%q) = %q, %v; want ok", tt.in, got, err)
		}
	}
}

func TestTokenConstructors(t *testing.T) {
	tests := []struct {
		name string
		make func(string) (string, error)
		good string
		bad  string
		want error
	}{
		{"site", func(s string) (string, error) { v, err := NewSiteID(s); return string(v), err }, "SITE-1", "a b", ErrInvalidSiteID},
		{"sku", func(s string) (string, error) { v, err := NewSKU(s); return string(v), err }, "SKU-1", "", ErrInvalidSKU},
		{"slot", func(s string) (string, error) { v, err := NewSlotCode(s); return string(v), err }, "FWD-01-02", "FWD/01", ErrInvalidSlotCode},
		{"policy", func(s string) (string, error) { v, err := NewPolicyName(s); return string(v), err }, string(PolicyABCVelocityV1), "abc velocity", ErrInvalidPolicy},
	}
	for _, tt := range tests {
		if v, err := tt.make(tt.good); err != nil || v != tt.good {
			t.Errorf("%s ok case: %q %v", tt.name, v, err)
		}
		if v, err := tt.make(tt.bad); !errors.Is(err, tt.want) || v != "" {
			t.Errorf("%s bad case: %q %v", tt.name, v, err)
		}
	}
}

func TestNewWindow(t *testing.T) {
	from := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	to := from.AddDate(0, 0, 28)
	w, err := NewWindow(from, to)
	if err != nil || !w.From.Equal(from) || !w.To.Equal(to) {
		t.Fatalf("NewWindow ok case: %+v %v", w, err)
	}
	local := time.FixedZone("BRT", -3*3600)
	w, err = NewWindow(from.In(local), to.In(local))
	if err != nil || w.From.Location() != time.UTC || w.To.Location() != time.UTC {
		t.Fatalf("window must be normalised to UTC: %+v %v", w, err)
	}
	// One nanosecond apart is the smallest legal window.
	if _, err := NewWindow(from, from.Add(time.Nanosecond)); err != nil {
		t.Fatalf("tiny window must be valid: %v", err)
	}
	for name, c := range map[string][2]time.Time{
		"zero from": {{}, to},
		"zero to":   {from, {}},
		"equal":     {from, from},
		"reversed":  {to, from},
	} {
		if _, err := NewWindow(c[0], c[1]); !errors.Is(err, ErrInvalidWindow) {
			t.Errorf("%s: want ErrInvalidWindow, got %v", name, err)
		}
	}
}

func checkEnum(t *testing.T, name string, valid func(string) bool, good, bad []string) {
	t.Helper()
	for _, g := range good {
		if !valid(g) {
			t.Errorf("%s %q must be valid", name, g)
		}
	}
	for _, b := range bad {
		if valid(b) {
			t.Errorf("%s %q must be invalid", name, b)
		}
	}
}

func TestEnumsValid(t *testing.T) {
	checkEnum(t, "state", func(s string) bool { return State(s).Valid() },
		[]string{"Draft", "Approved", "Rejected", "Superseded"}, []string{"", "draft", "Pending"})
	checkEnum(t, "class", func(s string) bool { return ABCClass(s).Valid() },
		[]string{"A", "B", "C"}, []string{"", "D", "a"})
	checkEnum(t, "kind", func(s string) bool { return MoveKind(s).Valid() },
		[]string{"Assign", "Relocate", "Vacate"}, []string{"", "Move", "assign"})
	checkEnum(t, "reason", func(s string) bool { return UnassignedReason(s).Valid() },
		[]string{"NoEligibleSlot", "NoPhysicalProfile", "NoCapacityFit"}, []string{"", "x", "NoSlot"})
}
