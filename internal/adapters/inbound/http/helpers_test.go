package http_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	inboundhttp "github.com/claudioed/slotting-optimization/internal/adapters/inbound/http"
	"github.com/claudioed/slotting-optimization/internal/adapters/outbound/ids"
	"github.com/claudioed/slotting-optimization/internal/adapters/outbound/kafka"
	"github.com/claudioed/slotting-optimization/internal/adapters/outbound/memory"
	"github.com/claudioed/slotting-optimization/internal/application/repository"
	"github.com/claudioed/slotting-optimization/internal/application/usecases"
	"github.com/claudioed/slotting-optimization/internal/domain/planning"
	"github.com/claudioed/slotting-optimization/internal/domain/slotplan"
)

var now = time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)

type fixedClock struct{}

func (fixedClock) Now() time.Time { return now }

// faultyRepo wraps the memory repository and injects errors.
type faultyRepo struct {
	*memory.SlotPlanRepo
	getErr, saveErr, currentErr, listErr error
}

func (r *faultyRepo) Get(ctx context.Context, id slotplan.PlanID) (*slotplan.SlotPlan, error) {
	if r.getErr != nil {
		return nil, r.getErr
	}
	return r.SlotPlanRepo.Get(ctx, id)
}

func (r *faultyRepo) Save(ctx context.Context, p *slotplan.SlotPlan, loaded int64) error {
	if r.saveErr != nil {
		return r.saveErr
	}
	return r.SlotPlanRepo.Save(ctx, p, loaded)
}

func (r *faultyRepo) Current(ctx context.Context, site slotplan.SiteID) (*slotplan.SlotPlan, error) {
	if r.currentErr != nil {
		return nil, r.currentErr
	}
	return r.SlotPlanRepo.Current(ctx, site)
}

func (r *faultyRepo) List(ctx context.Context, f repository.PlanFilter, after slotplan.PlanID, limit int) ([]*slotplan.SlotPlan, error) {
	if r.listErr != nil {
		return nil, r.listErr
	}
	return r.SlotPlanRepo.List(ctx, f, after, limit)
}

type fixture struct {
	repo      *faultyRepo
	demand    *memory.DemandLedger
	profiles  *memory.ProfileDirectory
	catalogue *memory.SlotCatalogue
	outbox    *memory.OutboxRepo
	server    *httptest.Server
	readiness *inboundhttp.Readiness
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	plans, ob := memory.NewSlotPlanRepo(), memory.NewOutboxRepo()
	demand, profiles, cat := memory.NewDemandLedger(), memory.NewProfileDirectory(), memory.NewSlotCatalogue()
	repo := &faultyRepo{SlotPlanRepo: plans}
	planner, err := planning.NewPlanner(planning.LexicalRanking{})
	if err != nil {
		t.Fatal(err)
	}
	w := usecases.Writer{Plans: repo, Outbox: ob, Encoder: kafka.NewEncoder(), UoW: memory.NewUnitOfWork(plans, ob, demand, profiles, cat), Clock: fixedClock{}}
	readiness := &inboundhttp.Readiness{}
	s := &inboundhttp.Server{
		GeneratePlan: &usecases.GeneratePlan{
			Writer: w, Demand: demand, Profiles: profiles, Catalogue: cat, IDs: ids.UUID{}, Planner: planner,
			DefaultSite: "SITE-1", ForwardZoneCodes: []string{"FWD"},
		},
		ApprovePlan:      &usecases.ApprovePlan{Writer: w},
		RejectPlan:       &usecases.RejectPlan{Writer: w},
		GetPlan:          &usecases.GetPlan{Plans: repo},
		ListPlans:        &usecases.ListPlans{Plans: repo},
		ListForwardSlots: &usecases.ListForwardSlots{Plans: repo, DefaultSite: "SITE-1"},
		ListSkuVelocity:  &usecases.ListSkuVelocity{Demand: demand, Clock: fixedClock{}, DefaultSite: "SITE-1"},
		Readiness:        readiness,
		Metrics:          http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "# metrics\n") }),
	}
	srv := httptest.NewServer(inboundhttp.NewRouter(s))
	t.Cleanup(srv.Close)
	return &fixture{repo: repo, demand: demand, profiles: profiles, catalogue: cat, outbox: ob, server: srv, readiness: readiness}
}

