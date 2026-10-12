package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/claudioed/slotting-optimization/internal/application/ports"
	"github.com/claudioed/slotting-optimization/internal/application/repository"
	"github.com/claudioed/slotting-optimization/internal/domain/slotplan"
)

// Constraint names the repository translates into typed errors.
const (
	pkSlotPlans         = "slot_plans_pkey"
	uqOneApprovedInSite = "uq_slot_plans_one_approved_per_site"
	pgUniqueViolation   = "23505"
)

// SlotPlanRepo is the pgxpool-backed ports.SlotPlanRepository. Inside a
// UnitOfWork it joins the transaction carried in ctx.
type SlotPlanRepo struct {
	pool *pgxpool.Pool
}

var _ ports.SlotPlanRepository = (*SlotPlanRepo)(nil)

// NewSlotPlanRepo constructs a SlotPlanRepo over pool.
func NewSlotPlanRepo(pool *pgxpool.Pool) *SlotPlanRepo { return &SlotPlanRepo{pool: pool} }

const planColumns = `plan_id, site_id, state, policy, window_from, window_to, generated_at,
	approved_at, rejected_at, superseded_at, reject_reason, supersedes_plan_id, version`

// Get implements ports.SlotPlanRepository.
func (r *SlotPlanRepo) Get(ctx context.Context, id slotplan.PlanID) (*slotplan.SlotPlan, error) {
	q := queryFor(ctx, r.pool)
	rows, err := q.Query(ctx, `SELECT `+planColumns+` FROM slot_plans WHERE plan_id = $1`, string(id))
	if err != nil {
		return nil, fmt.Errorf("get slot plan: %w", err)
	}
	snaps, err := scanPlans(rows)
	if err != nil {
		return nil, err
	}
	if len(snaps) == 0 {
		return nil, repository.ErrPlanNotFound
	}
	plans, err := r.withChildren(ctx, q, snaps)
	if err != nil {
		return nil, err
	}
	return plans[0], nil
}

// Current implements ports.SlotPlanRepository.
func (r *SlotPlanRepo) Current(ctx context.Context, site slotplan.SiteID) (*slotplan.SlotPlan, error) {
	q := queryFor(ctx, r.pool)
	rows, err := q.Query(ctx, `SELECT `+planColumns+` FROM slot_plans WHERE site_id = $1 AND state = 'Approved'`, string(site))
	if err != nil {
		return nil, fmt.Errorf("get approved slot plan: %w", err)
	}
	snaps, err := scanPlans(rows)
	if err != nil {
		return nil, err
	}
	if len(snaps) == 0 {
		return nil, repository.ErrPlanNotFound
	}
	plans, err := r.withChildren(ctx, q, snaps)
	if err != nil {
		return nil, err
	}
	return plans[0], nil
}

// List implements ports.SlotPlanRepository: newest first, keyset paged on
// (generated_at, plan_id). A cursor plan that does not exist yields an empty
// page (the row comparison against a missing row is NULL).
func (r *SlotPlanRepo) List(ctx context.Context, f repository.PlanFilter, after slotplan.PlanID, limit int) ([]*slotplan.SlotPlan, error) {
	q := queryFor(ctx, r.pool)
	rows, err := q.Query(ctx, `
		SELECT `+planColumns+` FROM slot_plans
		WHERE ($1 = '' OR site_id = $1)
		  AND ($2 = '' OR state = $2)
		  AND ($3 = '' OR (generated_at, plan_id) < (SELECT generated_at, plan_id FROM slot_plans WHERE plan_id = $3))
		ORDER BY generated_at DESC, plan_id DESC
		LIMIT $4
	`, string(f.Site), string(f.State), string(after), limit)
	if err != nil {
		return nil, fmt.Errorf("list slot plans: %w", err)
	}
	snaps, err := scanPlans(rows)
	if err != nil {
		return nil, err
	}
	return r.withChildren(ctx, q, snaps)
}

