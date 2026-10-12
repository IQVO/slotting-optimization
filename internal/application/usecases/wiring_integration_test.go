//go:build integration

// Integration tests for the write use cases wired exactly like cmd/api's
// composition root: the REAL Postgres repositories, the REAL UnitOfWork, the
// REAL CloudEvents encoder and the real read-model adapters from one
// testcontainers Postgres (internal/testing/pgtest: one container per test
// binary, one private migrated database per test). The only substitution is
// the clock (fixed, for deterministic timestamps) — no in-memory repo fakes
// anywhere in the path. These prove the cross-component contracts the unit
// tests only fake: the aggregate lifecycle (generate -> approve -> supersede
// -> reject) persists through the real schema, the transactional outbox rows
// land in the SAME unit of work as the plan, and the read models serve what
// the writes persisted. Never an external DATABASE_URL, never t.Skip.
package usecases_test

import (
	"context"
	"testing"
	"time"

	ce "github.com/cloudevents/sdk-go/v2/event"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/claudioed/slotting-optimization/internal/adapters/kafka/cloudevents"
	"github.com/claudioed/slotting-optimization/internal/adapters/outbound/kafka"
	"github.com/claudioed/slotting-optimization/internal/adapters/outbound/postgres"
	"github.com/claudioed/slotting-optimization/internal/application/repository"
	"github.com/claudioed/slotting-optimization/internal/application/usecases"
	"github.com/claudioed/slotting-optimization/internal/domain/planning"
	"github.com/claudioed/slotting-optimization/internal/domain/slotplan"
	"github.com/claudioed/slotting-optimization/internal/testing/pgtest"
	"github.com/claudioed/slotting-optimization/internal/testing/repocontract"
)

// cloudeventsDecode parses a persisted outbox value as a CloudEvents 1.0
// envelope (the same validation consumers apply on the wire).
func cloudeventsDecode(value []byte) (ce.Event, error) {
	return cloudevents.Decode(value)
}

// cloudeventsDataAs binds an envelope's data into out.
func cloudeventsDataAs(t *testing.T, value []byte, out any) error {
	t.Helper()
	e, err := cloudeventsDecode(value)
	if err != nil {
		return err
	}
	return e.DataAs(out)
}

// wiredClock is the fixed instant every write is stamped with (the unit-test
// fakes' fixedClock, reused: deterministic timestamps keep the published
// events comparable). repocontract.T0 keeps the window fixtures in range.
var wiredClock = fixedClock{repocontract.T0}

// wiredStack is the real adapter stack over one private migrated database,
// wired exactly like cmd/api's buildServer for the write side.
type wiredStack struct {
	generate *usecases.GeneratePlan
	approve  *usecases.ApprovePlan
	reject   *usecases.RejectPlan
	get      *usecases.GetPlan
	list     *usecases.ListPlans
	forward  *usecases.ListForwardSlots
	pool     *pgxpool.Pool
}

// newWiredStack seeds the three local copies (demand, product profiles,
// layout) through the REAL Postgres read-model adapters, then wires the real
// use cases over them: two fast-moving SKUs with physical profiles and two
// forward pick slots, so the planner has real work to place.
func newWiredStack(t *testing.T) *wiredStack {
	t.Helper()
	ctx := context.Background()
	pool := pgtest.NewPool(t)

	demand := postgres.NewDemandLedger(pool)
	profiles := postgres.NewProfileDirectory(pool)
	catalogue := postgres.NewSlotCatalogue(pool)

	const site = "SITE-ITCOV"
	seedLayout(t, ctx, catalogue, site)
	seedDemand(t, ctx, demand, site)
	seedProfiles(t, ctx, profiles)

	planner, err := planning.NewPlanner(planning.LexicalRanking{})
	if err != nil {
		t.Fatalf("planner: %v", err)
	}
	ids := &sequenceIDs{}
	w := usecases.Writer{
		Plans: postgres.NewSlotPlanRepo(pool), Outbox: postgres.NewOutboxRepo(pool),
		Encoder: kafka.NewEncoder(), UoW: postgres.NewUnitOfWork(pool), Clock: wiredClock,
	}
	return &wiredStack{
		generate: &usecases.GeneratePlan{
			Writer: w, Demand: demand, Profiles: profiles, Catalogue: catalogue,
			IDs: ids, Planner: planner, ForwardZoneCodes: []string{"FWD"},
		},
		approve: &usecases.ApprovePlan{Writer: w},
		reject:  &usecases.RejectPlan{Writer: w},
		get:     &usecases.GetPlan{Plans: w.Plans},
		list:    &usecases.ListPlans{Plans: w.Plans},
		forward: &usecases.ListForwardSlots{Plans: w.Plans},
		pool:    pool,
	}
}

