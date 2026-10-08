// Package main_test hosts the godog (Cucumber for Go) acceptance suite. It
// drives the REAL chi router over HTTP, and feeds the local copies through the
// REAL consumers' HandleMessage with raw CloudEvents (the producers' own
// contracts), over the in-memory adapters wired the way cmd/api wires them. So
// every scenario in features/*.feature is a black-box test of the service.
package main_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/cucumber/godog"

	inboundhttp "github.com/claudioed/slotting-optimization/internal/adapters/inbound/http"
	inboundkafka "github.com/claudioed/slotting-optimization/internal/adapters/inbound/kafka"
	outboundkafka "github.com/claudioed/slotting-optimization/internal/adapters/outbound/kafka"
	"github.com/claudioed/slotting-optimization/internal/adapters/outbound/memory"
	"github.com/claudioed/slotting-optimization/internal/application/usecases"
	"github.com/claudioed/slotting-optimization/internal/domain/planning"
	"github.com/claudioed/slotting-optimization/internal/domain/slotplan"
)

// TestFeatures runs every Gherkin feature under features/.
func TestFeatures(t *testing.T) {
	suite := godog.TestSuite{
		ScenarioInitializer: InitializeScenario,
		Options: &godog.Options{
			Format:   "pretty",
			Paths:    []string{"features"},
			Strict:   true,
			TestingT: t,
		},
	}
	if suite.Run() != 0 {
		t.Fatal("non-zero status returned, failed to run feature tests")
	}
}

// bddNow is the service clock of every scenario; demand is due three days
// earlier, inside the default 28-day window.
var bddNow = time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)

const dueAt = "2026-10-05T10:00:00Z"

type fixedClock struct{}

func (fixedClock) Now() time.Time { return bddNow }

// sequentialIDs mints plan-0001, plan-0002, ... so scenarios can name plans
// by number and "newest first" has a total order under a fixed clock.
type sequentialIDs struct{ n int }

func (s *sequentialIDs) NewPlanID() slotplan.PlanID {
	s.n++
	return slotplan.PlanID(planID(s.n))
}

func planID(n int) string { return fmt.Sprintf("plan-%04d", n) }

// world is the per-scenario state.
type world struct {
	server    *httptest.Server
	outbox    *memory.OutboxRepo
	demand    *inboundkafka.Consumer
	product   *inboundkafka.Consumer
	layout    *inboundkafka.Consumer
	status    int
	body      []byte
	listQuery string

	eventSeq int
	versions map[string]int64 // product version per SKU
	units    map[string]int   // units per demand line per SKU
}

func (w *world) start() {
	plans, ob, processed := memory.NewSlotPlanRepo(), memory.NewOutboxRepo(), memory.NewProcessedEventRepo()
	demand, profiles, catalogue := memory.NewDemandLedger(), memory.NewProfileDirectory(), memory.NewSlotCatalogue()
	uow := memory.NewUnitOfWork(plans, ob, demand, profiles, catalogue, processed)
	writer := usecases.Writer{Plans: plans, Outbox: ob, Encoder: outboundkafka.NewEncoder(), UoW: uow, Clock: fixedClock{}}
	planner, err := planning.NewPlanner(planning.LexicalRanking{})
	if err != nil {
		panic(err)
	}
	s := &inboundhttp.Server{
		GeneratePlan: &usecases.GeneratePlan{
			Writer: writer, Demand: demand, Profiles: profiles, Catalogue: catalogue, IDs: &sequentialIDs{}, Planner: planner,
			DefaultSite: "SITE-1", ForwardZoneCodes: []string{"FWD"},
		},
		ApprovePlan:      &usecases.ApprovePlan{Writer: writer},
		RejectPlan:       &usecases.RejectPlan{Writer: writer},
		GetPlan:          &usecases.GetPlan{Plans: plans},
		ListPlans:        &usecases.ListPlans{Plans: plans},
		ListForwardSlots: &usecases.ListForwardSlots{Plans: plans, DefaultSite: "SITE-1"},
		ListSkuVelocity:  &usecases.ListSkuVelocity{Demand: demand, Clock: fixedClock{}, DefaultSite: "SITE-1"},
	}
	w.server = httptest.NewServer(inboundhttp.NewRouter(s))
	w.outbox = ob

	intake := usecases.Intake{UoW: uow, Processed: processed}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	classified := &usecases.ApplyProductClassified{Intake: intake, Profiles: profiles}
	physical := &usecases.ApplyPhysicalProfile{Intake: intake, Profiles: profiles}
	w.demand = &inboundkafka.Consumer{Name: "demand", Logger: logger,
		Handlers: inboundkafka.DemandHandlers(&usecases.ApplyDemandChanged{Intake: intake, Demand: demand}, logger)}
	w.product = &inboundkafka.Consumer{Name: "product", Logger: logger, Handlers: inboundkafka.ProductHandlers(classified, physical, logger)}
	w.layout = &inboundkafka.Consumer{Name: "layout", Logger: logger, Handlers: inboundkafka.LayoutHandlers(
		&usecases.ApplyZoneRegistered{Intake: intake, Catalogue: catalogue},
		&usecases.ApplyLocationSlotRegistered{Intake: intake, Catalogue: catalogue},
		&usecases.ApplyLocationSlotDecommissioned{Intake: intake, Catalogue: catalogue}, logger)}

	w.status, w.body, w.listQuery, w.eventSeq = 0, nil, "", 0
	w.versions, w.units = map[string]int64{}, map[string]int{}
}

