package kafka

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
	"testing"

	kafkago "github.com/segmentio/kafka-go"
	"gopkg.in/yaml.v3"

	ce "github.com/cloudevents/sdk-go/v2/event"

	"github.com/claudioed/slotting-optimization/internal/adapters/outbound/memory"
	"github.com/claudioed/slotting-optimization/internal/analytics/report"
	"github.com/claudioed/slotting-optimization/internal/application/usecases"
)

// Shared machinery for the per-producer pinned-AsyncAPI contract tests
// (archetypes E1-E4). Each producer this service consumes has its own
// spec_test file listing its pinned copy, its reader-config topic constant
// and the CloudEvents types its consumer handles; this file only knows how
// to read an AsyncAPI document, its CloudEvents envelope conventions, and
// how to stand the real consumers up on the in-memory read side.
//
// Fleet reality encoded here: the pinned specs are channel-perspective
// (producer's own topics under subscribe:, consumed topics under publish:),
// so the pinned file is used as the MESSAGE CATALOG only — what this repo
// actually subscribes to is derived from the topic constants in this
// package's consumer files, never from the spec's operations.

const (
	pinnedCEContentType = "application/cloudevents+json"
	pinnedJSONBody      = "application/json"
	pinnedURNPrefix     = "urn:warehouse:"
	pinnedURNSuffix     = ":v1"
	pinnedUnknownKey    = "x_future_contract_field"
)

type pinnedSpecDoc struct {
	DefaultContentType string                   `yaml:"defaultContentType"`
	Channels           map[string]pinnedChannel `yaml:"channels"`
	Components         struct {
		Messages map[string]pinnedSpecMessage `yaml:"messages"`
		Schemas  map[string]any               `yaml:"schemas"`
	} `yaml:"components"`
}

type pinnedChannel struct {
	Messages  map[string]pinnedRef `yaml:"messages"`
	Subscribe *pinnedOp            `yaml:"subscribe"`
	Publish   *pinnedOp            `yaml:"publish"`
}

type pinnedOp struct {
	Message *pinnedOpMessage `yaml:"message"`
}

type pinnedOpMessage struct {
	OneOf []pinnedRef `yaml:"oneOf"`
	Ref   string      `yaml:"$ref"`
}

type pinnedRef struct {
	Ref string `yaml:"$ref"`
}

type pinnedSpecMessage struct {
	ContentType string              `yaml:"contentType"`
	Payload     any                 `yaml:"payload"`
	Examples    []pinnedSpecExample `yaml:"examples"`
}

type pinnedSpecExample struct {
	Name    string         `yaml:"name"`
	Payload map[string]any `yaml:"payload"`
}

func loadPinnedSpec(t *testing.T, file string) *pinnedSpecDoc {
	t.Helper()
	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("read pinned spec %s: %v", file, err)
	}
	var doc pinnedSpecDoc
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parse pinned spec %s: %v", file, err)
	}
	return &doc
}

// pinnedMessageKeys lists the components.messages keys a channel declares,
// across the fleet's AsyncAPI shapes (channel.messages map, or the 2.x
// subscribe/publish operation message oneOf).
func pinnedMessageKeys(t *testing.T, doc *pinnedSpecDoc, channel string) []string {
	t.Helper()
	ch, ok := doc.Channels[channel]
	if !ok {
		t.Fatalf("pinned spec does not document channel %q", channel)
	}
	var keys []string
	for _, ref := range ch.Messages {
		keys = append(keys, pinnedRefName(ref.Ref))
	}
	for _, op := range []*pinnedOp{ch.Subscribe, ch.Publish} {
		if op == nil || op.Message == nil {
			continue
		}
		for _, ref := range op.Message.OneOf {
			keys = append(keys, pinnedRefName(ref.Ref))
		}
		if op.Message.Ref != "" {
			keys = append(keys, pinnedRefName(op.Message.Ref))
		}
	}
	if len(keys) == 0 {
		t.Fatalf("channel %q declares no message catalog", channel)
	}
	return keys
}

