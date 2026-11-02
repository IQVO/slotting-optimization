//go:build integration

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	ce "github.com/cloudevents/sdk-go/v2/event"
	"github.com/jackc/pgx/v5/pgxpool"
	kafkago "github.com/segmentio/kafka-go"
	"github.com/testcontainers/testcontainers-go"
	tckafka "github.com/testcontainers/testcontainers-go/modules/kafka"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

// The end-to-end proof that the shipped binary is really wired: the test boots
// the REAL entry point (run, in-process) against testcontainers Postgres and
// Kafka with nothing but the environment a deployment sets, and drives it only
// through REST and Kafka. It feeds the four local copies and the plan
// execution through the input topics, reads the effects from Postgres and from
// the produced topics (the integration topic and the analytics topic),
// restarts the service, and finally takes the dependencies away to watch
// /readyz follow them. Every Kafka read is bounded by a deadline; run it with
// -timeout 300s.

const (
	topicDemand    = "warehouse.order-management.events"
	topicProducts  = "warehouse.product-master.events"
	topicFacility  = "warehouse.facility.events"
	topicInventory = "warehouse.inventory.events"
	topicEvents    = "warehouse.slotting-optimization.events"
	topicAnalytics = "warehouse.slotting-optimization.analytics"

	source     = "/warehouse/slotting-optimization"
	typePrefix = "com.warehouse.wms.slotting-optimization.slotplan."

	siteID = "WH1"
)

// containerStartTimeout bounds a testcontainers start.
const containerStartTimeout = 120 * time.Second

func startPostgres(t *testing.T) (*tcpostgres.PostgresContainer, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), containerStartTimeout)
	defer cancel()
	c, err := tcpostgres.Run(ctx, "postgres:16-alpine",
		tcpostgres.WithDatabase("slotting_optimization_e2e"), tcpostgres.WithUsername("e2e"), tcpostgres.WithPassword("e2e"),
		testcontainers.WithWaitStrategy(wait.ForLog("database system is ready to accept connections").WithOccurrence(2).WithStartupTimeout(60*time.Second)))
	if err != nil {
		t.Fatalf("start postgres: %v", err)
	}
	t.Cleanup(func() { _ = testcontainers.TerminateContainer(c) })
	url, err := c.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("postgres dsn: %v", err)
	}
	return c, url
}

func startKafka(t *testing.T) (*tckafka.KafkaContainer, []string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), containerStartTimeout)
	defer cancel()
	c, err := tckafka.Run(ctx, "confluentinc/confluent-local:7.6.1", tckafka.WithClusterID("slotting-optimization-e2e"))
	if err != nil {
		t.Fatalf("start kafka: %v", err)
	}
	t.Cleanup(func() { _ = testcontainers.TerminateContainer(c) })
	brokers, err := c.Brokers(ctx)
	if err != nil {
		t.Fatalf("kafka brokers: %v", err)
	}
	return c, brokers
}

// topicExists asks the broker for the topic through a metadata request that
// cannot create it (kafka-go's Client.Metadata never auto-creates).
func topicExists(t *testing.T, brokers []string, topic string) bool {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	resp, err := (&kafkago.Client{Addr: kafkago.TCP(brokers...)}).Metadata(ctx, &kafkago.MetadataRequest{Topics: []string{topic}})
	if err != nil {
		t.Fatalf("metadata: %v", err)
	}
	for _, tp := range resp.Topics {
		if tp.Name == topic && tp.Error == nil {
			return true
		}
	}
	return false
}

// createTopic creates a one-partition topic and waits for its leader.
func createTopic(t *testing.T, brokers []string, topic string) {
	t.Helper()
	conn, err := kafkago.Dial("tcp", brokers[0])
	if err != nil {
		t.Fatalf("dial broker: %v", err)
	}
	defer conn.Close()
	if err := conn.CreateTopics(kafkago.TopicConfig{Topic: topic, NumPartitions: 1, ReplicationFactor: 1}); err != nil {
		t.Fatalf("create topic %s: %v", topic, err)
	}
	for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); time.Sleep(200 * time.Millisecond) {
		if parts, err := conn.ReadPartitions(topic); err == nil && len(parts) == 1 && parts[0].Leader.ID != 0 {
			return
		}
	}
	t.Fatalf("topic %s never got a leader", topic)
}