func (w *world) stop() {
	if w.server != nil {
		w.server.Close()
		w.server = nil
	}
}

// --- HTTP -------------------------------------------------------------

func (w *world) call(method, path, body string) error {
	var reader io.Reader = http.NoBody
	if body != "" {
		reader = strings.NewReader(body)
	}
	req, err := http.NewRequestWithContext(context.Background(), method, w.server.URL+path, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	w.status = resp.StatusCode
	w.body, err = io.ReadAll(resp.Body)
	return err
}

// mustCall is call for Given steps: the setup itself must succeed.
func (w *world) mustCall(method, path, body string) error {
	if err := w.call(method, path, body); err != nil {
		return err
	}
	if w.status != http.StatusOK && w.status != http.StatusCreated {
		return fmt.Errorf("setup %s %s = %d %s", method, path, w.status, w.body)
	}
	return nil
}

// iRequest remembers a GET path so "I list the next page" can extend it.
func (w *world) iRequest(verb, path string) error {
	w.listQuery = path
	return w.call(verb, path, "")
}

func (w *world) iGenerateAPlan() error { return w.mustCall(http.MethodPost, "/slot-plans", "{}") }

func (w *world) iApprovePlan(n int) error {
	return w.call(http.MethodPost, "/slot-plans/"+planID(n)+"/approve", "")
}

func (w *world) iRejectPlan(n int, reason string) error {
	b, _ := json.Marshal(map[string]string{"reason": reason})
	return w.call(http.MethodPost, "/slot-plans/"+planID(n)+"/reject", string(b))
}

func (w *world) planIsInState(n int, state string) error {
	if err := w.call(http.MethodGet, "/slot-plans/"+planID(n), ""); err != nil {
		return err
	}
	return w.theResponseFieldIs("state", state)
}

// --- consumed events (raw CloudEvents, the producers' contracts) ----------

func (w *world) deliver(c *inboundkafka.Consumer, typ, source, subject, schema, data string) error {
	w.eventSeq++
	msg := fmt.Sprintf(`{"specversion":"1.0","id":"evt-%d","source":%q,"type":%q,"subject":%q,"time":"2026-10-07T09:00:00Z",`+
		`"datacontenttype":"application/json","dataschema":%q,"data":%s}`, w.eventSeq, source, typ, subject, schema, data)
	return c.HandleMessage(context.Background(), []byte(msg))
}

func (w *world) demandEvent(orderID, site, sku, state string, units int) error {
	data := fmt.Sprintf(`{"source_order_id":%q,"line_no":1,"site_id":%q,"sku":%q,"demanded_units":%d,"due_at":%q,"state":%q,"assignment_version":"static-site-v1"}`,
		orderID, site, sku, units, dueAt, state)
	return w.deliver(w.demand, inboundkafka.TypeSiteSkuDemandChanged, "/warehouse/order-management", orderID+"/line/1",
		"urn:warehouse:order-management:events:SiteSkuDemandChanged:v1", data)
}

func orderOf(sku string) string { return "ord-" + sku + "-1" }

func (w *world) hasDemandLines(sku string, lines, units int, site string) error {
	w.units[sku] = units
	for i := 1; i <= lines; i++ {
		if err := w.demandEvent(fmt.Sprintf("ord-%s-%d", sku, i), site, sku, "ACTIVE", units); err != nil {
			return err
		}
	}
	return nil
}

func (w *world) firstLineRemoved(sku, site string) error {
	return w.demandEvent(orderOf(sku), site, sku, "REMOVED", w.units[sku])
}

func (w *world) firstLineChangesTo(sku, site, other string) error {
	return w.demandEvent(orderOf(sku), site, other, "ACTIVE", w.units[sku])
}

func (w *world) classifiedWith(sku, tags, temperature string) error {
	w.versions[sku]++
	tagJSON, _ := json.Marshal(splitList(tags))
	data := fmt.Sprintf(`{"sku":%q,"handling_tags":%s,"temperature_class":%q,"version":%d}`, sku, tagJSON, temperature, w.versions[sku])
	return w.deliver(w.product, "com.warehouse.wms.product-master.product.ProductClassified", "/warehouse/product-master", sku,
		"urn:warehouse:product-master:events:ProductClassified:v1", data)
}

func (w *world) physical(sku string, volume, weight int, version int64) error {
	data := fmt.Sprintf(`{"sku":%q,"effective":{"volume_mm3":%d,"weight_g":%d},"effective_source":"measured","version":%d}`, sku, volume, weight, version)
	return w.deliver(w.product, inboundkafka.TypeProductMeasured, "/warehouse/product-master", sku,
		"urn:warehouse:product-master:events:ProductMeasured:v1", data)
}

func (w *world) hasEffectiveSize(sku string, volume, weight int) error {
	w.versions[sku]++
	return w.physical(sku, volume, weight, w.versions[sku])
}

func (w *world) staleProfile(sku string, volume, weight int) error {
	return w.physical(sku, volume, weight, w.versions[sku]-1)
}

func (w *world) zoneRegistered(hazmat bool, zone, site, code, temperature string) error {
	data := fmt.Sprintf(`{"zoneId":%q,"siteCode":%q,"areaCode":"A","zoneCode":%q,"temperatureClass":%q,"hazmat":%t}`, zone, site, code, temperature, hazmat)
	return w.deliver(w.layout, inboundkafka.TypeZoneRegistered, "/warehouse/facility-layout", zone,
		"urn:warehouse:facility-layout:events:ZoneRegistered:v1", data)
}

func (w *world) plainZone(zone, site, code, temperature string) error {
	return w.zoneRegistered(false, zone, site, code, temperature)
}

func (w *world) hazmatZone(zone, site, code, temperature string) error {
	return w.zoneRegistered(true, zone, site, code, temperature)
}

func (w *world) slotRegistered(code, zone string, kg, m3 float64) error {
	data := fmt.Sprintf(`{"locationCode":%q,"zoneId":%q,"locationType":"ShelfBin","maxWeightKg":%v,"maxVolumeM3":%v}`, code, zone, kg, m3)
	return w.deliver(w.layout, inboundkafka.TypeLocationSlotRegistered, "/warehouse/facility-layout", code,
		"urn:warehouse:facility-layout:events:LocationSlotRegistered:v1", data)
}

func (w *world) slotDecommissioned(code string) error {
	return w.deliver(w.layout, inboundkafka.TypeLocationSlotDecommissioned, "/warehouse/facility-layout", code,
		"urn:warehouse:facility-layout:events:LocationSlotDecommissioned:v1", fmt.Sprintf(`{"locationCode":%q}`, code))
}

func splitList(raw string) []string {
	out := []string{}
	for _, p := range strings.Split(raw, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// --- assertions ---------------------------------------------------------

func (w *world) theResponseStatusIs(want int) error {
	if w.status != want {
		return fmt.Errorf("status = %d (%s), want %d", w.status, w.body, want)
	}
	return nil
}

// field walks a dotted path into the last JSON response.
func (w *world) field(path string) (any, bool, error) {
	var cur any
	if err := json.Unmarshal(w.body, &cur); err != nil {
		return nil, false, fmt.Errorf("response is not JSON: %s", w.body)
	}
	for _, key := range strings.Split(path, ".") {
		obj, ok := cur.(map[string]any)
		if !ok {
			return nil, false, nil
		}
		if cur, ok = obj[key]; !ok {
			return nil, false, nil
		}
	}
	return cur, true, nil
}

func render(v any) string {
	if f, ok := v.(float64); ok {
		return strconv.FormatFloat(f, 'f', -1, 64)
	}
	return fmt.Sprint(v)
}

func (w *world) theResponseFieldIs(path, want string) error {
	v, ok, err := w.field(path)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("field %q absent in %s", path, w.body)
	}
	if got := render(v); got != want {
		return fmt.Errorf("field %q = %q, want %q (%s)", path, got, want, w.body)
	}
	return nil
}

func (w *world) theResponseFieldIsPlan(path string, n int) error {
	return w.theResponseFieldIs(path, planID(n))
}

func (w *world) theResponseFieldIsAbsent(path string) error {
	_, ok, err := w.field(path)
	if err != nil {
		return err
	}
	if ok {
		return fmt.Errorf("field %q present in %s", path, w.body)
	}
	return nil
}

func (w *world) theProblemTypeIs(slug string) error {
	return w.theResponseFieldIs("type", "https://errors.slotting-optimization.warehouse-systems.dev/"+slug)
}

func (w *world) theOutboxEventTypesAre(names string) error {
	var want, got []string
	for _, n := range splitList(names) {
		want = append(want, "com.warehouse.wms.slotting-optimization.slotplan."+n)
	}
	for _, m := range w.outbox.Messages() {
		got = append(got, m.EventType)
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		return fmt.Errorf("outbox = %v, want %v", got, want)
	}
	return nil
}

// list returns the array under key of the last response as objects.
func (w *world) list(key string) ([]map[string]any, error) {
	v, ok, err := w.field(key)
	if err != nil || !ok {
		return nil, fmt.Errorf("no %q in %s (%v)", key, w.body, err)
	}
	raw, _ := v.([]any)
	out := make([]map[string]any, 0, len(raw))
	for _, item := range raw {
		m, _ := item.(map[string]any)
		out = append(out, m)
	}
	return out, nil
}

func (w *world) find(key, sku string) (map[string]any, error) {
	items, err := w.list(key)
	if err != nil {
		return nil, err
	}
	for _, it := range items {
		if it["sku"] == sku {
			return it, nil
		}
	}
	return nil, nil
}

func (w *world) thePlanAssigns(sku, slot, class string) error {
	a, err := w.find("assignments", sku)
	if err != nil {
		return err
	}
	if a == nil {
		return fmt.Errorf("%s has no assignment in %s", sku, w.body)
	}
	if a["slot"] != slot || a["abcClass"] != class {
		return fmt.Errorf("%s = slot %v class %v, want %s %s", sku, a["slot"], a["abcClass"], slot, class)
	}
	return nil
}

func (w *world) thePlanCounts(sku string, picks, units int) error {
	a, err := w.find("assignments", sku)
	if err != nil || a == nil {
		return fmt.Errorf("%s has no assignment in %s (%v)", sku, w.body, err)
	}
	if a["picks"] != float64(picks) || a["units"] != float64(units) {
		return fmt.Errorf("%s counts %v picks and %v units, want %d and %d", sku, a["picks"], a["units"], picks, units)
	}
	return nil
}

func (w *world) skuIsNotInThePlan(sku string) error {
	for _, key := range []string{"assignments", "unassigned"} {
		it, err := w.find(key, sku)
		if err != nil {
			return err
		}
		if it != nil {
			return fmt.Errorf("%s is in the plan's %s", sku, key)
		}
	}
	return nil
}

func (w *world) thePlanLists(n int) error {
	items, err := w.list("assignments")
	if err != nil {
		return err
	}
	if len(items) != n {
		return fmt.Errorf("%d assignments, want %d (%s)", len(items), n, w.body)
	}
	return nil
}

func (w *world) thePlanHasMoves(n int) error {
	items, err := w.list("moves")
	if err != nil {
		return err
	}
	if len(items) != n {
		return fmt.Errorf("%d moves, want %d (%s)", len(items), n, w.body)
	}
	return nil
}

func (w *world) hasMove(kind, sku string, from, to any) error {
	items, err := w.list("moves")
	if err != nil {
		return err
	}
	for _, m := range items {
		if m["sku"] == sku && m["kind"] == kind && m["fromSlot"] == from && m["toSlot"] == to {
			return nil
		}
	}
	return fmt.Errorf("no %s move of %s from %v to %v in %s", kind, sku, from, to, w.body)
}

func (w *world) hasRelocate(kind, sku, from, to string) error { return w.hasMove(kind, sku, from, to) }
func (w *world) hasAssign(kind, sku, to string) error         { return w.hasMove(kind, sku, nil, to) }
func (w *world) hasVacate(kind, sku, from string) error       { return w.hasMove(kind, sku, from, nil) }

func (w *world) isUnassigned(sku, reason string) error {
	u, err := w.find("unassigned", sku)
	if err != nil {
		return err
	}
	if u == nil || u["reason"] != reason {
		return fmt.Errorf("%s unassigned = %v, want reason %s (%s)", sku, u, reason, w.body)
	}
	return nil
}

func (w *world) velocityItemsAre(want string) error {
	items, err := w.list("items")
	if err != nil {
		return err
	}
	got := make([]string, len(items))
	for i, it := range items {
		got[i] = fmt.Sprintf("%v:%v:%v", it["sku"], it["picks"], it["units"])
	}
	if strings.Join(got, ",") != strings.Join(splitList(want), ",") {
		return fmt.Errorf("velocity = %v, want %s", got, want)
	}
	return nil
}

func (w *world) listedPlansAre(numbers string) error {
	items, err := w.list("items")
	if err != nil {
		return err
	}
	got := make([]string, len(items))
	for i, it := range items {
		n, _ := strconv.Atoi(strings.TrimPrefix(fmt.Sprint(it["planId"]), "plan-"))
		got[i] = strconv.Itoa(n)
	}
	if strings.Join(got, ",") != strings.Join(splitList(numbers), ",") {
		return fmt.Errorf("listed plans %v, want %s", got, numbers)
	}
	return nil
}

func (w *world) iListTheNextPage() error {
	cursor, ok, err := w.field("nextCursor")
	if err != nil || !ok || cursor == nil {
		return fmt.Errorf("no nextCursor in %s (%v)", w.body, err)
	}
	return w.call(http.MethodGet, w.listQuery+"&cursor="+fmt.Sprint(cursor), "")
}

func (w *world) thereIsANextPage() error {
	if v, ok, _ := w.field("nextCursor"); !ok || v == nil {
		return fmt.Errorf("no nextCursor in %s", w.body)
	}
	return nil
}

func (w *world) thereIsNoNextPage() error {
	if v, ok, _ := w.field("nextCursor"); ok && v != nil {
		return fmt.Errorf("unexpected nextCursor in %s", w.body)
	}
	return nil
}

// InitializeScenario registers every step.
func InitializeScenario(sc *godog.ScenarioContext) {
	w := &world{}
	sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
		w.start()
		return ctx, nil
	})
	sc.After(func(ctx context.Context, _ *godog.Scenario, err error) (context.Context, error) {
		w.stop()
		return ctx, err
	})
	registerGiven(sc, w)
	registerWhen(sc, w)
	registerThen(sc, w)
}

