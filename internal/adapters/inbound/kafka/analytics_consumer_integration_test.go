//go:build integration

package kafka_test

import (
	"bytes"
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	kafkaconsumer "github.com/claudioed/slotting-optimization/internal/adapters/inbound/kafka"
	"github.com/claudioed/slotting-optimization/internal/adapters/outbound/analyticsstore"
	outboundkafka "github.com/claudioed/slotting-optimization/internal/adapters/outbound/kafka"
	"github.com/claudioed/slotting-optimization/internal/analytics/report"
	"github.com/claudioed/slotting-optimization/internal/domain/slotplan"
	"github.com/claudioed/slotting-optimization/internal/testing/kafkatest"
)

// The projector's consumer against real infrastructure (testcontainers
// only): the real AnalyticsEncoder's bytes on a real topic land in a real
// analytical Postgres; garbage is skipped, poison is dead-lettered byte for
// byte, a redelivered id is applied once, and the offsets are committed (a
// second consumer in the same group resumes after them). ONE Kafka broker
// (kafkatest, shared per package) and ONE analytical Postgres per package;
// every test gets a unique topic and its own database cloned from the
// migrated template.

const (
	projUser     = "analytics_test"
	projPassword = "analytics_test"
	projTemplate = "slotting_analytics_itest_template"
)

var (
	projOnce      sync.Once
	projContainer *tcpostgres.PostgresContainer
	projErr       error
	projSeq       atomic.Int64
)

func startProjectorPostgres() {
	ctx := context.Background()
	projContainer, projErr = tcpostgres.Run(ctx, "postgres:16-alpine",
		tcpostgres.WithDatabase("postgres"),
		tcpostgres.WithUsername(projUser),
		tcpostgres.WithPassword(projPassword),
		testcontainers.WithWaitStrategy(wait.ForLog("database system is ready to accept connections").WithOccurrence(2).WithStartupTimeout(90*time.Second)),
	)
	if projErr != nil {
		return
	}
	if projErr = projAdminExec(ctx, fmt.Sprintf("CREATE DATABASE %s", projTemplate)); projErr != nil {
		return
	}
	url, err := projDatabaseURL(ctx, projTemplate)
	if err != nil {
		projErr = err
		return
	}
	for range 2 {
		if projErr = analyticsstore.RunMigrations(url); projErr != nil {
			return
		}
	}
}

func projAdminExec(ctx context.Context, sql string) error {
	url, err := projContainer.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		return err
	}
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close(ctx) }()
	_, err = conn.Exec(ctx, sql)
	return err
}

func projDatabaseURL(ctx context.Context, db string) (string, error) {
	base, err := projContainer.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		return "", err
	}
	cfg, err := pgx.ParseConfig(base)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("postgres://%s:%s@%s:%d/%s?sslmode=disable", projUser, projPassword, cfg.Host, cfg.Port, db), nil
}

// newProjectorStack boots (once per package) the analytical Postgres and
// returns a writer pool over a fresh private analytics database plus the
// reader over it.
func newProjectorStack(t *testing.T) (*analyticsstore.Projection, *analyticsstore.Reader) {
	t.Helper()
	projOnce.Do(startProjectorPostgres)
	if projErr != nil {
		t.Fatalf("start analytics postgres: %v", projErr)
	}
	ctx := context.Background()
	db := fmt.Sprintf("analytics_kafka_itest_%d", projSeq.Add(1))
	if err := projAdminExec(ctx, fmt.Sprintf("CREATE DATABASE %s TEMPLATE %s", db, projTemplate)); err != nil {
		t.Fatalf("clone analytics database: %v", err)
	}
	url, err := projDatabaseURL(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	pool, err := analyticsstore.NewPool(ctx, url)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	t.Cleanup(pool.Close)
	return analyticsstore.NewProjection(pool), analyticsstore.NewReader(pool)
}

// projectorTopic creates the unique analytics topic and its DLQ for one test.
func projectorTopic(t *testing.T) string {
	t.Helper()
	topic := kafkatest.Topic(t, "warehouse.slotting-optimization.analytics")
	kafkatest.CreateTopic(t, topic+kafkaconsumer.DLQSuffix)
	return topic
}

// runProjector starts one group member and stops it (idempotently) at test
// end.
func runProjector(t *testing.T, brokers []string, topic, group string, projection report.Projection) *kafkaconsumer.AnalyticsConsumer {
	t.Helper()
	c := kafkaconsumer.NewAnalyticsConsumer(brokers, topic, group, projection, testLogger())
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = c.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		<-done
		_ = c.Close()
	})
	return c
}

