package kafka

import (
	"context"
	"log/slog"
	"time"

	ce "github.com/cloudevents/sdk-go/v2/event"

	"github.com/claudioed/slotting-optimization/internal/application/usecases"
	"github.com/claudioed/slotting-optimization/internal/domain/packlevel"
)

// The product-master pack-level types this service acts on (ADR 0012),
// byte-identical to its AsyncAPI. Every other type on the topic is ignored.
const (
	TypeProductPackLevelDeclared = "com.warehouse.wms.product-master.product.ProductPackLevelDeclared"
	TypeProductPackLevelRetired  = "com.warehouse.wms.product-master.product.ProductPackLevelRetired"
	TypeProductPackLevelMeasured = "com.warehouse.wms.product-master.product.ProductPackLevelMeasured"
)

// packDimsData mirrors product-master's PackDimensions (snake_case on the
// wire, like every product-master payload).
type packDimsData struct {
	LengthMM int64 `json:"length_mm"`
	WidthMM  int64 `json:"width_mm"`
	HeightMM int64 `json:"height_mm"`
	WeightG  int64 `json:"weight_g"`
}

func (d packDimsData) domain() packlevel.Dims {
	return packlevel.Dims{LengthMM: d.LengthMM, WidthMM: d.WidthMM, HeightMM: d.HeightMM, WeightG: d.WeightG}
}

// The payloads mirror product-master's v1 events (restated here, never
// imported). Their extra fields (child_gtin, qty_of_child, is_orderable,
// is_despatch_unit, reason, measured_at, device_id) are ignored.

type productPackLevelDeclaredData struct {
	SKU           string        `json:"sku"`
	Level         string        `json:"level"`
	GTIN          string        `json:"gtin"`
	UnitsPerPack  int64         `json:"units_per_pack"`
	Dims          *packDimsData `json:"dims"`
	EffectiveFrom time.Time     `json:"effective_from"`
	Version       int64         `json:"version"`
}

type productPackLevelRetiredData struct {
	SKU         string    `json:"sku"`
	Level       string    `json:"level"`
	GTIN        string    `json:"gtin"`
	EffectiveTo time.Time `json:"effective_to"`
	Version     int64     `json:"version"`
}

type productPackLevelMeasuredData struct {
	SKU     string       `json:"sku"`
	Level   string       `json:"level"`
	GTIN    string       `json:"gtin"`
	Dims    packDimsData `json:"dims"`
	Version int64        `json:"version"`
}

// PackLevelUseCases are the use cases the pack-level consumer dispatches to.
type PackLevelUseCases struct {
	Declared *usecases.ApplyPackLevelDeclared
	Retired  *usecases.ApplyPackLevelRetired
	Measured *usecases.ApplyPackLevelMeasured
}

// PackLevelHandlers maps product-master's pack-level types to their use cases.
func PackLevelHandlers(uc PackLevelUseCases, logger *slog.Logger) map[string]HandlerFunc {
	logger = defaultLogger(logger)
	return map[string]HandlerFunc{
		TypeProductPackLevelDeclared: func(ctx context.Context, e ce.Event) error {
			d, err := decodeData[productPackLevelDeclaredData](e)
			if err != nil {
				return err
			}
			change := usecases.PackLevelDeclaredChange{
				SKU: d.SKU, Level: d.Level, GTIN: d.GTIN, UnitsPerPack: d.UnitsPerPack,
				EffectiveFrom: d.EffectiveFrom, Version: d.Version,
			}
			if d.Dims != nil {
				dims := d.Dims.domain()
				change.Dims = &dims
			}
			outcome, err := uc.Declared.Handle(ctx, e.ID(), change)
			if err == nil {
				logOutcome(ctx, logger, e, outcome)
			}
			return err
		},
		TypeProductPackLevelRetired: func(ctx context.Context, e ce.Event) error {
			d, err := decodeData[productPackLevelRetiredData](e)
			if err != nil {
				return err
			}
			outcome, err := uc.Retired.Handle(ctx, e.ID(), usecases.PackLevelRetiredChange{
				SKU: d.SKU, Level: d.Level, GTIN: d.GTIN, EffectiveTo: d.EffectiveTo, Version: d.Version,
			})
			if err == nil {
				logOutcome(ctx, logger, e, outcome)
			}
			return err
		},
		TypeProductPackLevelMeasured: func(ctx context.Context, e ce.Event) error {
			d, err := decodeData[productPackLevelMeasuredData](e)
			if err != nil {
				return err
			}
			outcome, err := uc.Measured.Handle(ctx, e.ID(), usecases.PackLevelMeasuredChange{
				SKU: d.SKU, Level: d.Level, GTIN: d.GTIN, Dims: d.Dims.domain(), Version: d.Version,
			})
			if err == nil {
				logOutcome(ctx, logger, e, outcome)
			}
			return err
		},
	}
}

// NewPackLevelConsumer reads product-master's topic under groupID
// (PACK_LEVEL_CONSUMER_GROUP, never a literal). It is a consumer of its own,
// not part of the product consumer: a new group starts at the beginning of the
// topic, so pack levels declared before this service learned to read them are
// replayed into the view.
func NewPackLevelConsumer(brokers []string, groupID string, uc PackLevelUseCases, logger *slog.Logger) *Consumer {
	return NewPackLevelConsumerForTopic(brokers, ProductTopic, groupID, uc, logger)
}

// NewPackLevelConsumerForTopic is NewPackLevelConsumer on an explicit topic. A
// pack-level event that can never be applied is dead-lettered at once
// (DeadLetterInvalid).
func NewPackLevelConsumerForTopic(brokers []string, topic, groupID string, uc PackLevelUseCases, logger *slog.Logger) *Consumer {
	c := newConsumer("pack-level consumer", brokers, topic, groupID, PackLevelHandlers(uc, logger), logger)
	c.DeadLetterInvalid = true
	return c
}