// seedLayout registers one forward zone and its two pick slots.
func seedLayout(t *testing.T, ctx context.Context, c *postgres.SlotCatalogue, site string) {
	t.Helper()
	if err := c.SaveZone(ctx, repository.Zone{
		ID: "zone-itcov", SiteCode: site, ZoneCode: "FWD", TemperatureClass: planning.Ambient,
	}); err != nil {
		t.Fatalf("seed zone: %v", err)
	}
	for _, code := range []string{"FWD-01", "FWD-02"} {
		if err := c.SaveSlot(ctx, repository.Slot{
			Code: slotplan.SlotCode(code), ZoneID: "zone-itcov", Role: repository.RoleStorage,
			MaxWeightKg: 2, MaxVolumeM3: 0.01,
		}); err != nil {
			t.Fatalf("seed slot %s: %v", code, err)
		}
	}
}

// seedDemand applies ACTIVE order lines through the real ledger: SKU-FAST
// has two picks, SKU-SLOW one, both due inside the default 28-day window of
// the fixed clock.
func seedDemand(t *testing.T, ctx context.Context, d *postgres.DemandLedger, site string) {
	t.Helper()
	lines := []repository.DemandLine{
		{SourceOrderID: "ORD-ITCOV-1", LineNo: 1, Site: slotplan.SiteID(site), SKU: "SKU-FAST", Units: 4, DueAt: repocontract.T0.Add(-24 * time.Hour), Active: true},
		{SourceOrderID: "ORD-ITCOV-2", LineNo: 1, Site: slotplan.SiteID(site), SKU: "SKU-FAST", Units: 4, DueAt: repocontract.T0.Add(-25 * time.Hour), Active: true},
		{SourceOrderID: "ORD-ITCOV-3", LineNo: 1, Site: slotplan.SiteID(site), SKU: "SKU-SLOW", Units: 2, DueAt: repocontract.T0.Add(-48 * time.Hour), Active: true},
	}
	for _, l := range lines {
		if err := d.Apply(ctx, l); err != nil {
			t.Fatalf("seed demand %s/%d: %v", l.SourceOrderID, l.LineNo, err)
		}
	}
}

// seedProfiles gives both SKUs an effective physical profile that fits the
// seeded slots.
func seedProfiles(t *testing.T, ctx context.Context, p *postgres.ProfileDirectory) {
	t.Helper()
	for sku, unit := range map[slotplan.SKU]planning.UnitSize{
		"SKU-FAST": {VolumeMM3: 1000, WeightG: 200},
		"SKU-SLOW": {VolumeMM3: 500, WeightG: 100},
	} {
		if err := p.Save(ctx, repository.ProductProfile{SKU: sku, Unit: &unit, Version: 1}); err != nil {
			t.Fatalf("seed profile %s: %v", sku, err)
		}
	}
}

// outboxRow is one persisted outbox row in the shape the tests assert on
// (what the relay would drain to the broker, in publication order).
type outboxRow struct {
	EventID   string
	EventType string
	Subject   string
	Key       []byte
	Value     []byte
}

