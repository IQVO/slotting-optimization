package memory

import (
	"context"
	"maps"
	"sync"

	"github.com/claudioed/slotting-optimization/internal/application/ports"
	"github.com/claudioed/slotting-optimization/internal/domain/slotplan"
)

// MoveExecutionLedger is the in-memory execution-progress read model
// (ports.MoveExecutionLedger, ADR 0010), keyed on the relocation id like
// the Postgres table's primary key.
type MoveExecutionLedger struct {
	mu   sync.Mutex
	rows map[string]executionRow
}

type executionRow struct {
	plan slotplan.PlanID
	exec slotplan.MoveExecution
}

var _ ports.MoveExecutionLedger = (*MoveExecutionLedger)(nil)

// NewMoveExecutionLedger constructs an empty MoveExecutionLedger.
func NewMoveExecutionLedger() *MoveExecutionLedger {
	return &MoveExecutionLedger{rows: make(map[string]executionRow)}
}

// Record implements ports.MoveExecutionLedger.
func (l *MoveExecutionLedger) Record(_ context.Context, plan slotplan.PlanID, e slotplan.MoveExecution) (bool, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, dup := l.rows[e.RelocationID]; dup {
		return false, nil
	}
	e.At = e.At.UTC()
	l.rows[e.RelocationID] = executionRow{plan: plan, exec: e}
	return true, nil
}

// ForPlan implements ports.MoveExecutionLedger.
func (l *MoveExecutionLedger) ForPlan(_ context.Context, plan slotplan.PlanID) ([]slotplan.MoveExecution, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []slotplan.MoveExecution
	for _, r := range l.rows {
		if r.plan == plan {
			out = append(out, r.exec)
		}
	}
	return out, nil
}

// Snapshot implements Snapshotter.
func (l *MoveExecutionLedger) Snapshot() func() {
	l.mu.Lock()
	defer l.mu.Unlock()
	saved := maps.Clone(l.rows)
	return func() {
		l.mu.Lock()
		defer l.mu.Unlock()
		l.rows = saved
	}
}