func registerGiven(sc *godog.ScenarioContext, w *world) {
	sc.Step(`^the zone "([^"]*)" of site "([^"]*)" with code "([^"]*)" is registered with temperature "([^"]*)"$`, w.plainZone)
	sc.Step(`^the hazmat zone "([^"]*)" of site "([^"]*)" with code "([^"]*)" is registered with temperature "([^"]*)"$`, w.hazmatZone)
	sc.Step(`^the slot "([^"]*)" in zone "([^"]*)" holds up to ([\d.]+) kg and ([\d.]+) m3$`, w.slotRegistered)
	sc.Step(`^the slot "([^"]*)" is decommissioned$`, w.slotDecommissioned)
	sc.Step(`^"([^"]*)" has (\d+) demand lines of (\d+) units each at site "([^"]*)"$`, w.hasDemandLines)
	sc.Step(`^the first demand line of "([^"]*)" at site "([^"]*)" is removed$`, w.firstLineRemoved)
	sc.Step(`^the first demand line of "([^"]*)" at site "([^"]*)" changes to "([^"]*)"$`, w.firstLineChangesTo)
	sc.Step(`^"([^"]*)" is classified with tags "([^"]*)" and temperature "([^"]*)"$`, w.classifiedWith)
	sc.Step(`^"([^"]*)" has an effective size of (\d+) mm3 and (\d+) g$`, w.hasEffectiveSize)
	sc.Step(`^"([^"]*)" gets a newer physical profile of (\d+) mm3 and (\d+) g$`, w.hasEffectiveSize)
	sc.Step(`^"([^"]*)" gets a stale physical profile of (\d+) mm3 and (\d+) g$`, w.staleProfile)
	sc.Step(`^I generate a slot plan$`, w.iGenerateAPlan)
	sc.Step(`^I approve plan number (\d+)$`, w.iApprovePlan)
	sc.Step(`^I reject plan number (\d+) with reason "([^"]*)"$`, w.iRejectPlan)
}

