//go:build integration

package usecases_test

import (
	"os"
	"testing"

	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/claudioed/slotting-optimization/internal/testing/pgtest"
)

// The package's databases come from internal/testing/pgtest: ONE shared
// testcontainers Postgres per test binary, migrated once into a template,
// one private database per test (never an external DATABASE_URL, never
// t.Skip). pgtest itself starts the container lazily; TestMain only tears
// it down at the end. The tcpostgres reference keeps the testcontainers
// postgres module import reachable in this package's directory, which
// internal/architecture's TestPostgresIntegrationTestsUseTestcontainers
// requires of any integration test package that talks to pgx.
var _ = tcpostgres.Run

func TestMain(m *testing.M) {
	code := m.Run()
	pgtest.Shutdown()
	os.Exit(code)
}
