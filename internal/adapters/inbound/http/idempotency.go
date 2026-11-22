package http

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// IdempotencyKeyHeader is the request header a caller must supply on a route
// wrapped by RequireIdempotencyKey.
const IdempotencyKeyHeader = "Idempotency-Key"

// IdempotencyReplayedHeader is set to "true" on a response replayed from the
// stored answer of an earlier request with the same key, and only then.
const IdempotencyReplayedHeader = "Idempotency-Replayed"

// maxIdempotencyKeyLen is the longest accepted Idempotency-Key.
const maxIdempotencyKeyLen = 128

// IdempotencyTTL is how long a stored answer lives: the ONE constant the
// claim (claimExpiredKeySQL below), the replay read (replayStoredKeysSQL)
// and the retention purge (idempotency_retention.go) all use, so they can
// never disagree about which rows are live. A key older than this is
// forgotten: the request under it executes again (ADR 0016, addendum
// 2026-11-22).
const IdempotencyTTL = 24 * time.Hour

// claimExpiredKeySQL forgets an expired key inside the request's OWN
// transaction, before the INSERT: created_at <= now() - ttl is the exact
// complement of the replay read's created_at > now() - ttl and matches the
// purge's predicate, all on the same IdempotencyTTL. The DELETE takes the
// row lock, so of the requests racing on an expired key exactly one claims
// it and re-executes; the losers block on the claimer's INSERT, then replay
// the fresh answer.
const claimExpiredKeySQL = `DELETE FROM idempotency_keys
WHERE key = $1 AND created_at <= now() - make_interval(secs => $2)`

// replayStoredKeysSQL reads the stored answer of a key. The age guard
// (created_at > now() - ttl) is the exact complement of the claim and the
// purge: a row past the TTL can never answer, not even in the window
// between its expiry and the sweep.
const replayStoredKeysSQL = `SELECT request_hash, status_code, response_body, response_headers
FROM idempotency_keys
WHERE key = $1 AND created_at > now() - $2::interval`

// validIdempotencyKey reports whether key is 1-128 printable ASCII characters.
func validIdempotencyKey(key string) bool {
	if len(key) == 0 || len(key) > maxIdempotencyKeyLen {
		return false
	}
	for i := 0; i < len(key); i++ {
		if key[i] < 0x20 || key[i] > 0x7e {
			return false
		}
	}
	return true
}

// TxBinder returns a child context carrying tx, so the use case's own unit of
// work (the Postgres adapter's) JOINS the transaction the middleware opened
// instead of beginning a second one. cmd/ passes the outbound adapter's
// binder in: adapters never import each other.
type TxBinder func(ctx context.Context, tx pgx.Tx) context.Context

// RequireIdempotencyKey is the fleet's transactional Idempotency-Key
// middleware (the reference implementation is order-management's), applied
// route by route to every POST and PATCH of the service.
//
// The correctness argument in short:
//
//   - idempotency_keys.status_code starts NULL and is set by the SAME
//     transaction that inserted the row, immediately before it commits. A
//     committed row therefore always carries its outcome: there is no "in
//     progress" state, no timeout and no retry-later answer.
//   - Two concurrent requests with the same key race on INSERT ... ON CONFLICT
//     DO NOTHING. Postgres serialises them on the primary-key unique index:
//     the second statement blocks until the first transaction resolves, so a
//     reader that sees "0 rows inserted" can rely on the original transaction
//     being finished. If it rolled back, the blocked insert succeeds instead.
//   - The wrapped handler runs with the transaction bound into its context
//     (TxBinder), so the idempotency row, the plan and its outbox rows commit
//     or roll back together.
//   - A 2xx is cached and so is a refusal that is final for the request: the
//     same key and body replays the same answer, a different method, path or
//     body is 409 idempotency-key-reused. Which refusal is final is the replay
//     class its problem type declares (FINAL and OUTCOME are cached; RETRYABLE
//     is released; ADR 0016). A 5xx, a panic, a RETRYABLE refusal and a refusal
//     with no class roll everything back, key row included, and are never
//     cached, so the same key can retry the real work. The key is global: it
//     is looked up alone.
//   - A stored answer lives for IdempotencyTTL (24h). An expired row is
//     claimed (deleted) by the next request under the key, which then
//     executes the work again, and is never answered by the replay read;
//     the background purge deletes what is left (ADR 0016, addendum
//     2026-11-22).
func RequireIdempotencyKey(pool *pgxpool.Pool, bind TxBinder) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			key := r.Header.Get(IdempotencyKeyHeader)
			if key == "" {
				writeProblem(w, r, idempotencyKeyRequired,
					"send an Idempotency-Key header with a unique value per logical request")
				return
			}
			if !validIdempotencyKey(key) {
				writeProblem(w, r, idempotencyKeyInvalid,
					"the Idempotency-Key must be 1 to 128 printable ASCII characters")
				return
			}
			body, err := io.ReadAll(io.LimitReader(r.Body, maxBodyBytes))
			if err != nil {
				writeError(w, r, errMalformedRequest)
				return
			}
			_ = r.Body.Close()
			r.Body = io.NopCloser(bytes.NewReader(body))
			sum := sha256.Sum256(append([]byte(r.Method+" "+r.URL.Path+"\n"), body...))
			requestHash := hex.EncodeToString(sum[:])

			ctx := r.Context()
			tx, err := pool.Begin(ctx)
			if err != nil {
				writeError(w, r, err)
				return
			}
			inserted, err := claimKey(ctx, tx, key, r.Method, r.URL.Path, requestHash)
			if err != nil {
				_ = tx.Rollback(ctx)
				writeError(w, r, err)
				return
			}
			if !inserted {
				_ = tx.Rollback(ctx)
				replayCachedResponse(w, r, pool, key, requestHash)
				return
			}
			runFreshRequest(ctx, w, r, tx, bind, key, next)
		})
	}
}

