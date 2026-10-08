//go:build integration

package http_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	inboundhttp "github.com/claudioed/slotting-optimization/internal/adapters/inbound/http"
	"github.com/claudioed/slotting-optimization/internal/adapters/outbound/ids"
	"github.com/claudioed/slotting-optimization/internal/adapters/outbound/kafka"
	"github.com/claudioed/slotting-optimization/internal/adapters/outbound/postgres"
	"github.com/claudioed/slotting-optimization/internal/adapters/outbound/postgres/pgtx"
	"github.com/claudioed/slotting-optimization/internal/application/repository"
	"github.com/claudioed/slotting-optimization/internal/application/usecases"
	"github.com/claudioed/slotting-optimization/internal/domain/planning"
	"github.com/claudioed/slotting-optimization/internal/domain/slotplan"
	"github.com/claudioed/slotting-optimization/internal/testing/pgtest"
)

// pgFixture is the real service over a real Postgres: Postgres adapters, the
// fleet Idempotency-Key middleware on POST /slot-plans, a fixed clock.
type pgFixture struct {
	server *httptest.Server
	query  func(sql string, args ...any) int
}

func newPgFixture(t *testing.T) *pgFixture {
	t.Helper()
	pool := pgtest.NewPool(t)
	plans, ob := postgres.NewSlotPlanRepo(pool), postgres.NewOutboxRepo(pool)
	demand, profiles, cat := postgres.NewDemandLedger(pool), postgres.NewProfileDirectory(pool), postgres.NewSlotCatalogue(pool)
	planner, err := planning.NewPlanner(planning.LexicalRanking{})
	if err != nil {
		t.Fatal(err)
	}
	w := usecases.Writer{Plans: plans, Outbox: ob, Encoder: kafka.NewEncoder(), UoW: postgres.NewUnitOfWork(pool), Clock: fixedClock{}}
	srv := httptest.NewServer(inboundhttp.NewRouter(&inboundhttp.Server{
		GeneratePlan: &usecases.GeneratePlan{
			Writer: w, Demand: demand, Profiles: profiles, Catalogue: cat, IDs: ids.UUID{}, Planner: planner,
			DefaultSite: "SITE-1", ForwardZoneCodes: []string{"FWD"},
		},
		ApprovePlan: &usecases.ApprovePlan{Writer: w},
		RejectPlan:  &usecases.RejectPlan{Writer: w},
		GetPlan:     &usecases.GetPlan{Plans: plans},
		ListPlans:   &usecases.ListPlans{Plans: plans},
		Idempotency: inboundhttp.RequireIdempotencyKey(pool, pgtx.With),
	}))
	t.Cleanup(srv.Close)

	ctx := context.Background()
	due := now.Add(-time.Hour)
	for _, l := range []repository.DemandLine{
		{SourceOrderID: "o1", LineNo: 1, Site: "SITE-1", SKU: "SKU-1", Units: 10, DueAt: due, Active: true},
		{SourceOrderID: "o2", LineNo: 1, Site: "SITE-1", SKU: "SKU-2", Units: 4, DueAt: due, Active: true},
	} {
		if err := demand.Apply(ctx, l); err != nil {
			t.Fatal(err)
		}
	}
	for _, sku := range []string{"SKU-1", "SKU-2"} {
		if err := profiles.Save(ctx, repository.ProductProfile{SKU: slotplan.SKU(sku), Unit: &planning.UnitSize{VolumeMM3: 1000, WeightG: 100}, Version: 1}); err != nil {
			t.Fatal(err)
		}
	}
	if err := cat.SaveZone(ctx, repository.Zone{ID: "Z1", SiteCode: "SITE-1", ZoneCode: "FWD", TemperatureClass: planning.Ambient}); err != nil {
		t.Fatal(err)
	}
	for _, code := range []string{"A-01", "A-02"} {
		if err := cat.SaveSlot(ctx, repository.Slot{Code: slotplan.SlotCode(code), ZoneID: "Z1", Role: repository.RoleStorage, MaxWeightKg: 40, MaxVolumeM3: 0.5}); err != nil {
			t.Fatal(err)
		}
	}
	return &pgFixture{server: srv, query: func(sql string, args ...any) int {
		var n int
		if err := pool.QueryRow(ctx, sql, args...).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}}
}

func (f *pgFixture) post(t *testing.T, path, key, body string) response {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, f.server.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
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
