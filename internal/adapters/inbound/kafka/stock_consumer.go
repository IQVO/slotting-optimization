package kafka

import (
	"context"
	"log/slog"
	"time"

	ce "github.com/cloudevents/sdk-go/v2/event"

	"github.com/claudioed/slotting-optimization/internal/application/usecases"
)

// StockTopic is inventory-storage's integration topic; this service acts
// only on StockRelocated there (ADR 0010).
const StockTopic = "warehouse.inventory.events"

// TypeStockRelocated is inventory-storage's StockRelocated type,
// byte-identical to its AsyncAPI (inventory-storage ADR 0039). Every other
// type on the topic is ignored.
const TypeStockRelocated = "com.warehouse.wms.inventory-storage.stock.StockRelocated"

// stockRelocatedData mirrors inventory-storage's StockRelocated v1 payload
// (restated here, never imported). Every field is required by the producer;
// source_ref is always present and "" when the caller gave none.
type stockRelocatedData struct {
	RelocationID string    `json:"relocation_id"`
	SiteCode     string    `json:"site_code"`
	SKU          string    `json:"sku"`
	FromBin      string    `json:"from_bin"`
	ToBin        string    `json:"to_bin"`
	Quantity     int64     `json:"quantity"`
	Reason       string    `json:"reason"`
	SourceRef    string    `json:"source_ref"`
	OccurredAt   time.Time `json:"occurred_at"`
}

// StockHandlers maps inventory-storage's StockRelocated to the
// RecordRelocation use case.
func StockHandlers(uc *usecases.RecordRelocation, logger *slog.Logger) map[string]HandlerFunc {
	logger = defaultLogger(logger)
	return map[string]HandlerFunc{
		TypeStockRelocated: func(ctx context.Context, e ce.Event) error {
			d, err := decodeData[stockRelocatedData](e)
			if err != nil {
				return err
			}
			res, err := uc.Handle(ctx, e.ID(), usecases.RelocationFact{
				RelocationID: d.RelocationID, SiteCode: d.SiteCode, SKU: d.SKU, FromBin: d.FromBin, ToBin: d.ToBin,
				Quantity: d.Quantity, Reason: d.Reason, SourceRef: d.SourceRef, OccurredAt: d.OccurredAt,
			})
			if err == nil {
				logRelocation(ctx, logger, e, d, res)
			}
			return err
		},
	}
}

// logRelocation records what the consumer did with one relocation: an
// unmatched one is logged with why (it is counted, never dead-lettered).
func logRelocation(ctx context.Context, logger *slog.Logger, e ce.Event, d stockRelocatedData, res usecases.RelocationResult) {
	attrs := []any{
		"type", e.Type(), "event_id", e.ID(), "relocation_id", d.RelocationID, "site_code", d.SiteCode, "sku", d.SKU,
		"source_ref", d.SourceRef, "outcome", string(res.Outcome), "plan_id", string(res.Plan),
	}
	if res.Outcome == usecases.OutcomeUnmatched {
		logger.InfoContext(ctx, "relocation matched no slot-plan move", append(attrs, "reason", string(res.Unmatched))...)
		return
	}
	logger.InfoContext(ctx, "event processed", append(attrs, "move_seq", res.MoveSeq)...)
}

// NewStockConsumer reads inventory-storage's topic under groupID
// (RELOCATION_CONSUMER_GROUP, never a literal).
func NewStockConsumer(brokers []string, groupID string, uc *usecases.RecordRelocation, logger *slog.Logger) *Consumer {
	return NewStockConsumerForTopic(brokers, StockTopic, groupID, uc, logger)
}

// NewStockConsumerForTopic is NewStockConsumer on an explicit topic. A
// StockRelocated that can never be applied is dead-lettered at once
// (DeadLetterInvalid).
func NewStockConsumerForTopic(brokers []string, topic, groupID string, uc *usecases.RecordRelocation, logger *slog.Logger) *Consumer {
	c := newConsumer("relocation consumer", brokers, topic, groupID, StockHandlers(uc, logger), logger)
	c.DeadLetterInvalid = true
	return c
}
