package usecases

import (
	"context"
	"fmt"
	"time"

	"github.com/claudioed/slotting-optimization/internal/application/ports"
	"github.com/claudioed/slotting-optimization/internal/domain/planning"
	"github.com/claudioed/slotting-optimization/internal/domain/slotplan"
)

// GeneratePlan computes a Draft plan for a site from the local copies, with
// the site's Approved plan as the "previous" map (stickiness), stores it and
// enqueues SlotPlanGenerated, all in ONE unit of work. Nothing moves stock:
// the plan is only a proposal until it is approved.
type GeneratePlan struct {
	Writer
	Demand           ports.DemandLedger
	Profiles         ports.ProfileDirectory
	Catalogue        ports.SlotCatalogue
	IDs              ports.IDGenerator
	Planner          *planning.Planner
	DefaultSite      string
	ForwardZoneCodes []string
	// DefaultLookback is the window used when a request names none
	// (LOOKBACK_DAYS); 0 means DefaultLookbackDays.
	DefaultLookback int
}

// GenerateInput is the request: both fields are optional (site defaults to
// the configured default site, LookbackDays to 28).
type GenerateInput struct {
	SiteID       string
	LookbackDays int
}

// Handle runs the use case and returns the stored Draft plan.
func (uc *GeneratePlan) Handle(ctx context.Context, in GenerateInput) (*slotplan.SlotPlan, error) {
	site, err := resolveSite(in.SiteID, uc.DefaultSite)
	if err != nil {
		return nil, err
	}
	days, err := lookbackDays(in.LookbackDays, uc.DefaultLookback)
	if err != nil {
		return nil, err
	}
	var out *slotplan.SlotPlan
	err = uc.UoW.Do(ctx, func(ctx context.Context) error {
		now := uc.now()
		window, err := slotplan.NewWindow(now.Add(-time.Duration(days)*24*time.Hour), now)
		if err != nil {
			return err
		}
		input, err := uc.gather(ctx, site, window)
		if err != nil {
			return err
		}
		res, err := uc.Planner.Plan(input)
		if err != nil {
			return fmt.Errorf("compute plan: %w", err)
		}
		plan, events, err := slotplan.Generate(slotplan.NewPlan{
			ID: uc.IDs.NewPlanID(), Site: site, Window: window, Policy: uc.Planner.Policy(),
			Assignments: res.Assignments, Moves: res.Moves, Unassigned: res.Unassigned,
		}, now)
		if err != nil {
			return err
		}
		if err := uc.persist(ctx, plan, 0, events); err != nil {
			return err
		}
		out = plan
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// gather reads the three local copies and the current Approved map.
func (uc *GeneratePlan) gather(ctx context.Context, site slotplan.SiteID, w slotplan.Window) (planning.Input, error) {
	velocities, err := uc.Demand.Velocity(ctx, site, w.From, w.To)
	if err != nil {
		return planning.Input{}, fmt.Errorf("read demand: %w", err)
	}
	skus := make([]slotplan.SKU, len(velocities))
	for i, v := range velocities {
		skus[i] = v.SKU
	}
	profiles, err := uc.Profiles.Many(ctx, skus)
	if err != nil {
		return planning.Input{}, fmt.Errorf("read product profiles: %w", err)
	}
	slots, err := uc.Catalogue.ForwardSlots(ctx, site, uc.ForwardZoneCodes)
	if err != nil {
		return planning.Input{}, fmt.Errorf("read forward slots: %w", err)
	}
	previous, err := uc.previous(ctx, site)
	if err != nil {
		return planning.Input{}, err
	}
	return planning.Input{
		Velocities: velocities, Profiles: profiles, Slots: slots,
		Previous: previous, Params: planning.DefaultParameters(),
	}, nil
}

// previous is the SKU -> slot map of the site's Approved plan (empty if none).
func (uc *GeneratePlan) previous(ctx context.Context, site slotplan.SiteID) (map[slotplan.SKU]slotplan.SlotCode, error) {
	cur, err := uc.Plans.Current(ctx, site)
	if isNotFound(err) {
		return map[slotplan.SKU]slotplan.SlotCode{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read approved plan: %w", err)
	}
	snap := cur.Snapshot()
	prev := make(map[slotplan.SKU]slotplan.SlotCode, len(snap.Assignments))
	for _, a := range snap.Assignments {
		prev[a.SKU] = a.Slot
	}
	return prev, nil
}