func pinnedRefName(ref string) string {
	return strings.TrimPrefix(ref, "#/components/messages/")
}

// pinnedMessageSchema resolves a message's payload: either inline or a
// $ref into components.schemas.
func pinnedMessageSchema(t *testing.T, doc *pinnedSpecDoc, key string) map[string]any {
	t.Helper()
	msg, ok := doc.Components.Messages[key]
	if !ok {
		t.Fatalf("message %q is not in components.messages", key)
	}
	if payload, ok := msg.Payload.(map[string]any); ok {
		if ref, ok := payload["$ref"].(string); ok {
			name := strings.TrimPrefix(ref, "#/components/schemas/")
			schema, ok := doc.Components.Schemas[name].(map[string]any)
			if !ok {
				t.Fatalf("payload $ref %q does not resolve", ref)
			}
			return schema
		}
		return payload
	}
	t.Fatalf("message %q has no object payload", key)
	return nil
}

// pinnedResolveRef returns the components.schemas node a {$ref: ...} map
// points at, or the node itself when it is not a ref.
func pinnedResolveRef(t *testing.T, doc *pinnedSpecDoc, node map[string]any) map[string]any {
	t.Helper()
	ref, ok := node["$ref"].(string)
	if !ok || !strings.HasPrefix(ref, "#/components/schemas/") {
		return node
	}
	name := strings.TrimPrefix(ref, "#/components/schemas/")
	resolved, ok := doc.Components.Schemas[name].(map[string]any)
	if !ok {
		t.Fatalf("schema $ref %q does not resolve", ref)
	}
	return resolved
}

// pinnedSchemaConst reads properties.<attr>.const out of a schema's allOf
// branches (the fleet's CloudEvents envelope narrowing shape), resolving
// $ref branches into components.schemas (facility-layout narrows source in
// a shared cloudEvent base schema referenced by every payload).
func pinnedSchemaConst(t *testing.T, doc *pinnedSpecDoc, schema map[string]any, attr string) (string, bool) {
	t.Helper()
	branches, _ := schema["allOf"].([]any)
	branches = append(branches, schema)
	for _, b := range branches {
		branch, ok := b.(map[string]any)
		if !ok {
			continue
		}
		branch = pinnedResolveRef(t, doc, branch)
		props, _ := branch["properties"].(map[string]any)
		node, _ := props[attr].(map[string]any)
		if c, ok := node["const"].(string); ok {
			return c, true
		}
	}
	return "", false
}

// pinnedMessageType returns the full CloudEvents `type` a channel message
// carries, from the schema const (falling back to its first example).
func pinnedMessageType(t *testing.T, doc *pinnedSpecDoc, key string) string {
	t.Helper()
	if c, ok := pinnedSchemaConst(t, doc, pinnedMessageSchema(t, doc, key), "type"); ok {
		return c
	}
	for _, ex := range doc.Components.Messages[key].Examples {
		if ty, _ := ex.Payload["type"].(string); ty != "" {
			return ty
		}
	}
	t.Fatalf("message %q declares no CloudEvents type const or example type", key)
	return ""
}

// pinnedChannelTypes maps every CloudEvents type on a channel to its
// message key: the E1 view of "what the pinned catalog offers".
func pinnedChannelTypes(t *testing.T, doc *pinnedSpecDoc, channel string) map[string]string {
	t.Helper()
	types := make(map[string]string)
	for _, key := range pinnedMessageKeys(t, doc, channel) {
		types[pinnedMessageType(t, doc, key)] = key
	}
	return types
}

// pinnedExamples returns every example payload of the given type on the
// channel (spec examples are the first-choice fixtures, per the kit).
func pinnedExamples(t *testing.T, doc *pinnedSpecDoc, channel, wantType string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, key := range pinnedMessageKeys(t, doc, channel) {
		if pinnedMessageType(t, doc, key) != wantType {
			continue
		}
		for _, ex := range doc.Components.Messages[key].Examples {
			if ex.Payload["type"] == wantType {
				out = append(out, ex.Payload)
			}
		}
	}
	return out
}