// produce writes already-encoded messages to topic with RequireAll acks.
func produce(t *testing.T, brokers []string, topic string, msgs ...kafkago.Message) {
	t.Helper()
	transport := &kafkago.Transport{}
	defer transport.CloseIdleConnections()
	w := &kafkago.Writer{Addr: kafkago.TCP(brokers...), Topic: topic, Balancer: &kafkago.Hash{}, RequiredAcks: kafkago.RequireAll, BatchTimeout: 10 * time.Millisecond, Transport: transport}
	defer w.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := w.WriteMessages(ctx, msgs...); err != nil {
		t.Fatalf("produce to %s: %v", topic, err)
	}
}

// inputEvent builds a CloudEvent as a sibling context publishes it.
func inputEvent(t *testing.T, id, src, typ, subject, data string) []byte {
	t.Helper()
	e := ce.New(ce.CloudEventsVersionV1)
	e.SetID(id)
	e.SetSource(src)
	e.SetType(typ)
	e.SetSubject(subject)
	e.SetTime(time.Now().UTC())
	e.SetDataSchema("urn:warehouse:e2e:events:X:v1")
	if err := e.SetData("application/json", json.RawMessage(data)); err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// consume reads exactly n messages of the one-partition topic from the start,
// bounded by a deadline.
func consume(t *testing.T, brokers []string, topic string, n int) []kafkago.Message {
	t.Helper()
	r := kafkago.NewReader(kafkago.ReaderConfig{Brokers: brokers, Topic: topic, Partition: 0, MinBytes: 1, MaxBytes: 1 << 20, MaxWait: 200 * time.Millisecond})
	defer r.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := r.SetOffset(kafkago.FirstOffset); err != nil {
		t.Fatal(err)
	}
	var out []kafkago.Message
	for len(out) < n {
		m, err := r.ReadMessage(ctx)
		if err != nil {
			t.Fatalf("read %s: got %d of %d messages: %v", topic, len(out), n, err)
		}
		out = append(out, m)
	}
	return out
}

// noMoreMessages fails if the topic holds more than want messages after a
// settling period (nothing was duplicated).
func noMoreMessages(t *testing.T, brokers []string, topic string, want int) {
	t.Helper()
	r := kafkago.NewReader(kafkago.ReaderConfig{Brokers: brokers, Topic: topic, Partition: 0, MinBytes: 1, MaxBytes: 1 << 20, MaxWait: 200 * time.Millisecond})
	defer r.Close()
	if err := r.SetOffset(kafkago.FirstOffset); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	got := 0
	for {
		if _, err := r.ReadMessage(ctx); err != nil {
			break
		}
		got++
	}
	if got != want {
		t.Fatalf("%s holds %d messages, want exactly %d", topic, got, want)
	}
}

func freeAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().String()
}

// service is one running instance of the real entry point.
type service struct {
	t      *testing.T
	base   string
	cancel context.CancelFunc
	done   chan error
}

// env is the environment a deployment sets.
type env struct {
	dbURL   string
	brokers []string
	addr    string
	// casePickZones is CASE_PICK_ZONE_CODES ("" = no case-pick faces).
	casePickZones string
}

func (e env) apply(t *testing.T) {
	t.Helper()
	t.Setenv("DATABASE_URL", e.dbURL)
	t.Setenv("MIGRATIONS_DATABASE_URL", "")
	t.Setenv("KAFKA_BROKERS", strings.Join(e.brokers, ","))
	t.Setenv("EVENT_PUBLISHER", "kafka")
	for _, c := range []string{"DEMAND", "PRODUCT", "LAYOUT", "RELOCATION", "PACK_LEVEL"} {
		t.Setenv(c+"_MODE", "kafka")
		t.Setenv(c+"_CONSUMER_GROUP", "slotting-optimization-"+strings.ToLower(c)+"-e2e")
	}
	t.Setenv("DEMAND_SITE_ID", siteID)
	t.Setenv("CASE_PICK_ZONE_CODES", e.casePickZones)
	t.Setenv("HTTP_ADDR", e.addr)
	t.Setenv("SHUTDOWN_DRAIN_DELAY", "0")
	t.Setenv("OUTBOX_RELAY_INTERVAL", "200ms")
	t.Setenv("LOG_LEVEL", "warn")
}

