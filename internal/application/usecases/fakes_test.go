package usecases_test

import (
	"context"
	"errors"
	"slices"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/claudioed/slotting-optimization/internal/application/outbox"
	"github.com/claudioed/slotting-optimization/internal/application/repository"
	"github.com/claudioed/slotting-optimization/internal/application/usecases"
	"github.com/claudioed/slotting-optimization/internal/domain/planning"
	"github.com/claudioed/slotting-optimization/internal/domain/slotplan"
)

var (
	t0      = time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	errBoom = errors.New("boom")
)

// fakeRepo is a version-guarded in-test SlotPlanRepository enforcing the
// "one Approved plan per site" rule like the partial unique index does.
type fakeRepo struct {
	mu      sync.Mutex
	plans   map[slotplan.PlanID]slotplan.Snapshot
	saves   []savedCall
	getErr  error
	saveErr map[slotplan.PlanID]error
	curErr  error
	listErr error

	listFilter repository.PlanFilter
	listAfter  slotplan.PlanID
	listLimit  int
}

type savedCall struct {
	id            slotplan.PlanID
	loadedVersion int64
	state         slotplan.State
}

func newFakeRepo() *fakeRepo {
	return &fakeRepo{plans: map[slotplan.PlanID]slotplan.Snapshot{}, saveErr: map[slotplan.PlanID]error{}}
}

func (r *fakeRepo) Get(_ context.Context, id slotplan.PlanID) (*slotplan.SlotPlan, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.getErr != nil {
		return nil, r.getErr
	}
	s, ok := r.plans[id]
	if !ok {
		return nil, repository.ErrPlanNotFound
	}
	return restore(s), nil
}

func (r *fakeRepo) Save(_ context.Context, p *slotplan.SlotPlan, loaded int64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	snap := p.Snapshot()
	if err := r.saveErr[snap.ID]; err != nil {
		return err
	}
	cur, ok := r.plans[snap.ID]
	if (loaded == 0 && ok) || (loaded != 0 && (!ok || cur.Version != loaded)) {
		return repository.ErrConcurrentModification
	}
	if snap.State == slotplan.StateApproved {
		for id, o := range r.plans {
			if id != snap.ID && o.Site == snap.Site && o.State == slotplan.StateApproved {
				return repository.ErrApprovedPlanConflict
			}
		}
	}
	r.plans[snap.ID] = snap
	r.saves = append(r.saves, savedCall{id: snap.ID, loadedVersion: loaded, state: snap.State})
	return nil
}

func (r *fakeRepo) Current(_ context.Context, site slotplan.SiteID) (*slotplan.SlotPlan, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.curErr != nil {
		return nil, r.curErr
	}
	for _, s := range r.plans {
		if s.Site == site && s.State == slotplan.StateApproved {
			return restore(s), nil
		}
	}
	return nil, repository.ErrPlanNotFound
}

func (r *fakeRepo) List(_ context.Context, f repository.PlanFilter, after slotplan.PlanID, limit int) ([]*slotplan.SlotPlan, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.listFilter, r.listAfter, r.listLimit = f, after, limit
	if r.listErr != nil {
		return nil, r.listErr
	}
	var snaps []slotplan.Snapshot
	for _, s := range r.plans {
		if (f.Site == "" || s.Site == f.Site) && (f.State == "" || s.State == f.State) {
			snaps = append(snaps, s)
		}
	}
	// newest first: generated-at desc, id desc
	sort.Slice(snaps, func(i, j int) bool {
		if !snaps[i].GeneratedAt.Equal(snaps[j].GeneratedAt) {
			return snaps[i].GeneratedAt.After(snaps[j].GeneratedAt)
		}
		return snaps[i].ID > snaps[j].ID
	})
	if after != "" {
		i := slices.IndexFunc(snaps, func(s slotplan.Snapshot) bool { return s.ID == after })
		if i < 0 {
			snaps = nil
		} else {
			snaps = snaps[i+1:]
		}
	}
	if len(snaps) > limit {
		snaps = snaps[:limit]
	}
	out := make([]*slotplan.SlotPlan, len(snaps))
	for i, s := range snaps {
		out[i] = restore(s)
	}
	return out, nil
}

func restore(s slotplan.Snapshot) *slotplan.SlotPlan {
	p, err := slotplan.Restore(s)
	if err != nil {
		panic(err)
	}
	return p
}