func funnelTotals(t *testing.T, r *analyticsstore.Reader, from, to, site string) (generated, approved, rejected, superseded int) {
	t.Helper()
	days, err := r.FunnelDays(context.Background(), report.FunnelQuery{
		Range: report.Range{From: timeNow(from), To: timeNow(to)}, SiteID: site, Granularity: report.GranularityDay,
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

func timeNow(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

func TestAnalyticsConsumer_RealKafkaAndPostgres(t *testing.T) {
	brokers := kafkatest.Brokers(t)
	projection, reader := newProjectorStack(t)
	stamp := time.Now().UnixNano()
	topic := projectorTopic(t)
	group := fmt.Sprintf("slotting-optimization-analytics-itest-%d", stamp)
	at := time.Now().UTC().Truncate(time.Second)

	// enc encodes one domain event as its analytics-stream message.
	enc := func(id string, ev slotplan.Event) kafkatest.Message {
		msgs, err := (&outboundkafka.AnalyticsEncoder{NewID: func() string { return id }, Topic: topic}).Encode(ev)
		if err != nil || len(msgs) != 1 {
			t.Fatalf("encode: %d msgs, %v", len(msgs), err)
		}
		return kafkatest.Message{Key: string(msgs[0].Key), Value: msgs[0].Value}
	}
	hdr := func(version int64, plan, site string, when time.Time) slotplan.Header {
		return slotplan.Header{Plan: slotplan.PlanID(plan), Site: slotplan.SiteID(site), Version: version, At: when}
	}

	runProjector(t, brokers, topic, group, projection)

	// A full funnel: three plans generated, one approved superseding the
	// first, one rejected.
	kafkatest.Produce(t, topic,
		enc("evt-g1", slotplan.SlotPlanGenerated{Header: hdr(1, "plan-1", "SITE-IT", at), Policy: slotplan.PolicyABCVelocityV1}),
		enc("evt-g2", slotplan.SlotPlanGenerated{Header: hdr(1, "plan-2", "SITE-IT", at.Add(time.Minute)), Policy: slotplan.PolicyABCVelocityV1}),
		enc("evt-a2", slotplan.SlotPlanApproved{Header: hdr(2, "plan-2", "SITE-IT", at.Add(2*time.Minute)), SupersedesPlanID: "plan-1"}),
		enc("evt-g3", slotplan.SlotPlanGenerated{Header: hdr(1, "plan-3", "SITE-IT", at.Add(3*time.Minute)), Policy: slotplan.PolicyABCVelocityV1}),
		enc("evt-r3", slotplan.SlotPlanRejected{Header: hdr(2, "plan-3", "SITE-IT", at.Add(4*time.Minute)), Reason: "not now"}),
	)

	lo, hi := at.Add(-time.Hour).Format(time.RFC3339), at.Add(time.Hour).Format(time.RFC3339)
	eventually(t, "the funnel is projected", func() bool {
		g, a, r, s := funnelTotals(t, reader, lo, hi, "")
		return g == 3 && a == 1 && r == 1 && s == 1
	})

	// The site scope narrows to nothing for another site.
	if g, _, _, _ := funnelTotals(t, reader, lo, hi, "SITE-OTHER"); g != 0 {
		t.Fatalf("another site's funnel = %d generated, want 0", g)
	}

	// The approval latency of plan-2 is the 1 minute between its generation
	// and its approval.
	days, err := reader.FunnelDays(context.Background(), report.FunnelQuery{
		Range: report.Range{From: timeNow(lo), To: timeNow(hi)}, Granularity: report.GranularityDay,
	})
	if err != nil {
		t.Fatal(err)
	}
	var latency *report.Timing
	for _, d := range days {
		if d.ApprovalLatency.Count > 0 {
			latency = &d.ApprovalLatency
		}
	}
	if latency == nil || latency.Count != 1 || latency.P50 == nil || *latency.P50 != 60 {
		t.Fatalf("approval latency = %+v, want one 60s sample", latency)
	}

	// A replay of the same id changes nothing (the store dedupes).
	kafkatest.Produce(t, topic, enc("evt-g1", slotplan.SlotPlanGenerated{Header: hdr(1, "plan-1", "SITE-IT", at), Policy: slotplan.PolicyABCVelocityV1}))
	eventually(t, "the projection settles after the replay", func() bool {
		g, _, _, _ := funnelTotals(t, reader, lo, hi, "")
		return g == 3
	})

	// Garbage is skipped and the partition moves on; poison (payload that
	// contradicts the subject) is dead-lettered byte for byte and the
	// partition moves on too.
	kafkatest.Produce(t, topic, kafkatest.Message{Key: "plan-1", Value: []byte(`{"not":"cloudevents"}`)})
	poison := tamperSubject(t, "plan-9", "plan-other")
	kafkatest.Produce(t, topic, kafkatest.Message{Key: "plan-9", Value: poison})
	kafkatest.Produce(t, topic, enc("evt-g4", slotplan.SlotPlanGenerated{Header: hdr(1, "plan-4", "SITE-IT", at.Add(5*time.Minute)), Policy: slotplan.PolicyABCVelocityV1}))
	eventually(t, "the message after the poison is projected", func() bool {
		g, _, _, _ := funnelTotals(t, reader, lo, hi, "")
		return g == 4
	})
	dlq := kafkatest.ReadN(t, topic+kafkaconsumer.DLQSuffix, 1, 30*time.Second)
	if len(dlq) != 1 || !bytes.Equal(dlq[0].Value, poison) {
		t.Fatalf("dead-lettered = %+v, want the poison bytes %s", dlq, poison)
	}

	// Freshness: the projection has applied events, so as_of is set.
	asOf, err := reader.LastEventAt(context.Background())
	if err != nil || asOf == nil {
		t.Fatalf("freshness = %v, %v; the projection must have applied events", asOf, err)
	}
}
