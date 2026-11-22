package http

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

// spyExec records the purge statement and answers with a scripted row count.
type spyExec struct {
	sql      string
	args     []any
	affected int64
	err      error
	calls    int
}

func (s *spyExec) Exec(_ context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	s.calls++
	s.sql, s.args = sql, args
	return pgconn.NewCommandTag("DELETE " + strconv.FormatInt(s.affected, 10)), s.err
}

// The purge uses the SAME window as the read path: one constant, one interval argument.
func TestThePurgeUsesTheTTLOfTheReadPath(t *testing.T) {
	db := &spyExec{affected: 3}
	n, err := PurgeExpiredIdempotencyKeys(context.Background(), db, 50)
	if n != 3 || err != nil {
		t.Fatalf("purge = %d, %v, want the rows the server deleted", n, err)
	}
	if len(db.args) != 2 || db.args[0] != IdempotencyTTL.String() || db.args[1] != 50 {
		t.Fatalf("args = %v, want the TTL %q and the batch limit 50", db.args, IdempotencyTTL.String())
	}
	for _, want := range []string{
		"DELETE FROM idempotency_keys",
		"created_at <= now() - $1::interval",
		"ORDER BY created_at",
		"LIMIT $2",
		"FOR UPDATE SKIP LOCKED",
	} {
		if !strings.Contains(db.sql, want) {
			t.Errorf("purge SQL lacks %q:\n%s", want, db.sql)
		}
	}
}

// What the purge deletes is exactly what the read path ignores: replay reads
// created_at > now() - ttl, the claim and the purge delete created_at <=
// now() - ttl — nothing in between, all on IdempotencyTTL.
func TestThePurgeWindowIsTheExactComplementOfTheReadPath(t *testing.T) {
	if !strings.Contains(replayStoredKeysSQL, "created_at > now() - $2::interval") ||
		strings.Count(purgeExpiredKeysSQL, "created_at <= now() - $1::interval") != 2 ||
		!strings.Contains(claimExpiredKeySQL, "created_at <= now() - make_interval(secs => $2)") {
		t.Fatalf("claim: %s\nreplay: %s\npurge: %s", claimExpiredKeySQL, replayStoredKeysSQL, purgeExpiredKeysSQL)
	}
}

func TestThePurgeWithNoBatchDoesNothing(t *testing.T) {
	for _, limit := range []int{0, -1} {
		db := &spyExec{affected: 99}
		n, err := PurgeExpiredIdempotencyKeys(context.Background(), db, limit)
		if n != 0 || err != nil || db.calls != 0 {
			t.Fatalf("limit %d: purge = %d, %v, calls = %d, want nothing run", limit, n, err, db.calls)
		}
	}
}

func TestThePurgePropagatesTheServerError(t *testing.T) {
	boom := errors.New("connection refused")
	if _, err := PurgeExpiredIdempotencyKeys(context.Background(), &spyExec{err: boom}, 10); !errors.Is(err, boom) {
		t.Fatalf("purge error = %v, want the server error", err)
	}
}
