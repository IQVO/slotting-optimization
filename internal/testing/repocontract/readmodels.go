package repocontract

import (
	"context"
	"testing"
	"time"

	"github.com/claudioed/slotting-optimization/internal/application/ports"
	"github.com/claudioed/slotting-optimization/internal/application/repository"
	"github.com/claudioed/slotting-optimization/internal/domain/planning"
	"github.com/claudioed/slotting-optimization/internal/domain/slotplan"
)

func line(order string, n int, site, sku string, units int64, due time.Time, active bool) repository.DemandLine {
	return repository.DemandLine{
		SourceOrderID: order, LineNo: n, Site: slotplan.SiteID(site), SKU: slotplan.SKU(sku),
		Units: units, DueAt: due, Active: active,
	}
}

// DemandLedger runs the DemandLedger contract. newLedger must return an
// EMPTY ledger on every call.
func DemandLedger(t *testing.T, newLedger func(t *testing.T) ports.DemandLedger) {
	t.Helper()
	cases := map[string]func(t *testing.T, d ports.DemandLedger){
		"velocity counts active lines and sums units per sku": velocityCounts,
		"the window is half open on due_at":                   velocityWindow,
		"last writer wins per source line":                    lastWriterWins,
		"a removed line stops counting but stays":             removedLines,
		"velocity is per site":                                velocityPerSite,
		"velocity is ordered picks, units, sku":               velocityOrder,
		"an empty ledger has no velocity":                     velocityEmpty,
		"line units gcd per sku over active lines":            lineUnitsGCD,
	}
	for name, fn := range cases {
		t.Run(name, func(t *testing.T) { fn(t, newLedger(t)) })
	}
}

func apply(t *testing.T, d ports.DemandLedger, ls ...repository.DemandLine) {
	t.Helper()
	for _, l := range ls {
		must(t, d.Apply(context.Background(), l))
	}
}

func velocity(t *testing.T, d ports.DemandLedger, site string, from, to time.Time) []planning.SkuVelocity {
	t.Helper()
	got, err := d.Velocity(context.Background(), slotplan.SiteID(site), from, to)
	must(t, err)
	return got
}

func sv(sku string, picks, units int64) planning.SkuVelocity {
	return planning.SkuVelocity{SKU: slotplan.SKU(sku), Picks: picks, Units: units}
}

func velocityCounts(t *testing.T, d ports.DemandLedger) {
	due := T0.Add(-time.Hour)
	apply(t, d,
		line("o1", 1, "S", "A", 5, due, true), line("o1", 2, "S", "A", 7, due, true), line("o2", 1, "S", "B", 2, due, true))
	got := velocity(t, d, "S", T0.Add(-24*time.Hour), T0)
	if !equalSlice(got, []planning.SkuVelocity{sv("A", 2, 12), sv("B", 1, 2)}) {
		t.Fatalf("velocity = %+v", got)
	}
}

func velocityWindow(t *testing.T, d ports.DemandLedger) {
	from, to := T0.Add(-24*time.Hour), T0
	apply(t, d,
		line("o1", 1, "S", "AT-FROM", 1, from, true),
		line("o2", 1, "S", "AT-TO", 1, to, true),
		line("o3", 1, "S", "BEFORE", 1, from.Add(-time.Nanosecond), true),
		line("o4", 1, "S", "INSIDE", 1, to.Add(-time.Nanosecond), true))
	got := velocity(t, d, "S", from, to)
	if !equalSlice(got, []planning.SkuVelocity{sv("AT-FROM", 1, 1), sv("INSIDE", 1, 1)}) {
		t.Fatalf("[from, to) must include from and exclude to: %+v", got)
	}
}

func lastWriterWins(t *testing.T, d ports.DemandLedger) {
	due := T0.Add(-time.Hour)
	apply(t, d, line("o1", 1, "S", "A", 5, due, true), line("o1", 1, "S", "B", 9, due, true))
	got := velocity(t, d, "S", T0.Add(-24*time.Hour), T0)
	if !equalSlice(got, []planning.SkuVelocity{sv("B", 1, 9)}) {
		t.Fatalf("the second message for the same line replaces the first: %+v", got)
	}
}