// claimKey runs the request-time claim inside the middleware's transaction:
// an expired row for the key is forgotten first (the claim, ADR 0016 addendum
// 2026-11-22), then the key's own row is inserted. inserted is false when a
// LIVE row won the race -- the caller replays that row's stored answer; the
// row lock the DELETE and INSERT take makes exactly one racing request the
// claimer, on an expired key as on a fresh one.
func claimKey(ctx context.Context, tx pgx.Tx, key, method, path, requestHash string) (bool, error) {
	if _, err := tx.Exec(ctx, claimExpiredKeySQL, key, IdempotencyTTL.Seconds()); err != nil {
		return false, err
	}
	tag, err := tx.Exec(ctx, `
		INSERT INTO idempotency_keys (key, method, path, request_hash)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (key) DO NOTHING
	`, key, method, path, requestHash)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// replayCachedResponse answers a request whose key already has a committed
// row: the stored outcome when the fingerprint (method + path + body hash,
// folded into requestHash) matches, 409 idempotency-key-reused when it does
// not. A row past IdempotencyTTL never answers (the age guard).
func replayCachedResponse(w http.ResponseWriter, r *http.Request, pool *pgxpool.Pool, key, requestHash string) {
	var (
		storedHash string
		statusCode *int
		body       []byte
		headersRaw []byte
	)
	err := pool.QueryRow(r.Context(), replayStoredKeysSQL, key, IdempotencyTTL.String()).
		Scan(&storedHash, &statusCode, &body, &headersRaw)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// The row expired in the microscopic window between the
			// conflicting INSERT and this read. It must not answer: fail the
			// request (a 500 is never stored) -- the client's retry claims
			// the expired row and executes the work again.
			writeError(w, r, errIdempotencyRowExpired)
			return
		}
		writeError(w, r, err)
		return
	}
	if storedHash != requestHash {
		writeProblem(w, r, idempotencyKeyReused, "the Idempotency-Key was used with a different request")
		return
	}
	if statusCode == nil {
		// Unreachable (a committed row always has its outcome); a defensive
		// 500 rather than a fabricated response.
		writeError(w, r, errMissingOutcome)
		return
	}
	copyStoredHeaders(w.Header(), headersRaw)
	w.Header().Set(IdempotencyReplayedHeader, "true")
	w.WriteHeader(*statusCode)
	_, _ = w.Write(body)
}

var errMissingOutcome = idempotencyError("idempotency row committed with no recorded outcome")

// errIdempotencyRowExpired guards the microscopic window in which a row
// expires between the conflicting INSERT and the replay read: the row must
// not answer, and the failed request (never stored) makes the client's retry
// claim the expired key and execute again.
var errIdempotencyRowExpired = idempotencyError("the idempotency row expired during the replay lookup; a retry executes the work again")

type idempotencyError string

func (e idempotencyError) Error() string { return string(e) }

func copyStoredHeaders(dst http.Header, raw []byte) {
	if len(raw) == 0 {
		return
	}
	var stored http.Header
	if err := json.Unmarshal(raw, &stored); err != nil {
		return
	}
	for k, vs := range stored {
		for _, v := range vs {
			dst.Add(k, v)
		}
	}
}

