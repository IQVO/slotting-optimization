// Package usecases holds the application's use cases. Every write follows
// the same shape inside ONE ports.UnitOfWork: load the SlotPlan, apply the
// aggregate command, save it guarded by the version that was loaded, and
// enqueue the raised events in the transactional outbox.
package usecases

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/claudioed/slotting-optimization/internal/application/ports"
	"github.com/claudioed/slotting-optimization/internal/application/repository"
	"github.com/claudioed/slotting-optimization/internal/domain/slotplan"
)

// Errors of the request validation that is not the aggregate's own. The HTTP
// adapter maps each to a problem slug.
var (
	// ErrInvalidLookback marks a lookbackDays or windowDays outside 1..365.
	ErrInvalidLookback = errors.New("lookback must be between 1 and 365 days")
	// ErrInvalidLimit marks a list limit outside 1..500.
	ErrInvalidLimit = errors.New("limit must be between 1 and 500")
	// ErrInvalidCursor marks a cursor this API did not hand out.
	ErrInvalidCursor = errors.New("cursor is not a value returned by this API")
	// ErrInvalidState marks an unknown plan state filter.
	ErrInvalidState = errors.New("state must be one of Draft, Approved, Rejected, Superseded")
	// ErrNoSite marks a request that names no site while no default site is
	// configured.
	ErrNoSite = errors.New("no siteId given and no default site is configured")
)

// Window and page bounds (apis/openapi.yaml).
const (
	DefaultLookbackDays = 28
	MaxLookbackDays     = 365
	DefaultListLimit    = 100
	MaxListLimit        = 500
)

// Writer bundles the ports every write use case needs.
type Writer struct {
	Plans   ports.SlotPlanRepository
	Outbox  ports.OutboxRepository
	Encoder ports.EventEncoder
	UoW     ports.UnitOfWork
	Clock   ports.Clock
}

// now returns the clock's current time in UTC.
func (w Writer) now() time.Time { return w.Clock.Now().UTC() }

// enqueue encodes events and inserts them into the outbox, on the ctx of the
// surrounding unit of work.
func (w Writer) enqueue(ctx context.Context, events []slotplan.Event) error {
	msgs, err := w.Encoder.Encode(events...)
	if err != nil {
		return fmt.Errorf("encode events: %w", err)
	}
	if err := w.Outbox.Insert(ctx, msgs...); err != nil {
		return fmt.Errorf("enqueue events: %w", err)
	}
	return nil
}

// persist saves p guarded by loadedVersion and enqueues events.
func (w Writer) persist(ctx context.Context, p *slotplan.SlotPlan, loadedVersion int64, events []slotplan.Event) error {
	if err := w.Plans.Save(ctx, p, loadedVersion); err != nil {
		return err
	}
	return w.enqueue(ctx, events)
}

// resolveSite validates a request's site id, falling back to def.
func resolveSite(raw, def string) (slotplan.SiteID, error) {
	if raw == "" {
		raw = def
	}
	if raw == "" {
		return "", ErrNoSite
	}
	return slotplan.NewSiteID(raw)
}

// lookbackDays validates a window length in days; 0 means the default.
func lookbackDays(days int) (int, error) {
	if days == 0 {
		return DefaultLookbackDays, nil
	}
	if days < 1 || days > MaxLookbackDays {
		return 0, ErrInvalidLookback
	}
	return days, nil
}

// isNotFound reports whether err is the repository's plan-not-found error.
func isNotFound(err error) bool { return errors.Is(err, repository.ErrPlanNotFound) }