// outboxRows reads the persisted outbox rows in publication (id) order.
func (s *wiredStack) outboxRows(t *testing.T) []outboxRow {
	t.Helper()
	rows, err := s.pool.Query(context.Background(), `SELECT event_id, event_type, subject, key, value FROM outbox_events ORDER BY id`)
	if err != nil {
		t.Fatalf("read outbox: %v", err)
	}
	defer rows.Close()
	var out []outboxRow
	for rows.Next() {
		var r outboxRow
		if err := rows.Scan(&r.EventID, &r.EventType, &r.Subject, &r.Key, &r.Value); err != nil {
			t.Fatalf("scan outbox row: %v", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read outbox rows: %v", err)
	}
	return out
}

// TestUsecases_WiringPlanLifecycleEndToEnd drives the aggregate lifecycle
// through the real stack: generate (create), approve (state change), a second
// approval superseding the first, and a rejection — asserting the persisted
// state after each step, the stickiness of a regeneration against the
// Approved plan, and the read models serving the final picture.
func TestUsecases_WiringPlanLifecycleEndToEnd(t *testing.T) {
	s := newWiredStack(t)
	ctx := context.Background()
	const site = "SITE-ITCOV"

	// Generate: the planner places both SKUs (real local copies, real
	// planner) and the Draft plan is persisted at version 1.
	first, err := s.generate.Handle(ctx, usecases.GenerateInput{SiteID: site})
	if err != nil {
		t.Fatalf("generate first: %v", err)
	}
	if first.ID() != "plan-1" || first.State() != slotplan.StateDraft || first.Version() != 1 {
		t.Fatalf("first plan = %s %s v%d, want plan-1 Draft v1", first.ID(), first.State(), first.Version())
	}
	snap := first.Snapshot()
	if len(snap.Assignments) != 2 || len(snap.Unassigned) != 0 {
		t.Fatalf("assignments = %+v, unassigned = %+v; want both SKUs placed", snap.Assignments, snap.Unassigned)
	}
	got := map[slotplan.SKU]slotplan.SlotCode{}
	for _, a := range snap.Assignments {
		got[a.SKU] = a.Slot
	}
	if got["SKU-FAST"] != "FWD-01" || got["SKU-SLOW"] != "FWD-02" {
		t.Fatalf("the fastest SKU must take the first slot: %+v", got)
	}
	if len(snap.Moves) != 2 { // no previous plan: both moves are Assign
		t.Fatalf("moves = %+v, want two Assign moves", snap.Moves)
	}

	// Approve: Draft -> Approved, and the forward-slot read model serves the
	// approved map (PlanID + ApprovedAt + assignments).
	approved, err := s.approve.Handle(ctx, string(first.ID()))
	if err != nil {
		t.Fatalf("approve first: %v", err)
	}
	if approved.State() != slotplan.StateApproved || approved.Version() != 2 {
		t.Fatalf("approved plan = %s v%d", approved.State(), approved.Version())
	}
	fwd, err := s.forward.Handle(ctx, site)
	if err != nil {
		t.Fatalf("list forward slots: %v", err)
	}
	if fwd.PlanID != first.ID() || len(fwd.Assignments) != 2 || fwd.ApprovedAt.IsZero() {
		t.Fatalf("forward map = %+v", fwd)
	}

	// Regenerate: the Approved plan is the stickiness baseline, so the same
	// SKUs keep their slots and the new proposal implies NO moves.
	second, err := s.generate.Handle(ctx, usecases.GenerateInput{SiteID: site})
	if err != nil {
		t.Fatalf("generate second: %v", err)
	}
	if s2 := second.Snapshot(); len(s2.Moves) != 0 {
		t.Fatalf("a regeneration against an identical approved map must imply no moves: %+v", s2.Moves)
	}

	// Approve the second plan: the first is superseded in the SAME unit of
	// work and the site's current plan flips.
	approved2, err := s.approve.Handle(ctx, string(second.ID()))
	if err != nil {
		t.Fatalf("approve second: %v", err)
	}
	if s2 := approved2.Snapshot(); s2.SupersedesPlanID != first.ID() {
		t.Fatalf("second approval must supersede the first: %+v", s2)
	}
	old, err := s.get.Handle(ctx, string(first.ID()))
	if err != nil {
		t.Fatalf("get first plan: %v", err)
	}
	if old.State() != slotplan.StateSuperseded || old.Version() != 3 {
		t.Fatalf("superseded plan = %s v%d", old.State(), old.Version())
	}
	fwd2, err := s.forward.Handle(ctx, site)
	if err != nil {
		t.Fatalf("list forward slots after supersede: %v", err)
	}
	if fwd2.PlanID != second.ID() {
		t.Fatalf("current approved plan = %s, want %s", fwd2.PlanID, second.ID())
	}

	// Reject a third proposal: the decision and its reason persist.
	third, err := s.generate.Handle(ctx, usecases.GenerateInput{SiteID: site})
	if err != nil {
		t.Fatalf("generate third: %v", err)
	}
	rejected, err := s.reject.Handle(ctx, string(third.ID()), "capacity works better")
	if err != nil {
		t.Fatalf("reject third: %v", err)
	}
	if rejected.State() != slotplan.StateRejected {
		t.Fatalf("rejected plan = %s", rejected.State())
	}
	if r, err := s.get.Handle(ctx, string(third.ID())); err != nil || r.Snapshot().RejectReason != "capacity works better" {
		t.Fatalf("reject reason did not persist: %v %+v", err, r)
	}

	// The listing read model agrees: newest first, one per lifecycle step.
	page, err := s.list.Handle(ctx, usecases.ListPlansQuery{SiteID: site, Limit: 10})
	if err != nil {
		t.Fatalf("list plans: %v", err)
	}
	want := []slotplan.PlanID{third.ID(), second.ID(), first.ID()}
	if len(page.Items) != len(want) {
		t.Fatalf("listed %d plans, want %d", len(page.Items), len(want))
	}
	for i, p := range page.Items {
		if p.ID() != want[i] {
			t.Fatalf("list order = plan %s at %d, want %s", p.ID(), i, want[i])
		}
	}

	// Every write enqueued its event in the same unit of work: three
	// generations, two approvals and one rejection.
	rows := s.outboxRows(t)
	wantTypes := []string{
		"SlotPlanGenerated", "SlotPlanApproved",
		"SlotPlanGenerated", "SlotPlanApproved",
		"SlotPlanGenerated", "SlotPlanRejected",
	}
	if len(rows) != len(wantTypes) {
		t.Fatalf("%d outbox rows, want %d", len(rows), len(wantTypes))
	}
	for i, want := range wantTypes {
		if rows[i].EventType != "com.warehouse.wms.slotting-optimization.slotplan."+want {
			t.Fatalf("outbox row %d = %s, want a %s", i, rows[i].EventType, want)
		}
	}
}

// TestUsecases_WiringPublishedEventsCarryTheCloudEventsContract decodes every
// outbox row the lifecycle wrote and asserts the fleet-mandatory CloudEvents
// 1.0 envelope (specversion, id, source, type, subject, dataschema) and the
// pinned payloads — the exact bytes the relay drains to the broker.
func TestUsecases_WiringPublishedEventsCarryTheCloudEventsContract(t *testing.T) {
	s := newWiredStack(t)
	ctx := context.Background()

	first, err := s.generate.Handle(ctx, usecases.GenerateInput{SiteID: "SITE-ITCOV"})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if _, err := s.approve.Handle(ctx, string(first.ID())); err != nil {
		t.Fatalf("approve: %v", err)
	}
	second, err := s.generate.Handle(ctx, usecases.GenerateInput{SiteID: "SITE-ITCOV"})
	if err != nil {
		t.Fatalf("generate second: %v", err)
	}
	if _, err := s.reject.Handle(ctx, string(second.ID()), "not now"); err != nil {
		t.Fatalf("reject: %v", err)
	}

	for _, row := range s.outboxRows(t) {
		e, err := cloudeventsDecode(row.Value)
		if err != nil {
			t.Fatalf("outbox row %s: %v", row.EventType, err)
		}
		if e.SpecVersion() != "1.0" {
			t.Fatalf("specversion = %q", e.SpecVersion())
		}
		if e.ID() == "" || e.Source() != "/warehouse/slotting-optimization" {
			t.Fatalf("envelope identity = id %q source %q", e.ID(), e.Source())
		}
		if e.Type() != row.EventType || e.Subject() != row.Subject {
			t.Fatalf("envelope = type %q subject %q, row says %q %q", e.Type(), e.Subject(), row.EventType, row.Subject)
		}
		if e.DataSchema() == "" || e.DataContentType() != "application/json" {
			t.Fatalf("dataschema = %q datacontenttype = %q", e.DataSchema(), e.DataContentType())
		}
		if string(row.Key) != row.Subject {
			t.Fatalf("kafka key %q must be the plan id %q (partition ordering)", row.Key, row.Subject)
		}
	}

	// The rejected payload carries the reason; the generated payload the
	// shape counts. Both are pinned in apis/asyncapi.yaml.
	rows := s.outboxRows(t)
	var rejectedPayload struct {
		PlanID string `json:"plan_id"`
		Reason string `json:"reason"`
	}
	if err := cloudeventsDataAs(t, rows[len(rows)-1].Value, &rejectedPayload); err != nil {
		t.Fatal(err)
	}
	if rejectedPayload.PlanID != string(second.ID()) || rejectedPayload.Reason != "not now" {
		t.Fatalf("rejected payload = %+v", rejectedPayload)
	}
	var generatedPayload struct {
		PlanID          string `json:"plan_id"`
		SiteID          string `json:"site_id"`
		Policy          string `json:"policy"`
		AssignmentCount int    `json:"assignment_count"`
		MoveCount       int    `json:"move_count"`
		UnassignedCount int    `json:"unassigned_count"`
	}
	if err := cloudeventsDataAs(t, rows[0].Value, &generatedPayload); err != nil {
		t.Fatal(err)
	}
	if generatedPayload.PlanID != string(first.ID()) || generatedPayload.SiteID != "SITE-ITCOV" ||
		generatedPayload.Policy != string(slotplan.PolicyABCVelocityV1) ||
		generatedPayload.AssignmentCount != 2 || generatedPayload.MoveCount != 2 || generatedPayload.UnassignedCount != 0 {
		t.Fatalf("generated payload = %+v", generatedPayload)
	}
}
