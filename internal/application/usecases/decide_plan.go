package usecases

import (
	"context"
	"errors"
	"fmt"

	"github.com/claudioed/slotting-optimization/internal/application/repository"
	"github.com/claudioed/slotting-optimization/internal/domain/slotplan"
)

// ApprovePlan approves a Draft plan. The site's previously Approved plan is
// superseded in the SAME unit of work (it is saved first, so the partial
// unique index "one Approved plan per site" is never violated), and
// SlotPlanApproved, carrying the full map and the moves, is enqueued.
type ApprovePlan struct {
	Writer
}

// Handle runs the use case and returns the Approved plan. A plan that is not
// a Draft is slotplan.ErrNotDraft. Losing a race to another approval of the
// same site is repository.ErrApprovedPlanConflict.
func (uc *ApprovePlan) Handle(ctx context.Context, rawID string) (*slotplan.SlotPlan, error) {
	id, err := slotplan.NewPlanID(rawID)
	if err != nil {
		return nil, err
	}
	var out *slotplan.SlotPlan
	err = uc.UoW.Do(ctx, func(ctx context.Context) error {
		p, err := uc.Plans.Get(ctx, id)
		if err != nil {
			return err
		}
		if p.State() != slotplan.StateDraft {
			return slotplan.ErrNotDraft // before anything else is touched
		}
		loaded := p.Version()
		prevID, err := uc.supersedePrevious(ctx, p)
		if err != nil {
			return err
		}
		events, err := p.Approve(prevID, uc.now())
		if err != nil {
			return err
		}
		if err := uc.persist(ctx, p, loaded, events); err != nil {
			return err
		}
		out = p
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// supersedePrevious supersedes and saves the site's Approved plan, if any,
// and returns its id. Another approval changing it first is a lost race.
func (uc *ApprovePlan) supersedePrevious(ctx context.Context, p *slotplan.SlotPlan) (slotplan.PlanID, error) {
	prev, err := uc.Plans.Current(ctx, p.Site())
	if isNotFound(err) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read approved plan: %w", err)
	}
	loaded := prev.Version()
	if err := prev.Supersede(uc.now()); err != nil {
		return "", err
	}
	if err := uc.Plans.Save(ctx, prev, loaded); err != nil {
		if errors.Is(err, repository.ErrConcurrentModification) {
			return "", repository.ErrApprovedPlanConflict
		}
		return "", err
	}
	return prev.ID(), nil
}

// RejectPlan rejects a Draft plan and enqueues SlotPlanRejected.
type RejectPlan struct {
	Writer
}

// Handle runs the use case and returns the Rejected plan. The reason is
// optional (at most 500 characters).
func (uc *RejectPlan) Handle(ctx context.Context, rawID, reason string) (*slotplan.SlotPlan, error) {
	id, err := slotplan.NewPlanID(rawID)
	if err != nil {
		return nil, err
	}
	var out *slotplan.SlotPlan
	err = uc.UoW.Do(ctx, func(ctx context.Context) error {
		p, err := uc.Plans.Get(ctx, id)
		if err != nil {
			return err
		}
		loaded := p.Version()
		events, err := p.Reject(reason, uc.now())
		if err != nil {
			return err
		}
		if err := uc.persist(ctx, p, loaded, events); err != nil {
			return err
		}
		out = p
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