// put stores a plan directly (test setup).
func (r *fakeRepo) put(p *slotplan.SlotPlan) { r.plans[p.ID()] = p.Snapshot() }

// fakeEncoder records the events and returns one message per event.
type fakeEncoder struct {
	events []slotplan.Event
	err    error
}

func (e *fakeEncoder) Encode(events ...slotplan.Event) ([]outbox.Message, error) {
	if e.err != nil {
		return nil, e.err
	}
	e.events = append(e.events, events...)
	out := make([]outbox.Message, len(events))
	for i, ev := range events {
		out[i] = outbox.Message{EventType: ev.EventName(), Subject: string(ev.PlanID())}
	}
	return out, nil
}

type fakeOutbox struct {
	msgs []outbox.Message
	err  error
}

func (o *fakeOutbox) Insert(_ context.Context, msgs ...outbox.Message) error {
	if o.err != nil {
		return o.err
	}
	o.msgs = append(o.msgs, msgs...)
	return nil
}

func (o *fakeOutbox) types() []string {
	out := make([]string, len(o.msgs))
	for i, m := range o.msgs {
		out[i] = m.EventType
	}
	return out
}

// fakeUoW runs fn and records how many units ran and how many failed.
type fakeUoW struct{ runs, failures int }

func (u *fakeUoW) Do(ctx context.Context, fn func(ctx context.Context) error) error {
	u.runs++
	if err := fn(ctx); err != nil {
		u.failures++
		return err
	}
	return nil
}

type fixedClock struct{ t time.Time }

func (c fixedClock) Now() time.Time { return c.t }

type fakeProcessed struct {
	seen map[string]bool
	err  error
}

func (f *fakeProcessed) Claim(_ context.Context, consumer, id string) (bool, error) {
	if f.err != nil {
		return false, f.err
	}
	if f.seen == nil {
		f.seen = map[string]bool{}
	}
	k := consumer + "/" + id
	if f.seen[k] {
		return false, nil
	}
	f.seen[k] = true
	return true, nil
}

// sequenceIDs mints plan-1, plan-2, ...
type sequenceIDs struct{ n int }

func (s *sequenceIDs) NewPlanID() slotplan.PlanID {
	s.n++
	return slotplan.PlanID("plan-" + string(rune('0'+s.n)))
}

// fakeDemand serves canned velocities and records lines.
type fakeDemand struct {
	velocities       []planning.SkuVelocity
	velErr, applyErr error
	lines            []repository.DemandLine
	site             slotplan.SiteID
	from, to         time.Time
}

func (d *fakeDemand) Apply(_ context.Context, l repository.DemandLine) error {
	if d.applyErr != nil {
		return d.applyErr
	}
	d.lines = append(d.lines, l)
	return nil
}

func (d *fakeDemand) Velocity(_ context.Context, site slotplan.SiteID, from, to time.Time) ([]planning.SkuVelocity, error) {
	d.site, d.from, d.to = site, from, to
	return d.velocities, d.velErr
}

// fakeProfiles is an in-memory ProfileDirectory.
type fakeProfiles struct {
	rows            map[slotplan.SKU]repository.ProductProfile
	getErr, manyErr error
	saveErr         error
	saved           []repository.ProductProfile
	requestedSKUs   []slotplan.SKU
}

func newFakeProfiles() *fakeProfiles {
	return &fakeProfiles{rows: map[slotplan.SKU]repository.ProductProfile{}}
}

func (f *fakeProfiles) Get(_ context.Context, sku slotplan.SKU) (repository.ProductProfile, error) {
	if f.getErr != nil {
		return repository.ProductProfile{}, f.getErr
	}
	p, ok := f.rows[sku]
	if !ok {
		return repository.ProductProfile{}, repository.ErrProfileNotFound
	}
	return p, nil
}

func (f *fakeProfiles) Many(_ context.Context, skus []slotplan.SKU) (map[slotplan.SKU]planning.Profile, error) {
	f.requestedSKUs = skus
	if f.manyErr != nil {
		return nil, f.manyErr
	}
	out := map[slotplan.SKU]planning.Profile{}
	for _, s := range skus {
		if p, ok := f.rows[s]; ok {
			out[s] = planning.Profile{HandlingTags: p.HandlingTags, TemperatureClass: p.TemperatureClass, Unit: p.Unit}
		}
	}
	return out, nil
}

