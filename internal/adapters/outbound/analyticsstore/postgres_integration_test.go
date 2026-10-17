//go:build integration

package analyticsstore

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-migrate/migrate/v4"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/claudioed/slotting-optimization/analytics"
	"github.com/claudioed/slotting-optimization/internal/analytics/report"
)

// ONE analytical Postgres per test binary (fleet rule: one container per
// package), its template migrated once; every test gets a private database
// cloned from that template, so tests never see each other's rows. It never
// reads an env DSN and never skips: a missing Docker is a failure.
const (
	analyticsUser     = "analytics_test"
	analyticsPassword = "analytics_test"
	analyticsAdminDB  = "postgres"
	analyticsTemplate = "slotting_analytics_template"
)

var (
	analyticsOnce      sync.Once
	analyticsContainer *tcpostgres.PostgresContainer
	analyticsStartErr  error
	analyticsSeq       atomic.Int64
)

func startAnalyticsContainer() {
	ctx := context.Background()
	analyticsContainer, analyticsStartErr = tcpostgres.Run(ctx, "postgres:16-alpine",
		tcpostgres.WithDatabase(analyticsAdminDB),
		tcpostgres.WithUsername(analyticsUser),
		tcpostgres.WithPassword(analyticsPassword),
		testcontainers.WithWaitStrategy(wait.ForLog("database system is ready to accept connections").WithOccurrence(2).WithStartupTimeout(90*time.Second)),
	)
	if analyticsStartErr != nil {
		return
	}
	if analyticsStartErr = analyticsAdminExec(ctx, fmt.Sprintf("CREATE DATABASE %s", analyticsTemplate)); analyticsStartErr != nil {
		return
	}
	url, err := analyticsDatabaseURL(ctx, analyticsTemplate)
	if err != nil {
		analyticsStartErr = err
		return
	}
	// Twice: boot runs the migrations on every start, so the second run must
	// be a no-op (ErrNoChange).
	for range 2 {
		if analyticsStartErr = RunMigrations(url); analyticsStartErr != nil {
			return
		}
	}
}

// analyticsAdminExec runs sql against the container's admin database.
func analyticsAdminExec(ctx context.Context, sql string) error {
	url, err := analyticsContainer.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		return err
	}
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close(ctx) }()
	_, err = conn.Exec(ctx, sql)
	return err
}

func analyticsDatabaseURL(ctx context.Context, db string) (string, error) {
	base, err := analyticsContainer.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		return "", err
	}
	cfg, err := pgx.ParseConfig(base)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("postgres://%s:%s@%s:%d/%s?sslmode=disable", analyticsUser, analyticsPassword, cfg.Host, cfg.Port, db), nil
}

