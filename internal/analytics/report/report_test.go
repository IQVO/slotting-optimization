package report

import (
	"errors"
	"math"
	"testing"
	"time"
)

func at(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

func ptr(t time.Time) *time.Time { return &t }

func TestKindsAreTheThreePublishedEvents(t *testing.T) {
	got := Kinds()
	want := []Kind{KindSlotPlanGenerated, KindSlotPlanApproved, KindSlotPlanRejected}
	if len(got) != len(want) {
		t.Fatalf("kinds = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("kinds[%d] = %s, want %s", i, got[i], want[i])
		}
	}
	if (KindSlotPlanGenerated).Known() != true || (Kind("SlotPlanExecuted")).Known() != false {
		t.Fatal("Kind.Known must accept exactly the catalogue")
	}
}

func TestEventValidate(t *testing.T) {
	base := Event{Kind: KindSlotPlanGenerated, EventID: "e1", At: at("2026-10-05T10:00:00Z"), PlanID: "plan-1", SiteID: "SITE-1"}
	if err := base.Validate(); err != nil {
		t.Fatalf("valid event rejected: %v", err)
	}
	approved := base
	approved.Kind = KindSlotPlanApproved
	approved.SupersedesPlanID = "plan-0"
	if err := approved.Validate(); err != nil {
		t.Fatalf("approval superseding a plan rejected: %v", err)
	}
	for name, mutate := range map[string]func(*Event){
		"unknown kind":     func(e *Event) { e.Kind = "SlotPlanExploded" },
		"missing id":       func(e *Event) { e.EventID = "" },
		"missing time":     func(e *Event) { e.At = time.Time{} },
		"missing plan":     func(e *Event) { e.PlanID = "" },
		"missing site":     func(e *Event) { e.SiteID = "" },
		"wrong supersedia": func(e *Event) { e.SupersedesPlanID = "plan-0" },
	} {
		e := base
		if name == "wrong supersedia" {
			e.Kind = KindSlotPlanRejected // not an approval
		}
		mutate(&e)
		err := e.Validate()
		if !errors.Is(err, ErrInvalidEvent) {
			t.Fatalf("%s: err = %v, want ErrInvalidEvent", name, err)
		}
	}
}

func TestMilestonesOf(t *testing.T) {
	g := at("2026-10-05T10:00:00Z")
	approval := Event{Kind: KindSlotPlanApproved, At: g, PlanID: "plan-2", SupersedesPlanID: "plan-1"}
	if m := MilestonesOf(approval); !milestoneAt(m.Approved, g) || !milestoneAt(m.Superseded, g) {
		t.Fatalf("approval milestones = %+v; it marks approved and superseded", m)
	}
	if m := MilestonesOf(Event{Kind: KindSlotPlanApproved, At: g, PlanID: "plan-2"}); m.Superseded != nil || m.Approved == nil {
		t.Fatalf("a first approval offers no Superseded milestone: %+v", m)
	}
	if m := MilestonesOf(Event{Kind: KindSlotPlanGenerated, At: g}); !milestoneAt(m.Generated, g) || m.Approved != nil || m.Superseded != nil {
		t.Fatalf("generation milestones = %+v", m)
	}
	if m := MilestonesOf(Event{Kind: KindSlotPlanRejected, At: g}); !milestoneAt(m.Rejected, g) || m.Superseded != nil {
		t.Fatalf("rejection milestones = %+v", m)
	}
}

func milestoneAt(got *time.Time, want time.Time) bool {
	return got != nil && got.Equal(want)
}

func TestParseGranularity(t *testing.T) {
	for raw, want := range map[string]Granularity{"": GranularityDay, "day": GranularityDay} {
		got, err := ParseGranularity(raw)
		if err != nil || got != want {
			t.Fatalf("ParseGranularity(%q) = %v, %v; want %v", raw, got, err, want)
		}
	}
	if _, err := ParseGranularity("hour"); !errors.Is(err, ErrInvalidGranularity) {
		t.Fatalf("hour granularity err = %v, want ErrInvalidGranularity", err)
	}
}

func TestParseRange(t *testing.T) {
	now := at("2026-10-17T12:00:00Z")
	// No bounds: the 30 days ending now.
	rg, err := ParseRange("", "", now)
	if err != nil || !rg.From.Equal(at("2026-09-17T12:00:00Z")) || !rg.To.Equal(now) {
		t.Fatalf("default range = %v, %v", rg, err)
	}
	// Only to: from = to - 30d.
	rg, err = ParseRange("", "2026-10-10T00:00:00Z", now)
	if err != nil || !rg.From.Equal(at("2026-09-10T00:00:00Z")) || !rg.To.Equal(at("2026-10-10T00:00:00Z")) {
		t.Fatalf("to-only range = %v, %v", rg, err)
	}
	// Only from: to = now.
	rg, err = ParseRange("2026-10-01T00:00:00Z", "", now)
	if err != nil || !rg.From.Equal(at("2026-10-01T00:00:00Z")) || !rg.To.Equal(now) {
		t.Fatalf("from-only range = %v, %v", rg, err)
	}
	// Both, in UTC even when the input carries an offset.
	rg, err = ParseRange("2026-10-01T00:00:00-03:00", "2026-10-02T00:00:00-03:00", now)
	if err != nil || !rg.From.Equal(at("2026-10-01T03:00:00Z")) {
		t.Fatalf("offset range = %v, %v", rg, err)
	}
	for name, tc := range map[string]struct {
		from, to string
		want     error
	}{
		"bad from":        {"not-a-time", "", ErrInvalidFrom},
		"bad to":          {"", "not-a-time", ErrInvalidTo},
		"inverted":        {"2026-10-05T00:00:00Z", "2026-10-05T00:00:00Z", ErrEmptyRange},
		"equal bounds":    {"2026-10-05T00:00:01Z", "2026-10-05T00:00:00Z", ErrEmptyRange},
		"range too large": {"2025-09-01T00:00:00Z", "2026-10-05T00:00:00Z", ErrRangeTooLarge},
	} {
		_, err := ParseRange(tc.from, tc.to, now)
		if !errors.Is(err, tc.want) {
			t.Fatalf("%s: err = %v, want %v", name, err, tc.want)
		}
	}
	// Exactly 366 days is allowed.
	if _, err := ParseRange("2025-10-05T00:00:00Z", "2026-10-06T00:00:00Z", now); err != nil {
		t.Fatalf("366-day range rejected: %v", err)
	}
}

func TestDayWindows(t *testing.T) {
	// Partial first and last days, two whole days between.
	ws := DayWindows(Range{From: at("2026-10-05T10:00:00Z"), To: at("2026-10-08T06:30:00Z")})
	if len(ws) != 4 {
		t.Fatalf("windows = %d, want 4", len(ws))
	}
	wantEdges := [][2]string{
		{"2026-10-05T10:00:00Z", "2026-10-06T00:00:00Z"},
		{"2026-10-06T00:00:00Z", "2026-10-07T00:00:00Z"},
		{"2026-10-07T00:00:00Z", "2026-10-08T00:00:00Z"},
		{"2026-10-08T00:00:00Z", "2026-10-08T06:30:00Z"},
	}
	for i, w := range ws {
		lo, _ := time.Parse(time.RFC3339, wantEdges[i][0])
		hi, _ := time.Parse(time.RFC3339, wantEdges[i][1])
		if !w.From.Equal(lo) || !w.To.Equal(hi) || !w.Day.Equal(lo.Truncate(24*time.Hour).UTC()) {
			t.Fatalf("window %d = [%v, %v) day %v, want [%v, %v)", i, w.From, w.To, w.Day, lo, hi)
		}
		if !w.Day.Equal(time.Date(2026, 10, 5+i, 0, 0, 0, 0, time.UTC)) {
			t.Fatalf("window %d day = %v", i, w.Day)
		}
	}
	// One whole day exactly.
	ws = DayWindows(Range{From: at("2026-10-05T00:00:00Z"), To: at("2026-10-06T00:00:00Z")})
	if len(ws) != 1 || !ws[0].From.Equal(at("2026-10-05T00:00:00Z")) || !ws[0].To.Equal(at("2026-10-06T00:00:00Z")) {
		t.Fatalf("whole-day windows = %+v", ws)
	}
	// An instant-wide window still yields its day.
	ws = DayWindows(Range{From: at("2026-10-05T10:00:00Z"), To: at("2026-10-05T10:00:01Z")})
	if len(ws) != 1 || !ws[0].From.Equal(at("2026-10-05T10:00:00Z")) || !ws[0].To.Equal(at("2026-10-05T10:00:01Z")) {
		t.Fatalf("instant windows = %+v", ws)
	}
	// Empty and inverted ranges yield none.
	if ws := DayWindows(Range{From: at("2026-10-05T00:00:00Z"), To: at("2026-10-05T00:00:00Z")}); ws != nil {
		t.Fatalf("empty range windows = %+v", ws)
	}
	if ws := DayWindows(Range{From: at("2026-10-06T00:00:00Z"), To: at("2026-10-05T00:00:00Z")}); ws != nil {
		t.Fatalf("inverted range windows = %+v", ws)
	}
}

func TestComputeFreshness(t *testing.T) {
	now := at("2026-10-07T12:00:00Z")
	f := ComputeFreshness(nil, now)
	if f.AsOf != nil || f.LagSeconds != nil {
		t.Fatalf("no-event freshness = %+v, want nils", f)
	}
	f = ComputeFreshness(ptr(at("2026-10-07T11:59:55Z")), now)
	if f.AsOf == nil || !f.AsOf.Equal(at("2026-10-07T11:59:55Z")) || f.LagSeconds == nil || math.Abs(*f.LagSeconds-5) > 1e-9 {
		t.Fatalf("freshness = %+v", f)
	}
	// A clock behind the event reads as zero lag, never negative.
	f = ComputeFreshness(ptr(at("2026-10-07T12:00:05Z")), now)
	if f.LagSeconds == nil || *f.LagSeconds != 0 {
		t.Fatalf("skew freshness lag = %v, want 0", f.LagSeconds)
	}
}

func TestPercentileLinearInterpolation(t *testing.T) {
	if p := Percentile(nil, 0.5); p != 0 {
		t.Fatalf("empty percentile = %v, want 0", p)
	}
	sample := []float64{10, 20, 30, 40}
	if p := Percentile(sample, 0.5); p != 25 {
		t.Fatalf("p50 = %v, want 25", p)
	}
	if p := Percentile(sample, 0.95); math.Abs(p-38.5) > 1e-9 {
		t.Fatalf("p95 = %v, want 38.5", p)
	}
	if p := Percentile(sample, 0); p != 10 {
		t.Fatalf("min = %v, want 10", p)
	}
	if p := Percentile(sample, 1); p != 40 {
		t.Fatalf("max = %v, want 40", p)
	}
	if p := Percentile([]float64{7}, 0.95); p != 7 {
		t.Fatalf("single-sample percentile = %v, want 7", p)
	}
}

func TestTimingOfSortsAndSummarises(t *testing.T) {
	empty := TimingOf(nil)
	if empty.Count != 0 || empty.P50 != nil || empty.P95 != nil {
		t.Fatalf("empty timing = %+v", empty)
	}
	// Deliberately unsorted input.
	tm := TimingOf([]float64{40, 10, 30, 20})
	if tm.Count != 4 || tm.P50 == nil || *tm.P50 != 25 || tm.P95 == nil || math.Abs(*tm.P95-38.5) > 1e-9 {
		t.Fatalf("timing = %+v", tm)
	}
	// The input slice is not mutated.
	in := []float64{40, 10}
	_ = TimingOf(in)
	if in[0] != 40 || in[1] != 10 {
		t.Fatalf("TimingOf mutated its input: %v", in)
	}
}

func TestSeconds(t *testing.T) {
	if s := Seconds(90 * time.Minute); s != 5400 {
		t.Fatalf("Seconds = %v, want 5400", s)
	}
}
