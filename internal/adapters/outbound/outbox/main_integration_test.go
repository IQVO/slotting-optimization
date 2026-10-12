//go:build integration

package outbox_test

import (
	"os"
	"testing"

	"github.com/claudioed/slotting-optimization/internal/testing/kafkatest"
	"github.com/claudioed/slotting-optimization/internal/testing/pgtest"
)

func TestMain(m *testing.M) {
	code := m.Run()
	kafkatest.Shutdown()
	pgtest.Shutdown()
	os.Exit(code)
}