// requirePinnedEnvelope (E3) asserts the CloudEvents 1.0 invariants every
// fleet envelope carries, on a concrete payload.
func requirePinnedEnvelope(t *testing.T, payload map[string]any) {
	t.Helper()
	str := func(key string) string { v, _ := payload[key].(string); return v }
	if str("specversion") != "1.0" {
		t.Errorf("specversion = %q, want 1.0", str("specversion"))
	}
	for _, attr := range []string{"id", "source", "type", "subject", "time"} {
		if str(attr) == "" {
			t.Errorf("envelope attribute %q is missing/empty", attr)
		}
	}
	if str("datacontenttype") != pinnedJSONBody {
		t.Errorf("datacontenttype = %q, want %s", str("datacontenttype"), pinnedJSONBody)
	}
	if ds := str("dataschema"); !strings.HasPrefix(ds, pinnedURNPrefix) || !strings.HasSuffix(ds, pinnedURNSuffix) {
		t.Errorf("dataschema = %q, want a %s...%s urn", ds, pinnedURNPrefix, pinnedURNSuffix)
	}
	if _, ok := payload["data"].(map[string]any); !ok {
		t.Errorf("data is not an object: %T", payload["data"])
	}
}

// requirePinnedMessageContentType (E3) asserts the wire content type the
// producer documents for every handled message. An empty message-level
// contentType inherits the document's defaultContentType (AsyncAPI 2.6),
// which requirePinnedDefaultContentType pins separately.
func requirePinnedMessageContentType(t *testing.T, doc *pinnedSpecDoc, key string) {
	t.Helper()
	ct := doc.Components.Messages[key].ContentType
	if ct != pinnedCEContentType && ct != "" {
		t.Errorf("message %q contentType = %q, want %s (or inherit the default)", key, ct, pinnedCEContentType)
	}
}

func requirePinnedDefaultContentType(t *testing.T, doc *pinnedSpecDoc) {
	t.Helper()
	if doc.DefaultContentType != pinnedCEContentType {
		t.Errorf("defaultContentType = %q, want %s", doc.DefaultContentType, pinnedCEContentType)
	}
}

// pinnedWithUnknownDataKey (E4) deep-ish-copies a payload and injects a
// data field no pinned schema declares: additive producer evolution the
// decoders must tolerate (encoding/json ignores unknown keys).
func pinnedWithUnknownDataKey(t *testing.T, payload map[string]any) map[string]any {
	t.Helper()
	clone := make(map[string]any, len(payload)+1)
	for k, v := range payload {
		clone[k] = v
	}
	data, _ := payload["data"].(map[string]any)
	if data == nil {
		t.Fatal("payload has no data object to perturb")
	}
	dataClone := make(map[string]any, len(data)+1)
	for k, v := range data {
		dataClone[k] = v
	}
	dataClone[pinnedUnknownKey] = "added-after-pinning"
	clone["data"] = dataClone
	return clone
}

// pinnedEventBytes marshals a payload into the bytes a consumer sees.
func pinnedEventBytes(t *testing.T, payload map[string]any) []byte {
	t.Helper()
	value, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal pinned payload: %v", err)
	}
	return value
}

