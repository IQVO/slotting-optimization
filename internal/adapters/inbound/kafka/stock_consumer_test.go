package kafka_test

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	kafkaconsumer "github.com/claudioed/slotting-optimization/internal/adapters/inbound/kafka"
	"github.com/claudioed/slotting-optimization/internal/adapters/outbound/memory"
	"github.com/claudioed/slotting-optimization/internal/application/usecases"
	"github.com/claudioed/slotting-optimization/internal/domain/slotplan"
)

// The StockRelocated consumer (ADR 0010): inventory-storage's
// StockRelocated, exactly as its apis/asyncapi.yaml example publishes it,
// becomes an execution of a slot-plan move.

const (
	fromA01 = "WH1-STOR-AMB-A01-01-01-A"
	toA02   = "WH1-STOR-AMB-A02-01-01-A"
	resB07  = "WH1-RES-B07"
)

func stockMsg(id, relocationID, data string) []byte {
	return ce(id, "/warehouse/inventory-storage", kafkaconsumer.TypeStockRelocated, relocationID,
		"urn:warehouse:inventory-storage:events:StockRelocated:v1", data)
}

func relocatedData(relocationID, sku, from, to, sourceRef string) string {
	return `{"relocation_id":"` + relocationID + `","site_code":"WH1","sku":"` + sku + `","from_bin":"` + from + `","to_bin":"` + to +
		`","quantity":7,"reason":"SLOTTING","source_ref":"` + sourceRef + `","occurred_at":"2026-10-13T12:00:00Z"}`
}

// execStores is the read side the StockRelocated consumer writes to.
type execStores struct {
	plans     *memory.SlotPlanRepo
	ledger    *memory.MoveExecutionLedger
	processed *memory.ProcessedEventRepo
	uow       *countingUoW
}

func newExecStores() *execStores {
	plans, ledger, processed := memory.NewSlotPlanRepo(), memory.NewMoveExecutionLedger(), memory.NewProcessedEventRepo()
	return &execStores{
		plans: plans, ledger: ledger, processed: processed,
		uow: &countingUoW{inner: memory.NewUnitOfWork(plans, ledger, processed)},
	}
}

