package kafka

import (
	"context"
	"log/slog"

	ce "github.com/cloudevents/sdk-go/v2/event"

	"github.com/claudioed/slotting-optimization/internal/application/usecases"
)

// The product-master types this service acts on, byte-identical to its
// AsyncAPI. Every other type on the topic is ignored.
const (
	TypeProductClassified         = "com.warehouse.wms.product-master.product.ProductClassified"
	TypeProductDimensionsDeclared = "com.warehouse.wms.product-master.product.ProductDimensionsDeclared"
	TypeProductMeasured           = "com.warehouse.wms.product-master.product.ProductMeasured"
)

// effectiveSourceNone is the effective_source of a physical profile with no
// dimensions at all.
const effectiveSourceNone = "none"

// productClassifiedData mirrors product-master's v1 ProductClassified payload.
type productClassifiedData struct {
	SKU              string   `json:"sku"`
	HandlingTags     []string `json:"handling_tags"`
	TemperatureClass string   `json:"temperature_class"`
	Version          int64    `json:"version"`
}

// dimensionsData is one dimension set of a physical profile payload.
type dimensionsData struct {
	VolumeMm3 int64 `json:"volume_mm3"`
	WeightG   int64 `json:"weight_g"`
}

// physicalProfileData mirrors product-master's v1 ProductDimensionsDeclared /
// ProductMeasured payload; only the effective dimensions are acted on.
type physicalProfileData struct {
	SKU             string          `json:"sku"`
	Effective       *dimensionsData `json:"effective"`
	EffectiveSource string          `json:"effective_source"`
	Version         int64           `json:"version"`
}

// ProductHandlers maps the product topic's consumed types to their use cases.
func ProductHandlers(classified *usecases.ApplyProductClassified, physical *usecases.ApplyPhysicalProfile, logger *slog.Logger) map[string]HandlerFunc {
	logger = defaultLogger(logger)
	onPhysical := func(ctx context.Context, e ce.Event) error {
		d, err := decodeData[physicalProfileData](e)
		if err != nil {
			return err
		}
		change := usecases.PhysicalChange{SKU: d.SKU, Version: d.Version}
		if d.Effective != nil && d.EffectiveSource != effectiveSourceNone {
			change.HasEffective, change.VolumeMM3, change.WeightG = true, d.Effective.VolumeMm3, d.Effective.WeightG
		}
		outcome, err := physical.Handle(ctx, e.ID(), change)
		if err == nil {
			logOutcome(ctx, logger, e, outcome)
		}
		return err
	}
	return map[string]HandlerFunc{
		TypeProductClassified: func(ctx context.Context, e ce.Event) error {
			d, err := decodeData[productClassifiedData](e)
			if err != nil {
				return err
			}
			outcome, err := classified.Handle(ctx, e.ID(), usecases.ProductClassification{
				SKU: d.SKU, HandlingTags: d.HandlingTags, TemperatureClass: d.TemperatureClass, Version: d.Version,
			})
			if err == nil {
				logOutcome(ctx, logger, e, outcome)
			}
			return err
		},
		TypeProductDimensionsDeclared: onPhysical,
		TypeProductMeasured:           onPhysical,
	}
}

// NewProductConsumer reads product-master's topic under groupID
// (PRODUCT_CONSUMER_GROUP, never a literal).
func NewProductConsumer(brokers []string, groupID string, classified *usecases.ApplyProductClassified, physical *usecases.ApplyPhysicalProfile, logger *slog.Logger) *Consumer {
	return NewProductConsumerForTopic(brokers, ProductTopic, groupID, classified, physical, logger)
}

// NewProductConsumerForTopic is NewProductConsumer on an explicit topic.
func NewProductConsumerForTopic(brokers []string, topic, groupID string, classified *usecases.ApplyProductClassified, physical *usecases.ApplyPhysicalProfile, logger *slog.Logger) *Consumer {
	return newConsumer("product consumer", brokers, topic, groupID, ProductHandlers(classified, physical, logger), logger)
}