// pinnedDerivedFixture builds an envelope from the pinned schema itself for
// message types that ship no examples: envelope consts from the schema,
// data synthesized from its required properties. Provenance:
// derived-from-schema, not a spec example.
func pinnedDerivedFixture(t *testing.T, doc *pinnedSpecDoc, channel, wantType string) map[string]any {
	t.Helper()
	types := pinnedChannelTypes(t, doc, channel)
	key, ok := types[wantType]
	if !ok {
		t.Fatalf("type %s not documented on %s", wantType, channel)
	}
	schema := pinnedMessageSchema(t, doc, key)
	fixture := map[string]any{
		"specversion": "1.0",
		"id":          "derived-from-pinned-schema-0001",
		"subject":     "derived-from-pinned-schema",
		"time":        "2026-11-01T00:00:00Z",
		"type":        wantType,
	}
	for _, attr := range []string{"source", "dataschema"} {
		c, ok := pinnedSchemaConst(t, doc, schema, attr)
		if !ok {
			t.Fatalf("%s %q declares no %s const", channel, key, attr)
		}
		fixture[attr] = c
	}
	if c, ok := pinnedSchemaConst(t, doc, schema, "datacontenttype"); ok {
		fixture["datacontenttype"] = c
	} else {
		fixture["datacontenttype"] = pinnedJSONBody
	}
	var fixtureData map[string]any
	if data := pinnedOptionalDataNode(t, doc, schema); data != nil {
		props, _ := data["properties"].(map[string]any)
		fixtureData = make(map[string]any, max(len(props), 1))
		for _, field := range pinnedRequiredList(t, data) {
			fixtureData[field] = pinnedSynthesizeValue(props[field])
		}
	} else {
		fixtureData = map[string]any{}
	}
	fixture["data"] = fixtureData
	return fixture
}

// pinnedOptionalDataNode finds the payload's data node WITH a required
// list, tolerating the fleet's schema shapes (a bare {data: {...}} branch,
// a {properties: {data: {...}}} branch, or a data node whose own allOf
// $refs a shared base carrying the required list); nil when none has one.
func pinnedOptionalDataNode(t *testing.T, doc *pinnedSpecDoc, schema map[string]any) map[string]any {
	t.Helper()
	branches, _ := schema["allOf"].([]any)
	for _, b := range branches {
		branch, ok := b.(map[string]any)
		if !ok {
			continue
		}
		props, _ := branch["properties"].(map[string]any)
		for _, node := range []any{branch["data"], mapIndex(props, "data")} {
			for _, data := range pinnedDataCandidates(t, doc, node) {
				if data["required"] != nil {
					return data
				}
			}
		}
	}
	if props, ok := schema["properties"].(map[string]any); ok {
		for _, data := range pinnedDataCandidates(t, doc, mapIndex(props, "data")) {
			if data["required"] != nil {
				return data
			}
		}
	}
	return nil
}

// pinnedDataCandidates flattens a data node and its allOf members (each
// $ref-resolved) into the nodes a required list could live on.
func pinnedDataCandidates(t *testing.T, doc *pinnedSpecDoc, node any) []map[string]any {
	t.Helper()
	data, ok := node.(map[string]any)
	if !ok {
		return nil
	}
	data = pinnedResolveRef(t, doc, data)
	out := []map[string]any{data}
	members, _ := data["allOf"].([]any)
	for _, m := range members {
		if member, ok := m.(map[string]any); ok {
			out = append(out, pinnedResolveRef(t, doc, member))
		}
	}
	return out
}

// mapIndex returns m[k] as any (nil when m is nil), keeping call sites terse.
func mapIndex(m map[string]any, k string) any {
	if m == nil {
		return nil
	}
	return m[k]
}

func pinnedRequiredList(t *testing.T, data map[string]any) []string {
	t.Helper()
	raw, _ := data["required"].([]any)
	out := make([]string, 0, len(raw))
	for _, v := range raw {
		s, _ := v.(string)
		if s == "" {
			t.Fatal("data.required carries a non-string entry")
		}
		out = append(out, s)
	}
	return out
}

func pinnedSynthesizeValue(prop any) any {
	node, ok := prop.(map[string]any)
	if !ok {
		return "derived"
	}
	switch node["type"] {
	case "integer":
		return 1
	case "number":
		return 1.5
	case "boolean":
		return true
	default:
		return "derived-from-pinned-schema"
	}
}