func (f *fakeProfiles) Save(_ context.Context, p repository.ProductProfile) error {
	if f.saveErr != nil {
		return f.saveErr
	}
	f.rows[p.SKU] = p
	f.saved = append(f.saved, p)
	return nil
}

// fakeCatalogue serves canned forward slots and records layout writes.
type fakeCatalogue struct {
	slots      []planning.Slot
	slotsErr   error
	err        error
	zones      []repository.Zone
	savedSlots []repository.Slot
	retired    []slotplan.SlotCode
	site       slotplan.SiteID
	zoneCodes  []string
}

func (c *fakeCatalogue) ForwardSlots(_ context.Context, site slotplan.SiteID, codes []string) ([]planning.Slot, error) {
	c.site, c.zoneCodes = site, codes
	return c.slots, c.slotsErr
}

func (c *fakeCatalogue) SaveZone(_ context.Context, z repository.Zone) error {
	if c.err != nil {
		return c.err
	}
	c.zones = append(c.zones, z)
	return nil
}

func (c *fakeCatalogue) SaveSlot(_ context.Context, s repository.Slot) error {
	if c.err != nil {
		return c.err
	}
	c.savedSlots = append(c.savedSlots, s)
	return nil
}

func (c *fakeCatalogue) Decommission(_ context.Context, code slotplan.SlotCode) error {
	if c.err != nil {
		return c.err
	}
	c.retired = append(c.retired, code)
	return nil
}

// harness wires every fake into a Writer.
type harness struct {
	repo    *fakeRepo
	enc     *fakeEncoder
	outbox  *fakeOutbox
	uow     *fakeUoW
	writer  usecases.Writer
	process *fakeProcessed
	demand  *fakeDemand
	profile *fakeProfiles
	cat     *fakeCatalogue
	ids     *sequenceIDs
}

func newHarness() *harness {
	h := &harness{
		repo: newFakeRepo(), enc: &fakeEncoder{}, outbox: &fakeOutbox{}, uow: &fakeUoW{},
		process: &fakeProcessed{}, demand: &fakeDemand{}, profile: newFakeProfiles(), cat: &fakeCatalogue{}, ids: &sequenceIDs{},
	}
	h.writer = usecases.Writer{Plans: h.repo, Outbox: h.outbox, Encoder: h.enc, UoW: h.uow, Clock: fixedClock{t0}}
	return h
}

func (h *harness) intake() usecases.Intake {
	return usecases.Intake{UoW: h.uow, Processed: h.process}
}

// draft stores a Draft plan of site with one assignment and returns it.
func (h *harness) draft(id, site string, generatedAt time.Time) *slotplan.SlotPlan {
	w, err := slotplan.NewWindow(generatedAt.Add(-28*24*time.Hour), generatedAt)
	if err != nil {
		panic(err)
	}
	p, _, err := slotplan.Generate(slotplan.NewPlan{
		ID: slotplan.PlanID(id), Site: slotplan.SiteID(site), Window: w, Policy: slotplan.PolicyABCVelocityV1,
		Assignments: []slotplan.Assignment{{SKU: "SKU-1", Slot: "S-" + slotplan.SlotCode(id), Class: slotplan.ClassA, Picks: 3, Units: 9}},
		Moves:       []slotplan.Move{{SKU: "SKU-1", To: "S-" + slotplan.SlotCode(id), Kind: slotplan.MoveAssign}},
	}, generatedAt)
	if err != nil {
		panic(err)
	}
	h.repo.put(p)
	return p
}

// approved stores an Approved plan of site.
func (h *harness) approved(id, site string, generatedAt time.Time) *slotplan.SlotPlan {
	p := h.draft(id, site, generatedAt)
	if _, err := p.Approve("", generatedAt.Add(time.Minute)); err != nil {
		panic(err)
	}
	h.repo.put(p)
	return p
}

// Assertion helpers: they keep the tests flat (the linters bound the
// complexity of test functions too).

func eq[T comparable](t *testing.T, what string, got, want T) {
	t.Helper()
	if got != want {
		t.Errorf("%s = %v, want %v", what, got, want)
	}
}

func noErr(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func isErr(t *testing.T, err, want error) {
	t.Helper()
	if !errors.Is(err, want) {
		t.Fatalf("err = %v, want %v", err, want)
	}
}

func timeEq(t *testing.T, what string, got, want time.Time) {
	t.Helper()
	if !got.Equal(want) {
		t.Errorf("%s = %v, want %v", what, got, want)
	}
}
