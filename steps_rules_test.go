package main_test

// Step definitions of the documentation-derived scenarios (BDD wave 3). They
// extend the world of features_test.go and reuse its vocabulary: this file
// adds the raw consumed-event steps (demand, product, layout and the
// StockRelocated consumer of ADR 0010), the execution-progress assertions and
// a few layout/demand givens the older steps do not cover.

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"

	"github.com/cucumber/godog"
	kafkago "github.com/segmentio/kafka-go"

	inboundkafka "github.com/claudioed/slotting-optimization/internal/adapters/inbound/kafka"
	"github.com/claudioed/slotting-optimization/internal/application/usecases"
)

// relocatedAt is the instant a relocation happened unless a step says so.
const relocatedAt = "2026-10-09T08:30:00Z"

// relocationLog records what RecordRelocation reported to its metrics port:
// every handled relocation, by outcome and (for unmatched ones) reason.
type relocationLog struct {
	mu    sync.Mutex
	calls [][2]string
}

// RelocationObserved implements ports.RelocationMetrics.
func (l *relocationLog) RelocationObserved(_ context.Context, outcome, reason string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.calls = append(l.calls, [2]string{outcome, reason})
}

// outcomeLog is the consumers' logger: it keeps the "outcome" of every
// "event processed" record (applied, duplicate, stale, ignored, unmatched),
// which is what the docs say the adapter logs for every consumed message.
type outcomeLog struct {
	mu   sync.Mutex
	seen []string
}

func (o *outcomeLog) Enabled(context.Context, slog.Level) bool { return true }
func (o *outcomeLog) WithAttrs([]slog.Attr) slog.Handler       { return o }
func (o *outcomeLog) WithGroup(string) slog.Handler            { return o }

func (o *outcomeLog) Handle(_ context.Context, rec slog.Record) error {
	rec.Attrs(func(a slog.Attr) bool {
		if a.Key == "outcome" {
			o.mu.Lock()
			o.seen = append(o.seen, a.Value.String())
			o.mu.Unlock()
		}
		return true
	})
	return nil
}

func (o *outcomeLog) count() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return len(o.seen)
}

func (o *outcomeLog) last() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	if len(o.seen) == 0 {
		return ""
	}
	return o.seen[len(o.seen)-1]
}

// nopDLQ stands in for the dead-letter writer: the StockRelocated consumer
// only dead-letters (returns a poison error) when it has one.
type nopDLQ struct{}

func (nopDLQ) WriteMessages(context.Context, ...kafkago.Message) error { return nil }
func (nopDLQ) Close() error                                            { return nil }

// consumedEvent describes where a consumed event comes from.
type consumedEvent struct{ typ, source, schema string }

var consumedEvents = map[string]consumedEvent{
	"SiteSkuDemandChanged":       {inboundkafka.TypeSiteSkuDemandChanged, "/warehouse/order-management", "urn:warehouse:order-management:events:SiteSkuDemandChanged:v1"},
	"ProductClassified":          {inboundkafka.TypeProductClassified, "/warehouse/product-master", "urn:warehouse:product-master:events:ProductClassified:v1"},
	"ProductDimensionsDeclared":  {inboundkafka.TypeProductDimensionsDeclared, "/warehouse/product-master", "urn:warehouse:product-master:events:ProductDimensionsDeclared:v1"},
	"ProductMeasured":            {inboundkafka.TypeProductMeasured, "/warehouse/product-master", "urn:warehouse:product-master:events:ProductMeasured:v1"},
	"ZoneRegistered":             {inboundkafka.TypeZoneRegistered, "/warehouse/facility-layout", "urn:warehouse:facility-layout:events:ZoneRegistered:v1"},
	"LocationSlotRegistered":     {inboundkafka.TypeLocationSlotRegistered, "/warehouse/facility-layout", "urn:warehouse:facility-layout:events:LocationSlotRegistered:v1"},
	"LocationSlotDecommissioned": {inboundkafka.TypeLocationSlotDecommissioned, "/warehouse/facility-layout", "urn:warehouse:facility-layout:events:LocationSlotDecommissioned:v1"},
	"StockRelocated":             {inboundkafka.TypeStockRelocated, "/warehouse/inventory-storage", "urn:warehouse:inventory-storage:events:StockRelocated:v1"},
	"LocationGeometryUpdated":    {"com.warehouse.wms.facility-layout.locationslot.LocationGeometryUpdated", "/warehouse/facility-layout", "urn:warehouse:facility-layout:events:LocationGeometryUpdated:v1"},
	"StockAdjusted":              {"com.warehouse.wms.inventory-storage.stock.StockAdjusted", "/warehouse/inventory-storage", "urn:warehouse:inventory-storage:events:StockAdjusted:v1"},
	"OrderPlaced":                {"com.warehouse.wes.order-management.order.OrderPlaced", "/warehouse/order-management", "urn:warehouse:order-management:events:OrderPlaced:v1"},
}

// rules is the per-scenario state of this file.
type rules struct {
	*world
	lastErr error // what the consumer returned for the last delivery
	before  int   // outcomes logged before the last receive
}