// pinnedDispatch records every handler invocation by CloudEvents type, so
// a spec test can assert exactly-one routing: the example's own type fired
// once and nothing else did.
type pinnedDispatch struct {
	mu     sync.Mutex
	counts map[string]int
}

func (d *pinnedDispatch) wrap(handlers map[string]HandlerFunc) map[string]HandlerFunc {
	out := make(map[string]HandlerFunc, len(handlers))
	for typ, h := range handlers {
		out[typ] = func(ctx context.Context, e ce.Event) error {
			d.mu.Lock()
			d.counts[e.Type()]++
			d.mu.Unlock()
			return h(ctx, e)
		}
	}
	return out
}

func (d *pinnedDispatch) of(typ string) int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.counts[typ]
}

func (d *pinnedDispatch) total() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	n := 0
	for _, c := range d.counts {
		n += c
	}
	return n
}

// pinnedClaims counts every SUCCESSFUL processed-event claim, so a dedupe
// assertion can compare the claim-set SIZE before and after a replay.
type pinnedClaims struct {
	*memory.ProcessedEventRepo
	mu     sync.Mutex
	claims int
}

func (c *pinnedClaims) Claim(ctx context.Context, consumer, eventID string) (bool, error) {
	ok, err := c.ProcessedEventRepo.Claim(ctx, consumer, eventID)
	if ok {
		c.mu.Lock()
		c.claims++
		c.mu.Unlock()
	}
	return ok, err
}

func (c *pinnedClaims) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.claims
}

// pinnedSpecCopies is the whole read side the pinned-spec consumers write
// to: the same wiring the composition root builds, on the in-memory
// adapters, with every handler routed through the recorder.
type pinnedSpecCopies struct {
	layout   *Consumer
	product  *Consumer
	packs    *Consumer
	demand   *Consumer
	stock    *Consumer
	rec      *pinnedDispatch
	claims   *pinnedClaims
	packView *memory.PackLevelView
}

func newPinnedSpecCopies() *pinnedSpecCopies {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	claims := &pinnedClaims{ProcessedEventRepo: memory.NewProcessedEventRepo()}
	demandLedger := memory.NewDemandLedger()
	profiles := memory.NewProfileDirectory()
	catalogue := memory.NewSlotCatalogue()
	packs := memory.NewPackLevelView()
	uow := memory.NewUnitOfWork(demandLedger, profiles, catalogue, packs, claims)
	intake := usecases.Intake{UoW: uow, Processed: claims}
	rec := &pinnedDispatch{counts: map[string]int{}}
	mk := func(name, topic string, handlers map[string]HandlerFunc) *Consumer {
		return &Consumer{Name: name, Topic: topic, Handlers: rec.wrap(handlers), Logger: logger}
	}
	cp := &pinnedSpecCopies{
		layout: mk("layout", LayoutTopic, LayoutHandlers(
			&usecases.ApplyZoneRegistered{Intake: intake, Catalogue: catalogue},
			&usecases.ApplyLocationSlotRegistered{Intake: intake, Catalogue: catalogue},
			&usecases.ApplyLocationSlotDecommissioned{Intake: intake, Catalogue: catalogue}, logger)),
		product: mk("product", ProductTopic, ProductHandlers(
			&usecases.ApplyProductClassified{Intake: intake, Profiles: profiles},
			&usecases.ApplyPhysicalProfile{Intake: intake, Profiles: profiles}, logger)),
		packs: mk("pack-level", ProductTopic, PackLevelHandlers(PackLevelUseCases{
			Declared: &usecases.ApplyPackLevelDeclared{Intake: intake, PackLevels: packs},
			Retired:  &usecases.ApplyPackLevelRetired{Intake: intake, PackLevels: packs},
			Measured: &usecases.ApplyPackLevelMeasured{Intake: intake, PackLevels: packs},
		}, logger)),
		demand: mk("demand", DemandTopic, DemandHandlers(
			&usecases.ApplyDemandChanged{Intake: intake, Demand: demandLedger}, logger)),
		stock: mk("relocation", StockTopic, StockHandlers(&usecases.RecordRelocation{
			Intake: intake, Plans: memory.NewSlotPlanRepo(), Executions: memory.NewMoveExecutionLedger(),
		}, logger)),
		rec: rec, claims: claims, packView: packs,
	}
	// Mirror the production wiring (NewStockConsumerForTopic): a
	// StockRelocated that can never be applied is poison, not a skip.
	cp.stock.DeadLetterInvalid = true
	cp.stock.DLQ = &pinnedDLQRecorder{}
	return cp
}

