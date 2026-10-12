package mcp_test

import (
	"context"
	"strings"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	inboundmcp "github.com/claudioed/slotting-optimization/internal/adapters/inbound/mcp"
	"github.com/claudioed/slotting-optimization/internal/adapters/outbound/ids"
	outboundkafka "github.com/claudioed/slotting-optimization/internal/adapters/outbound/kafka"
	"github.com/claudioed/slotting-optimization/internal/adapters/outbound/memory"
	"github.com/claudioed/slotting-optimization/internal/application/ports"
	"github.com/claudioed/slotting-optimization/internal/application/repository"
	"github.com/claudioed/slotting-optimization/internal/application/usecases"
	"github.com/claudioed/slotting-optimization/internal/domain/planning"
	"github.com/claudioed/slotting-optimization/internal/domain/slotplan"
)

// now is the fixture's fixed "now".
var now = time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)

type fixedClock struct{}

func (fixedClock) Now() time.Time { return now }

// harness is a connected in-memory MCP client over the real server wired to
// in-memory repos, exactly as cmd/mcp wires them (minus Postgres). The write
// use cases exist ONLY here, to seed data the way the REST adapter would:
// the MCP surface itself has none.
type harness struct {
	session  *sdk.ClientSession
	demand   *memory.DemandLedger
	profiles *memory.ProfileDirectory
	slots    *memory.SlotCatalogue
	outbox   *memory.OutboxRepo

	generate *usecases.GeneratePlan
	approve  *usecases.ApprovePlan
	reject   *usecases.RejectPlan
}

func depsFor(plans ports.SlotPlanRepository, demand *memory.DemandLedger, site string) inboundmcp.Deps {
	return inboundmcp.Deps{
		GetPlan:          &usecases.GetPlan{Plans: plans},
		ListPlans:        &usecases.ListPlans{Plans: plans},
		ListForwardSlots: &usecases.ListForwardSlots{Plans: plans, DefaultSite: site},
		ListSkuVelocity:  &usecases.ListSkuVelocity{Demand: demand, Clock: fixedClock{}, DefaultSite: site},
	}
}

