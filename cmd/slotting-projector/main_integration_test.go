//go:build integration

package main

import (
	"context"
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
	outboundkafka "github.com/claudioed/slotting-optimization/internal/adapters/outbound/kafka"
	"github.com/claudioed/slotting-optimization/internal/analytics/report"
	"github.com/claudioed/slotting-optimization/internal/domain/slotplan"
	"github.com/claudioed/slotting-optimization/internal/testing/kafkatest"
)

// ONE empty analytical Postgres per test binary. Unlike pgtest (whose
// databases carry the OLTP migrations), these databases carry NO schema:
// run() itself must apply the embedded analytics/migrations over the direct
// DSN (cmd/slotting-projector/main.go openAnalyticsPool; configuration.md,
// ANALYTICS_DATABASE_URL). One Kafka broker is shared from kafkatest.

var (
	pgOnce      sync.Once
	pgContainer *tcpostgres.PostgresContainer
	pgErr       error
	pgSeq       atomic.Int64
)

func TestMain(m *testing.M) {
	code := m.Run()
	kafkatest.Shutdown()
	if pgContainer != nil {
		_ = testcontainers.TerminateContainer(pgContainer)
	}
	os.Exit(code)
}

func startEmptyAnalyticsPostgres() {
	pgContainer, pgErr = tcpostgres.Run(context.Background(), "postgres:16-alpine",
		tcpostgres.WithDatabase("postgres"),
		tcpostgres.WithUsername("analytics_itest"),
		tcpostgres.WithPassword("analytics_itest"),
		testcontainers.WithWaitStrategy(wait.ForLog("database system is ready to accept connections").
			WithOccurrence(2).WithStartupTimeout(90*time.Second)),
	)
}

// emptyAnalyticsURL creates a database with NO schema on the shared container
// and returns its direct DSN.
func emptyAnalyticsURL(t *testing.T) string {
	t.Helper()
	pgOnce.Do(startEmptyAnalyticsPostgres)
	if pgErr != nil {
		t.Fatalf("start the empty analytics postgres: %v", pgErr)
	}
	ctx := context.Background()
	name := fmt.Sprintf("slotting_projector_itest_%d", pgSeq.Add(1))
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
		t.Fatalf("create the empty analytics database: %v", err)
	}
	cfg, err := pgx.ParseConfig(base)
	if err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf("postgres://analytics_itest:analytics_itest@%s:%d/%s?sslmode=disable", cfg.Host, cfg.Port, name)
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

func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(90 * time.Second); time.Now().Before(deadline); time.Sleep(200 * time.Millisecond) {
		if cond() {
			return
		}
	}
	t.Fatalf("timed out waiting for %s", what)
}

