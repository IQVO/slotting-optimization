//go:build integration

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/claudioed/slotting-optimization/internal/adapters/outbound/analyticsstore"
	"github.com/claudioed/slotting-optimization/internal/analytics/report"
)

// ONE empty analytical Postgres per test binary (testcontainers only; no
// DATABASE_URL, no localhost DSN, no skip). The database starts with NO
// schema: the repo's own analytics migrations are applied here, so the facts
// are seeded exactly the way cmd/slotting-projector leaves them.

var (
	pgOnce      sync.Once
	pgContainer *tcpostgres.PostgresContainer
	pgErr       error
	pgSeq       atomic.Int64
)

func TestMain(m *testing.M) {
	code := m.Run()
	if pgContainer != nil {
		_ = testcontainers.TerminateContainer(pgContainer)
	}
	os.Exit(code)
}

func startAnalyticsPostgres() {
	pgContainer, pgErr = tcpostgres.Run(context.Background(), "postgres:16-alpine",
		tcpostgres.WithDatabase("postgres"),
		tcpostgres.WithUsername("analytics_itest"),
		tcpostgres.WithPassword("analytics_itest"),
		testcontainers.WithWaitStrategy(wait.ForLog("database system is ready to accept connections").
			WithOccurrence(2).WithStartupTimeout(90*time.Second)),
	)
}

// migratedAnalyticsURL creates a database, applies the embedded analytics
// migrations to it and returns the direct DSN.
func migratedAnalyticsURL(t *testing.T) string {
	t.Helper()
	pgOnce.Do(startAnalyticsPostgres)
	if pgErr != nil {
		t.Fatalf("start the analytics postgres: %v", pgErr)
	}
	ctx := context.Background()
	name := fmt.Sprintf("slotting_reports_itest_%d", pgSeq.Add(1))
	base, err := pgContainer.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	admin, err := pgx.Connect(ctx, base)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = admin.Close(ctx) }()
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatalf("create the analytics database: %v", err)
	}
	cfg, err := pgx.ParseConfig(base)
	if err != nil {
		t.Fatal(err)
	}
	url := fmt.Sprintf("postgres://analytics_itest:analytics_itest@%s:%d/%s?sslmode=disable", cfg.Host, cfg.Port, name)
	if err := analyticsstore.RunMigrations(url); err != nil {
		t.Fatalf("apply the analytics migrations: %v", err)
	}
	return url
}

func freeAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Close() }()
	return l.Addr().String()
}

func getJSON(t *testing.T, url string) (int, map[string]any) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		return 0, nil
	}
	defer func() { _ = resp.Body.Close() }()
	var body map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&body)
	return resp.StatusCode, body
}

func waitHealthy(t *testing.T, url string) {
	t.Helper()
	for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
		if code, _ := getJSON(t, url); code == http.StatusOK {
			return
		}
	}
	t.Fatalf("%s never answered 200", url)
}

