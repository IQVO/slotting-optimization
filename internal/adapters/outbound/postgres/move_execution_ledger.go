package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/claudioed/slotting-optimization/internal/application/ports"
	"github.com/claudioed/slotting-optimization/internal/domain/slotplan"
)

// MoveExecutionLedger is the pgxpool-backed ports.MoveExecutionLedger
// (slot_plan_move_executions, migration 0002, ADR 0010). Inside a
// UnitOfWork it joins the transaction carried in ctx.
type MoveExecutionLedger struct {
	pool *pgxpool.Pool
}

var _ ports.MoveExecutionLedger = (*MoveExecutionLedger)(nil)

// NewMoveExecutionLedger constructs a MoveExecutionLedger over pool.
func NewMoveExecutionLedger(pool *pgxpool.Pool) *MoveExecutionLedger {
	return &MoveExecutionLedger{pool: pool}
}

// Record implements ports.MoveExecutionLedger: INSERT ... ON CONFLICT
// (relocation_id) DO NOTHING, recorded when a row was inserted.
func (l *MoveExecutionLedger) Record(ctx context.Context, plan slotplan.PlanID, e slotplan.MoveExecution) (bool, error) {
	tag, err := queryFor(ctx, l.pool).Exec(ctx, `
		INSERT INTO slot_plan_move_executions (relocation_id, plan_id, move_seq, sku, from_bin, to_bin, quantity, executed_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT (relocation_id) DO NOTHING
	`, e.RelocationID, string(plan), e.Seq, string(e.SKU), string(e.FromBin), string(e.ToBin), e.Quantity, e.At.UTC())
	if err != nil {
		return false, fmt.Errorf("insert move execution: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

// ForPlan implements ports.MoveExecutionLedger.
func (l *MoveExecutionLedger) ForPlan(ctx context.Context, plan slotplan.PlanID) ([]slotplan.MoveExecution, error) {
	rows, err := queryFor(ctx, l.pool).Query(ctx, `
		SELECT relocation_id, move_seq, sku, from_bin, to_bin, quantity, executed_at
		FROM slot_plan_move_executions WHERE plan_id = $1
	`, string(plan))
	if err != nil {
		return nil, fmt.Errorf("read move executions: %w", err)
	}
	defer rows.Close()
	var out []slotplan.MoveExecution
	for rows.Next() {
		var (
			e             slotplan.MoveExecution
			sku, from, to string
			executedAt    time.Time
		)
		if err := rows.Scan(&e.RelocationID, &e.Seq, &sku, &from, &to, &e.Quantity, &executedAt); err != nil {
			return nil, fmt.Errorf("scan move execution: %w", err)
		}
		e.SKU, e.FromBin, e.ToBin, e.At = slotplan.SKU(sku), slotplan.SlotCode(from), slotplan.SlotCode(to), executedAt.UTC()
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read move executions: %w", err)
	}
	return out, nil
}