// storePlan9 stores plan-9 of WH1, Approved, with move 0 = Relocate SKU-R1
// from A01 to A02 and move 1 = Vacate SKU-R2 from the B07 slot.
func (s *execStores) storePlan9(t *testing.T) {
	t.Helper()
	w, err := slotplan.NewWindow(time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	p, _, err := slotplan.Generate(slotplan.NewPlan{
		ID: "plan-9", Site: "WH1", Window: w, Policy: slotplan.PolicyABCVelocityV1,
		Assignments: []slotplan.Assignment{{SKU: "SKU-R1", Slot: toA02, Class: slotplan.ClassA, Picks: 5, Units: 5}},
		Moves: []slotplan.Move{
			{SKU: "SKU-R1", From: fromA01, To: toA02, Kind: slotplan.MoveRelocate},
			{SKU: "SKU-R2", From: "WH1-FWD-B07", Kind: slotplan.MoveVacate},
		},
	}, w.To)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Approve("", w.To.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := s.plans.Save(context.Background(), p, 0); err != nil {
		t.Fatal(err)
	}
}

func (s *execStores) useCase() *usecases.RecordRelocation {
	return &usecases.RecordRelocation{Intake: usecases.Intake{UoW: s.uow, Processed: s.processed}, Plans: s.plans, Executions: s.ledger}
}

func (s *execStores) consumer(dlq *fakeDLQWriter) *kafkaconsumer.Consumer {
	return &kafkaconsumer.Consumer{
		Name: "relocation", Topic: kafkaconsumer.StockTopic, Handlers: kafkaconsumer.StockHandlers(s.useCase(), testLogger()),
		Logger: testLogger(), Retry: fastRetry, DLQ: dlq, DeadLetterInvalid: true,
	}
}

func (s *execStores) executions(t *testing.T) []slotplan.MoveExecution {
	t.Helper()
	got, err := s.ledger.ForPlan(context.Background(), "plan-9")
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestStockConsumer_GoldenStockRelocatedBecomesAMoveExecution(t *testing.T) {
	golden, err := os.ReadFile("testdata/inventory_StockRelocated.golden.json")
	if err != nil {
		t.Fatal(err)
	}
	s := newExecStores()
	s.storePlan9(t)
	if err := s.consumer(&fakeDLQWriter{}).HandleMessage(context.Background(), golden); err != nil {
		t.Fatalf("HandleMessage = %v", err)
	}
	got := s.executions(t)
	want := slotplan.MoveExecution{
		Seq: 0, RelocationID: "rel-42", SKU: "SKU-R1", FromBin: fromA01, ToBin: toA02, Quantity: 7,
		At: time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC),
	}
	// source_ref "plan-9#3" is no move reference: matched on the bins.
	if len(got) != 1 || got[0] != want {
		t.Fatalf("executions = %+v, want [%+v]", got, want)
	}
	if !s.processed.Has(usecases.RelocationConsumer, "3c9e1a52-7b4d-4e18-a6f2-5d0b8c7e9f21") {
		t.Fatal("the CloudEvents id must be claimed")
	}
}

func TestStockConsumer_MoveReferenceFromFulfillmentExecution(t *testing.T) {
	s := newExecStores()
	s.storePlan9(t)
	msg := stockMsg("e-1", "rel-1", relocatedData("rel-1", "SKU-R2", "WH1-FWD-B07", resB07, "plan-9:1"))
	if err := s.consumer(&fakeDLQWriter{}).HandleMessage(context.Background(), msg); err != nil {
		t.Fatal(err)
	}
	got := s.executions(t)
	if len(got) != 1 || got[0].Seq != 1 || got[0].ToBin != resB07 {
		t.Fatalf("executions = %+v, want move 1 (the Vacate) executed into the reserve bin", got)
	}
}

func TestStockConsumer_PoisonIsDeadLetteredAtOnce(t *testing.T) {
	poison := map[string]string{
		"missing quantity":   `{"relocation_id":"r","site_code":"WH1","sku":"S","from_bin":"A","to_bin":"B","reason":"SLOTTING","source_ref":"","occurred_at":"2026-10-13T12:00:00Z"}`,
		"zero quantity":      `{"relocation_id":"r","site_code":"WH1","sku":"S","from_bin":"A","to_bin":"B","quantity":0,"reason":"SLOTTING","source_ref":"","occurred_at":"2026-10-13T12:00:00Z"}`,
		"quantity not a int": `{"relocation_id":"r","site_code":"WH1","sku":"S","from_bin":"A","to_bin":"B","quantity":"seven","reason":"SLOTTING","source_ref":"","occurred_at":"2026-10-13T12:00:00Z"}`,
		"missing occurred":   `{"relocation_id":"r","site_code":"WH1","sku":"S","from_bin":"A","to_bin":"B","quantity":3,"reason":"SLOTTING","source_ref":""}`,
		"missing id":         `{"site_code":"WH1","sku":"S","from_bin":"A","to_bin":"B","quantity":3,"reason":"SLOTTING","source_ref":"","occurred_at":"2026-10-13T12:00:00Z"}`,
		"data not an object": `"oops"`,
	}
	for name, data := range poison {
		t.Run(name, func(t *testing.T) {
			s := newExecStores()
			s.storePlan9(t)
			dlq := &fakeDLQWriter{}
			bad := stockMsg("e-bad", "r", data)
			good := stockMsg("e-good", "rel-1", relocatedData("rel-1", "SKU-R1", fromA01, toA02, ""))
			reader := newFakeReader(bad, good)
			if err := runUntilCommits(t, s.consumer(dlq), reader, 2); err != nil && !errors.Is(err, context.Canceled) {
				t.Fatalf("Run = %v", err)
			}
			published := dlq.published()
			if len(published) != 1 || string(published[0].Value) != string(bad) {
				t.Fatalf("dead-lettered %d messages, want exactly the poison one", len(published))
			}
			if got := strings.Join(reader.log(), ","); got != "fetch:0,commit:0,fetch:1,commit:1" {
				t.Fatalf("events = %s: the poison is dead-lettered without retry and committed past", got)
			}
			if s.uow.count() != 1 {
				t.Fatalf("units of work = %d: the poison must never reach the database", s.uow.count())
			}
			if len(s.executions(t)) != 1 {
				t.Fatal("the next message must still be applied")
			}
		})
	}
}

func TestStockConsumer_UnmatchedIsCommittedNotDeadLettered(t *testing.T) {
	s := newExecStores()
	s.storePlan9(t)
	dlq := &fakeDLQWriter{}
	reader := newFakeReader(
		stockMsg("e-1", "rel-1", relocatedData("rel-1", "SKU-R1", fromA01, toA02, "plan-404:0")),
		stockMsg("e-2", "rel-2", relocatedData("rel-2", "SKU-ZZ", "X", "Y", "")),
	)
	if err := runUntilCommits(t, s.consumer(dlq), reader, 2); err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("Run = %v", err)
	}
	if len(dlq.published()) != 0 || len(s.executions(t)) != 0 {
		t.Fatalf("unmatched relocations: dlq=%d executions=%d, want none", len(dlq.published()), len(s.executions(t)))
	}
	if !s.processed.Has(usecases.RelocationConsumer, "e-1") || !s.processed.Has(usecases.RelocationConsumer, "e-2") {
		t.Fatal("unmatched relocations are claimed")
	}
}

func TestStockConsumer_IgnoresOtherTypesAndNonCloudEvents(t *testing.T) {
	s := newExecStores()
	dlq := &fakeDLQWriter{}
	reserved := ce("e-1", "/warehouse/inventory-storage", "com.warehouse.wms.inventory-storage.stock.StockReserved", "res-1",
		"urn:warehouse:inventory-storage:events:StockReserved:v1", `{"reservation_id":"res-1"}`)
	reader := newFakeReader(reserved, []byte(legacyFlat), []byte("not json"))
	if err := runUntilCommits(t, s.consumer(dlq), reader, 3); err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("Run = %v", err)
	}
	if len(dlq.published()) != 0 || s.uow.count() != 0 {
		t.Fatalf("dlq=%d units=%d: other types and non-CloudEvents are skipped, never dead-lettered", len(dlq.published()), s.uow.count())
	}
}

