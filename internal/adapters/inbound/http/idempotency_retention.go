package http

import (
	"context"

	"github.com/jackc/pgx/v5/pgconn"
)

// IdempotencyPurgeBatch is how many expired key rows one purge statement
// deletes at most: the purge never holds a long lock nor writes a long
// transaction, however far behind it is.
const IdempotencyPurgeBatch = 500

// Execer is the part of a pgx pool the retention purge needs.
type Execer interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// purgeExpiredKeysSQL deletes one batch of rows the read path already
// ignores: created_at <= now() - ttl is the exact complement of the replay
// read's created_at > now() - ttl and the same predicate the request-time
// claim uses, all on the one IdempotencyTTL argument. The rows are picked
// with FOR UPDATE SKIP LOCKED, so a row a request holds is skipped, never
// waited for: a request claiming an expired key runs its DELETE and INSERT
// in its own transaction and holds the row lock until it commits, and the
// predicate is re-checked on the DELETE itself. A purge that deletes an
// expired row just before a request claims it only makes the claim's DELETE
// a no-op and its INSERT a plain insert.
const purgeExpiredKeysSQL = `DELETE FROM idempotency_keys WHERE key IN (
	SELECT key FROM idempotency_keys WHERE created_at <= now() - $1::interval
	ORDER BY created_at LIMIT $2 FOR UPDATE SKIP LOCKED
) AND created_at <= now() - $1::interval`

// PurgeExpiredIdempotencyKeys deletes up to limit expired idempotency_keys
// rows (older than IdempotencyTTL) and returns how many it deleted. A
// result below limit means nothing expired is left. Expiry is already
// enforced at read (the claim deletes an expired row and the replay read
// ignores it); the purge only stops the table from growing (ADR 0016,
// addendum 2026-11-22).
func PurgeExpiredIdempotencyKeys(ctx context.Context, db Execer, limit int) (int, error) {
	if limit <= 0 {
		return 0, nil
	}
	tag, err := db.Exec(ctx, purgeExpiredKeysSQL, IdempotencyTTL.String(), limit)
	if err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), nil
}
