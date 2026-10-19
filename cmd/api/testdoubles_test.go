package main

import (
	"testing"

	"github.com/claudioed/slotting-optimization/internal/adapters/outbound/memory"
)

// testDoubleAdapters wires the in-memory TEST DOUBLES into the adapters set so
// the composition functions (buildServer, startConsumers) can be unit-tested
// without containers. The binary itself never does this (the sensor
// TestRuntimeBinaryHasNoInMemoryAdapters forbids the import outside tests);
// the real wiring is proven by TestRunOnRealPostgresAndKafka in
// e2e_integration_test.go.
func testDoubleAdapters(t *testing.T) adapters {
	t.Helper()
	plans, ob, processed := memory.NewSlotPlanRepo(), memory.NewOutboxRepo(), memory.NewProcessedEventRepo()
	demand, profiles, catalogue := memory.NewDemandLedger(), memory.NewProfileDirectory(), memory.NewSlotCatalogue()
	executions := memory.NewMoveExecutionLedger()
	return adapters{
		plans: plans, demand: demand, profiles: profiles, catalogue: catalogue,
		outbox: ob, outboxStore: ob, processed: processed, executions: executions,
		uow: memory.NewUnitOfWork(plans, ob, demand, profiles, catalogue, processed, executions),
	}
}
