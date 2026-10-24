package usecases

import (
	"context"
	"errors"
	"fmt"
	"slices"
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
	// CasePickZoneCodes are the zone codes whose faces are CASE-pick faces
	// (CASE_PICK_ZONE_CODES, ADR 0012). They are read in addition to
	// ForwardZoneCodes. Empty = no case-pick faces: every SKU is EACH-picked
	// and PackLevels is not consulted.
	CasePickZoneCodes []string
	// PackLevels is the local pack-level view (nil only when
	// CasePickZoneCodes is empty).
	PackLevels ports.PackLevelView
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
		input, err := uc.gather(ctx, site, window, now)
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
func (uc *GeneratePlan) gather(ctx context.Context, site slotplan.SiteID, w slotplan.Window, now time.Time) (planning.Input, error) {
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
	slots, err := uc.Catalogue.ForwardSlots(ctx, site, uc.zoneCodes())
	if err != nil {
		return planning.Input{}, fmt.Errorf("read forward slots: %w", err)
	}
	previous, err := uc.previous(ctx, site)
	if err != nil {
		return planning.Input{}, err
	}
	in := planning.Input{
		Velocities: velocities, Profiles: profiles, Slots: markCaseFaces(slots, uc.CasePickZoneCodes),
		Previous: previous, Params: planning.DefaultParameters(),
	}
	if len(uc.CasePickZoneCodes) == 0 {
		return in, nil
	}
	if err := uc.addCasePick(ctx, &in, site, w, now, skus); err != nil {
		return planning.Input{}, err
	}
	return in, nil
}

// zoneCodes is every zone whose faces take part in the plan: the forward
// zones and the case-pick zones.
func (uc *GeneratePlan) zoneCodes() []string {
	if len(uc.CasePickZoneCodes) == 0 {
		return uc.ForwardZoneCodes
	}
	codes := slices.Clone(uc.ForwardZoneCodes)
	for _, c := range uc.CasePickZoneCodes {
		if !slices.Contains(codes, c) {
			codes = append(codes, c)
		}
	}
	return codes
}

// markCaseFaces flags the slots of the case-pick zones as CASE-pick faces.
func markCaseFaces(slots []planning.Slot, casePickZones []string) []planning.Slot {
	if len(casePickZones) == 0 {
		return slots
	}
	out := slices.Clone(slots)
	for i, s := range out {
		if slices.Contains(casePickZones, s.ZoneCode) {
			out[i].PickUnit = slotplan.PickCase
		}
	}
	return out
}

// addCasePick adds what the planner needs to pick a SKU in cases: the CASE
// size in force from the local pack-level view and the greatest common
// divisor of the SKU's demand lines. The case size lands only on SKUs that
// already have a product profile (the handling tags and temperature class of
// the profile still decide where a SKU may go).
func (uc *GeneratePlan) addCasePick(ctx context.Context, in *planning.Input, site slotplan.SiteID, w slotplan.Window, now time.Time, skus []slotplan.SKU) error {
	if uc.PackLevels == nil {
		return errors.New("compute plan: CASE_PICK_ZONE_CODES is set but the pack-level view is not wired")
	}
	cases, err := uc.PackLevels.ActiveCases(ctx, skus, now)
	if err != nil {
		return fmt.Errorf("read case levels: %w", err)
	}
	for sku, c := range cases {
		profile, ok := in.Profiles[sku]
		if !ok {
			continue
		}
		c := c
		profile.Case = &c
		in.Profiles[sku] = profile
	}
	in.LineUnitsGCD, err = uc.Demand.LineUnitsGCD(ctx, site, w.From, w.To)
	if err != nil {
		return fmt.Errorf("read demand line quantities: %w", err)
	}
	return nil
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
