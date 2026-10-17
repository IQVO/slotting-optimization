package analyticsstore

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/claudioed/slotting-optimization/internal/analytics/report"
)

// Projection is the Postgres WRITER (report.Projection). Apply claims the
// event id and folds the event into the fact table in ONE transaction: the
// id is recorded if and only if its effect is, so a failure anywhere leaves
// nothing behind and the redelivered event is applied afresh, while a replay
// of an applied id is a no-op.
type Projection struct {
	pool *pgxpool.Pool
}

// NewProjection constructs a Projection over the writer pool.
func NewProjection(pool *pgxpool.Pool) *Projection { return &Projection{pool: pool} }

var _ report.Projection = (*Projection)(nil)

const claimSQL = `
	INSERT INTO analytics_processed_events (event_id, event_type, occurred_at)
	VALUES ($1, $2, $3)
	ON CONFLICT (event_id) DO NOTHING`

// Every fact upsert keeps the EARLIEST candidate of each milestone: LEAST
// ignores NULLs, so a NULL parameter (the event does not mark that milestone)
// keeps the stored value, and the row converges whatever order the events
// arrive in.
const upsertPlanSQL = `
	INSERT INTO slot_plan_facts (plan_id, site_id, generated_at, approved_at, rejected_at, superseded_at, supersedes_plan_id)
	VALUES ($1, $2, $3::timestamptz, $4::timestamptz, $5::timestamptz, $6::timestamptz, $7)
	ON CONFLICT (plan_id) DO UPDATE SET
		generated_at       = LEAST(slot_plan_facts.generated_at, EXCLUDED.generated_at),
		approved_at        = LEAST(slot_plan_facts.approved_at, EXCLUDED.approved_at),
		rejected_at        = LEAST(slot_plan_facts.rejected_at, EXCLUDED.rejected_at),
		superseded_at      = LEAST(slot_plan_facts.superseded_at, EXCLUDED.superseded_at),
		supersedes_plan_id = COALESCE(slot_plan_facts.supersedes_plan_id, EXCLUDED.supersedes_plan_id)`

// Apply implements report.Projection.
func (p *Projection) Apply(ctx context.Context, e report.Event) (bool, error) {
	if err := e.Validate(); err != nil {
		// A pure check of the decoded event: deterministic, the same bytes
		// always fail (the consumer dead-letters it).
		return false, fmt.Errorf("%w: %w", report.ErrRejected, err)
	}
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return false, fmt.Errorf("analyticsstore: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }() // no-op once committed

	tag, err := tx.Exec(ctx, claimSQL, e.EventID, string(e.Kind), e.At.UTC())
	if err != nil {
		return false, classify(fmt.Errorf("analyticsstore: claim event %s: %w", e.EventID, err))
	}
	if tag.RowsAffected() == 0 {
		return false, nil // already applied: the deferred rollback ends the empty tx
	}
	if err := fold(ctx, tx, e); err != nil {
		return false, classify(fmt.Errorf("analyticsstore: project %s %s: %w", e.Kind, e.EventID, err))
	}
	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("analyticsstore: commit: %w", err)
	}
	return true, nil
}

// fold writes the facts of one event inside tx. An approval that supersedes a
// previous plan stamps that plan's superseded_at too: the superseded plan is
// of the same site (the approve invariant), so the site of the approving
// event seeds the row when the superseded plan was never seen.
func fold(ctx context.Context, tx pgx.Tx, e report.Event) error {
	m := report.MilestonesOf(e)
	if _, err := tx.Exec(ctx, upsertPlanSQL, e.PlanID, e.SiteID, m.Generated, m.Approved, m.Rejected, nil, supersedesOf(e)); err != nil {
		return err
	}
	if m.Superseded != nil {
		if _, err := tx.Exec(ctx, upsertPlanSQL, e.SupersedesPlanID, e.SiteID, nil, nil, nil, m.Superseded, nil); err != nil {
			return err
		}
	}
	return nil
}

func supersedesOf(e report.Event) any {
	if e.Kind == report.KindSlotPlanApproved && e.SupersedesPlanID != "" {
		return e.SupersedesPlanID
	}
	return nil
}

// classify wraps a Postgres data-exception (SQLSTATE class 22) or
// integrity-violation (class 23) error in report.ErrRejected: the same event
// can never succeed, so the consumer dead-letters it instead of retrying
// forever. Everything else (connection loss, timeouts, locks, failovers)
// stays transient.
func classify(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && len(pgErr.Code) >= 2 && (pgErr.Code[:2] == "22" || pgErr.Code[:2] == "23") {
		return fmt.Errorf("%w: %w", report.ErrRejected, err)
	}
	return err
}
