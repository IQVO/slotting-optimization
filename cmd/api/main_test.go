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
)

func quietLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// validEnv is the smallest environment the binary boots with; tests delete or
// overwrite single keys of it.
func validEnv() map[string]string {
	return map[string]string{
		"DATABASE_URL": "postgres://u@db:5432/x", "KAFKA_BROKERS": "a:9092,b:9092",
		"DEMAND_CONSUMER_GROUP": "g-demand", "PRODUCT_CONSUMER_GROUP": "g-product",
		"LAYOUT_CONSUMER_GROUP": "g-layout", "RELOCATION_CONSUMER_GROUP": "g-relocation",
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

func TestLoadRuntimeAcceptsTheMinimalRealEnvironment(t *testing.T) {
	rt, err := loadRuntime(withEnv(nil))
	if err != nil || len(rt.brokers) != 2 || rt.brokers[1] != "b:9092" {
		t.Fatalf("rt = %+v, err = %v", rt, err)
	}
	for env, want := range map[string]string{"DEMAND_CONSUMER_GROUP": "g-demand", "PRODUCT_CONSUMER_GROUP": "g-product", "LAYOUT_CONSUMER_GROUP": "g-layout", "RELOCATION_CONSUMER_GROUP": "g-relocation"} {
		if rt.groups[env] != want {
			t.Errorf("group %s = %q, want %q", env, rt.groups[env], want)
		}
	}
	// The explicit values the chart renders are accepted too.
	explicit := map[string]string{"DEMAND_MODE": "kafka", "PRODUCT_MODE": "kafka", "LAYOUT_MODE": "kafka", "RELOCATION_MODE": "kafka", "EVENT_PUBLISHER": "kafka"}
	if _, err := loadRuntime(withEnv(explicit)); err != nil {
		t.Fatalf("explicit kafka modes: %v", err)
	}
}

// Every refused environment is a boot error whose message names the variable
// (ADR 0011), raised by loadRuntime before anything is opened or bound.
func TestLoadRuntimeRefusesWithAnErrorNamingTheVariable(t *testing.T) {
	cases := []struct {
		name     string
		override map[string]string
		wantVar  string
	}{
		{"no DATABASE_URL", map[string]string{"DATABASE_URL": "-"}, "DATABASE_URL"},
		{"no KAFKA_BROKERS", map[string]string{"KAFKA_BROKERS": "-"}, "KAFKA_BROKERS"},
		{"blank KAFKA_BROKERS", map[string]string{"KAFKA_BROKERS": " , "}, "KAFKA_BROKERS"},
		{"no DEMAND_CONSUMER_GROUP", map[string]string{"DEMAND_CONSUMER_GROUP": "-"}, "DEMAND_CONSUMER_GROUP"},
		{"no PRODUCT_CONSUMER_GROUP", map[string]string{"PRODUCT_CONSUMER_GROUP": "-"}, "PRODUCT_CONSUMER_GROUP"},
		{"no LAYOUT_CONSUMER_GROUP", map[string]string{"LAYOUT_CONSUMER_GROUP": "-"}, "LAYOUT_CONSUMER_GROUP"},
		{"no RELOCATION_CONSUMER_GROUP", map[string]string{"RELOCATION_CONSUMER_GROUP": "-"}, "RELOCATION_CONSUMER_GROUP"},
		{"DEMAND_MODE=permissive", map[string]string{"DEMAND_MODE": "permissive"}, "DEMAND_MODE=permissive is not supported"},
		{"PRODUCT_MODE=permissive", map[string]string{"PRODUCT_MODE": "permissive"}, "PRODUCT_MODE=permissive is not supported"},
		{"LAYOUT_MODE=permissive", map[string]string{"LAYOUT_MODE": "permissive"}, "LAYOUT_MODE=permissive is not supported"},
		{"RELOCATION_MODE=permissive", map[string]string{"RELOCATION_MODE": "permissive"}, "RELOCATION_MODE=permissive is not supported"},
		{"DEMAND_MODE=off", map[string]string{"DEMAND_MODE": "off"}, "DEMAND_MODE=off is not supported"},
		{"RELOCATION_MODE=off", map[string]string{"RELOCATION_MODE": "off"}, "RELOCATION_MODE=off is not supported"},
		{"unknown LAYOUT_MODE", map[string]string{"LAYOUT_MODE": "http"}, "LAYOUT_MODE"},
		{"EVENT_PUBLISHER=log", map[string]string{"EVENT_PUBLISHER": "log"}, "EVENT_PUBLISHER=log is not supported"},
		{"unknown EVENT_PUBLISHER", map[string]string{"EVENT_PUBLISHER": "rabbit"}, "EVENT_PUBLISHER"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := loadRuntime(withEnv(tc.override))
			if err == nil || !strings.Contains(err.Error(), tc.wantVar) {
				t.Fatalf("err = %v, want a boot error containing %q", err, tc.wantVar)
			}
		})
	}
}

func TestLoadRuntimePermissiveMessageNamesRealKafka(t *testing.T) {
	_, err := loadRuntime(withEnv(map[string]string{"DEMAND_MODE": "permissive"}))
	if err == nil || err.Error() != "DEMAND_MODE=permissive is not supported: this context runs on real Kafka" {
		t.Fatalf("err = %v", err)
	}
}

// A completely empty environment fails on the first mandatory variable, and
// buildAdapters refuses an empty DATABASE_URL by itself (no in-memory branch).
func TestNoDatabaseURLIsABootErrorEverywhere(t *testing.T) {
	if _, err := loadRuntime(func(string) string { return "" }); !errors.Is(err, errMissingDatabaseURL) {
		t.Fatalf("loadRuntime(empty) = %v, want errMissingDatabaseURL", err)
	}
	_, closeFn, err := buildAdapters(context.Background(), "", quietLogger())
	if !errors.Is(err, errMissingDatabaseURL) {
		t.Fatalf("buildAdapters(\"\") = %v, want errMissingDatabaseURL", err)
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

func TestParseLookbackDays(t *testing.T) {
	for raw, want := range map[string]int{"": 0, "7": 7, "\t14": 14, "365": 365, "0": 0, "-1": 0, "366": 0, "abc": 0} {
		if got := parseLookbackDays(raw, quietLogger()); got != want {
			t.Errorf("parseLookbackDays(%q) = %d, want %d", raw, got, want)
		}
	}
}

func TestParseZoneCodes(t *testing.T) {
	cases := map[string]string{"": "FWD", " , ": "FWD", "FWD": "FWD", "FWD, PICK ,": "FWD,PICK"}
	for raw, want := range cases {
		if got := strings.Join(parseZoneCodes(raw), ","); got != want {
			t.Errorf("parseZoneCodes(%q) = %q, want %q", raw, got, want)
		}
	}
}

func TestConfigFromEnv(t *testing.T) {
	t.Setenv("DEMAND_SITE_ID", "")
	t.Setenv("DEFAULT_SITE_ID", "")
	t.Setenv("LOOKBACK_DAYS", "")
	t.Setenv("FORWARD_ZONE_CODES", "")
	cfg := configFromEnv(quietLogger())
	if cfg.defaultSite != "" || cfg.onlySite != "" || cfg.lookbackDays != 0 || len(cfg.forwardZoneCodes) != 1 {
		t.Fatalf("defaults = %+v", cfg)
	}
	t.Setenv("DEMAND_SITE_ID", "WH1")
	t.Setenv("LOOKBACK_DAYS", "7")
	cfg = configFromEnv(quietLogger())
	if cfg.defaultSite != "WH1" || cfg.onlySite != "WH1" || cfg.lookbackDays != 7 {
		t.Fatalf("DEMAND_SITE_ID = %+v: it is both the default site and the demand filter", cfg)
	}
	t.Setenv("DEMAND_SITE_ID", "")
	t.Setenv("DEFAULT_SITE_ID", "WH2")
	cfg = configFromEnv(quietLogger())
	if cfg.defaultSite != "WH2" || cfg.onlySite != "" {
		t.Fatalf("DEFAULT_SITE_ID = %+v: a default site alone must not filter demand", cfg)
	}
}

func TestShutdownDrainDelay(t *testing.T) {
	cases := map[string]time.Duration{"": DefaultShutdownDrainDelay, "0": 0, "2s": 2 * time.Second, "x": DefaultShutdownDrainDelay, "-1s": DefaultShutdownDrainDelay}
	for raw, want := range cases {
		t.Setenv("SHUTDOWN_DRAIN_DELAY", raw)
		if got := shutdownDrainDelay(quietLogger()); got != want {
			t.Errorf("shutdownDrainDelay(%q) = %v, want %v", raw, got, want)
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
	if _, _, err := startOutboxRelay(countingStore{}, nil, quietLogger()); !errors.Is(err, errMissingBrokers) {
		t.Fatalf("err = %v, want errMissingBrokers", err)
	}
}

func realtimeEnv() runtimeEnv {
	rt, err := loadRuntime(withEnv(map[string]string{"KAFKA_BROKERS": "127.0.0.1:1"}))
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
	if len(workers) != 4 {
		t.Fatalf("workers=%d, want 4", len(workers))
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
	s, err := buildServer(ad, config{defaultSite: "SITE-1", forwardZoneCodes: []string{"FWD"}}, nil, nil, nil)
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
	s, err := buildServer(ad, config{defaultSite: "SITE-1", lookbackDays: 7, forwardZoneCodes: []string{"FWD"}}, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(routerFor(s))
	defer srv.Close()
	plan := call(t, srv, http.MethodPost, "/slot-plans", http.StatusCreated)
	if plan.id == "" || !strings.Contains(plan.body, `"siteId":"SITE-1"`) {
		t.Fatalf("plan = %s", plan.body)
	}
	call(t, srv, http.MethodPost, "/slot-plans/"+plan.id+"/approve", http.StatusOK)
	call(t, srv, http.MethodGet, "/forward-slots", http.StatusOK)
	call(t, srv, http.MethodGet, "/sku-velocity", http.StatusOK)
	call(t, srv, http.MethodPost, "/slot-plans/"+plan.id+"/approve", http.StatusConflict)
	if got := call(t, srv, http.MethodGet, "/slot-plans/"+plan.id, http.StatusOK); !strings.Contains(got.body, `"execution":{"movesTotal":`) {
		t.Fatalf("GET /slot-plans/{planId} must carry the execution progress: %s", got.body)
	}
}

func TestMigrationsDatabaseURL(t *testing.T) {
	const pooled, direct = "postgres://u@pgbouncer:6432/db", "postgres://u@postgres:5432/db"
	t.Setenv("MIGRATIONS_DATABASE_URL", "")
	if got := migrationsDatabaseURL(pooled); got != pooled {
		t.Errorf("unset: got %q", got)
	}
	t.Setenv("MIGRATIONS_DATABASE_URL", direct)
	if got := migrationsDatabaseURL(pooled); got != direct {
		t.Errorf("set: got %q", got)
	}
}

func TestNewLoggerAndGetenv(t *testing.T) {
	for _, lvl := range []string{"debug", "info", "warn", "error", "nonsense"} {
		if newLogger(lvl) == nil {
			t.Fatalf("newLogger(%q) = nil", lvl)
		}
	}
	t.Setenv("SO_TEST_KEY", "")
	if getenv("SO_TEST_KEY", "fallback") != "fallback" {
		t.Fatal("getenv fallback")
	}
	t.Setenv("SO_TEST_KEY", "v")
	if getenv("SO_TEST_KEY", "fallback") != "v" {
		t.Fatal("getenv value")
	}
}
