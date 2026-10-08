package kafka

import (
	"context"
	"log/slog"
	"time"

	ce "github.com/cloudevents/sdk-go/v2/event"

	"github.com/claudioed/slotting-optimization/internal/application/usecases"
)

// TypeSiteSkuDemandChanged is order-management's per-line demand projection,
// byte-identical to its AsyncAPI. Every other type on the topic is ignored.
const TypeSiteSkuDemandChanged = "com.warehouse.wes.order-management.siteskudemand.SiteSkuDemandChanged"

// siteSkuDemandChangedData mirrors order-management's v1 payload (restated
// here, never imported from that service).
type siteSkuDemandChangedData struct {
	SourceOrderID     string    `json:"source_order_id"`
	LineNo            int       `json:"line_no"`
	SiteID            string    `json:"site_id"`
	SKU               string    `json:"sku"`
	DemandedUnits     int64     `json:"demanded_units"`
	DueAt             time.Time `json:"due_at"`
	State             string    `json:"state"`
	AssignmentVersion string    `json:"assignment_version"`
}

// DemandHandlers maps the demand topic's consumed type to its use case.
func DemandHandlers(uc *usecases.ApplyDemandChanged, logger *slog.Logger) map[string]HandlerFunc {
	logger = defaultLogger(logger)
	return map[string]HandlerFunc{
		TypeSiteSkuDemandChanged: func(ctx context.Context, e ce.Event) error {
			d, err := decodeData[siteSkuDemandChangedData](e)
			if err != nil {
				return err
			}
			outcome, err := uc.Handle(ctx, e.ID(), usecases.DemandChange{
				SourceOrderID: d.SourceOrderID, LineNo: d.LineNo, SiteID: d.SiteID, SKU: d.SKU,
				Units: d.DemandedUnits, DueAt: d.DueAt, State: d.State,
			})
			if err == nil {
				logOutcome(ctx, logger, e, outcome)
			}
			return err
		},
	}
}

// NewDemandConsumer reads order-management's topic under groupID
// (DEMAND_CONSUMER_GROUP, never a literal).
func NewDemandConsumer(brokers []string, groupID string, uc *usecases.ApplyDemandChanged, logger *slog.Logger) *Consumer {
	return NewDemandConsumerForTopic(brokers, DemandTopic, groupID, uc, logger)
}

// NewDemandConsumerForTopic is NewDemandConsumer on an explicit topic, so an
// integration test can point the identical logic at a throwaway topic.
func NewDemandConsumerForTopic(brokers []string, topic, groupID string, uc *usecases.ApplyDemandChanged, logger *slog.Logger) *Consumer {
	return newConsumer("demand consumer", brokers, topic, groupID, DemandHandlers(uc, logger), logger)
}