// newAnalyticsPool returns a WRITER pool over a fresh private analytics
// database (migrated through the template clone), its URL and its name.
func newAnalyticsPool(t *testing.T) (*pgxpool.Pool, string, string) {
	t.Helper()
	analyticsOnce.Do(startAnalyticsContainer)
	if analyticsStartErr != nil {
		t.Fatalf("start analytics postgres: %v", analyticsStartErr)
	}
	ctx := context.Background()
	db := fmt.Sprintf("analytics_itest_%d", analyticsSeq.Add(1))
	if err := analyticsAdminExec(ctx, fmt.Sprintf("CREATE DATABASE %s TEMPLATE %s", db, analyticsTemplate)); err != nil {
		t.Fatalf("clone analytics database: %v", err)
	}
	url, err := analyticsDatabaseURL(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	pool, err := NewPool(ctx, url)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool, url, db
}

// pgStore is the Projection and the Reader over one database.
type pgStore struct {
	*Projection
	*Reader
}

func TestPostgres_Contract(t *testing.T) {
	runContract(t, func(t *testing.T) store {
		pool, _, _ := newAnalyticsPool(t)
		return pgStore{NewProjection(pool), NewReader(pool)}
	})
}

// TestPostgres_MigrationsUpDownAreRepeatable: the embedded analytical
// migrations apply up (idempotently), drop cleanly down to nothing, and
// re-apply — the read model can be dropped and rebuilt from the topic
// (ADR 0006 section 7) only if DOWN is real.
func TestPostgres_MigrationsUpDownAreRepeatable(t *testing.T) {
	analyticsOnce.Do(startAnalyticsContainer)
	if analyticsStartErr != nil {
		t.Fatalf("start analytics postgres: %v", analyticsStartErr)
	}
	ctx := context.Background()
	db := fmt.Sprintf("analytics_migrate_%d", analyticsSeq.Add(1))
	if err := analyticsAdminExec(ctx, fmt.Sprintf("CREATE DATABASE %s TEMPLATE %s", db, analyticsTemplate)); err != nil {
		t.Fatalf("clone database: %v", err)
	}
	url, err := analyticsDatabaseURL(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	src, err := iofs.New(analytics.Migrations, "migrations")
	if err != nil {
		t.Fatal(err)
	}
	m, err := migrate.NewWithSourceInstance("iofs", src, url)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = m.Close() }()
	// Up is a no-op on the already-migrated clone.
	if err := m.Up(); err != nil && err != migrate.ErrNoChange {
		t.Fatalf("up on migrated db: %v", err)
	}
	// Fully down: every table gone.
	if err := m.Down(); err != nil {
		t.Fatalf("down: %v", err)
	}
	if _, err := mustConn(t, ctx, url).Exec(ctx, `SELECT 1 FROM analytics_processed_events`); err == nil {
		t.Fatal("analytics_processed_events survived the down migration")
	}
	if _, err := mustConn(t, ctx, url).Exec(ctx, `SELECT 1 FROM slot_plan_facts`); err == nil {
		t.Fatal("slot_plan_facts survived the down migration")
	}
	// And up again: the cycle is repeatable.
	if err := m.Up(); err != nil {
		t.Fatalf("re-up: %v", err)
	}
	pool, err := NewPool(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if _, err := NewProjection(pool).Apply(ctx, genEv("e1", "plan-1", "SITE-1", "2026-10-05T10:00:00Z")); err != nil {
		t.Fatalf("apply after the up/down/up cycle: %v", err)
	}
}

func mustConn(t *testing.T, ctx context.Context, url string) *pgx.Conn {
	t.Helper()
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close(ctx) })
	return conn
}