// funnelTotals sums the per-day funnel the reader serves, over a range and
// (optionally) one site.
func funnelTotals(t *testing.T, r *analyticsstore.Reader, from, to, site string) (generated, approved, rejected, superseded int) {
	t.Helper()
	days, err := r.FunnelDays(context.Background(), report.FunnelQuery{
		Range: report.Range{From: parseTime(t, from), To: parseTime(t, to)}, SiteID: site, Granularity: report.GranularityDay,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range days {
		generated += d.Generated
		approved += d.Approved
		rejected += d.Rejected
		superseded += d.Supersessions
	}
	return generated, approved, rejected, superseded
}

func parseTime(t *testing.T, s string) time.Time {
	t.Helper()
	at, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatalf("parse %s: %v", s, err)
	}
	return at
}

// The real entry point over real infrastructure (testcontainers only): run()
// migrates an EMPTY analytical database itself, serves /healthz and /readyz on
// the admin port, and its consumer projects the repo's own AnalyticsEncoder
// bytes from the fixed analytics topic into the documented funnel facts
// (ADR 0006); a SIGTERM drains to a clean nil exit.
func TestProjectorBootsMigratesProjectsAndDrainsCleanly(t *testing.T) {
	url := emptyAnalyticsURL(t)
	brokers := kafkatest.Brokers(t)
	// run() consumes the FIXED topic constant (outboundkafka.AnalyticsTopic,
	// ADR 0006), so the test's private broker gets exactly that topic and its
	// DLQ, and a fresh group id keeps this run's offsets its own.
	kafkatest.CreateTopic(t, outboundkafka.AnalyticsTopic)
	kafkatest.CreateTopic(t, outboundkafka.AnalyticsTopic+".dlq")

	addr := freeAddr(t)
	t.Setenv("ANALYTICS_DATABASE_URL", url)
	t.Setenv("KAFKA_BROKERS", brokers[0])
	t.Setenv("ANALYTICS_CONSUMER_GROUP", fmt.Sprintf("slotting-projector-itest-%d", time.Now().UnixNano()))
	t.Setenv("ADMIN_ADDR", addr)
	t.Setenv("LOG_LEVEL", "error")
	// An unreachable collector: telemetry export must never block boot
	// (cmd/slotting-projector/main.go setupTelemetry).
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

	waitUntil(t, "/readyz 200", func() bool {
		resp, err := http.Get("http://" + addr + "/readyz")
		if err != nil {
			return false
		}
		_ = resp.Body.Close()
		return resp.StatusCode == http.StatusOK
	})

	// The repo's own analytics encoder (the same bytes the outbox relay
	// publishes for cmd/api's analytics rows, ADR 0006) writes a full review
	// funnel: plan-r1 and plan-r2 generated, plan-r2 approved superseding
	// plan-r1, plan-r3 generated then rejected.
	at := time.Date(2026, 11, 13, 9, 0, 0, 0, time.UTC)
	hdr := func(version int64, plan string, when time.Time) slotplan.Header {
		return slotplan.Header{Plan: slotplan.PlanID(plan), Site: slotplan.SiteID("SITE-PROJECTOR-IT"), Version: version, At: when}
	}
	msgs, err := (&outboundkafka.AnalyticsEncoder{}).Encode(
		slotplan.SlotPlanGenerated{Header: hdr(1, "plan-r1", at), Policy: slotplan.PolicyABCVelocityV1},
		slotplan.SlotPlanGenerated{Header: hdr(1, "plan-r2", at.Add(time.Minute)), Policy: slotplan.PolicyABCVelocityV1},
		slotplan.SlotPlanApproved{Header: hdr(2, "plan-r2", at.Add(2*time.Minute)), SupersedesPlanID: slotplan.PlanID("plan-r1")},
		slotplan.SlotPlanGenerated{Header: hdr(1, "plan-r3", at.Add(3*time.Minute)), Policy: slotplan.PolicyABCVelocityV1},
		slotplan.SlotPlanRejected{Header: hdr(2, "plan-r3", at.Add(4*time.Minute)), Reason: "not now"},
	)
	if err != nil {
		t.Fatal(err)
	}
	encoded := make([]kafkatest.Message, len(msgs))
	for i, m := range msgs {
		encoded[i] = kafkatest.Message{Key: string(m.Key), Value: m.Value}
	}
	kafkatest.Produce(t, outboundkafka.AnalyticsTopic, encoded...)

	// The documented projection (ADR 0006): the funnel over slot_plan_facts,
	// read with the repo's own reader over the database run() migrated.
	pool, err := analyticsstore.NewPool(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	reader := analyticsstore.NewReader(pool)
	lo, hi := at.Add(-time.Hour).Format(time.RFC3339), at.Add(time.Hour).Format(time.RFC3339)
	waitUntil(t, "the funnel is projected", func() bool {
		g, a, r, s := funnelTotals(t, reader, lo, hi, "")
		return g == 3 && a == 1 && r == 1 && s == 1
	})

	// The site scope narrows to nothing for another site (siteId filter).
	if g, _, _, _ := funnelTotals(t, reader, lo, hi, "SITE-OTHER"); g != 0 {
		t.Fatalf("another site's funnel = %d generated, want 0", g)
	}

	// Freshness: the newest applied CloudEvents time is the rejection's.
	asOf, err := reader.LastEventAt(context.Background())
	if err != nil || asOf == nil || !asOf.Equal(at.Add(4*time.Minute)) {
		t.Fatalf("freshness = %v, %v; want the rejection at %v", asOf, err, at.Add(4*time.Minute))
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
			t.Fatalf("a SIGTERM must drain the projector cleanly, got %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the projector did not stop")
	}
}
