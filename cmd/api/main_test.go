package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	outboxrelay "github.com/claudioed/slotting-optimization/internal/adapters/outbound/outbox"
	"github.com/claudioed/slotting-optimization/internal/application/outbox"
	"github.com/claudioed/slotting-optimization/internal/config"
)

func quietLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// validEnv is the smallest environment the binary boots with; tests delete or
// overwrite single keys of it.
func validEnv() map[string]string {
	return map[string]string{
		"DATABASE_URL": "postgres://u@db:5432/x", "KAFKA_BROKERS": "a:9092,b:9092",
		"DEMAND_CONSUMER_GROUP": "g-demand", "PRODUCT_CONSUMER_GROUP": "g-product",
		"LAYOUT_CONSUMER_GROUP": "g-layout", "RELOCATION_CONSUMER_GROUP": "g-relocation",
		"PACK_LEVEL_CONSUMER_GROUP": "g-pack-level",
	}
}

func withEnv(overrides map[string]string) func(string) string {
	env := validEnv()
	for k, v := range overrides {
		if v == "-" {
			delete(env, k)
			continue
		}
		env[k] = v
	}
	return func(k string) string { return env[k] }
}

// buildAdapters refuses an empty DATABASE_URL by itself (no in-memory branch);
// the config package's own tests cover loadRuntime's refusal.
func TestNoDatabaseURLIsABootErrorEverywhere(t *testing.T) {
	_, closeFn, err := buildAdapters(context.Background(), "", quietLogger())
	if !errors.Is(err, config.ErrMissingDatabaseURL) {
		t.Fatalf("buildAdapters(\"\") = %v, want ErrMissingDatabaseURL", err)
	}
	closeFn()
}

func TestParseRelayInterval(t *testing.T) {
	cases := map[string]time.Duration{
		"": time.Second, "250ms": 250 * time.Millisecond, "3s": 3 * time.Second, "2": 2 * time.Second,
		"0.5": 500 * time.Millisecond, "abc": time.Second, "-1s": time.Second, "0": time.Second,
	}
	for raw, want := range cases {
		if got := parseRelayInterval(raw, quietLogger()); got != want {
			t.Errorf("parseRelayInterval(%q) = %v, want %v", raw, got, want)
		}
	}
}

type countingStore struct{ drains chan struct{} }

func (s countingStore) Drain(ctx context.Context, _, _ int, _ func(context.Context, outbox.Message) error) (int, error) {
	select {
	case s.drains <- struct{}{}:
	default:
	}
	return 0, ctx.Err()
}

var _ outboxrelay.Store = countingStore{}