// TestPostgres_DedupeMarkAndEffectAreAtomic: a projection failure after the
// claim (a trigger raising on slot_plan_facts) leaves neither the mark nor
// the effect, and the error is transient (not ErrRejected) for a non-22/23
// code; the redelivered event is then applied.
func TestPostgres_DedupeMarkAndEffectAreAtomic(t *testing.T) {
	ctx := context.Background()
	pool, _, _ := newAnalyticsPool(t)
	if _, err := pool.Exec(ctx, `
		CREATE FUNCTION boom() RETURNS trigger AS $$ BEGIN RAISE EXCEPTION 'boom' USING ERRCODE = '57P01'; END $$ LANGUAGE plpgsql;
		CREATE TRIGGER boom BEFORE INSERT ON slot_plan_facts FOR EACH ROW EXECUTE FUNCTION boom();`); err != nil {
		t.Fatal(err)
	}
	p := NewProjection(pool)
	e := genEv("e1", "plan-1", "SITE-1", "2026-10-05T10:00:00Z")
	if _, err := p.Apply(ctx, e); err == nil {
		t.Fatal("the triggered failure did not surface")
	}
	var marks int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM analytics_processed_events`).Scan(&marks); err != nil || marks != 0 {
		t.Fatalf("marks = %d, %v; the mark must roll back with the effect", marks, err)
	}
	if _, err := pool.Exec(ctx, `DROP TRIGGER boom ON slot_plan_facts`); err != nil {
		t.Fatal(err)
	}
	if applied, err := p.Apply(ctx, e); err != nil || !applied {
		t.Fatalf("redelivery after the failure = %v, %v; want applied", applied, err)
	}
}

// TestPostgres_IntegrityViolationIsRejected: the SQL store wraps SQLSTATE
// class 23 in report.ErrRejected (deterministic: dead-letter, do not retry).
func TestPostgres_IntegrityViolationIsRejected(t *testing.T) {
	ctx := context.Background()
	pool, _, _ := newAnalyticsPool(t)
	if _, err := pool.Exec(ctx, `CREATE FUNCTION nullsite() RETURNS trigger AS $$ BEGIN NEW.site_id := NULL; RETURN NEW; END $$ LANGUAGE plpgsql;
		CREATE TRIGGER nullsite BEFORE INSERT ON slot_plan_facts FOR EACH ROW EXECUTE FUNCTION nullsite();`); err != nil {
		t.Fatal(err)
	}
	_, err := NewProjection(pool).Apply(ctx, genEv("e1", "plan-1", "SITE-1", "2026-10-05T10:00:00Z"))
	if err == nil {
		t.Fatal("the integrity violation did not surface")
	}
	if !errors.Is(err, report.ErrRejected) {
		t.Fatalf("err = %v, want ErrRejected", err)
	}
}

// TestPostgres_ReadOnlyPoolCannotWrite: the reports pool refuses writes even
// on a read-write role.
func TestPostgres_ReadOnlyPoolCannotWrite(t *testing.T) {
	ctx := context.Background()
	pool, url, _ := newAnalyticsPool(t)
	if _, err := NewProjection(pool).Apply(ctx, genEv("e1", "plan-1", "SITE-1", "2026-10-05T10:00:00Z")); err != nil {
		t.Fatal(err)
	}
	ro, err := NewReadOnlyPool(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Close()
	if _, err := NewProjection(ro).Apply(ctx, genEv("e2", "plan-2", "SITE-1", "2026-10-05T11:00:00Z")); err == nil {
		t.Fatal("a write through the read-only pool succeeded")
	}
	if _, err := NewReader(ro).FunnelDays(ctx, report.FunnelQuery{
		Range: report.Range{From: at("2026-10-05T00:00:00Z"), To: at("2026-10-06T00:00:00Z")}, Granularity: report.GranularityDay,
	}); err != nil {
		t.Fatalf("a read through the read-only pool failed: %v", err)
	}
}

// TestPostgres_DayWindowsMatchTheGoDefinition: the SQL day series equals
// report.DayWindows for awkward ranges (partial days, a session time zone
// with DST, a single instant-wide window).
func TestPostgres_DayWindowsMatchTheGoDefinition(t *testing.T) {
	ctx := context.Background()
	pool, _, db := newAnalyticsPool(t)
	if _, err := pool.Exec(ctx, fmt.Sprintf(`ALTER DATABASE %s SET timezone TO 'America/Sao_Paulo'`, db)); err != nil {
		t.Fatal(err)
	}
	pool.Reset() // new connections pick up the time zone
	r := NewReader(pool)
	for _, rg := range []report.Range{
		{From: at("2026-10-05T10:00:00Z"), To: at("2026-10-07T06:30:00Z")},
		{From: at("2026-10-05T00:00:00Z"), To: at("2026-10-06T00:00:00Z")},
		{From: at("2026-02-27T13:00:00Z"), To: at("2026-03-02T00:00:01Z")},
		{From: at("2026-10-05T10:00:00Z"), To: at("2026-10-05T10:00:01Z")},
	} {
		got, err := r.FunnelDays(ctx, report.FunnelQuery{Range: rg, Granularity: report.GranularityDay})
		if err != nil {
			t.Fatal(err)
		}
		want := report.DayWindows(rg)
		if len(got) != len(want) {
			t.Fatalf("%v: %d SQL days, %d Go windows", rg, len(got), len(want))
		}
		for i := range got {
			if !got[i].Day.Equal(want[i].Day) {
				t.Fatalf("%v: day %d = %v, want %v", rg, i, got[i].Day, want[i].Day)
			}
		}
	}
}