// start boots run in-process and waits until the listener answers /healthz.
func (e env) start(t *testing.T) *service {
	t.Helper()
	e.apply(t)
	ctx, cancel := context.WithCancel(context.Background())
	s := &service{t: t, base: "http://" + e.addr, cancel: cancel, done: make(chan error, 1)}
	go func() { s.done <- run(ctx) }()
	t.Cleanup(s.stopQuietly)
	waitFor(t, 90*time.Second, "the service to listen on "+e.addr, func() bool {
		select {
		case err := <-s.done:
			t.Fatalf("run returned while booting: %v", err)
		default:
		}
		status, _ := s.try(http.MethodGet, "/healthz", "", "")
		return status == http.StatusOK
	})
	return s
}

// stop cancels run and requires a clean shutdown within a bound.
func (s *service) stop() {
	s.t.Helper()
	s.cancel()
	select {
	case err := <-s.done:
		if err != nil {
			s.t.Fatalf("shutdown returned %v", err)
		}
	case <-time.After(60 * time.Second):
		s.t.Fatal("run did not return after the shutdown")
	}
	s.done <- nil // let the cleanup find it stopped
}

func (s *service) stopQuietly() {
	s.cancel()
	select {
	case <-s.done:
	case <-time.After(60 * time.Second):
	}
}

// expectExit waits for run to return on its own (nobody cancelled it) and
// returns its error.
func (s *service) expectExit() error {
	s.t.Helper()
	select {
	case err := <-s.done:
		s.done <- nil // let the cleanup find it stopped
		return err
	case <-time.After(90 * time.Second):
		s.t.Fatal("run did not return although Kafka is gone")
		return nil
	}
}

