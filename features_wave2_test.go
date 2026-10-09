package main_test

// Step definitions added by BDD coverage wave 2. They extend the world of
// features_test.go without touching it: request validation, response headers,
// the outbox's CloudEvents, and the raw consumed-event paths (duplicates,
// stale versions, unusable messages).

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strconv"
	"strings"

	"github.com/cucumber/godog"
)

// wave2 is the per-scenario state wave 2 adds to the world.
type wave2 struct {
	*world
	header  http.Header
	event   map[string]any // the outbox CloudEvent selected by "I read the ... event"
	eventKy string         // its Kafka key
	eventCT string         // its content-type header
	lastErr error          // the last error a consumer returned
	planRaw []byte         // the last plan body kept for a read-back comparison
}

func registerWave2(sc *godog.ScenarioContext, w *world) {
	x := &wave2{world: w}
	sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
		x.header, x.event, x.eventKy, x.eventCT, x.lastErr, x.planRaw = nil, nil, "", "", nil, nil
		return ctx, nil
	})
	x.registerRequests(sc)
	x.registerConsumerGivens(sc)
	x.registerLayoutGivens(sc)
	x.registerResponseChecks(sc)
	x.registerEventChecks(sc)
}

// --- requests -----------------------------------------------------------

func (x *wave2) send(method, path, body string) error {
	var reader io.Reader = http.NoBody
	if body != "" {
		reader = strings.NewReader(body)
	}
	req, err := http.NewRequestWithContext(context.Background(), method, x.server.URL+path, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	x.status, x.header = resp.StatusCode, resp.Header
	x.body, err = io.ReadAll(resp.Body)
	return err
}

func (x *wave2) mustSend(path, body string) error {
	if err := x.send(http.MethodPost, path, body); err != nil {
		return err
	}
	if x.status != http.StatusCreated && x.status != http.StatusOK {
		return fmt.Errorf("setup POST %s = %d %s", path, x.status, x.body)
	}
	return nil
}

func (x *wave2) registerRequests(sc *godog.ScenarioContext) {
	sc.Step(`^I send (GET|POST) "([^"]*)"$`, func(verb, path string) error { return x.send(verb, path, "") })
	sc.Step(`^I send POST "([^"]*)" with the JSON (.+)$`, func(path, body string) error {
		return x.send(http.MethodPost, path, body)
	})
	sc.Step(`^I generate a slot plan for site "([^"]*)"$`, func(site string) error {
		return x.mustSend("/slot-plans", fmt.Sprintf(`{"siteId":%q}`, site))
	})
	sc.Step(`^I generate a slot plan looking back (\d+) days$`, func(days int) error {
		return x.mustSend("/slot-plans", fmt.Sprintf(`{"lookbackDays":%d}`, days))
	})
	sc.Step(`^I reject plan number (\d+) without a reason$`, func(n int) error {
		return x.send(http.MethodPost, "/slot-plans/"+planID(n)+"/reject", "")
	})
	sc.Step(`^I reject plan number (\d+) with a reason made of (\d+) times "([^"]*)"$`, func(n, times int, unit string) error {
		b, _ := json.Marshal(map[string]string{"reason": strings.Repeat(unit, times)})
		return x.send(http.MethodPost, "/slot-plans/"+planID(n)+"/reject", string(b))
	})
	sc.Step(`^I keep the generated plan$`, func() error { x.planRaw = append([]byte(nil), x.body...); return nil })
	sc.Step(`^reading plan number (\d+) returns exactly the plan that was kept$`, x.readBackEqualsKept)
}

func (x *wave2) readBackEqualsKept(n int) error {
	if err := x.send(http.MethodGet, "/slot-plans/"+planID(n), ""); err != nil {
		return err
	}
	var want, got any
	if err := json.Unmarshal(x.planRaw, &want); err != nil {
		return fmt.Errorf("kept plan is not JSON: %w", err)
	}
	if err := json.Unmarshal(x.body, &got); err != nil {
		return fmt.Errorf("read-back is not JSON: %w", err)
	}
	if !reflect.DeepEqual(want, got) {
		return fmt.Errorf("plan read back differs:\n kept: %s\n read: %s", x.planRaw, x.body)
	}
	return nil
}

// --- consumed events: demand, product ----------------------------------

func (x *wave2) deliverID(id, typ, source, subject, schema, data string, c func(string) error) error {
	msg := fmt.Sprintf(`{"specversion":"1.0","id":%q,"source":%q,"type":%q,"subject":%q,"time":"2026-10-07T09:00:00Z",`+
		`"datacontenttype":"application/json","dataschema":%q,"data":%s}`, id, source, typ, subject, schema, data)
	x.lastErr = c(msg)
	return nil
}

func (x *wave2) demandRaw(id, order string, line int, site, sku, state string, units int, due string) error {
	data := fmt.Sprintf(`{"source_order_id":%q,"line_no":%d,"site_id":%q,"sku":%q,"demanded_units":%d,"due_at":%q,"state":%q,"assignment_version":"static-site-v1"}`,
		order, line, site, sku, units, due, state)
	return x.deliverID(id, "com.warehouse.wes.order-management.siteskudemand.SiteSkuDemandChanged", "/warehouse/order-management",
		fmt.Sprintf("%s/line/%d", order, line), "urn:warehouse:order-management:events:SiteSkuDemandChanged:v1", data,
		func(m string) error { return x.demand.HandleMessage(context.Background(), []byte(m)) })
}

func (x *wave2) nextID() string {
	x.eventSeq++
	return "w2-evt-" + strconv.Itoa(x.eventSeq)
}

func (x *wave2) productRaw(typ, sku, data string) error {
	return x.deliverID(x.nextID(), typ, "/warehouse/product-master", sku,
		"urn:warehouse:product-master:events:"+typ[strings.LastIndex(typ, ".")+1:]+":v1", data,
		func(m string) error { return x.product.HandleMessage(context.Background(), []byte(m)) })
}

func (x *wave2) registerConsumerGivens(sc *godog.ScenarioContext) {
	sc.Step(`^the demand event "([^"]*)" marks line (\d+) of order "([^"]*)" as "(ACTIVE|REMOVED)" for "([^"]*)" with (\d+) units at site "([^"]*)"$`,
		func(id string, line int, order, state, sku string, units int, site string) error {
			return x.demandRaw(id, order, line, site, sku, state, units, dueAt)
		})
	sc.Step(`^order "([^"]*)" line (\d+) of "([^"]*)" at site "([^"]*)" is re-sent as "(ACTIVE|REMOVED)" with (\d+) units$`,
		func(order string, line int, sku, site, state string, units int) error {
			return x.demandRaw(x.nextID(), order, line, site, sku, state, units, dueAt)
		})
	sc.Step(`^a demand event arrives for order "([^"]*)" line (-?\d+) of "([^"]*)" at site "([^"]*)" with state "([^"]*)" and (-?\d+) units$`,
		func(order string, line int, sku, site, state string, units int) error {
			return x.demandRaw(x.nextID(), order, line, site, sku, state, units, dueAt)
		})
	sc.Step(`^"([^"]*)" has a demand line of (\d+) units due at "([^"]*)" at site "([^"]*)"$`,
		func(sku string, units int, due, site string) error {
			return x.demandRaw(x.nextID(), "ord-"+sku+"-"+due, 1, site, sku, "ACTIVE", units, due)
		})
	sc.Step(`^the demand consumer receives (a message that is not JSON|a CloudEvent of a type it does not consume|a demand event whose data is a string)$`,
		x.demandUnusable)
	sc.Step(`^the delivery raised no error$`, func() error {
		if x.lastErr != nil {
			return fmt.Errorf("consumer returned %v, want nil", x.lastErr)
		}
		return nil
	})
	sc.Step(`^"([^"]*)" is classified at version (\d+) with tags "([^"]*)" and temperature "([^"]*)"$`, x.classifiedAt)
	sc.Step(`^the product-master declares "([^"]*)" at (\d+) mm3 and (\d+) g with version (\d+)$`, func(sku string, v, g, ver int) error {
		return x.physicalRaw("com.warehouse.wms.product-master.product.ProductDimensionsDeclared", sku, v, g, ver, "declared")
	})
	sc.Step(`^the product-master measures "([^"]*)" at (\d+) mm3 and (\d+) g with version (\d+)$`, func(sku string, v, g, ver int) error {
		return x.physicalRaw("com.warehouse.wms.product-master.product.ProductMeasured", sku, v, g, ver, "measured")
	})
	sc.Step(`^the product-master reports "([^"]*)" with no effective dimensions at version (\d+)$`, func(sku string, ver int) error {
		data := fmt.Sprintf(`{"sku":%q,"effective":null,"effective_source":"none","version":%d}`, sku, ver)
		return x.productRaw("com.warehouse.wms.product-master.product.ProductMeasured", sku, data)
	})
}

func (x *wave2) demandUnusable(kind string) error {
	switch kind {
	case "a message that is not JSON":
		x.lastErr = x.demand.HandleMessage(context.Background(), []byte("definitely not a cloud event"))
	case "a CloudEvent of a type it does not consume":
		return x.deliverID(x.nextID(), "com.warehouse.wes.order-management.order.OrderPlaced", "/warehouse/order-management", "ord-1",
			"urn:warehouse:order-management:events:OrderPlaced:v1", `{"anything":true}`,
			func(m string) error { return x.demand.HandleMessage(context.Background(), []byte(m)) })
	default:
		return x.deliverID(x.nextID(), "com.warehouse.wes.order-management.siteskudemand.SiteSkuDemandChanged", "/warehouse/order-management", "ord-1/line/1",
			"urn:warehouse:order-management:events:SiteSkuDemandChanged:v1", `"not an object"`,
			func(m string) error { return x.demand.HandleMessage(context.Background(), []byte(m)) })
	}
	return nil
}

func (x *wave2) classifiedAt(sku string, version int, tags, temperature string) error {
	tagJSON, _ := json.Marshal(splitList(tags))
	data := fmt.Sprintf(`{"sku":%q,"handling_tags":%s,"temperature_class":%q,"version":%d}`, sku, tagJSON, temperature, version)
	return x.productRaw("com.warehouse.wms.product-master.product.ProductClassified", sku, data)
}

func (x *wave2) physicalRaw(typ, sku string, volume, weight, version int, source string) error {
	data := fmt.Sprintf(`{"sku":%q,"effective":{"volume_mm3":%d,"weight_g":%d},"effective_source":%q,"version":%d}`, sku, volume, weight, source, version)
	return x.productRaw(typ, sku, data)
}

// --- consumed events: layout -------------------------------------------

func (x *wave2) layoutRaw(typ, subject, data string) error {
	name := typ[strings.LastIndex(typ, ".")+1:]
	return x.deliverID(x.nextID(), typ, "/warehouse/facility-layout", subject,
		"urn:warehouse:facility-layout:events:"+name+":v1", data,
		func(m string) error { return x.layout.HandleMessage(context.Background(), []byte(m)) })
}

func (x *wave2) registerLayoutGivens(sc *godog.ScenarioContext) {
	sc.Step(`^the slot "([^"]*)" in zone "([^"]*)" with role "([^"]*)" holds up to ([\d.]+) kg and ([\d.]+) m3$`,
		func(code, zone, role string, kg, m3 float64) error {
			data := fmt.Sprintf(`{"locationCode":%q,"zoneId":%q,"locationType":"ShelfBin","role":%q,"maxWeightKg":%v,"maxVolumeM3":%v}`, code, zone, role, kg, m3)
			return x.layoutRaw("com.warehouse.wms.facility-layout.locationslot.LocationSlotRegistered", code, data)
		})
	sc.Step(`^the slot "([^"]*)" in zone "([^"]*)" is registered with a negative weight capacity$`, func(code, zone string) error {
		data := fmt.Sprintf(`{"locationCode":%q,"zoneId":%q,"locationType":"ShelfBin","maxWeightKg":-1,"maxVolumeM3":0.5}`, code, zone)
		return x.layoutRaw("com.warehouse.wms.facility-layout.locationslot.LocationSlotRegistered", code, data)
	})
}

// --- response checks ----------------------------------------------------

func (x *wave2) registerResponseChecks(sc *godog.ScenarioContext) {
	sc.Step(`^the response header "([^"]*)" is "([^"]*)"$`, func(name, want string) error {
		if got := x.header.Get(name); got != want {
			return fmt.Errorf("header %s = %q, want %q", name, got, want)
		}
		return nil
	})
	sc.Step(`^the response header "([^"]*)" starts with "([^"]*)"$`, func(name, prefix string) error {
		if got := x.header.Get(name); !strings.HasPrefix(got, prefix) {
			return fmt.Errorf("header %s = %q, want prefix %q", name, got, prefix)
		}
		return nil
	})
	sc.Step(`^the response field "([^"]*)" is present$`, func(path string) error {
		if _, ok, err := x.field(path); err != nil || !ok {
			return fmt.Errorf("field %q absent in %s (%v)", path, x.body, err)
		}
		return nil
	})
	sc.Step(`^the response list "([^"]*)" has (\d+) entries$`, func(key string, n int) error {
		items, err := x.list(key)
		if err != nil {
			return err
		}
		if len(items) != n {
			return fmt.Errorf("%q has %d entries, want %d (%s)", key, len(items), n, x.body)
		}
		return nil
	})
	sc.Step(`^the response list "([^"]*)" names the SKUs "([^"]*)" in that order$`, x.listSKUsInOrder)
	sc.Step(`^plan number (\d+) is listed with (\d+) assignments, (\d+) moves and (\d+) unassigned at version (\d+)$`, x.planListed)
	sc.Step(`^the outbox event ids are all distinct$`, x.outboxIDsDistinct)
}

func (x *wave2) listSKUsInOrder(key, want string) error {
	items, err := x.list(key)
	if err != nil {
		return err
	}
	got := make([]string, len(items))
	for i, it := range items {
		got[i] = fmt.Sprint(it["sku"])
	}
	if strings.Join(got, ",") != strings.Join(splitList(want), ",") {
		return fmt.Errorf("%q SKUs = %v, want %s", key, got, want)
	}
	return nil
}

func (x *wave2) planListed(n, assignments, moves, unassigned, version int) error {
	items, err := x.list("items")
	if err != nil {
		return err
	}
	for _, it := range items {
		if it["planId"] != planID(n) {
			continue
		}
		got := fmt.Sprintf("%v/%v/%v/%v", it["assignmentCount"], it["moveCount"], it["unassignedCount"], it["version"])
		want := fmt.Sprintf("%d/%d/%d/%d", assignments, moves, unassigned, version)
		if got != want {
			return fmt.Errorf("plan %d summary = %s, want %s (%s)", n, got, want, x.body)
		}
		return nil
	}
	return fmt.Errorf("plan %d is not listed in %s", n, x.body)
}

func (x *wave2) outboxIDsDistinct() error {
	seen := map[string]bool{}
	msgs := x.outbox.Messages()
	for _, m := range msgs {
		if m.EventID == "" || seen[m.EventID] {
			return fmt.Errorf("event id %q is empty or repeated among %d events", m.EventID, len(msgs))
		}
		seen[m.EventID] = true
	}
	return nil
}

// --- outbox event checks ------------------------------------------------

func (x *wave2) registerEventChecks(sc *godog.ScenarioContext) {
	sc.Step(`^I read the "([^"]*)" event of plan number (\d+)$`, x.readEvent)
	sc.Step(`^the event field "([^"]*)" is "([^"]*)"$`, x.eventFieldIs)
	sc.Step(`^the event field "([^"]*)" is plan number (\d+)$`, func(path string, n int) error { return x.eventFieldIs(path, planID(n)) })
	sc.Step(`^the event field "([^"]*)" is present$`, func(path string) error {
		if _, ok := x.eventField(path); !ok {
			return fmt.Errorf("event field %q absent in %v", path, x.event)
		}
		return nil
	})
	sc.Step(`^the event field "([^"]*)" is absent$`, func(path string) error {
		if _, ok := x.eventField(path); ok {
			return fmt.Errorf("event field %q present in %v", path, x.event)
		}
		return nil
	})
	sc.Step(`^the event field "([^"]*)" has (\d+) items$`, func(path string, n int) error {
		v, ok := x.eventField(path)
		arr, isArr := v.([]any)
		if !ok || !isArr || len(arr) != n {
			return fmt.Errorf("event field %q = %v, want a list of %d", path, v, n)
		}
		return nil
	})
	sc.Step(`^the event Kafka key is plan number (\d+)$`, func(n int) error {
		if x.eventKy != planID(n) {
			return fmt.Errorf("kafka key = %q, want %q", x.eventKy, planID(n))
		}
		return nil
	})
	sc.Step(`^the event carries the CloudEvents content type header$`, func() error {
		if x.eventCT != "application/cloudevents+json; charset=UTF-8" {
			return fmt.Errorf("content-type header = %q", x.eventCT)
		}
		return nil
	})
	sc.Step(`^the event assigns "([^"]*)" to slot "([^"]*)"$`, x.eventAssigns)
	sc.Step(`^the event has an "Assign" move of "([^"]*)" to "([^"]*)"$`, func(sku, to string) error { return x.eventMove("Assign", sku, "", to) })
	sc.Step(`^the event has a "Relocate" move of "([^"]*)" from "([^"]*)" to "([^"]*)"$`, func(sku, from, to string) error {
		return x.eventMove("Relocate", sku, from, to)
	})
	sc.Step(`^the event has a "Vacate" move of "([^"]*)" from "([^"]*)"$`, func(sku, from string) error { return x.eventMove("Vacate", sku, from, "") })
}

func (x *wave2) readEvent(name string, n int) error {
	for _, m := range x.outbox.Messages() {
		if m.Subject != planID(n) || !strings.HasSuffix(m.EventType, "."+name) {
			continue
		}
		var ev map[string]any
		if err := json.Unmarshal(m.Value, &ev); err != nil {
			return fmt.Errorf("outbox value is not JSON: %w", err)
		}
		x.event, x.eventKy = ev, string(m.Key)
		for _, h := range m.Headers {
			if h.Key == "content-type" {
				x.eventCT = h.Value
			}
		}
		return nil
	}
	return fmt.Errorf("no %s event of %s in the outbox (%d events)", name, planID(n), len(x.outbox.Messages()))
}

func (x *wave2) eventField(path string) (any, bool) {
	var cur any = x.event
	for _, key := range strings.Split(path, ".") {
		obj, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		if cur, ok = obj[key]; !ok {
			return nil, false
		}
	}
	return cur, true
}

func (x *wave2) eventFieldIs(path, want string) error {
	v, ok := x.eventField(path)
	if !ok {
		return fmt.Errorf("event field %q absent in %v", path, x.event)
	}
	if got := render(v); got != want {
		return fmt.Errorf("event field %q = %q, want %q", path, got, want)
	}
	return nil
}

func (x *wave2) eventList(path string) []map[string]any {
	v, _ := x.eventField(path)
	raw, _ := v.([]any)
	out := make([]map[string]any, 0, len(raw))
	for _, it := range raw {
		m, _ := it.(map[string]any)
		out = append(out, m)
	}
	return out
}

func (x *wave2) eventAssigns(sku, slot string) error {
	for _, a := range x.eventList("data.assignments") {
		if a["sku"] == sku {
			if a["slot"] != slot {
				return fmt.Errorf("event assigns %s to %v, want %s", sku, a["slot"], slot)
			}
			return nil
		}
	}
	return fmt.Errorf("event has no assignment of %s in %v", sku, x.event["data"])
}

// eventMove finds the move of sku and checks its kind and that the from/to
// slots are present exactly when expected (an empty slot must be omitted).
func (x *wave2) eventMove(kind, sku, from, to string) error {
	for _, m := range x.eventList("data.moves") {
		if m["sku"] != sku {
			continue
		}
		if m["kind"] != kind {
			return fmt.Errorf("move of %s is %v, want %s", sku, m["kind"], kind)
		}
		for key, want := range map[string]string{"from_slot": from, "to_slot": to} {
			got, present := m[key]
			if want == "" && present {
				return fmt.Errorf("%s move of %s carries %s=%v, want it omitted", kind, sku, key, got)
			}
			if want != "" && got != want {
				return fmt.Errorf("%s move of %s has %s=%v, want %s", kind, sku, key, got, want)
			}
		}
		return nil
	}
	return fmt.Errorf("event has no move of %s in %v", sku, x.event["data"])
}