// pinnedGolden is one spec example marshalled to wire bytes.
type pinnedGolden struct {
	Type  string
	ID    string
	Bytes []byte
}

// pinnedGoldenFlow (E2): every spec example of every handled type decodes
// through the REAL consumer and routes to exactly its own handler; each
// example claims its CloudEvents id once.
func pinnedGoldenFlow(t *testing.T, c *Consumer, doc *pinnedSpecDoc, topic string, types []string, rec *pinnedDispatch) []pinnedGolden {
	t.Helper()
	var goldens []pinnedGolden
	routed := map[string]int{}
	for _, typ := range types {
		examples := pinnedExamples(t, doc, topic, typ)
		if len(examples) == 0 {
			t.Logf("NOTE: %s ships no spec examples on %s; E1 covers the type, E2/E3/E4 fixtures await producer example backfill", typ, topic)
			continue
		}
		for _, ex := range examples {
			requirePinnedEnvelope(t, ex)
			bytes := pinnedEventBytes(t, ex)
			if err := c.HandleMessage(t.Context(), bytes); err != nil {
				t.Fatalf("handle golden %s example %v: %v", typ, ex["id"], err)
			}
			goldens = append(goldens, pinnedGolden{Type: typ, ID: strVal(ex["id"]), Bytes: bytes})
			routed[typ]++
		}
	}
	for _, typ := range types {
		if got, want := rec.of(typ), routed[typ]; got != want {
			t.Errorf("%s routed %d times, want exactly %d", typ, got, want)
		}
	}
	return goldens
}

// pinnedReplayNoNewClaims (E2, id-dedupe): replaying every golden byte
// through the consumer must leave the processed-claim set SIZE unchanged —
// every id was already claimed by the first pass.
func pinnedReplayNoNewClaims(t *testing.T, c *Consumer, goldens []pinnedGolden, claims *pinnedClaims) {
	t.Helper()
	claimsBefore := claims.count()
	for _, g := range goldens {
		if err := c.HandleMessage(t.Context(), g.Bytes); err != nil {
			t.Fatalf("replay of %s %s: %v", g.Type, g.ID, err)
		}
	}
	if got := claims.count(); got != claimsBefore {
		t.Fatalf("replay changed the claim-set size: %d before, %d after (dedupe on CloudEvents id failed)", claimsBefore, got)
	}
}

// strVal returns payload[key] as a string, "" when absent.
func strVal(v any) string {
	s, _ := v.(string)
	return s
}

// pinnedEnvelopeConsts (E3): every envelope attribute the pin narrows with
// a schema const — type, source, dataschema, datacontenttype — carries
// exactly that const in the example.
func pinnedEnvelopeConsts(t *testing.T, doc *pinnedSpecDoc, topic, typ string) {
	t.Helper()
	key, ok := pinnedChannelTypes(t, doc, topic)[typ]
	if !ok {
		t.Fatalf("%s not documented on %s", typ, topic)
	}
	schema := pinnedMessageSchema(t, doc, key)
	examples := pinnedExamples(t, doc, topic, typ)
	if len(examples) == 0 {
		t.Logf("NOTE: %s has no examples to check envelope consts against", typ)
		return
	}
	for _, ex := range examples {
		for _, attr := range []string{"type", "source", "dataschema", "datacontenttype"} {
			c, ok := pinnedSchemaConst(t, doc, schema, attr)
			if !ok {
				continue
			}
			if got := strVal(ex[attr]); got != c {
				t.Errorf("%s example %v: %s = %q, pin consts %q", typ, ex["id"], attr, got, c)
			}
		}
	}
}