// try performs a request and returns -1 when the connection fails. A non-empty
// key is sent as the Idempotency-Key.
func (s *service) try(method, path, body, key string) (int, string) {
	req, err := http.NewRequestWithContext(context.Background(), method, s.base+path, strings.NewReader(body))
	if err != nil {
		return -1, err.Error()
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return -1, err.Error()
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(raw)
}

func (s *service) call(method, path, body, key string, want int) string {
	s.t.Helper()
	status, raw := s.try(method, path, body, key)
	if status != want {
		s.t.Fatalf("%s %s = %d %s, want %d", method, path, status, raw, want)
	}
	return raw
}

func waitFor(t *testing.T, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("timed out after %s waiting for %s", timeout, what)
}

// pgView reads the service's own database.
type pgView struct {
	t    *testing.T
	pool *pgxpool.Pool
}

func newPGView(t *testing.T, url string) pgView {
	t.Helper()
	pool, err := pgxpool.New(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pgView{t: t, pool: pool}
}

func (v pgView) count(query string, args ...any) int {
	v.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var n int
	if err := v.pool.QueryRow(ctx, query, args...).Scan(&n); err != nil {
		v.t.Fatalf("%s: %v", query, err)
	}
	return n
}

// The CloudEvents context attributes of a published message, plus its data.
type envelope struct {
	SpecVersion     string          `json:"specversion"`
	ID              string          `json:"id"`
	Source          string          `json:"source"`
	Type            string          `json:"type"`
	Subject         string          `json:"subject"`
	Time            string          `json:"time"`
	DataContentType string          `json:"datacontenttype"`
	DataSchema      string          `json:"dataschema"`
	Data            json.RawMessage `json:"data"`
}

func decodeEnvelope(t *testing.T, m kafkago.Message) envelope {
	t.Helper()
	var e envelope
	if err := json.Unmarshal(m.Value, &e); err != nil {
		t.Fatalf("not a CloudEvent: %v: %s", err, m.Value)
	}
	return e
}

// expectedEvent is one published occurrence: event name, subject (CloudEvents
// `subject`) and Kafka key (both the plan id).
type expectedEvent struct{ name, plan string }

// assertEnvelope checks every context attribute and the Kafka key/header of a
// message of this service.
func assertEnvelope(t *testing.T, m kafkago.Message, want expectedEvent, stream string) envelope {
	t.Helper()
	e := decodeEnvelope(t, m)
	wantType := typePrefix + want.name
	schema := fmt.Sprintf("urn:warehouse:slotting-optimization:%s:%s:v1", stream, want.name)
	if e.SpecVersion != "1.0" || e.Type != wantType || e.Source != source || e.DataSchema != schema ||
		e.DataContentType != "application/json" || e.Subject != want.plan || e.ID == "" {
		t.Fatalf("envelope = %+v, want 1.0 %s from %s (%s) about %s", e, wantType, source, schema, want.plan)
	}
	if _, err := time.Parse(time.RFC3339Nano, e.Time); err != nil {
		t.Fatalf("time %q is not RFC 3339: %v", e.Time, err)
	}
	if string(m.Key) != want.plan {
		t.Fatalf("kafka key = %q, want %q", m.Key, want.plan)
	}
	var ct string
	for _, h := range m.Headers {
		if h.Key == "content-type" {
			ct = string(h.Value)
		}
	}
	if ct != "application/cloudevents+json; charset=UTF-8" {
		t.Fatalf("content-type header = %q", ct)
	}
	return e
}

func readyz(s *service) (int, string) { return s.try(http.MethodGet, "/readyz", "", "") }

func waitReadyzStatus(t *testing.T, s *service, want int, what string) string {
	t.Helper()
	var body string
	waitFor(t, 60*time.Second, what, func() bool {
		var status int
		status, body = readyz(s)
		return status == want
	})
	return body
}

func mustDecode(t *testing.T, raw string, dest any) {
	t.Helper()
	if err := json.Unmarshal([]byte(raw), dest); err != nil {
		t.Fatalf("decode %s: %v", raw, err)
	}
}

func assertProblem(t *testing.T, body, slug string) {
	t.Helper()
	if !strings.Contains(body, "/"+slug+`"`) {
		t.Fatalf("body %s is not the %s problem", body, slug)
	}
}

// world is what the phases share.
type world struct {
	e        env
	pg       *tcpostgres.PostgresContainer
	kc       *tckafka.KafkaContainer
	db       pgView
	brokers  []string
	plan     planView
	rejected string
	expected []expectedEvent
}

// planView is the part of a slot plan the phases read back.
type planView struct {
	PlanID      string `json:"planId"`
	SiteID      string `json:"siteId"`
	State       string `json:"state"`
	Assignments []struct {
		SKU  string `json:"sku"`
		Slot string `json:"slot"`
	} `json:"assignments"`
	Execution *struct {
		MovesTotal    int `json:"movesTotal"`
		MovesExecuted int `json:"movesExecuted"`
	} `json:"execution"`
}

// Phase 1: the output topics do not exist yet (a fresh cluster before
// anything published). Readiness needs Postgres plus a broker answering
// metadata, NOT topics, so the service is ready at once, and it creates no
// output topic itself.
func phaseTopicsMissing(t *testing.T, s *service, brokers []string) {
	t.Helper()
	body := waitReadyzStatus(t, s, http.StatusOK, "/readyz to be 200 although no output topic exists")
	if !strings.Contains(body, "ready") {
		t.Fatalf("readyz = %s", body)
	}
	for _, topic := range []string{topicEvents, topicAnalytics} {
		if topicExists(t, brokers, topic) {
			t.Fatalf("topic %s exists: the service must never create its output topics itself", topic)
		}
	}
}

func demandData(order, sku string, units int) string {
	return fmt.Sprintf(`{"source_order_id":%q,"line_no":1,"site_id":%q,"sku":%q,"demanded_units":%d,"due_at":%q,"state":"ACTIVE","assignment_version":"static-site-v1"}`,
		order, siteID, sku, units, time.Now().Add(-time.Hour).UTC().Format(time.RFC3339))
}

func measuredData(sku string) string {
	return `{"sku":"` + sku + `","effective":{"length_mm":205,"width_mm":121,"height_mm":82,"weight_g":1720,"volume_mm3":2034010},"effective_source":"measured","version":5}`
}

// Phase 2: the four local copies are fed through Kafka only. Duplicates (same
// CloudEvents id) are applied once.
func phaseLocalCopies(t *testing.T, s *service, w *world) {
	t.Helper()
	b := w.brokers
	d1 := inputEvent(t, "dem-1", "/warehouse/order-management", "com.warehouse.wes.order-management.siteskudemand.SiteSkuDemandChanged", "ord-1/line/1", demandData("ord-1", "SKU-1", 12))
	d2 := inputEvent(t, "dem-2", "/warehouse/order-management", "com.warehouse.wes.order-management.siteskudemand.SiteSkuDemandChanged", "ord-2/line/1", demandData("ord-2", "SKU-2", 3))
	produce(t, b, topicDemand, kafkago.Message{Key: []byte("ord-1"), Value: d1}, kafkago.Message{Key: []byte("ord-1"), Value: d1}, kafkago.Message{Key: []byte("ord-2"), Value: d2})
	for i, sku := range []string{"SKU-1", "SKU-2"} {
		produce(t, b, topicProducts, kafkago.Message{Key: []byte(sku), Value: inputEvent(t, fmt.Sprintf("prod-%d", i), "/warehouse/product-master",
			"com.warehouse.wms.product-master.product.ProductMeasured", sku, measuredData(sku))})
	}
	layoutSource, layoutType := "/warehouse/facility-layout", "com.warehouse.wms.facility-layout."
	produce(t, b, topicFacility,
		kafkago.Message{Key: []byte("WH1-FWD-AMB"), Value: inputEvent(t, "zone-1", layoutSource, layoutType+"zone.ZoneRegistered", "WH1-FWD-AMB",
			`{"zoneId":"WH1-FWD-AMB","siteCode":"WH1","zoneCode":"FWD","temperatureClass":"Ambient"}`)},
		kafkago.Message{Key: []byte("L-1"), Value: inputEvent(t, "slot-1", layoutSource, layoutType+"locationslot.LocationSlotRegistered", "L-1",
			`{"locationCode":"L-1","zoneId":"WH1-FWD-AMB","maxWeightKg":40,"maxVolumeM3":0.5}`)},
		kafkago.Message{Key: []byte("L-2"), Value: inputEvent(t, "slot-2", layoutSource, layoutType+"locationslot.LocationSlotRegistered", "L-2",
			`{"locationCode":"L-2","zoneId":"WH1-FWD-AMB","maxWeightKg":40,"maxVolumeM3":0.5}`)})

	waitFor(t, 60*time.Second, "the four local copies to be fed from Kafka", func() bool {
		return w.db.count(`SELECT count(*) FROM demand_lines`) == 2 && w.db.count(`SELECT count(*) FROM product_profiles`) == 2 &&
			w.db.count(`SELECT count(*) FROM zones`) == 1 && w.db.count(`SELECT count(*) FROM slots`) == 2
	})
	// 2 demand + 2 product + 1 zone + 2 slot events; the duplicate demand
	// delivery was deduped by its CloudEvents id.
	if n := w.db.count(`SELECT count(*) FROM processed_events`); n != 7 {
		t.Fatalf("processed_events = %d, want 7 (the duplicate delivery was deduped by CloudEvents id)", n)
	}
	vel := s.call(http.MethodGet, "/sku-velocity", "", "", http.StatusOK)
	if !strings.Contains(vel, `"sku":"SKU-1"`) || !strings.Contains(vel, `"units":12`) {
		t.Fatalf("GET /sku-velocity = %s, want SKU-1 with 12 units counted once", vel)
	}
}

// Phase 3: the plan workflow through REST only: generate (idempotent),
// approve, a second plan generated and rejected. Four occurrences reach the
// outbox, each written to the integration AND the analytics topic.
func phaseWorkflow(t *testing.T, s *service, w *world) {
	t.Helper()
	first := s.call(http.MethodPost, "/slot-plans", `{}`, "gen-1", http.StatusCreated)
	// The same key and body replays the first response and records nothing new.
	if replay := s.call(http.MethodPost, "/slot-plans", `{}`, "gen-1", http.StatusCreated); replay != first {
		t.Fatalf("idempotent replay = %s, want the first response %s", replay, first)
	}
	assertProblem(t, s.call(http.MethodPost, "/slot-plans", `{"lookbackDays":7}`, "gen-1", http.StatusConflict), "idempotency-key-reused")
	assertProblem(t, s.call(http.MethodPost, "/slot-plans", `{}`, "", http.StatusBadRequest), "idempotency-key-required")
	mustDecode(t, first, &w.plan)
	if w.plan.State != "Draft" || w.plan.SiteID != siteID || len(w.plan.Assignments) != 2 {
		t.Fatalf("generated plan = %s, want a Draft of %s with 2 assignments", first, siteID)
	}
	s.call(http.MethodPost, "/slot-plans/"+w.plan.PlanID+"/approvals", "", "ap-1", http.StatusOK)
	assertProblem(t, s.call(http.MethodPost, "/slot-plans/"+w.plan.PlanID+"/approvals", "", "ap-2", http.StatusConflict), "plan-not-draft")
	fwd := s.call(http.MethodGet, "/forward-slots", "", "", http.StatusOK)
	if !strings.Contains(fwd, w.plan.PlanID) {
		t.Fatalf("GET /forward-slots = %s, want the approved plan %s", fwd, w.plan.PlanID)
	}
	w.expected = []expectedEvent{{"SlotPlanGenerated", w.plan.PlanID}, {"SlotPlanApproved", w.plan.PlanID}}
}

// Phase 4: a NUL is a uniform client error on the real runtime (the guard's
// slugs malformed-request / resource-not-found), never a Postgres 22021 ->
// 500, and a rejected request does not consume its Idempotency-Key. The same
// key is then free for a clean request, whose Draft plan is rejected.
func phaseNUL(t *testing.T, s *service, w *world) {
	t.Helper()
	before := w.db.count(`SELECT count(*) FROM idempotency_keys`)
	assertProblem(t, s.call(http.MethodPost, "/slot-plans", `{"siteId":"WH\u00001"}`, "nul-1", http.StatusBadRequest), "malformed-request")
	assertProblem(t, s.call(http.MethodGet, "/slot-plans/pl%00an", "", "", http.StatusNotFound), "resource-not-found")
	assertProblem(t, s.call(http.MethodGet, "/forward-slots?siteId=WH%001", "", "", http.StatusBadRequest), "malformed-request")
	assertProblem(t, s.call(http.MethodGet, "/slot-plans?cursor=%00", "", "", http.StatusBadRequest), "malformed-request")
	if after := w.db.count(`SELECT count(*) FROM idempotency_keys`); after != before {
		t.Fatalf("idempotency_keys %d -> %d: a NUL request recorded its key", before, after)
	}
	var second planView
	mustDecode(t, s.call(http.MethodPost, "/slot-plans", `{}`, "nul-1", http.StatusCreated), &second)
	s.call(http.MethodPost, "/slot-plans/"+second.PlanID+"/rejections", `{"reason":"not needed"}`, "rj-1", http.StatusOK)
	w.rejected = second.PlanID
	w.expected = append(w.expected, expectedEvent{"SlotPlanGenerated", second.PlanID}, expectedEvent{"SlotPlanRejected", second.PlanID})
}

// Phase 5: inventory-storage's StockRelocated (a SLOTTING relocation into the
// slot of the first assignment) executes the matching move of the approved
// plan, once, even when delivered twice and replayed under a new id.
func phaseRelocation(t *testing.T, s *service, w *world) {
	t.Helper()
	a := w.plan.Assignments[0]
	data := fmt.Sprintf(`{"relocation_id":"rel-1","site_code":%q,"sku":%q,"from_bin":"WH1-RES-X","to_bin":%q,"quantity":7,"reason":"SLOTTING","source_ref":"","occurred_at":%q}`,
		siteID, a.SKU, a.Slot, time.Now().UTC().Format(time.RFC3339))
	typ := "com.warehouse.wms.inventory-storage.stock.StockRelocated"
	first := inputEvent(t, "rel-e1", "/warehouse/inventory-storage", typ, "rel-1", data)
	produce(t, w.brokers, topicInventory,
		kafkago.Message{Key: []byte("rel-1"), Value: first}, kafkago.Message{Key: []byte("rel-1"), Value: first},
		kafkago.Message{Key: []byte("rel-1"), Value: inputEvent(t, "rel-e2", "/warehouse/inventory-storage", typ, "rel-1", data)})
	waitFor(t, 60*time.Second, "the relocation to execute a move of the approved plan", func() bool {
		return w.db.count(`SELECT count(*) FROM slot_plan_move_executions`) >= 1
	})
	waitFor(t, 60*time.Second, "all three StockRelocated deliveries to be claimed", func() bool {
		return w.db.count(`SELECT count(*) FROM processed_events WHERE consumer = 'slotting-relocation'`) == 2
	})
	if n := w.db.count(`SELECT count(*) FROM slot_plan_move_executions`); n != 1 {
		t.Fatalf("slot_plan_move_executions = %d, want 1 (redelivery and replay count once)", n)
	}
	var got planView
	mustDecode(t, s.call(http.MethodGet, "/slot-plans/"+w.plan.PlanID, "", "", http.StatusOK), &got)
	if got.Execution == nil || got.Execution.MovesExecuted != 1 || got.Execution.MovesTotal != 2 {
		t.Fatalf("GET /slot-plans/%s execution = %+v, want 1 of 2 moves executed", w.plan.PlanID, got.Execution)
	}
}

// Phase 6: the rows are in Postgres, and the relay drained every outbox row.
func phaseRows(t *testing.T, w *world) {
	t.Helper()
	db := w.db
	if n := db.count(`SELECT count(*) FROM slot_plans`); n != 2 {
		t.Fatalf("slot_plans rows = %d, want 2", n)
	}
	for id, state := range map[string]string{w.plan.PlanID: "Approved", w.rejected: "Rejected"} {
		if n := db.count(`SELECT count(*) FROM slot_plans WHERE plan_id = $1 AND state = $2`, id, state); n != 1 {
			t.Fatalf("plan %s is not %s in Postgres", id, state)
		}
	}
	// Each occurrence is written twice in the same transaction: the integration
	// topic and the analytics topic.
	if n := db.count(`SELECT count(*) FROM outbox_events`); n != 2*len(w.expected) {
		t.Fatalf("outbox rows = %d, want %d", n, 2*len(w.expected))
	}
	waitFor(t, 60*time.Second, "the relay to publish every outbox row", func() bool {
		return db.count(`SELECT count(*) FROM outbox_events WHERE published_at IS NULL`) == 0
	})
	if n := db.count(`SELECT count(*) FROM outbox_events WHERE topic = $1`, topicEvents); n != len(w.expected) {
		t.Fatalf("outbox rows for %s = %d, want %d", topicEvents, n, len(w.expected))
	}
}

// Phase 7: the CloudEvents on both output topics, exact envelope and key; the
// SlotPlanApproved payload carries the assignments consumers read.
func phaseTopics(t *testing.T, w *world) {
	t.Helper()
	for _, tc := range []struct{ topic, stream string }{{topicEvents, "events"}, {topicAnalytics, "analytics"}} {
		msgs := consume(t, w.brokers, tc.topic, len(w.expected))
		for i, m := range msgs {
			assertEnvelope(t, m, w.expected[i], tc.stream)
		}
	}
	// One occurrence, one CloudEvents id on both topics.
	ev, an := consume(t, w.brokers, topicEvents, len(w.expected)), consume(t, w.brokers, topicAnalytics, len(w.expected))
	for i := range ev {
		if decodeEnvelope(t, ev[i]).ID != decodeEnvelope(t, an[i]).ID {
			t.Fatalf("occurrence %d has different ids on the two topics", i)
		}
	}
	var approved struct {
		PlanID      string `json:"plan_id"`
		SiteID      string `json:"site_id"`
		ApprovedAt  string `json:"approved_at"`
		Assignments []struct {
			SKU  string `json:"sku"`
			Slot string `json:"slot"`
		} `json:"assignments"`
	}
	mustDecode(t, string(decodeEnvelope(t, ev[1]).Data), &approved)
	if approved.PlanID != w.plan.PlanID || approved.SiteID != siteID || approved.ApprovedAt == "" || len(approved.Assignments) != 2 ||
		approved.Assignments[0].SKU != w.plan.Assignments[0].SKU || approved.Assignments[0].Slot != w.plan.Assignments[0].Slot {
		t.Fatalf("SlotPlanApproved data = %+v, want plan %s with the REST assignments %+v", approved, w.plan.PlanID, w.plan.Assignments)
	}
}

// Phase 8: restart the real entry point in-process on the same database and
// broker: nothing is lost, nothing is published or consumed twice.
func phaseRestart(t *testing.T, w *world, s *service) *service {
	t.Helper()
	s.stop()
	s2 := w.e.start(t)
	var got planView
	mustDecode(t, s2.call(http.MethodGet, "/slot-plans/"+w.plan.PlanID, "", "", http.StatusOK), &got)
	if got.State != "Approved" {
		t.Fatalf("after the restart the plan is %q, want Approved", got.State)
	}
	// Give the restarted consumers time to (wrongly) re-apply and the relay to
	// (wrongly) re-publish; neither may happen.
	time.Sleep(3 * time.Second)
	if n := w.db.count(`SELECT count(*) FROM processed_events`); n != 9 {
		t.Fatalf("processed_events after the restart = %d, want still 9", n)
	}
	noMoreMessages(t, w.brokers, topicEvents, len(w.expected))
	noMoreMessages(t, w.brokers, topicAnalytics, len(w.expected))
	return s2
}

// Phase 9: /readyz follows Postgres going away (Kafka still up): the 503
// problem names only postgres, and /healthz stays liveness (200). Then Kafka
// goes away: the consumers and the relay lose their broker and the service
// EXITS non-zero naming the dead worker, so the orchestrator restarts it
// instead of leaving a pod alive with consumers that will never come back.
func phaseDependenciesGoAway(t *testing.T, s *service, w *world) {
	t.Helper()
	stopTimeout := 10 * time.Second
	if err := w.pg.Stop(context.Background(), &stopTimeout); err != nil {
		t.Fatalf("stop postgres: %v", err)
	}
	body := waitReadyzStatus(t, s, http.StatusServiceUnavailable, "/readyz to turn 503 once Postgres is gone")
	if !strings.Contains(body, "dependency-unavailable") || !strings.Contains(body, "postgres") || strings.Contains(body, "kafka") {
		t.Fatalf("readyz with Postgres down = %s, want dependency-unavailable naming only postgres", body)
	}
	if status, body := s.try(http.MethodGet, "/healthz", "", ""); status != http.StatusOK {
		t.Fatalf("healthz with Postgres down = %d %s, want 200 (liveness only)", status, body)
	}
	if err := w.kc.Stop(context.Background(), &stopTimeout); err != nil {
		t.Fatalf("stop kafka: %v", err)
	}
	err := s.expectExit()
	if err == nil || !strings.Contains(err.Error(), "stopped unexpectedly") {
		t.Fatalf("run returned %v after Kafka went away, want a 'stopped unexpectedly' error naming the dead worker", err)
	}
}

func TestRunOnRealPostgresAndKafka(t *testing.T) {
	pg, dbURL := startPostgres(t)
	kc, brokers := startKafka(t)
	// The input topics are infra's: create them the way the cluster has them.
	for _, topic := range []string{topicDemand, topicProducts, topicFacility, topicInventory} {
		createTopic(t, brokers, topic)
	}
	w := &world{e: env{dbURL: dbURL, brokers: brokers, addr: freeAddr(t)}, pg: pg, kc: kc, db: newPGView(t, dbURL), brokers: brokers}

	s := w.e.start(t)
	phaseTopicsMissing(t, s, brokers)
	phaseLocalCopies(t, s, w)
	phaseWorkflow(t, s, w)
	phaseNUL(t, s, w)
	phaseRelocation(t, s, w)
	phaseRows(t, w)
	phaseTopics(t, w)
	s = phaseRestart(t, w, s)
	phaseDependenciesGoAway(t, s, w)
}
