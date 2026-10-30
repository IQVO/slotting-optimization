//go:build integration

package main

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2aclient"

	inbounda2a "github.com/claudioed/slotting-optimization/internal/adapters/inbound/a2a"
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

// The binary migrates a database itself (idempotently: pgtest's databases are
// already migrated, a second run must be a no-op), then answers both skills
// over the REAL JSON-RPC wire (the real router, the SDK client) with exactly
// what the write use cases persisted, and its reads add no outbox row: this
// binary has no write use case, so a delegated read can never publish.
func TestA2AAgainstPostgres_ReadsWhatTheAPIWroteAndNeverWrites(t *testing.T) {
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

	// The binary's own boot path: embedded migrations (a no-op here) and its
	// own pool, then the real router with the real card.
	deps, closeFn, err := buildDeps(ctx, quietLogger(), url, url, "SITE-1")
	if err != nil {
		t.Fatalf("buildDeps: %v", err)
	}
	t.Cleanup(closeFn)
	srv := httptest.NewServer(newRouter(inbounda2a.Handler(inbounda2a.NewExecutor(deps)),
		inbounda2a.Card(inbounda2a.Config{AgentURL: testAgentURL, Version: "test"})))
	t.Cleanup(srv.Close)
	client, err := a2aclient.NewFromCard(ctx, inbounda2a.Card(inbounda2a.Config{AgentURL: srv.URL + "/a2a", Version: "test"}))
	if err != nil {
		t.Fatal(err)
	}

	assertSeededReads(t, client, planID)

	if after := countOutbox(); after != before {
		t.Fatalf("outbox rows %d -> %d: the A2A binary wrote an event", before, after)
	}
}

// answer sends one skill call and returns the decoded data part (generic: the
// typed contract is pinned by the adapter's own wire tests).
func answer(t *testing.T, client *a2aclient.Client, skill string, args map[string]any) map[string]any {
	t.Helper()
	resp, err := client.SendMessage(context.Background(), &a2a.SendMessageRequest{
		Message: a2a.NewMessage(a2a.MessageRoleUser, a2a.NewDataPart(map[string]any{"skill": skill, "arguments": args})),
	})
	if err != nil {
		t.Fatalf("%s: %v", skill, err)
	}
	msg, ok := resp.(*a2a.Message)
	if !ok || len(msg.Parts) < 1 {
		t.Fatalf("%s: reply = %T", skill, resp)
	}
	data, ok := msg.Parts[0].Content.(a2a.Data)
	if !ok {
		t.Fatalf("%s: first part is %T, want data", skill, msg.Parts[0].Content)
	}
	raw, _ := json.Marshal(data.Value)
	var env map[string]any
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatal(err)
	}
	return env
}

// assertSeededReads reads back, through both skills, what
// seedThroughWriteUseCases persisted.
func assertSeededReads(t *testing.T, client *a2aclient.Client, planID string) {
	t.Helper()
	data := func(env map[string]any, key string) map[string]any {
		t.Helper()
		if env["problem"] != nil {
			t.Fatalf("unexpected problem: %v", env["problem"])
		}
		return env["data"].(map[string]any)[key].(map[string]any)
	}

	forward := data(answer(t, client, "forward_slot_map", map[string]any{}), "forwardSlotMap")
	if forward["planId"] != planID || forward["siteId"] != "SITE-1" || len(forward["assignments"].([]any)) != 2 {
		t.Fatalf("forward_slot_map = %v", forward)
	}

	for name, args := range map[string]map[string]any{
		"by id":              {"planId": planID},
		"newest of the site": {"siteId": "SITE-1", "state": "Approved"},
		"newest of all":      {},
	} {
		plan := data(answer(t, client, "slot_plan_review", args), "slotPlan")
		if plan["planId"] != planID || plan["state"] != "Approved" || plan["siteId"] != "SITE-1" || plan["policy"] != "abc-velocity-v1" {
			t.Fatalf("slot_plan_review %s = %v", name, plan)
		}
		if got := len(plan["assignments"].([]any)); got != 2 {
			t.Fatalf("slot_plan_review %s: assignments = %d, want 2: %v", name, got, plan)
		}
		if exec, ok := plan["execution"].(map[string]any); !ok || exec["movesTotal"] != 2.0 || exec["movesExecuted"] != 0.0 {
			t.Fatalf("slot_plan_review %s: execution = %v, want 0 of 2 moves executed", name, plan["execution"])
		}
	}

	// A plan that does not exist is answered in-band as a 404 problem.
	env := answer(t, client, "slot_plan_review", map[string]any{"planId": "plan-00000000-0000-4000-8000-0000000000ff"})
	if p, _ := env["problem"].(map[string]any); p == nil || p["status"] != 404.0 || !strings.HasSuffix(p["type"].(string), "/plan-not-found") {
		t.Fatalf("unknown plan = %v, want the in-band plan-not-found problem", env)
	}
	if empty := answer(t, client, "slot_plan_review", map[string]any{"siteId": "SITE-9"}); empty["problem"] == nil {
		t.Fatalf("a site without plans must be an in-band 404, got %v", empty)
	}
}
