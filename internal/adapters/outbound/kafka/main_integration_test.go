//go:build integration

package kafka_test

import (
	"os"
	"testing"

	"github.com/claudioed/slotting-optimization/internal/testing/kafkatest"
)

// The broker comes from internal/testing/kafkatest: ONE shared real Kafka
// (testcontainers, confluent-local:7.6.1) per test binary, unique
// timestamp-suffixed topics per test — never an external broker address,
// never a skip gate. kafkatest starts the container lazily; TestMain only
// tears it down at the end.
func TestMain(m *testing.M) {
	code := m.Run()
	kafkatest.Shutdown()
	os.Exit(code)
}
