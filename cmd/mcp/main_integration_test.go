//go:build integration

package main

import (
	"context"
	"os"
	"reflect"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	inboundmcp "github.com/claudioed/slotting-optimization/internal/adapters/inbound/mcp"
	"github.com/claudioed/slotting-optimization/internal/adapters/outbound/clock"
	"github.com/claudioed/slotting-optimization/internal/adapters/outbound/ids"
	outboundkafka "github.com/claudioed/slotting-optimization/internal/adapters/outbound/kafka"
	"github.com/claudioed/slotting-optimization/internal/adapters/outbound/postgres"
	"github.com/claudioed/slotting-optimization/internal/application/ports"
	"github.com/claudioed/slotting-optimization/internal/application/repository"
	"github.com/claudioed/slotting-optimization/internal/application/usecases"
	"github.com/claudioed/slotting-optimization/internal/domain/planning"
	"github.com/claudioed/slotting-optimization/internal/domain/slotplan"
	"github.com/claudioed/slotting-optimization/internal/testing/pgtest"
)

func TestMain(m *testing.M) {
	code := m.Run()
	pgtest.Shutdown()
	os.Exit(code)
}

// stores is the Postgres adapters the api would be wired with, as ports.
type stores struct {
	plans     ports.SlotPlanRepository
	outbox    ports.OutboxRepository
	demand    ports.DemandLedger
	profiles  ports.ProfileDirectory
	catalogue ports.SlotCatalogue
	uow       ports.UnitOfWork
}

// seedThroughWriteUseCases writes demand, profiles, a layout and an Approved
// plan the way cmd/api does (same use cases, Postgres adapters, transactional
// outbox), so the MCP binary is proven to read what the api wrote in the
// shared database. It returns the plan id.
func seedThroughWriteUseCases(t *testing.T, s stores) string {
	t.Helper()
	ctx := context.Background()

	due := time.Now().Add(-time.Hour)
	for _, l := range []repository.DemandLine{
		{SourceOrderID: "o1", LineNo: 1, Site: "SITE-1", SKU: "SKU-1", Units: 10, DueAt: due, Active: true},
		{SourceOrderID: "o1", LineNo: 2, Site: "SITE-1", SKU: "SKU-1", Units: 5, DueAt: due, Active: true},
		{SourceOrderID: "o2", LineNo: 1, Site: "SITE-1", SKU: "SKU-2", Units: 4, DueAt: due, Active: true},
	} {
		if err := s.demand.Apply(ctx, l); err != nil {
			t.Fatalf("seed demand: %v", err)
		}
	}
	for _, sku := range []slotplan.SKU{"SKU-1", "SKU-2"} {
		if err := s.profiles.Save(ctx, repository.ProductProfile{SKU: sku, Unit: &planning.UnitSize{VolumeMM3: 1_000_000, WeightG: 1000}, Version: 1}); err != nil {
			t.Fatalf("seed profile: %v", err)
		}
	}
	if err := s.catalogue.SaveZone(ctx, repository.Zone{ID: "Z1", SiteCode: "SITE-1", ZoneCode: "FWD", TemperatureClass: planning.Ambient}); err != nil {
		t.Fatalf("seed zone: %v", err)
	}
	for _, code := range []slotplan.SlotCode{"A-01", "A-02"} {
		if err := s.catalogue.SaveSlot(ctx, repository.Slot{Code: code, ZoneID: "Z1", Role: repository.RoleStorage, MaxWeightKg: 40, MaxVolumeM3: 0.5}); err != nil {
			t.Fatalf("seed slot: %v", err)
		}
	}

	planner, err := planning.NewPlanner(planning.LexicalRanking{})
	if err != nil {
		t.Fatal(err)
	}
	w := usecases.Writer{Plans: s.plans, Outbox: s.outbox, Encoder: outboundkafka.NewEncoder(), UoW: s.uow, Clock: clock.System{}}
	plan, err := (&usecases.GeneratePlan{
		Writer: w, Demand: s.demand, Profiles: s.profiles, Catalogue: s.catalogue, IDs: ids.UUID{}, Planner: planner,
		DefaultSite: "SITE-1", ForwardZoneCodes: []string{"FWD"},
	}).Handle(ctx, usecases.GenerateInput{})
	if err != nil {
		t.Fatalf("generate plan: %v", err)
	}
	if _, err := (&usecases.ApprovePlan{Writer: w}).Handle(ctx, string(plan.ID())); err != nil {
		t.Fatalf("approve plan: %v", err)
	}
	return string(plan.ID())
}

