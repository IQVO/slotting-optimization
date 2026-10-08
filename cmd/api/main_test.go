package main

import (
	"context"
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

func TestParsePublisherMode(t *testing.T) {
	for raw, want := range map[string]string{"": "log", "log": "log", "kafka": "kafka"} {
		got, err := parsePublisherMode(raw)
		if err != nil || got != want {
			t.Errorf("parsePublisherMode(%q) = %q, %v; want %q", raw, got, err, want)
		}
	}
	if _, err := parsePublisherMode("rabbit"); err == nil {
		t.Error("an unknown EVENT_PUBLISHER must be a config error")
	}
}

func TestParseConsumerMode(t *testing.T) {
	for raw, want := range map[string]string{"": "permissive", "permissive": "permissive", "kafka": "kafka"} {
		got, err := parseConsumerMode("DEMAND_MODE", raw)
		if err != nil || got != want {
			t.Errorf("parseConsumerMode(%q) = %q, %v; want %q", raw, got, err, want)
		}
	}
	if _, err := parseConsumerMode("DEMAND_MODE", "http"); err == nil {
		t.Error("an unknown mode must be a config error")
	}
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

func (s countingStore) Drain(ctx context.Context, _ int, _ func(context.Context, outbox.Message) error) (int, error) {
	select {
	case s.drains <- struct{}{}:
	default:
	}
	return 0, ctx.Err()
}

var _ outboxrelay.Store = countingStore{}

func TestStartOutboxRelay_DefaultsToTheLogSinkAndStops(t *testing.T) {
	t.Setenv("EVENT_PUBLISHER", "")
	t.Setenv("OUTBOX_RELAY_INTERVAL", "10ms")
	store := countingStore{drains: make(chan struct{}, 1)}
	w, stop, err := startOutboxRelay(store, quietLogger())
	if err != nil {
		t.Fatalf("startOutboxRelay: %v", err)
	}
	select {
	case <-store.drains:
	case <-time.After(5 * time.Second):
		t.Fatal("the relay never drained")
	}
	stop()
	select {
	case <-w.done:
	case <-time.After(5 * time.Second):
		t.Fatal("the relay did not stop")
	}
}

// A broker that is down at boot must not crash or block startup.
func TestStartOutboxRelay_KafkaModeDoesNotDialAtBoot(t *testing.T) {
	t.Setenv("EVENT_PUBLISHER", "kafka")
	t.Setenv("KAFKA_BROKERS", "127.0.0.1:1")
	t.Setenv("OUTBOX_RELAY_INTERVAL", "10ms")
	start := time.Now()
	w, stop, err := startOutboxRelay(countingStore{drains: make(chan struct{}, 1)}, quietLogger())
	if err != nil {
		t.Fatalf("startOutboxRelay with an unreachable broker: %v", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("startup took %v; it must not wait on Kafka", elapsed)
	}
	stop()
	await(quietLogger(), w)
}

func TestStartOutboxRelay_ConfigErrors(t *testing.T) {
	t.Setenv("EVENT_PUBLISHER", "kafka")
	t.Setenv("KAFKA_BROKERS", "")
	if _, _, err := startOutboxRelay(countingStore{}, quietLogger()); err == nil {
		t.Fatal("EVENT_PUBLISHER=kafka needs KAFKA_BROKERS")
	}
	t.Setenv("EVENT_PUBLISHER", "nope")
	if _, _, err := startOutboxRelay(countingStore{}, quietLogger()); err == nil {
		t.Fatal("want an error for an unknown EVENT_PUBLISHER")
	}
}

func inMemory(t *testing.T) adapters {
	t.Helper()
	ad, closeFn, err := buildAdapters(context.Background(), "", quietLogger())
	if err != nil {
		t.Fatalf("buildAdapters: %v", err)
	}
	t.Cleanup(closeFn)
	return ad
}

func clearConsumerEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{"DEMAND_MODE", "PRODUCT_MODE", "LAYOUT_MODE", "DEMAND_CONSUMER_GROUP", "PRODUCT_CONSUMER_GROUP", "LAYOUT_CONSUMER_GROUP", "KAFKA_BROKERS"} {
		t.Setenv(k, "")
	}
}

func TestStartConsumers_PermissiveByDefaultStartsNothing(t *testing.T) {
	clearConsumerEnv(t)
	workers, stop, err := startConsumers(inMemory(t), quietLogger())
	if err != nil || len(workers) != 0 {
		t.Fatalf("workers=%d err=%v, want none", len(workers), err)
	}
	stop()
}

func TestStartConsumers_KafkaModeNeedsItsGroupAndBrokers(t *testing.T) {
	clearConsumerEnv(t)
	ad := inMemory(t)
	t.Setenv("DEMAND_MODE", "kafka")
	if _, _, err := startConsumers(ad, quietLogger()); err == nil || !strings.Contains(err.Error(), "DEMAND_CONSUMER_GROUP") {
		t.Fatalf("a kafka consumer without a group must be a boot error, got %v", err)
	}
	t.Setenv("DEMAND_CONSUMER_GROUP", "slotting-demand-test")
	if _, _, err := startConsumers(ad, quietLogger()); err == nil || !strings.Contains(err.Error(), "KAFKA_BROKERS") {
		t.Fatalf("a kafka consumer without brokers must be a boot error, got %v", err)
	}
	t.Setenv("LAYOUT_MODE", "bogus")
	if _, _, err := startConsumers(ad, quietLogger()); err == nil || !strings.Contains(err.Error(), "LAYOUT_MODE") {
		t.Fatalf("an unknown mode must be a boot error, got %v", err)
	}
}

// Every consumer in kafka mode starts without dialling the broker, and stops.
func TestStartConsumers_StartsOneWorkerPerKafkaModeAndStops(t *testing.T) {
	clearConsumerEnv(t)
	for _, c := range []string{"DEMAND", "PRODUCT", "LAYOUT"} {
		t.Setenv(c+"_MODE", "kafka")
		t.Setenv(c+"_CONSUMER_GROUP", "slotting-"+strings.ToLower(c)+"-test")
	}
	t.Setenv("KAFKA_BROKERS", "127.0.0.1:1")
	start := time.Now()
	workers, stop, err := startConsumers(inMemory(t), quietLogger())
	if err != nil || len(workers) != 3 {
		t.Fatalf("workers=%d err=%v, want 3", len(workers), err)
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

func TestBuildAdapters_InMemoryWiresEveryPort(t *testing.T) {
	ad := inMemory(t)
	for name, wired := range map[string]bool{
		"plans": ad.plans != nil, "demand": ad.demand != nil, "profiles": ad.profiles != nil, "catalogue": ad.catalogue != nil,
		"outbox": ad.outbox != nil, "outboxStore": ad.outboxStore != nil, "processed": ad.processed != nil, "uow": ad.uow != nil,
	} {
		if !wired {
			t.Errorf("port %s not wired", name)
		}
	}
	if ad.idempotency != nil {
		t.Fatal("the in-memory mode has no Idempotency-Key store")
	}
	s, err := buildServer(ad, config{defaultSite: "SITE-1", forwardZoneCodes: []string{"FWD"}}, nil, nil)
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

// The assembled in-memory service answers over HTTP: generate -> approve ->
// read the forward-slot map, with the configured default lookback applied.
func TestInMemoryServiceEndToEnd(t *testing.T) {
	ad := inMemory(t)
	s, err := buildServer(ad, config{defaultSite: "SITE-1", lookbackDays: 7, forwardZoneCodes: []string{"FWD"}}, nil, nil)
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