func connectSession(t *testing.T, deps inboundmcp.Deps) *sdk.ClientSession {
	t.Helper()
	ctx := context.Background()
	clientTransport, serverTransport := sdk.NewInMemoryTransports()
	serverSession, err := inboundmcp.NewServer(deps).Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	t.Cleanup(func() { _ = serverSession.Close() })
	client := sdk.NewClient(&sdk.Implementation{Name: "test-client", Version: "0"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}

// newHarness builds the server with SITE-1 as the default site.
func newHarness(t *testing.T) *harness { return newHarnessWithSite(t, "SITE-1") }

func newHarnessWithSite(t *testing.T, defaultSite string) *harness {
	t.Helper()
	plans, ob := memory.NewSlotPlanRepo(), memory.NewOutboxRepo()
	demand, profiles, slots := memory.NewDemandLedger(), memory.NewProfileDirectory(), memory.NewSlotCatalogue()
	planner, err := planning.NewPlanner(planning.LexicalRanking{})
	if err != nil {
		t.Fatal(err)
	}
	w := usecases.Writer{
		Plans: plans, Outbox: ob, Encoder: outboundkafka.NewEncoder(),
		UoW: memory.NewUnitOfWork(plans, ob, demand, profiles, slots), Clock: fixedClock{},
	}
	return &harness{
		session: connectSession(t, depsFor(plans, demand, defaultSite)),
		demand:  demand, profiles: profiles, slots: slots, outbox: ob,
		generate: &usecases.GeneratePlan{
			Writer: w, Demand: demand, Profiles: profiles, Catalogue: slots, IDs: ids.UUID{}, Planner: planner,
			DefaultSite: "SITE-1", ForwardZoneCodes: []string{"FWD"},
		},
		approve: &usecases.ApprovePlan{Writer: w},
		reject:  &usecases.RejectPlan{Writer: w},
	}
}

// seed gives SITE-1 three SKUs with demand (SKU-1: 2 lines/15 units, SKU-2: 1
// line/4 units, SKU-3: 1 line/1 unit; SKU-3 has no dimensions), dimensions
// for SKU-1 and SKU-2, and two forward slots.
func (h *harness) seed(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	due := now.Add(-time.Hour)
	for _, l := range []repository.DemandLine{
		{SourceOrderID: "o1", LineNo: 1, Site: "SITE-1", SKU: "SKU-1", Units: 10, DueAt: due, Active: true},
		{SourceOrderID: "o1", LineNo: 2, Site: "SITE-1", SKU: "SKU-1", Units: 5, DueAt: due, Active: true},
		{SourceOrderID: "o2", LineNo: 1, Site: "SITE-1", SKU: "SKU-2", Units: 4, DueAt: due, Active: true},
		{SourceOrderID: "o3", LineNo: 1, Site: "SITE-1", SKU: "SKU-3", Units: 1, DueAt: due, Active: true},
		{SourceOrderID: "o4", LineNo: 1, Site: "SITE-2", SKU: "SKU-9", Units: 7, DueAt: due, Active: true},
		{SourceOrderID: "o5", LineNo: 1, Site: "SITE-1", SKU: "SKU-OLD", Units: 2, DueAt: now.Add(-60 * 24 * time.Hour), Active: true},
		{SourceOrderID: "o6", LineNo: 1, Site: "SITE-1", SKU: "SKU-CANCELLED", Units: 2, DueAt: due, Active: false},
	} {
		must(t, h.demand.Apply(ctx, l))
	}
	for _, sku := range []slotplan.SKU{"SKU-1", "SKU-2"} {
		must(t, h.profiles.Save(ctx, repository.ProductProfile{SKU: sku, Unit: &planning.UnitSize{VolumeMM3: 1_000_000, WeightG: 1000}, Version: 1}))
	}
	must(t, h.slots.SaveZone(ctx, repository.Zone{ID: "Z1", SiteCode: "SITE-1", ZoneCode: "FWD", TemperatureClass: planning.Ambient}))
	for _, code := range []slotplan.SlotCode{"A-01", "A-02"} {
		must(t, h.slots.SaveSlot(ctx, repository.Slot{Code: code, ZoneID: "Z1", Role: repository.RoleStorage, MaxWeightKg: 40, MaxVolumeM3: 0.5}))
	}
}

// draft generates a Draft plan of SITE-1 from the seeded data and returns
// its id.
func (h *harness) draft(t *testing.T) string {
	t.Helper()
	p, err := h.generate.Handle(context.Background(), usecases.GenerateInput{})
	if err != nil {
		t.Fatalf("generate plan: %v", err)
	}
	return string(p.ID())
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// call invokes a tool and fails the test on a TRANSPORT/protocol error;
// tool-level failures come back as res.IsError.
func call(t *testing.T, session *sdk.ClientSession, name string, args map[string]any) *sdk.CallToolResult {
	t.Helper()
	res, err := session.CallTool(context.Background(), &sdk.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: transport/protocol error (want a tool result): %v", name, err)
	}
	return res
}

// ok calls a tool, requires success, and returns its structured content.
func (h *harness) ok(t *testing.T, name string, args map[string]any) map[string]any {
	t.Helper()
	res := call(t, h.session, name, args)
	if res.IsError {
		t.Fatalf("%s returned a tool error: %s", name, text(res))
	}
	sc, isMap := res.StructuredContent.(map[string]any)
	if !isMap {
		t.Fatalf("%s: structured content = %#v, want an object", name, res.StructuredContent)
	}
	return sc
}

// failWith calls a tool and requires an isError result whose text starts
// with the slug want.
func failWith(t *testing.T, session *sdk.ClientSession, name string, args map[string]any, want string) {
	t.Helper()
	res := call(t, session, name, args)
	if !res.IsError {
		t.Fatalf("%s: expected an isError tool result, got %#v", name, res.StructuredContent)
	}
	if got := text(res); !strings.HasPrefix(got, want+": ") {
		t.Fatalf("%s: error text %q does not start with %q", name, got, want+": ")
	}
}

func text(res *sdk.CallToolResult) string {
	var b strings.Builder
	for _, c := range res.Content {
		if tc, isText := c.(*sdk.TextContent); isText {
			b.WriteString(tc.Text)
		}
	}
	return b.String()
}

// strs returns the string field key of every item of a list result, in order.
func strs(page map[string]any, listKey, key string) []string {
	var out []string
	items, _ := page[listKey].([]any)
	for _, item := range items {
		out = append(out, item.(map[string]any)[key].(string))
	}
	return out
}