// seed gives SITE-1 two SKUs with demand, dimensions and two forward slots.
func (f *fixture) seed(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	due := now.Add(-time.Hour)
	for i, l := range []repository.DemandLine{
		{SourceOrderID: "o1", LineNo: 1, Site: "SITE-1", SKU: "SKU-1", Units: 10, DueAt: due, Active: true},
		{SourceOrderID: "o1", LineNo: 2, Site: "SITE-1", SKU: "SKU-1", Units: 5, DueAt: due, Active: true},
		{SourceOrderID: "o2", LineNo: 1, Site: "SITE-1", SKU: "SKU-2", Units: 4, DueAt: due, Active: true},
		{SourceOrderID: "o3", LineNo: 1, Site: "SITE-1", SKU: "SKU-3", Units: 1, DueAt: due, Active: true},
	} {
		must(t, i, f.demand.Apply(ctx, l))
	}
	for _, sku := range []slotplan.SKU{"SKU-1", "SKU-2"} {
		must(t, 0, f.profiles.Save(ctx, repository.ProductProfile{SKU: sku, Unit: &planning.UnitSize{VolumeMM3: 1_000_000, WeightG: 1000}, Version: 1}))
	}
	must(t, 0, f.catalogue.SaveZone(ctx, repository.Zone{ID: "Z1", SiteCode: "SITE-1", ZoneCode: "FWD", TemperatureClass: planning.Ambient}))
	for _, code := range []slotplan.SlotCode{"A-01", "A-02"} {
		must(t, 0, f.catalogue.SaveSlot(ctx, repository.Slot{Code: code, ZoneID: "Z1", Role: repository.RoleStorage, MaxWeightKg: 40, MaxVolumeM3: 0.5}))
	}
}

func must(t *testing.T, _ int, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

type response struct {
	status      int
	contentType string
	location    string
	body        map[string]any
	raw         string
}

func (f *fixture) do(t *testing.T, method, path, body string) response {
	t.Helper()
	var reader io.Reader = http.NoBody
	if body != "" {
		reader = strings.NewReader(body)
	}
	req, err := http.NewRequestWithContext(context.Background(), method, f.server.URL+path, reader)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	out := response{status: resp.StatusCode, contentType: resp.Header.Get("Content-Type"), location: resp.Header.Get("Location"), raw: string(raw)}
	_ = json.Unmarshal(raw, &out.body)
	return out
}

// mustDo performs a setup request and fails unless it returned want.
func (f *fixture) mustDo(t *testing.T, method, path, body string, want int) response {
	t.Helper()
	r := f.do(t, method, path, body)
	if r.status != want {
		t.Fatalf("%s %s = %d %s, want %d", method, path, r.status, r.raw, want)
	}
	return r
}

// generate creates a plan from the seeded data and returns its id.
func (f *fixture) generate(t *testing.T) string {
	t.Helper()
	r := f.mustDo(t, http.MethodPost, "/slot-plans", `{}`, http.StatusCreated)
	return r.body["planId"].(string)
}

// expectProblem asserts an RFC 7807 response with the given status and slug.
func expectProblem(t *testing.T, r response, status int, slug string) {
	t.Helper()
	if r.status != status || r.contentType != "application/problem+json" ||
		r.body["type"] != "https://errors.slotting-optimization.warehouse-systems.dev/"+slug ||
		r.body["status"] != float64(status) || r.body["title"] == "" || r.body["detail"] == "" {
		t.Fatalf("got %d %s %s, want %d problem %s", r.status, r.contentType, r.raw, status, slug)
	}
}

var errDB = errors.New("db down")