func removedLines(t *testing.T, d ports.DemandLedger) {
	due := T0.Add(-time.Hour)
	apply(t, d, line("o1", 1, "S", "A", 5, due, true), line("o1", 1, "S", "A", 0, due, false))
	if got := velocity(t, d, "S", T0.Add(-24*time.Hour), T0); len(got) != 0 {
		t.Fatalf("a REMOVED line must not count: %+v", got)
	}
	apply(t, d, line("o1", 1, "S", "A", 5, due, true)) // an active message later reactivates it
	if got := velocity(t, d, "S", T0.Add(-24*time.Hour), T0); !equalSlice(got, []planning.SkuVelocity{sv("A", 1, 5)}) {
		t.Fatalf("last writer wins also for reactivation: %+v", got)
	}
}

func velocityPerSite(t *testing.T, d ports.DemandLedger) {
	due := T0.Add(-time.Hour)
	apply(t, d, line("o1", 1, "S1", "A", 5, due, true), line("o2", 1, "S2", "B", 5, due, true))
	got := velocity(t, d, "S2", T0.Add(-24*time.Hour), T0)
	if !equalSlice(got, []planning.SkuVelocity{sv("B", 1, 5)}) {
		t.Fatalf("velocity = %+v", got)
	}
}

func velocityOrder(t *testing.T, d ports.DemandLedger) {
	due := T0.Add(-time.Hour)
	apply(t, d,
		line("o1", 1, "S", "C", 1, due, true),
		line("o2", 1, "S", "B", 10, due, true), line("o2", 2, "S", "B", 10, due, true),
		line("o3", 1, "S", "A", 10, due, true), line("o3", 2, "S", "A", 10, due, true),
		line("o4", 1, "S", "D", 50, due, true), line("o4", 2, "S", "D", 50, due, true))
	got := velocity(t, d, "S", T0.Add(-24*time.Hour), T0)
	want := []planning.SkuVelocity{sv("D", 2, 100), sv("A", 2, 20), sv("B", 2, 20), sv("C", 1, 1)}
	if !equalSlice(got, want) {
		t.Fatalf("picks desc, units desc, sku asc:\n got %+v\nwant %+v", got, want)
	}
}

func lineUnitsGCD(t *testing.T, d ports.DemandLedger) {
	due := T0.Add(-time.Hour)
	from, to := T0.Add(-24*time.Hour), T0
	apply(t, d,
		line("o1", 1, "S", "CASED", 12, due, true), line("o1", 2, "S", "CASED", 36, due, true), line("o1", 3, "S", "CASED", 24, due, true),
		line("o2", 1, "S", "MIXED", 12, due, true), line("o2", 2, "S", "MIXED", 18, due, true),
		line("o3", 1, "S", "ONE", 7, due, true), line("o3", 2, "S", "ONE", 7, due, true),
		line("o4", 1, "S", "GONE", 6, due, false),
		line("o5", 1, "S", "OUTSIDE", 6, to, true),
		line("o6", 1, "OTHER", "ELSEWHERE", 6, due, true),
		line("o7", 1, "S", "ZERO", 0, due, true))
	got, err := d.LineUnitsGCD(context.Background(), "S", from, to)
	must(t, err)
	want := map[slotplan.SKU]int64{"CASED": 12, "MIXED": 6, "ONE": 7}
	if len(got) != len(want) {
		t.Fatalf("gcd = %+v, want %+v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("gcd = %+v, want %+v", got, want)
		}
	}
	apply(t, d, line("o1", 3, "S", "CASED", 0, due, false), line("o8", 1, "S", "CASED", 5, due, true))
	got, err = d.LineUnitsGCD(context.Background(), "S", from, to)
	must(t, err)
	if got["CASED"] != 1 {
		t.Fatalf("a new line of 5 in 12s and 36s brings the gcd to 1: %+v", got)
	}
}

func velocityEmpty(t *testing.T, d ports.DemandLedger) {
	if got := velocity(t, d, "S", T0.Add(-24*time.Hour), T0); len(got) != 0 {
		t.Fatalf("got %+v", got)
	}
}