// The relay is always on and always publishes to Kafka: with a down broker it
// starts without dialing, drains the store, and stops.
func TestStartOutboxRelay_StartsOverKafkaWithoutDialingAtBoot(t *testing.T) {
	t.Setenv("OUTBOX_RELAY_INTERVAL", "10ms")
	store := countingStore{drains: make(chan struct{}, 1)}
	start := time.Now()
	w, stop, err := startOutboxRelay(store, []string{"127.0.0.1:1"}, quietLogger())
	if err != nil {
		t.Fatalf("startOutboxRelay with an unreachable broker: %v", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("startup took %v; it must not wait on Kafka", elapsed)
	}
	select {
	case <-store.drains:
	case <-time.After(5 * time.Second):
		t.Fatal("the relay never drained")
	}
	stop()
	await(quietLogger(), w)
	select {
	case <-w.done:
	default:
		t.Fatal("the relay did not stop")
	}
}

func TestStartOutboxRelay_WithoutBrokersIsABootError(t *testing.T) {
	if _, _, err := startOutboxRelay(countingStore{}, nil, quietLogger()); !errors.Is(err, config.ErrMissingBrokers) {
		t.Fatalf("err = %v, want ErrMissingBrokers", err)
	}
}

func realtimeEnv() config.Runtime {
	rt, err := config.LoadRuntime(withEnv(map[string]string{"KAFKA_BROKERS": "127.0.0.1:1"}), consumerEnvs())
	if err != nil {
		panic(err)
	}
	return rt
}

// Every consumer starts (they are always on) without dialling the broker, and
// stops.
func TestStartConsumers_AlwaysStartsOneWorkerPerConsumerAndStops(t *testing.T) {
	start := time.Now()
	workers, stop := startConsumers(testDoubleAdapters(t), realtimeEnv(), quietLogger())
	if len(workers) != 5 {
		t.Fatalf("workers=%d, want 5", len(workers))
	}
	if time.Since(start) > time.Second {
		t.Fatal("starting the consumers must not dial Kafka")
	}
	stop()
	for _, w := range workers {
		select {
		case <-w.done:
		case <-time.After(10 * time.Second):
			t.Fatalf("%s did not stop", w.name)
		}
	}
}

// A worker that returns on its own is reported by name; a worker that was
// never started (nil) is ignored. cmd/api exits on it (serveUntilSignal).
func TestWatchWorkersReportsAWorkerThatDied(t *testing.T) {
	dead := &worker{name: "DEMAND_MODE consumer", done: make(chan struct{})}
	idle := &worker{name: "idle", done: make(chan struct{})}
	close(dead.done)
	select {
	case name := <-watchWorkers(nil, idle, dead):
		if name != "DEMAND_MODE consumer" {
			t.Fatalf("reported %q, want the dead worker", name)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a worker that returned on its own was not reported")
	}
}

func TestNewReadinessReportsTheDownDependency(t *testing.T) {
	up := func(context.Context) error { return nil }
	down := func(context.Context) error { return errors.New("connection refused") }
	deadBroker := []string{"127.0.0.1:1"}

	if got := newReadiness(up, deadBroker).Failing(context.Background()); len(got) != 1 || got[0] != "kafka" {
		t.Fatalf("failing = %v, want [kafka]", got)
	}
	if got := newReadiness(down, deadBroker).Failing(context.Background()); len(got) != 2 {
		t.Fatalf("failing = %v, want [postgres kafka]", got)
	}
}

func TestBuildServer_WiresEveryPort(t *testing.T) {
	ad := testDoubleAdapters(t)
	for name, wired := range map[string]bool{
		"plans": ad.plans != nil, "demand": ad.demand != nil, "profiles": ad.profiles != nil, "catalogue": ad.catalogue != nil,
		"outbox": ad.outbox != nil, "outboxStore": ad.outboxStore != nil, "processed": ad.processed != nil, "uow": ad.uow != nil,
		"executions": ad.executions != nil,
	} {
		if !wired {
			t.Errorf("port %s not wired", name)
		}
	}
	if relocationMetrics() == nil {
		t.Fatal("the relocation consumer must count its outcomes")
	}
	s, err := buildServer(ad, config.Config{DefaultSite: "SITE-1", ForwardZoneCodes: []string{"FWD"}}, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	for name, wired := range map[string]bool{
		"GeneratePlan": s.GeneratePlan != nil, "ApprovePlan": s.ApprovePlan != nil, "RejectPlan": s.RejectPlan != nil, "GetPlan": s.GetPlan != nil,
		"ListPlans": s.ListPlans != nil, "ListForwardSlots": s.ListForwardSlots != nil, "ListSkuVelocity": s.ListSkuVelocity != nil,
	} {
		if !wired {
			t.Errorf("use case %s not wired", name)
		}
	}
}

// The composition root over the TEST DOUBLES answers over HTTP: generate ->
// approve -> read the forward-slot map, with the configured default lookback
// applied. (The real Postgres/Kafka wiring is TestRunOnRealPostgresAndKafka.)
func TestCompositionRootServesThePlanFlow(t *testing.T) {
	ad := testDoubleAdapters(t)
	s, err := buildServer(ad, config.Config{DefaultSite: "SITE-1", LookbackDays: 7, ForwardZoneCodes: []string{"FWD"}}, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(routerFor(s))
	defer srv.Close()
	plan := call(t, srv, http.MethodPost, "/slot-plans", http.StatusCreated)
	if plan.id == "" || !strings.Contains(plan.body, `"siteId":"SITE-1"`) {
		t.Fatalf("plan = %s", plan.body)
	}
	call(t, srv, http.MethodPost, "/slot-plans/"+plan.id+"/approvals", http.StatusOK)
	call(t, srv, http.MethodGet, "/forward-slots", http.StatusOK)
	call(t, srv, http.MethodGet, "/sku-velocity", http.StatusOK)
	call(t, srv, http.MethodPost, "/slot-plans/"+plan.id+"/approvals", http.StatusConflict)
	if got := call(t, srv, http.MethodGet, "/slot-plans/"+plan.id, http.StatusOK); !strings.Contains(got.body, `"execution":{"movesTotal":`) {
		t.Fatalf("GET /slot-plans/{planId} must carry the execution progress: %s", got.body)
	}
}

func TestNewLogger(t *testing.T) {
	for _, lvl := range []string{"debug", "info", "warn", "error", "nonsense"} {
		if newLogger(lvl) == nil {
			t.Fatalf("newLogger(%q) = nil", lvl)
		}
	}
}