// The binary migrates a database itself (idempotently: pgtest's databases
// are already migrated, a second run must be a no-op), then reads through MCP
// tools exactly what the write use cases persisted, and its reads add no
// outbox row.
func TestMCPAgainstPostgres_ReadsWhatTheAPIWroteAndNeverWrites(t *testing.T) {
	url := pgtest.URL(t) // a fresh, already migrated database
	ctx := context.Background()

	pool, err := postgres.NewPool(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	countOutbox := func() int {
		t.Helper()
		var n int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM outbox_events`).Scan(&n); err != nil {
			t.Fatalf("count outbox_events: %v", err)
		}
		return n
	}

	planID := seedThroughWriteUseCases(t, stores{
		plans: postgres.NewSlotPlanRepo(pool), outbox: postgres.NewOutboxRepo(pool), demand: postgres.NewDemandLedger(pool),
		profiles: postgres.NewProfileDirectory(pool), catalogue: postgres.NewSlotCatalogue(pool), uow: postgres.NewUnitOfWork(pool),
	})
	before := countOutbox()
	if before < 2 {
		t.Fatalf("outbox rows after seeding = %d, want the generated and approved events", before)
	}

	// The binary's own boot path: embedded migrations (a no-op here, the
	// database is already migrated) and its own pool.
	deps, closeFn, err := buildDeps(ctx, quietLogger(), url, url, "SITE-1")
	if err != nil {
		t.Fatalf("buildDeps: %v", err)
	}
	t.Cleanup(closeFn)

	ct, st := sdk.NewInMemoryTransports()
	ss, err := inboundmcp.NewServer(deps).Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ss.Close()
	session, err := sdk.NewClient(&sdk.Implementation{Name: "t", Version: "0"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	assertSeededReads(t, session, planID)

	if after := countOutbox(); after != before {
		t.Fatalf("outbox rows %d -> %d: the MCP binary wrote an event", before, after)
	}
}

// assertSeededReads reads back, through every MCP tool, what
// seedThroughWriteUseCases persisted.
func assertSeededReads(t *testing.T, session *sdk.ClientSession, planID string) {
	t.Helper()
	ctx := context.Background()
	call := func(name string, args map[string]any) map[string]any {
		t.Helper()
		res, err := session.CallTool(ctx, &sdk.CallToolParams{Name: name, Arguments: args})
		if err != nil || res.IsError {
			t.Fatalf("%s: err=%v res=%+v", name, err, res)
		}
		return res.StructuredContent.(map[string]any)
	}
	skus := func(m map[string]any, list string) []string {
		var out []string
		for _, item := range m[list].([]any) {
			out = append(out, item.(map[string]any)["sku"].(string))
		}
		return out
	}

	plan := call("get_slot_plan", map[string]any{"plan_id": planID})
	if plan["plan_id"] != planID || plan["state"] != "Approved" || plan["site_id"] != "SITE-1" || plan["policy"] != "abc-velocity-v1" {
		t.Fatalf("get_slot_plan = %v", plan)
	}
	if got := len(plan["assignments"].([]any)); got != 2 {
		t.Fatalf("assignments = %d, want 2: %v", got, plan)
	}

	page := call("list_slot_plans", map[string]any{"state": "Approved"})
	items := page["items"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["plan_id"] != planID || items[0].(map[string]any)["assignment_count"] != 2.0 {
		t.Fatalf("list_slot_plans = %v", page)
	}
	if drafts := call("list_slot_plans", map[string]any{"state": "Draft"}); len(drafts["items"].([]any)) != 0 {
		t.Fatalf("list_slot_plans state=Draft = %v", drafts)
	}

	forward := call("get_forward_slots", map[string]any{})
	if forward["plan_id"] != planID || forward["site_id"] != "SITE-1" || len(forward["assignments"].([]any)) != 2 {
		t.Fatalf("get_forward_slots = %v", forward)
	}

	velocity := call("get_sku_velocity", map[string]any{"window_days": 7})
	if got := skus(velocity, "items"); !reflect.DeepEqual(got, []string{"SKU-1", "SKU-2"}) {
		t.Fatalf("get_sku_velocity = %v", velocity)
	}
	if top := velocity["items"].([]any)[0].(map[string]any); top["picks"] != 2.0 || top["units"] != 15.0 {
		t.Fatalf("SKU-1 velocity = %v", top)
	}
}