// capture records the handler's response so it can be persisted and
// committed BEFORE anything reaches the client.
type capture struct {
	header http.Header
	code   int
	body   bytes.Buffer
	// class is the replay class of the problem the handler answered with
	// (replay_class.go); "" for a success and for an answer that declared none.
	class replayClass
}

func (c *capture) Header() http.Header { return c.header }

func (c *capture) WriteHeader(code int) {
	if c.code == 0 {
		c.code = code
	}
}

func (c *capture) Write(b []byte) (int, error) {
	if c.code == 0 {
		c.code = http.StatusOK
	}
	return c.body.Write(b)
}

// runFreshRequest handles a genuinely new key: capture the response, persist
// the outcome in the same transaction, commit, and only then write to the
// real client.
//
// The handler runs behind a SAVEPOINT. A mapped domain conflict -- a racing
// approval losing on uq_slot_plans_one_approved_per_site, say -- fails a
// statement on the server and leaves the transaction ABORTED even though the
// handler answers a truthful 4xx. Releasing the savepoint exposes that: the
// release fails, ROLLBACK TO SAVEPOINT discards the half-done work and heals
// the transaction, and a FINAL 4xx outcome is recorded and committed like any
// other (the key row inserted before the savepoint survives). A 2xx answer
// behind a failed release is a lie (its writes are gone), and so is an
// OUTCOME refusal whose writes are gone: both roll everything back and fail
// the request instead. A RETRYABLE or unclassified refusal never reaches the
// release: it is rolled back whole, key row included (ADR 0016).
func runFreshRequest(ctx context.Context, w http.ResponseWriter, r *http.Request, tx pgx.Tx, bind TxBinder, key string, next http.Handler) {
	defer func() {
		if p := recover(); p != nil {
			_ = tx.Rollback(ctx)
			panic(p)
		}
	}()
	if _, err := tx.Exec(ctx, "SAVEPOINT handler_run"); err != nil {
		_ = tx.Rollback(ctx)
		writeError(w, r, err)
		return
	}
	rec := &capture{header: http.Header{}}
	next.ServeHTTP(rec, r.WithContext(bind(ctx, tx)))
	if rec.code == 0 {
		rec.code = http.StatusOK
	}
	if !rec.storable() {
		// An answer that is not final for the request is released: roll
		// everything back, key row included, so the same key executes again.
		// That is a 5xx (never cached), a refusal whose problem type declares
		// RETRYABLE and a refusal that declares no class (replay_class.go).
		_ = tx.Rollback(ctx)
		flush(w, rec)
		return
	}
	if _, err := tx.Exec(ctx, "RELEASE SAVEPOINT handler_run"); err != nil {
		// A statement inside the handler failed on the server: the
		// transaction is aborted. Rolling back to the savepoint discards the
		// handler's partial writes -- the correct all-or-nothing semantics
		// for an error answer -- and returns the transaction to health.
		if _, rerr := tx.Exec(ctx, "ROLLBACK TO SAVEPOINT handler_run"); rerr != nil {
			_ = tx.Rollback(ctx)
			writeError(w, r, err)
			return
		}
		if rec.code < http.StatusBadRequest || rec.class == classOutcome {
			// A success answer whose writes cannot be committed must neither
			// be returned nor cached, and neither must an OUTCOME refusal,
			// whose whole point is that its writes commit with it: fail the
			// request so the key retries.
			_ = tx.Rollback(ctx)
			writeError(w, r, err)
			return
		}
	}
	headers, err := json.Marshal(rec.header)
	if err != nil {
		_ = tx.Rollback(ctx)
		writeError(w, r, err)
		return
	}
	if _, err := tx.Exec(ctx, `
		UPDATE idempotency_keys
		SET status_code = $1, response_body = $2, response_headers = $3, completed_at = now()
		WHERE key = $4
	`, rec.code, rec.body.Bytes(), headers, key); err != nil {
		_ = tx.Rollback(ctx)
		writeError(w, r, err)
		return
	}
	if err := tx.Commit(ctx); err != nil {
		writeError(w, r, err)
		return
	}
	flush(w, rec)
}

// flush copies the captured response onto the real client connection.
func flush(w http.ResponseWriter, rec *capture) {
	for k, vs := range rec.header {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(rec.code)
	_, _ = w.Write(rec.body.Bytes())
}
