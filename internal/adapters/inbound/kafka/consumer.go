package kafka

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	ce "github.com/cloudevents/sdk-go/v2/event"
	kafkago "github.com/segmentio/kafka-go"

	"github.com/claudioed/slotting-optimization/internal/adapters/kafka/cloudevents"
	"github.com/claudioed/slotting-optimization/internal/application/usecases"
)

// Consumed topics: the producers' integration topics (their contracts are
// restated in apis/asyncapi.yaml, never imported).
const (
	DemandTopic  = "warehouse.order-management.events"
	ProductTopic = "warehouse.product-master.events"
	LayoutTopic  = "warehouse.facility.events"
)

// errBadPayload marks a CloudEvent whose `data` cannot be decoded: a
// deterministic problem, never retried.
var errBadPayload = errors.New("malformed event payload")

// HandlerFunc applies one decoded CloudEvent. nil means done (or deterministic
// and skippable); a non-nil error is transient unless it wraps errBadPayload
// or usecases.ErrInvalidEvent.
type HandlerFunc func(ctx context.Context, e ce.Event) error

// Consumer is one local-copy consumer: it reads Topic under a stable consumer
// group and dispatches each message on the FULL CloudEvents `type` to its
// handler. Types it has no handler for are ignored.
type Consumer struct {
	Name     string
	Topic    string
	Reader   Reader
	Handlers map[string]HandlerFunc
	Logger   *slog.Logger
	Retry    RetryPolicy

	// DLQ is the dead-letter writer (topic Topic + DLQSuffix): a transient
	// failure is retried domainMaxHandlerAttempts times before the message is
	// published there instead of blocking the partition forever.
	DLQ DeadLetterWriter

	sleep sleepFunc // test hook; nil => real, ctx-cancellable sleep
}

// newConsumer builds a Consumer reading topic from brokers under groupID
// (from an environment variable at the composition root, never a literal).
// Constructing the reader and the writer does not dial.
func newConsumer(name string, brokers []string, topic, groupID string, handlers map[string]HandlerFunc, logger *slog.Logger) *Consumer {
	return &Consumer{
		Name:     name,
		Topic:    topic,
		Reader:   kafkago.NewReader(readerConfig(brokers, topic, groupID)),
		Handlers: handlers,
		Logger:   defaultLogger(logger),
		DLQ:      newDomainDLQWriter(brokers, topic),
	}
}

// Run consumes until ctx is cancelled or the reader fails. A message's offset
// is committed only after HandleMessage returned nil; a transient failure
// retries the SAME message with capped exponential backoff, up to
// domainMaxHandlerAttempts, then dead-letters it.
func (c *Consumer) Run(ctx context.Context) error {
	loop := consumeLoop{
		reader: c.Reader,
		handle: func(ctx context.Context, msg kafkago.Message) error { return c.HandleMessage(ctx, msg.Value) },
		logger: defaultLogger(c.Logger),
		name:   c.Name,
		retry:  c.Retry,
		sleep:  c.sleep,
	}
	if c.DLQ != nil {
		loop.maxAttempts = domainMaxHandlerAttempts
		loop.deadLetter = func(ctx context.Context, msg kafkago.Message, cause error) error {
			return publishDeadLetter(ctx, c.DLQ, c.Topic+DLQSuffix, msg, cause)
		}
	}
	return loop.run(ctx)
}

// Close releases the reader and the dead-letter writer.
func (c *Consumer) Close() error {
	err := c.Reader.Close()
	if c.DLQ != nil {
		err = errors.Join(err, c.DLQ.Close())
	}
	return err
}

// HandleMessage decodes one CloudEvents 1.0 message and dispatches it. It
// returns nil for everything deterministic (logged at WARN: not a valid
// CloudEvent, a malformed payload, values the use case refuses; silently: a
// type this consumer does not act on) and a non-nil error ONLY for a
// transient failure, after the unit of work rolled back.
func (c *Consumer) HandleMessage(ctx context.Context, value []byte) error {
	logger := defaultLogger(c.Logger)
	e, err := cloudevents.Decode(value)
	if err != nil {
		logger.WarnContext(ctx, "skipping a message that is not a valid CloudEvent", "consumer", c.Name, "topic", c.Topic, "error", err)
		return nil
	}
	handle, ok := c.Handlers[e.Type()]
	if !ok {
		return nil
	}
	err = handle(ctx, e)
	if errors.Is(err, errBadPayload) || errors.Is(err, usecases.ErrInvalidEvent) {
		logger.WarnContext(ctx, "skipping an event that can never be applied", "consumer", c.Name, "type", e.Type(), "event_id", e.ID(), "error", err)
		return nil
	}
	return err
}

// decodeData decodes e's JSON `data` into a T.
func decodeData[T any](e ce.Event) (T, error) {
	var v T
	if err := e.DataAs(&v); err != nil {
		return v, fmt.Errorf("%w: %v", errBadPayload, err)
	}
	return v, nil
}

// logOutcome records what a consumer use case did.
func logOutcome(ctx context.Context, logger *slog.Logger, e ce.Event, outcome usecases.Outcome) {
	logger.InfoContext(ctx, "event processed", "type", e.Type(), "event_id", e.ID(), "subject", e.Subject(), "outcome", string(outcome))
}
