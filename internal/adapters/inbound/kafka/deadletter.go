// Dead-letter handling for the domain consumers: a TRANSIENT failure is
// retried a bounded number of times with capped backoff (consumeLoop's
// policy), and only once that bound is exhausted is the message published to
// "<topic>.dlq" with x-dlq-* headers, instead of blocking the partition
// forever. A deterministic problem (not a CloudEvents 1.0 message, unknown
// type, malformed payload, invalid values) never reaches this path: the
// consumer's HandleMessage returns nil for those and the loop commits past
// them.
package kafka

import (
	"context"
	"errors"
	"strconv"
	"time"

	kafkago "github.com/segmentio/kafka-go"
)

// DLQSuffix is appended to a consumed topic to name its dead-letter topic
// (warehouse.order-management.events.dlq for the demand consumer).
const DLQSuffix = ".dlq"

// DeadLetterWriter is the subset of *kafkago.Writer a dead-lettering
// consumer needs, so unit tests never need a broker.
type DeadLetterWriter interface {
	WriteMessages(ctx context.Context, msgs ...kafkago.Message) error
	Close() error
}

// Bounded retry while an auto-created DLQ topic has no leader yet.
const (
	dlqTopicReadyAttempts = 40
	dlqTopicReadyBackoff  = 250 * time.Millisecond
)

// writeDLQ publishes msg, retrying (bounded) while the auto-created DLQ topic
// has no leader yet. Any other error, or exhausting the budget, is returned.
func writeDLQ(ctx context.Context, w DeadLetterWriter, msg kafkago.Message) error {
	var err error
	for attempt := 0; attempt < dlqTopicReadyAttempts; attempt++ {
		if err = w.WriteMessages(ctx, msg); err == nil || !isTopicNotReady(err) {
			return err
		}
		select {
		case <-ctx.Done():
			return errors.Join(err, ctx.Err())
		case <-time.After(dlqTopicReadyBackoff):
		}
	}
	return err
}

// isTopicNotReady reports whether err only says the topic (or its leader)
// does not exist yet: the transient state right after auto-creation.
func isTopicNotReady(err error) bool {
	var werrs kafkago.WriteErrors
	if errors.As(err, &werrs) {
		for _, e := range werrs {
			if e != nil && !isTopicNotReady(e) {
				return false
			}
		}
		return werrs.Count() > 0
	}
	return errors.Is(err, kafkago.UnknownTopicOrPartition) || errors.Is(err, kafkago.LeaderNotAvailable)
}

// domainMaxHandlerAttempts bounds a domain consumer's in-loop retry of a
// transient failure before the message is dead-lettered: 1 initial
// attempt plus up to 4 retries. Chosen (rather than the fleet's smaller
// maxHandlerAttempts=3 used by order-management/fulfillment-execution,
// which retry inside ONE handler call) to stay comfortably above this
// package's existing "fails twice then succeeds" test fixtures while
// still bounding the previously-infinite retry.
const domainMaxHandlerAttempts = 5

// newDomainDLQWriter builds the dead-letter writer for one of the three
// domain Kafka consumers, publishing to topic+DLQSuffix. It follows the
// SAME sync-writer settings as this service's outbox relay
// (outbound/kafka.RelaySink): a Hash balancer on the message key (so one
// key's dead-lettered messages land on one partition, preserving
// relative order for that key), RequireAll acks, AllowAutoTopicCreation
// (the DLQ topic has never been written to before the first poison
// message) and a short BatchTimeout so a lone synchronous write is not
// held for kafka-go's 1s default.
func newDomainDLQWriter(brokers []string, topic string) DeadLetterWriter {
	return &kafkago.Writer{
		Addr:                   kafkago.TCP(brokers...),
		Topic:                  topic + DLQSuffix,
		Balancer:               &kafkago.Hash{},
		RequiredAcks:           kafkago.RequireAll,
		BatchTimeout:           10 * time.Millisecond,
		AllowAutoTopicCreation: true,
	}
}

// publishDeadLetter publishes msg (its original key/value/headers, plus
// x-dlq-source-topic/x-dlq-error/x-dlq-failed-at context) to dlqTopic via
// w, retrying while the auto-created topic has no leader yet (writeDLQ,
// shared with AnalyticsConsumer). The source topic/partition/offset are
// taken from msg itself, so callers never need to pass them separately.
func publishDeadLetter(ctx context.Context, w DeadLetterWriter, dlqTopic string, msg kafkago.Message, cause error) error {
	headers := append([]kafkago.Header{}, msg.Headers...)
	headers = append(headers,
		kafkago.Header{Key: "x-dlq-source-topic", Value: []byte(msg.Topic)},
		kafkago.Header{Key: "x-dlq-source-partition", Value: []byte(strconv.Itoa(msg.Partition))},
		kafkago.Header{Key: "x-dlq-source-offset", Value: []byte(strconv.FormatInt(msg.Offset, 10))},
		kafkago.Header{Key: "x-dlq-error", Value: []byte(cause.Error())},
		kafkago.Header{Key: "x-dlq-failed-at", Value: []byte(time.Now().UTC().Format(time.RFC3339))},
	)
	return writeDLQ(ctx, w, kafkago.Message{Topic: dlqTopic, Key: msg.Key, Value: msg.Value, Headers: headers})
}