func registerRules(sc *godog.ScenarioContext, w *world) {
	r := &rules{world: w}
	sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
		r.lastErr = nil
		return ctx, nil
	})
	r.registerEvents(sc)
	r.registerGivens(sc)
	r.registerRelocations(sc)
}

// --- raw consumed events --------------------------------------------------

func (r *rules) consumer(name string) *inboundkafka.Consumer {
	switch name {
	case "demand":
		return r.demand
	case "product":
		return r.product
	case "layout":
		return r.layout
	}
	return r.stock
}

// receive hands one raw CloudEvent to a consumer, with an id the scenario
// chooses (so a redelivery is the same id twice).
func (r *rules) receive(consumer, id, name, data string) error {
	ev, ok := consumedEvents[name]
	if !ok {
		return fmt.Errorf("unknown event name %q", name)
	}
	msg := fmt.Sprintf(`{"specversion":"1.0","id":%q,"source":%q,"type":%q,"subject":"subject-of-%s","time":"2026-10-07T09:00:00Z",`+
		`"datacontenttype":"application/json","dataschema":%q,"data":%s}`, id, ev.source, ev.typ, id, ev.schema, data)
	r.before = r.outcomes.count()
	r.lastErr = r.consumer(consumer).HandleMessage(context.Background(), []byte(msg))
	return nil
}

func (r *rules) registerEvents(sc *godog.ScenarioContext) {
	sc.Step(`^the last delivery was processed as "([^"]*)"$`, func(want string) error {
		if got := r.outcomes.last(); got != want {
			return fmt.Errorf("last outcome = %q, want %q", got, want)
		}
		return nil
	})
	sc.Step(`^the delivery was processed with no outcome$`, func() error {
		if n := r.outcomes.count(); n != r.before {
			return fmt.Errorf("the delivery was processed as %q, want none (it can never be applied)", r.outcomes.last())
		}
		return nil
	})
	sc.Step(`^the (demand|product|layout|inventory) consumer receives the event "([^"]*)" of type "([^"]*)" with data (.+)$`,
		func(consumer, id, name, data string) error { return r.receive(consumer, id, name, data) })
	sc.Step(`^the delivery was accepted$`, func() error {
		if r.lastErr != nil {
			return fmt.Errorf("consumer returned %w, want nil", r.lastErr)
		}
		return nil
	})
	sc.Step(`^the delivery was dead-lettered as a poison message$`, func() error {
		if r.lastErr == nil {
			return fmt.Errorf("consumer returned nil, want a poison-message error")
		}
		if !strings.Contains(r.lastErr.Error(), "poison message") {
			return fmt.Errorf("consumer returned %w, want a poison-message error", r.lastErr)
		}
		return nil
	})
}

// --- givens ---------------------------------------------------------------

func (r *rules) registerGivens(sc *godog.ScenarioContext) {
	sc.Step(`^I send POST "([^"]*)" with a raw NUL byte inside the siteId$`, func(path string) error {
		return r.call(http.MethodPost, path, "{\"siteId\":\"SITE\x00\"}")
	})
	sc.Step(`^I send POST "([^"]*)" with a body of (\d+) bytes$`, func(path string, size int) error {
		// {"siteId":"<n times S>"} is 13 bytes longer than its padding
		return r.call(http.MethodPost, path, `{"siteId":"`+strings.Repeat("S", size-13)+`"}`)
	})
	sc.Step(`^the plain zone "([^"]*)" of site "([^"]*)" with code "([^"]*)" is registered with temperature "([^"]*)"$`, r.plainZone)
	sc.Step(`^the (\d+) demand lines of "([^"]*)" at site "([^"]*)" are removed$`, func(lines int, sku, site string) error {
		for i := 1; i <= lines; i++ {
			if err := r.demandEvent(fmt.Sprintf("ord-%s-%d", sku, i), site, sku, "REMOVED", r.units[sku]); err != nil {
				return err
			}
		}
		return nil
	})
	sc.Step(`^the demand consumer only plans site "([^"]*)"$`, func(site string) error {
		r.demand = &inboundkafka.Consumer{Name: "demand", Logger: r.logger, Handlers: inboundkafka.DemandHandlers(
			&usecases.ApplyDemandChanged{Intake: r.intake, Demand: r.demandLedger, OnlySite: site}, r.logger)}
		return nil
	})
	sc.Step(`^the slot "([^"]*)" in zone "([^"]*)" is registered without any capacity$`, func(code, zone string) error {
		data := fmt.Sprintf(`{"locationCode":%q,"zoneId":%q,"locationType":"ShelfBin"}`, code, zone)
		return r.receive("layout", "cap-"+code, "LocationSlotRegistered", data)
	})
}

// --- StockRelocated (ADR 0010) --------------------------------------------

func (r *rules) relocation(event, relocation string, qty int, sku, from, to, site, reason, ref, at string) error {
	if at == "" {
		at = relocatedAt
	}
	data := fmt.Sprintf(`{"relocation_id":%q,"site_code":%q,"sku":%q,"from_bin":%q,"to_bin":%q,"quantity":%d,"reason":%q,"source_ref":%q,"occurred_at":%q}`,
		relocation, site, sku, from, to, qty, reason, ref, at)
	if event == "" {
		event = "evt-" + relocation
	}
	return r.receive("inventory", event, "StockRelocated", data)
}