// pinnedFixtureFor returns a decodable fixture for ANY documented type:
// its first spec example when it ships one, else a schema-derived envelope.
func pinnedFixtureFor(t *testing.T, doc *pinnedSpecDoc, topic, typ string) map[string]any {
	t.Helper()
	if examples := pinnedExamples(t, doc, topic, typ); len(examples) > 0 {
		return examples[0]
	}
	return pinnedDerivedFixture(t, doc, topic, typ)
}

// pinnedTolerancePolicy (E4): an unknown data key must not break decode and
// must still reach the handler; a documented-but-unhandled type must be
// skipped silently; a non-CloudEvent body must be skipped (WARN, nil).
func pinnedTolerancePolicy(t *testing.T, c *Consumer, doc *pinnedSpecDoc, topic string, handled map[string]bool, rec *pinnedDispatch) {
	t.Helper()
	var golden map[string]any
	for typ := range handled {
		if examples := pinnedExamples(t, doc, topic, typ); len(examples) > 0 {
			golden = examples[0]
			break
		}
	}
	if golden == nil {
		t.Fatalf("no golden example on %s to perturb", topic)
	}
	typ := strVal(golden["type"])
	before := rec.of(typ)
	if err := c.HandleMessage(t.Context(), pinnedEventBytes(t, pinnedWithUnknownDataKey(t, golden))); err != nil {
		t.Fatalf("unknown data key must not fail decode: %v", err)
	}
	if got := rec.of(typ); got != before+1 {
		t.Fatalf("unknown data key must still reach the handler: %d -> %d", before, got)
	}
	dispatched := rec.total()
	for candidate := range pinnedChannelTypes(t, doc, topic) {
		if handled[candidate] {
			continue
		}
		fixture := pinnedFixtureFor(t, doc, topic, candidate)
		if err := c.HandleMessage(t.Context(), pinnedEventBytes(t, fixture)); err != nil {
			t.Fatalf("documented-but-unhandled type %s must be skipped, got %v", candidate, err)
		}
	}
	if got := rec.total(); got != dispatched {
		t.Fatalf("an unhandled type reached a handler: %d -> %d", dispatched, got)
	}
	if err := c.HandleMessage(t.Context(), []byte("not-a-cloudevent")); err != nil {
		t.Fatalf("non-CloudEvent body must be skipped, got %v", err)
	}
	if got := rec.total(); got != dispatched {
		t.Fatalf("a non-CloudEvent body reached a handler: %d -> %d", dispatched, got)
	}
}

// pinnedProjection records what the analytics consumer projects (the
// port's contract: a replayed id is applied=false).
type pinnedProjection struct {
	mu     sync.Mutex
	events []report.Event
}

func (p *pinnedProjection) Apply(_ context.Context, e report.Event) (bool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, prev := range p.events {
		if prev.EventID == e.EventID {
			return false, nil
		}
	}
	p.events = append(p.events, e)
	return true, nil
}

func (p *pinnedProjection) applied() []report.Event {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]report.Event(nil), p.events...)
}

// pinnedDLQRecorder records every message the analytics consumer
// dead-letters (raw bytes plus x-dlq-* headers, per ADR 0006).
type pinnedDLQRecorder struct {
	mu   sync.Mutex
	msgs []kafkago.Message
}

func (w *pinnedDLQRecorder) WriteMessages(_ context.Context, msgs ...kafkago.Message) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.msgs = append(w.msgs, msgs...)
	return nil
}

func (w *pinnedDLQRecorder) Close() error { return nil }

func (w *pinnedDLQRecorder) published() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.msgs)
}
