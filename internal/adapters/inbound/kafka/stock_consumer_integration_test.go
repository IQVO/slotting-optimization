//go:build integration

package kafka_test

import (
	"context"
	"log/slog"
	"testing"
	"time"

	kafkaconsumer "github.com/claudioed/slotting-optimization/internal/adapters/inbound/kafka"
	"github.com/claudioed/slotting-optimization/internal/adapters/outbound/postgres"
	"github.com/claudioed/slotting-optimization/internal/application/usecases"
	"github.com/claudioed/slotting-optimization/internal/domain/slotplan"
	"github.com/claudioed/slotting-optimization/internal/testing/kafkatest"
	"github.com/claudioed/slotting-optimization/internal/testing/pgtest"
)

// TestStockConsumer_RealKafkaAndPostgres proves ADR 0010 end to end on a real
// broker and database: a StockRelocated carrying fulfillment-execution's move
// reference updates the plan's execution progress; a redelivery (same
// CloudEvents id) and a replay of the same relocation under a new id count
// once; a relocation of no plan is committed past; a poison StockRelocated
// is the only message on the dead-letter topic.
func TestStockConsumer_RealKafkaAndPostgres(t *testing.T) {
	ctx := context.Background()
	pool := pgtest.NewPool(t)
	plans, ledger := postgres.NewSlotPlanRepo(pool), postgres.NewMoveExecutionLedger(pool)
	s := newExecStores()
	s.storePlan9(t)
	stored, err := s.plans.Get(ctx, "plan-9")
	if err != nil {
		t.Fatal(err)
	}
	if err := plans.Save(ctx, stored, 0); err != nil {
		t.Fatal(err)
	}

	topic := kafkatest.Topic(t, kafkaconsumer.StockTopic)
	kafkatest.CreateTopic(t, topic+kafkaconsumer.DLQSuffix)
	executed := stockMsg("e-1", "rel-1", relocatedData("rel-1", "SKU-R1", fromA01, toA02, "plan-9:0"))
	poison := stockMsg("e-bad", "rel-bad", `{"relocation_id":"rel-bad","site_code":"WH1","sku":"SKU-R1"}`)
	kafkatest.Produce(t, topic,
		kafkatest.Message{Key: "rel-1", Value: executed},
		kafkatest.Message{Key: "rel-1", Value: executed}, // redelivery
		kafkatest.Message{Key: "rel-1", Value: stockMsg("e-2", "rel-1", relocatedData("rel-1", "SKU-R1", fromA01, toA02, "plan-9:0"))},
		kafkatest.Message{Key: "rel-9", Value: stockMsg("e-3", "rel-9", relocatedData("rel-9", "SKU-R1", fromA01, toA02, "plan-404:0"))},
		kafkatest.Message{Key: "rel-bad", Value: poison},
		kafkatest.Message{Key: "rel-2", Value: stockMsg("e-4", "rel-2", relocatedData("rel-2", "SKU-R2", "WH1-FWD-B07", resB07, ""))},
	)

	uc := &usecases.RecordRelocation{
		Intake: usecases.Intake{UoW: postgres.NewUnitOfWork(pool), Processed: postgres.NewProcessedEventRepo(pool)},
		Plans:  plans, Executions: ledger,
	}
	logger := slog.New(slog.NewTextHandler(testWriter{t}, nil))
	runConsumer(t, kafkaconsumer.NewStockConsumerForTopic(kafkatest.Brokers(t), topic, "itest-relocation-"+topic, uc, logger))

	view := &usecases.GetPlan{Plans: plans, Executions: ledger}
	eventually(t, "both moves of plan-9 executed", func() bool {
		v, err := view.View(ctx, "plan-9")
		return err == nil && v.Execution != nil && v.Execution.MovesExecuted == 2
	})
	v, err := view.View(ctx, "plan-9")
	if err != nil {
		t.Fatal(err)
	}
	want := slotplan.MoveProgress{
		Seq: 0, SKU: "SKU-R1", Kind: slotplan.MoveRelocate, Executed: true, ExecutedQuantity: 7,
		LastExecutedAt: time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC),
	}
	if v.Execution.MovesTotal != 2 || v.Execution.Moves[0] != want {
		t.Fatalf("progress = %+v: the redelivery and the replay must count once (want move 0 = %+v)", v.Execution, want)
	}
	if !v.Execution.LastExecutedAt.Equal(want.LastExecutedAt) {
		t.Fatalf("last executed at = %v", v.Execution.LastExecutedAt)
	}
	dead := kafkatest.ReadN(t, topic+kafkaconsumer.DLQSuffix, 1, 30*time.Second)
	if string(dead[0].Value) != string(poison) || dead[0].Headers["x-dlq-source-topic"] != topic {
		t.Fatalf("dead letter = %s %v, want the poison StockRelocated", dead[0].Value, dead[0].Headers)
	}
	var rows int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM slot_plan_move_executions`).Scan(&rows); err != nil || rows != 2 {
		t.Fatalf("execution rows = %d, %v; want 2 (rel-1 once, rel-2)", rows, err)
	}
}

// testWriter sends the consumer's log lines to the test output, so a failed
// run shows what the consumer did.
type testWriter struct{ t *testing.T }

func (w testWriter) Write(p []byte) (int, error) {
	w.t.Log(string(p))
	return len(p), nil
}
