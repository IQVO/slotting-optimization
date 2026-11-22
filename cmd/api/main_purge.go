package main

import (
	"context"
	"log/slog"
	"time"

	inboundhttp "github.com/claudioed/slotting-optimization/internal/adapters/inbound/http"
)

// idempotencyPurgeInterval is how often expired Idempotency-Key rows are
// purged (ADR 0016, addendum 2026-11-22). Expiry itself is enforced at read
// (the claim and the replay guard), so the interval only bounds how long a
// dead row stays on disk.
const idempotencyPurgeInterval = time.Hour

// purgeBatch deletes one batch of expired rows and returns how many it deleted.
type purgeBatch func(ctx context.Context, limit int) (int, error)

// startIdempotencyPurge runs the retention purge: at once, then every
// interval, until stopped. The purge is always on, like the relay; it ends
// only when the returned stop is called. A failed pass is logged and
// retried at the next tick — the purge only ever logs, it is never a death.
func startIdempotencyPurge(db inboundhttp.Execer, logger *slog.Logger) (*worker, func()) {
	ctx, stop := context.WithCancel(context.Background())
	w := &worker{name: "idempotency-purge", done: make(chan struct{})}
	purge := func(ctx context.Context, limit int) (int, error) {
		return inboundhttp.PurgeExpiredIdempotencyKeys(ctx, db, limit)
	}
	go func() {
		defer close(w.done)
		logger.Info("idempotency purge running",
			"interval", idempotencyPurgeInterval, "ttl", inboundhttp.IdempotencyTTL, "batch", inboundhttp.IdempotencyPurgeBatch)
		ticker := time.NewTicker(idempotencyPurgeInterval)
		defer ticker.Stop()
		for {
			purgeOnce(ctx, purge, logger)
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	return w, stop
}

// purgeOnce deletes expired rows batch by batch until a batch comes back
// short (nothing expired is left) or a pass fails; a failed pass is logged
// and retried at the next tick, the purge is idempotent.
func purgeOnce(ctx context.Context, purge purgeBatch, logger *slog.Logger) int {
	total := 0
	for ctx.Err() == nil {
		n, err := purge(ctx, inboundhttp.IdempotencyPurgeBatch)
		if err != nil {
			if ctx.Err() == nil {
				logger.ErrorContext(ctx, "idempotency purge failed", "error", err, "purged", total)
			}
			break
		}
		total += n
		if n < inboundhttp.IdempotencyPurgeBatch {
			break
		}
	}
	if total > 0 {
		logger.InfoContext(ctx, "idempotency purge", "purged", total)
	}
	return total
}
