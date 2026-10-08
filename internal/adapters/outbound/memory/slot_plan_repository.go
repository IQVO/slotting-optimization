package memory

import (
	"context"
	"slices"
	"sort"
	"sync"

	"github.com/claudioed/slotting-optimization/internal/application/ports"
	"github.com/claudioed/slotting-optimization/internal/application/repository"
	"github.com/claudioed/slotting-optimization/internal/domain/slotplan"
)

// SlotPlanRepo is the in-memory, version-guarded ports.SlotPlanRepository.
// It enforces what the Postgres adapter's constraints enforce: an insert of
// an existing id and a stale update are ErrConcurrentModification, and two
// Approved plans of one site are ErrApprovedPlanConflict.
type SlotPlanRepo struct {
	mu    sync.Mutex
	plans map[slotplan.PlanID]slotplan.Snapshot
}

var _ ports.SlotPlanRepository = (*SlotPlanRepo)(nil)

// NewSlotPlanRepo constructs an empty SlotPlanRepo.
func NewSlotPlanRepo() *SlotPlanRepo {
	return &SlotPlanRepo{plans: make(map[slotplan.PlanID]slotplan.Snapshot)}
}

// Get implements ports.SlotPlanRepository.
func (r *SlotPlanRepo) Get(_ context.Context, id slotplan.PlanID) (*slotplan.SlotPlan, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.plans[id]
	if !ok {
		return nil, repository.ErrPlanNotFound
	}
	return slotplan.Restore(s)
}

// Save implements ports.SlotPlanRepository.
func (r *SlotPlanRepo) Save(_ context.Context, p *slotplan.SlotPlan, loadedVersion int64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	snap := p.Snapshot()
	cur, exists := r.plans[snap.ID]
	if loadedVersion == 0 && exists {
		return repository.ErrConcurrentModification
	}
	if loadedVersion != 0 && (!exists || cur.Version != loadedVersion) {
		return repository.ErrConcurrentModification
	}
	if snap.State == slotplan.StateApproved && r.otherApproved(snap) {
		return repository.ErrApprovedPlanConflict
	}
	r.plans[snap.ID] = snap
	return nil
}

func (r *SlotPlanRepo) otherApproved(snap slotplan.Snapshot) bool {
	for id, o := range r.plans {
		if id != snap.ID && o.Site == snap.Site && o.State == slotplan.StateApproved {
			return true
		}
	}
	return false
}

// Current implements ports.SlotPlanRepository.
func (r *SlotPlanRepo) Current(_ context.Context, site slotplan.SiteID) (*slotplan.SlotPlan, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, s := range r.plans {
		if s.Site == site && s.State == slotplan.StateApproved {
			return slotplan.Restore(s)
		}
	}
	return nil, repository.ErrPlanNotFound
}

// List implements ports.SlotPlanRepository: newest first (generated-at, then
// id, both descending), keyset-paged after `after`.
func (r *SlotPlanRepo) List(_ context.Context, f repository.PlanFilter, after slotplan.PlanID, limit int) ([]*slotplan.SlotPlan, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var snaps []slotplan.Snapshot
	for _, s := range r.plans {
		if (f.Site == "" || s.Site == f.Site) && (f.State == "" || s.State == f.State) {
			snaps = append(snaps, s)
		}
	}
	sort.Slice(snaps, func(i, j int) bool {
		if !snaps[i].GeneratedAt.Equal(snaps[j].GeneratedAt) {
			return snaps[i].GeneratedAt.After(snaps[j].GeneratedAt)
		}
		return snaps[i].ID > snaps[j].ID
	})
	if after != "" {
		// Keyset semantics: the page continues strictly after `after` in the
		// total order; an unknown cursor plan yields an empty page, like the
		// SQL row-value comparison against a missing row.
		i := slices.IndexFunc(snaps, func(s slotplan.Snapshot) bool { return s.ID == after })
		if i < 0 {
			return nil, nil
		}
		snaps = snaps[i+1:]
	}
	if len(snaps) > limit {
		snaps = snaps[:limit]
	}
	out := make([]*slotplan.SlotPlan, 0, len(snaps))
	for _, s := range snaps {
		p, err := slotplan.Restore(s)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, nil
}

// Snapshot implements Snapshotter.
func (r *SlotPlanRepo) Snapshot() func() {
	r.mu.Lock()
	defer r.mu.Unlock()
	saved := make(map[slotplan.PlanID]slotplan.Snapshot, len(r.plans))
	for k, v := range r.plans {
		saved[k] = v
	}
	return func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.plans = saved
	}
}