// seedFacts writes a full review funnel with the repo's own analytics writer
// (the Projection cmd/slotting-projector uses): plan-r1 and plan-r2
// generated, plan-r2 approved superseding plan-r1, plan-r3 generated then
// rejected. It returns the newest applied event time (the rejection's).
func seedFacts(t *testing.T, url string) time.Time {
	t.Helper()
	ctx := context.Background()
	pool, err := analyticsstore.NewPool(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	projection := analyticsstore.NewProjection(pool)
	at := time.Date(2026, 11, 13, 9, 0, 0, 0, time.UTC)
	seed := func(id string, kind report.Kind, plan string, when time.Time, supersedes string) {
		t.Helper()
		if _, err := projection.Apply(ctx, report.Event{
			Kind: kind, EventID: id, At: when, PlanID: plan, SiteID: "SITE-REPORTS-IT", SupersedesPlanID: supersedes,
		}); err != nil {
			t.Fatalf("seed %s: %v", id, err)
		}
	}
	seed("evt-rpt-g1", report.KindSlotPlanGenerated, "plan-r1", at, "")
	seed("evt-rpt-g2", report.KindSlotPlanGenerated, "plan-r2", at.Add(time.Minute), "")
	seed("evt-rpt-a2", report.KindSlotPlanApproved, "plan-r2", at.Add(2*time.Minute), "plan-r1")
	seed("evt-rpt-g3", report.KindSlotPlanGenerated, "plan-r3", at.Add(3*time.Minute), "")
	seed("evt-rpt-r3", report.KindSlotPlanRejected, "plan-r3", at.Add(4*time.Minute), "")
	return at.Add(4 * time.Minute)
}

// The real entry point over an analytically-migrated EMPTY database seeded
// with the repo's own writer: run() serves the documented reports (ADR 0006,
// /reports/slot-plan-funnel and /reports/freshness) from facts it did not
// write, and a SIGTERM drains to a clean nil exit.
func TestReportsServeTheSeededFunnelAndDrainCleanly(t *testing.T) {
	url := migratedAnalyticsURL(t)
	newest := seedFacts(t, url)

	addr := freeAddr(t)
	t.Setenv("ANALYTICS_DATABASE_URL", url)
	t.Setenv("HTTP_ADDR", addr)
	t.Setenv("LOG_LEVEL", "error")
	// An unreachable collector: telemetry export must never block boot.
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "127.0.0.1:1")

	// run() takes no context: it installs its own SIGINT/SIGTERM
	// NotifyContext. The handler registered here FIRST keeps the TEST binary
	// alive when the signal is raised below; it is deliberately never
	// stopped, because signal.Stop would restore the default disposition and
	// a later signal would kill the binary mid-test.
	term := make(chan os.Signal, 1)
	signal.Notify(term, syscall.SIGTERM)
	done := make(chan error, 1)
	go func() { done <- run() }()

	from, to := "2026-11-13T08:00:00Z", "2026-11-13T10:00:00Z"
	waitHealthy(t, "http://"+addr+"/healthz")

	// The documented funnel answer: one day, the seeded counts and the one
	// 60s approval-latency sample (plan-r2 generated at 09:01, approved at
	// 09:02).
	code, body := getJSON(t, "http://"+addr+"/reports/slot-plan-funnel?from="+from+"&to="+to+"&granularity=day")
	if code != http.StatusOK {
		t.Fatalf("slot-plan-funnel = %d %v", code, body)
	}
	days, _ := body["days"].([]any)
	if len(days) != 1 {
		t.Fatalf("days = %v, want the single seeded day", body)
	}
	day := days[0].(map[string]any)
	if day["day"] != "2026-11-13" || day["plans_generated"] != 3.0 || day["plans_approved"] != 1.0 ||
		day["plans_rejected"] != 1.0 || day["supersessions"] != 1.0 {
		t.Fatalf("funnel day = %v", day)
	}
	latency, _ := day["approval_latency"].(map[string]any)
	if latency["count"] != 1.0 || latency["p50_seconds"] != 60.0 {
		t.Fatalf("approval latency = %v, want one 60s sample", latency)
	}

	// The siteId scope narrows to an empty funnel for another site.
	_, other := getJSON(t, "http://"+addr+"/reports/slot-plan-funnel?from="+from+"&to="+to+"&siteId=SITE-OTHER")
	var generated int
	for _, d := range other["days"].([]any) {
		generated += int(d.(map[string]any)["plans_generated"].(float64))
	}
	if generated != 0 {
		t.Fatalf("another site's funnel = %d generated, want 0", generated)
	}

	// The documented freshness answer: as_of is the newest applied event time
	// and the lag is measured against now.
	code, fresh := getJSON(t, "http://"+addr+"/reports/freshness")
	if code != http.StatusOK || fresh["as_of"] != newest.Format(time.RFC3339) || fresh["lag_seconds"] == nil {
		t.Fatalf("freshness = %d %v, want as_of %s and a lag", code, fresh, newest.Format(time.RFC3339))
	}

	// Clean drain: SIGTERM reaches run()'s own handler; the copy caught here
	// proves the test binary survived it.
	if err := syscall.Kill(syscall.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	<-term
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("a SIGTERM must drain the reports server cleanly, got %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the reports server did not stop")
	}
}