func registerWhen(sc *godog.ScenarioContext, w *world) {
	sc.Step(`^I (GET|POST) "([^"]*)"$`, w.iRequest)
	sc.Step(`^I list the next page$`, w.iListTheNextPage)
}

func registerThen(sc *godog.ScenarioContext, w *world) {
	sc.Step(`^the response status is (\d+)$`, w.theResponseStatusIs)
	sc.Step(`^the response field "([^"]*)" is "([^"]*)"$`, w.theResponseFieldIs)
	sc.Step(`^the response field "([^"]*)" is plan number (\d+)$`, w.theResponseFieldIsPlan)
	sc.Step(`^the response field "([^"]*)" is absent$`, w.theResponseFieldIsAbsent)
	sc.Step(`^the problem type is "([^"]*)"$`, w.theProblemTypeIs)
	sc.Step(`^the outbox event types are "([^"]*)"$`, w.theOutboxEventTypesAre)
	sc.Step(`^plan number (\d+) is "([^"]*)"$`, w.planIsInState)
	sc.Step(`^the plan assigns "([^"]*)" to slot "([^"]*)" in class "([^"]*)"$`, w.thePlanAssigns)
	sc.Step(`^the plan counts (\d+) picks and (\d+) units for "([^"]*)"$`, func(picks, units int, sku string) error { return w.thePlanCounts(sku, picks, units) })
	sc.Step(`^"([^"]*)" is not in the plan$`, w.skuIsNotInThePlan)
	sc.Step(`^the plan lists (\d+) assignments$`, w.thePlanLists)
	sc.Step(`^the plan has (\d+) moves$`, w.thePlanHasMoves)
	sc.Step(`^the plan has a "([^"]*)" move of "([^"]*)" from "([^"]*)" to "([^"]*)"$`, w.hasRelocate)
	sc.Step(`^the plan has a "([^"]*)" move of "([^"]*)" to "([^"]*)"$`, w.hasAssign)
	sc.Step(`^the plan has a "([^"]*)" move of "([^"]*)" from "([^"]*)"$`, w.hasVacate)
	sc.Step(`^"([^"]*)" is unassigned because "([^"]*)"$`, w.isUnassigned)
	sc.Step(`^the velocity items are "([^"]*)"$`, w.velocityItemsAre)
	sc.Step(`^the listed plans are numbers "([^"]*)"$`, w.listedPlansAre)
	sc.Step(`^there is a next page$`, w.thereIsANextPage)
	sc.Step(`^there is no next page$`, w.thereIsNoNextPage)
}