func (r *rules) executionOf(n int) error {
	if err := r.call(http.MethodGet, "/slot-plans/"+planID(n), ""); err != nil {
		return err
	}
	return r.theResponseStatusIs(http.StatusOK)
}

// moveProgress finds move seq in the execution of the last plan read.
func (r *rules) moveProgress(seq int) (map[string]any, error) {
	var body struct {
		Execution struct {
			Moves []map[string]any `json:"moves"`
		} `json:"execution"`
	}
	if err := json.Unmarshal(r.body, &body); err != nil {
		return nil, fmt.Errorf("response is not JSON: %s", r.body)
	}
	if seq < 0 || seq >= len(body.Execution.Moves) {
		return nil, fmt.Errorf("no move %d in the execution of %s", seq, r.body)
	}
	return body.Execution.Moves[seq], nil
}

func (r *rules) registerRelocations(sc *godog.ScenarioContext) {
	const common = `relocation "([^"]*)" of (\d+) units of "([^"]*)" from bin "([^"]*)" to bin "([^"]*)" in site "([^"]*)" for reason "([^"]*)" referencing "([^"]*)"(?: at "([^"]*)")?$`
	sc.Step(`^inventory-storage reports `+common, func(rel string, qty int, sku, from, to, site, reason, ref, at string) error {
		return r.relocation("", rel, qty, sku, from, to, site, reason, ref, at)
	})
	sc.Step(`^inventory-storage reports event "([^"]*)" with `+common, func(event, rel string, qty int, sku, from, to, site, reason, ref, at string) error {
		return r.relocation(event, rel, qty, sku, from, to, site, reason, ref, at)
	})
	sc.Step(`^the relocation was observed as "(applied|duplicate|unmatched)"(?: because "([^"]*)")?$`, func(outcome, reason string) error {
		r.relocations.mu.Lock()
		defer r.relocations.mu.Unlock()
		if len(r.relocations.calls) == 0 {
			return fmt.Errorf("no relocation was observed")
		}
		got := r.relocations.calls[len(r.relocations.calls)-1]
		if got[0] != outcome || got[1] != reason {
			return fmt.Errorf("last relocation observed as %q (reason %q), want %q (reason %q)", got[0], got[1], outcome, reason)
		}
		return nil
	})
	sc.Step(`^no relocation was observed$`, func() error {
		r.relocations.mu.Lock()
		defer r.relocations.mu.Unlock()
		if n := len(r.relocations.calls); n != 0 {
			return fmt.Errorf("%d relocations observed, want none: %v", n, r.relocations.calls)
		}
		return nil
	})
	sc.Step(`^plan number (\d+) has executed (\d+) of (\d+) moves$`, func(n, executed, total int) error {
		if err := r.executionOf(n); err != nil {
			return err
		}
		if err := r.theResponseFieldIs("execution.movesTotal", fmt.Sprint(total)); err != nil {
			return err
		}
		return r.theResponseFieldIs("execution.movesExecuted", fmt.Sprint(executed))
	})
	sc.Step(`^the execution shows no last executed time$`, func() error { return r.theResponseFieldIsAbsent("execution.lastExecutedAt") })
	sc.Step(`^the execution was last at "([^"]*)"$`, func(at string) error { return r.theResponseFieldIs("execution.lastExecutedAt", at) })
	sc.Step(`^move (\d+) of the execution is a "([A-Za-z]+)" of "([^"]*)" executed with (\d+) units$`, func(seq int, kind, sku string, qty int) error {
		return r.checkMove(seq, kind, sku, true, qty)
	})
	sc.Step(`^move (\d+) of the execution is a "([A-Za-z]+)" of "([^"]*)" not executed$`, func(seq int, kind, sku string) error {
		return r.checkMove(seq, kind, sku, false, 0)
	})
	sc.Step(`^move (\d+) of the execution was last executed at "([^"]*)"$`, func(seq int, at string) error {
		m, err := r.moveProgress(seq)
		if err != nil {
			return err
		}
		if m["lastExecutedAt"] != at {
			return fmt.Errorf("move %d last executed at %v, want %s", seq, m["lastExecutedAt"], at)
		}
		return nil
	})
	sc.Step(`^the plan read has no execution field$`, func() error { return r.theResponseFieldIsAbsent("execution") })
}

func (r *rules) checkMove(seq int, kind, sku string, executed bool, qty int) error {
	m, err := r.moveProgress(seq)
	if err != nil {
		return err
	}
	if m["moveSeq"] != float64(seq) || m["kind"] != kind || m["sku"] != sku {
		return fmt.Errorf("move %d = %v, want a %s of %s", seq, m, kind, sku)
	}
	if m["executed"] != executed || m["executedQuantity"] != float64(qty) {
		return fmt.Errorf("move %d executed=%v quantity=%v, want executed=%v quantity=%d", seq, m["executed"], m["executedQuantity"], executed, qty)
	}
	return nil
}
