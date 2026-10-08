// Package kafkatest boots ONE real Kafka broker through testcontainers per
// test binary. It never reads KAFKA_BROKERS, never hardcodes a broker address
// and never skips: a missing Docker is a test failure (fleet rule,
// TestKafkaIntegrationTestsUseTestcontainers).
package kafkatest

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	kafkago "github.com/segmentio/kafka-go"
	"github.com/testcontainers/testcontainers-go"
	tckafka "github.com/testcontainers/testcontainers-go/modules/kafka"
)

var (
	once      sync.Once
	container *tckafka.KafkaContainer
	brokers   []string
	startErr  error
	seq       atomic.Int64
)

// Brokers starts the shared broker on first use and returns its addresses.
func Brokers(t *testing.T) []string {
	t.Helper()
	once.Do(func() {
		ctx := context.Background()
		container, startErr = tckafka.Run(ctx, "confluentinc/confluent-local:7.6.1", tckafka.WithClusterID("slotting-optimization-itest"))
		if startErr != nil {
			return
		}
		brokers, startErr = container.Brokers(ctx)
	})
	if startErr != nil {
		t.Fatalf("start kafka container: %v", startErr)
	}
	return brokers
}

// Topic creates a unique one-partition topic named "<prefix>.itest-<n>" and
// waits until it has a leader (CreateTopics returns before the broker is
// done), then returns its name.
func Topic(t *testing.T, prefix string) string {
	t.Helper()
	topic := fmt.Sprintf("%s.itest-%d-%d", prefix, time.Now().UnixNano(), seq.Add(1))
	CreateTopic(t, topic)
	return topic
}

// CreateTopic creates a one-partition topic and waits for its leader.
func CreateTopic(t *testing.T, topic string) {
	t.Helper()
	conn, err := kafkago.Dial("tcp", Brokers(t)[0])
	if err != nil {
		t.Fatalf("dial broker: %v", err)
	}
	defer func() { _ = conn.Close() }()
	if err := conn.CreateTopics(kafkago.TopicConfig{Topic: topic, NumPartitions: 1, ReplicationFactor: 1}); err != nil {
		t.Fatalf("create topic %s: %v", topic, err)
	}
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if parts, err := conn.ReadPartitions(topic); err == nil && len(parts) == 1 && parts[0].Leader.ID != 0 {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("topic %s never got a leader", topic)
}

// Shutdown terminates the shared broker; call it from TestMain.
func Shutdown() {
	if container != nil {
		_ = testcontainers.TerminateContainer(container)
	}
}