func scanPlans(rows pgx.Rows) ([]slotplan.Snapshot, error) {
	defer rows.Close()
	var out []slotplan.Snapshot
	for rows.Next() {
		var (
			s                              slotplan.Snapshot
			id, site, state, policy        string
			approved, rejected, superseded *time.Time
			reason, supersedes             *string
		)
		if err := rows.Scan(&id, &site, &state, &policy, &s.Window.From, &s.Window.To, &s.GeneratedAt,
			&approved, &rejected, &superseded, &reason, &supersedes, &s.Version); err != nil {
			return nil, fmt.Errorf("scan slot plan: %w", err)
		}
		s.ID, s.Site, s.State, s.Policy = slotplan.PlanID(id), slotplan.SiteID(site), slotplan.State(state), slotplan.PolicyName(policy)
		s.ApprovedAt, s.RejectedAt, s.SupersededAt = deref(approved), deref(rejected), deref(superseded)
		if reason != nil {
			s.RejectReason = *reason
		}
		if supersedes != nil {
			s.SupersedesPlanID = slotplan.PlanID(*supersedes)
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read slot plans: %w", err)
	}
	return out, nil
}

func deref(t *time.Time) time.Time {
	if t == nil {
		return time.Time{}
	}
	return t.UTC()
}

func ptrTime(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

func ptrString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// withChildren loads the assignments, moves and unassigned entries of all
// snaps in three queries and restores the aggregates, in snaps order.
func (r *SlotPlanRepo) withChildren(ctx context.Context, q querier, snaps []slotplan.Snapshot) ([]*slotplan.SlotPlan, error) {
	if len(snaps) == 0 {
		return nil, nil
	}
	ids := make([]string, len(snaps))
	index := make(map[slotplan.PlanID]int, len(snaps))
	for i, s := range snaps {
		ids[i] = string(s.ID)
		index[s.ID] = i
	}
	if err := loadAssignments(ctx, q, ids, snaps, index); err != nil {
		return nil, err
	}
	if err := loadMoves(ctx, q, ids, snaps, index); err != nil {
		return nil, err
	}
	if err := loadUnassigned(ctx, q, ids, snaps, index); err != nil {
		return nil, err
	}
	out := make([]*slotplan.SlotPlan, len(snaps))
	for i, s := range snaps {
		p, err := slotplan.Restore(s)
		if err != nil {
			return nil, fmt.Errorf("restore slot plan %s: %w", s.ID, err)
		}
		out[i] = p
	}
	return out, nil
}

func loadAssignments(ctx context.Context, q querier, ids []string, snaps []slotplan.Snapshot, index map[slotplan.PlanID]int) error {
	rows, err := q.Query(ctx, `SELECT plan_id, sku, slot, abc_class, picks, units FROM slot_plan_assignments WHERE plan_id = ANY($1) ORDER BY plan_id, sku`, ids)
	if err != nil {
		return fmt.Errorf("load assignments: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var plan, sku, slot, class string
		var a slotplan.Assignment
		if err := rows.Scan(&plan, &sku, &slot, &class, &a.Picks, &a.Units); err != nil {
			return fmt.Errorf("scan assignment: %w", err)
		}
		a.SKU, a.Slot, a.Class = slotplan.SKU(sku), slotplan.SlotCode(slot), slotplan.ABCClass(class)
		i := index[slotplan.PlanID(plan)]
		snaps[i].Assignments = append(snaps[i].Assignments, a)
	}
	return rows.Err()
}

func loadMoves(ctx context.Context, q querier, ids []string, snaps []slotplan.Snapshot, index map[slotplan.PlanID]int) error {
	rows, err := q.Query(ctx, `SELECT plan_id, sku, from_slot, to_slot, kind FROM slot_plan_moves WHERE plan_id = ANY($1) ORDER BY plan_id, sku`, ids)
	if err != nil {
		return fmt.Errorf("load moves: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var plan, sku, kind string
		var from, to *string
		if err := rows.Scan(&plan, &sku, &from, &to, &kind); err != nil {
			return fmt.Errorf("scan move: %w", err)
		}
		m := slotplan.Move{SKU: slotplan.SKU(sku), Kind: slotplan.MoveKind(kind)}
		if from != nil {
			m.From = slotplan.SlotCode(*from)
		}
		if to != nil {
			m.To = slotplan.SlotCode(*to)
		}
		i := index[slotplan.PlanID(plan)]
		snaps[i].Moves = append(snaps[i].Moves, m)
	}
	return rows.Err()
}

func loadUnassigned(ctx context.Context, q querier, ids []string, snaps []slotplan.Snapshot, index map[slotplan.PlanID]int) error {
	rows, err := q.Query(ctx, `SELECT plan_id, sku, reason FROM slot_plan_unassigned WHERE plan_id = ANY($1) ORDER BY plan_id, sku`, ids)
	if err != nil {
		return fmt.Errorf("load unassigned: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var plan, sku, reason string
		if err := rows.Scan(&plan, &sku, &reason); err != nil {
			return fmt.Errorf("scan unassigned: %w", err)
		}
		i := index[slotplan.PlanID(plan)]
		snaps[i].Unassigned = append(snaps[i].Unassigned, slotplan.Unassigned{SKU: slotplan.SKU(sku), Reason: slotplan.UnassignedReason(reason)})
	}
	return rows.Err()
}

// Save implements ports.SlotPlanRepository: a two-branch write. loadedVersion
// 0 inserts the header and the immutable content; anything else updates only
// the decision columns and the version, and only if the stored version still
// equals loadedVersion (RowsAffected is checked).
func (r *SlotPlanRepo) Save(ctx context.Context, p *slotplan.SlotPlan, loadedVersion int64) error {
	snap := p.Snapshot()
	return inTx(ctx, r.pool, func(q querier) error {
		if loadedVersion == 0 {
			return translate(insertPlan(ctx, q, snap))
		}
		tag, err := q.Exec(ctx, `
			UPDATE slot_plans
			SET state = $2, approved_at = $3, rejected_at = $4, superseded_at = $5,
			    reject_reason = $6, supersedes_plan_id = $7, version = $8, updated_at = now()
			WHERE plan_id = $1 AND version = $9
		`, string(snap.ID), string(snap.State), ptrTime(snap.ApprovedAt), ptrTime(snap.RejectedAt), ptrTime(snap.SupersededAt),
			ptrString(snap.RejectReason), ptrString(string(snap.SupersedesPlanID)), snap.Version, loadedVersion)
		if err != nil {
			return translate(fmt.Errorf("update slot plan: %w", err))
		}
		if tag.RowsAffected() != 1 {
			return repository.ErrConcurrentModification
		}
		return nil
	})
}

func insertPlan(ctx context.Context, q querier, s slotplan.Snapshot) error {
	if _, err := q.Exec(ctx, `
		INSERT INTO slot_plans (plan_id, site_id, state, policy, window_from, window_to, generated_at,
			approved_at, rejected_at, superseded_at, reject_reason, supersedes_plan_id, version)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
	`, string(s.ID), string(s.Site), string(s.State), string(s.Policy), s.Window.From, s.Window.To, s.GeneratedAt,
		ptrTime(s.ApprovedAt), ptrTime(s.RejectedAt), ptrTime(s.SupersededAt), ptrString(s.RejectReason),
		ptrString(string(s.SupersedesPlanID)), s.Version); err != nil {
		return fmt.Errorf("insert slot plan: %w", err)
	}
	for _, a := range s.Assignments {
		if _, err := q.Exec(ctx, `INSERT INTO slot_plan_assignments (plan_id, sku, slot, abc_class, picks, units) VALUES ($1, $2, $3, $4, $5, $6)`,
			string(s.ID), string(a.SKU), string(a.Slot), string(a.Class), a.Picks, a.Units); err != nil {
			return fmt.Errorf("insert assignment: %w", err)
		}
	}
	for _, m := range s.Moves {
		if _, err := q.Exec(ctx, `INSERT INTO slot_plan_moves (plan_id, sku, from_slot, to_slot, kind) VALUES ($1, $2, $3, $4, $5)`,
			string(s.ID), string(m.SKU), ptrString(string(m.From)), ptrString(string(m.To)), string(m.Kind)); err != nil {
			return fmt.Errorf("insert move: %w", err)
		}
	}
	for _, u := range s.Unassigned {
		if _, err := q.Exec(ctx, `INSERT INTO slot_plan_unassigned (plan_id, sku, reason) VALUES ($1, $2, $3)`,
			string(s.ID), string(u.SKU), string(u.Reason)); err != nil {
			return fmt.Errorf("insert unassigned: %w", err)
		}
	}
	return nil
}

// translate maps the unique violations the schema guards into the typed
// errors of the port; everything else passes through unchanged.
func translate(err error) error {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != pgUniqueViolation {
		return err
	}
	if pgErr.ConstraintName == uqOneApprovedInSite {
		return repository.ErrApprovedPlanConflict
	}
	if pgErr.ConstraintName == pkSlotPlans {
		return repository.ErrConcurrentModification
	}
	return err
}