// parkingUoW blocks every unit of work until its ctx is cancelled, like a
// database call caught by a shutdown.
type parkingUoW struct {
	once    sync.Once
	entered chan struct{}
}

func (u *parkingUoW) Do(ctx context.Context, _ func(context.Context) error) error {
	u.once.Do(func() { close(u.entered) })
	<-ctx.Done()
	return ctx.Err()
}

func TestStockConsumer_ShutdownNeverDeadLetters(t *testing.T) {
	s := newExecStores()
	parked := &parkingUoW{entered: make(chan struct{})}
	uc := s.useCase()
	uc.UoW = parked
	dlq := &fakeDLQWriter{}
	c := &kafkaconsumer.Consumer{
		Name: "relocation", Topic: kafkaconsumer.StockTopic, Handlers: kafkaconsumer.StockHandlers(uc, testLogger()),
		Logger: testLogger(), Retry: fastRetry, DLQ: dlq, DeadLetterInvalid: true,
	}
	reader := newFakeReader(stockMsg("e-1", "rel-1", relocatedData("rel-1", "SKU-R1", fromA01, toA02, "")))
	c.Reader = reader
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- c.Run(ctx) }()
	select {
	case <-parked.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("the message never reached the unit of work")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatalf("Run = %v, want a clean stop", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
	if len(dlq.published()) != 0 || reader.commits() != 0 {
		t.Fatalf("dlq=%d commits=%d: a shutdown must neither dead-letter nor commit", len(dlq.published()), reader.commits())
	}
}

func TestNewStockConsumer_ReadsTheInventoryTopicAndDeadLettersPoison(t *testing.T) {
	group := "test-relocation-" + t.Name()
	c := kafkaconsumer.NewStockConsumer([]string{"127.0.0.1:1"}, group, newExecStores().useCase(), testLogger())
	defer func() { _ = c.Close() }()
	if c.Topic != "warehouse.inventory.events" || !c.DeadLetterInvalid || c.DLQ == nil {
		t.Fatalf("consumer = topic %q, dead-letter-invalid %v, dlq %v", c.Topic, c.DeadLetterInvalid, c.DLQ != nil)
	}
	if _, ok := c.Handlers["com.warehouse.wms.inventory-storage.stock.StockRelocated"]; !ok || len(c.Handlers) != 1 {
		t.Fatalf("handlers = %v, want StockRelocated only", c.Handlers)
	}
}
