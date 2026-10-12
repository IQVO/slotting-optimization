package kafka

import (
	"context"
	"log/slog"

	ce "github.com/cloudevents/sdk-go/v2/event"

	"github.com/claudioed/slotting-optimization/internal/application/usecases"
)

// The facility-layout types this service acts on, byte-identical to its
// AsyncAPI. Every other type on the topic is ignored.
const (
	TypeZoneRegistered             = "com.warehouse.wms.facility-layout.zone.ZoneRegistered"
	TypeLocationSlotRegistered     = "com.warehouse.wms.facility-layout.locationslot.LocationSlotRegistered"
	TypeLocationSlotDecommissioned = "com.warehouse.wms.facility-layout.locationslot.LocationSlotDecommissioned"
)

// The payloads mirror facility-layout's v1 events (restated here, never
// imported). Their extra fields (eventName, eventType, occurredAt, aisleId,
// locationType, dockFlow, activities) are ignored.

type zoneRegisteredData struct {
	ZoneID           string `json:"zoneId"`
	SiteCode         string `json:"siteCode"`
	AreaCode         string `json:"areaCode"`
	ZoneCode         string `json:"zoneCode"`
	TemperatureClass string `json:"temperatureClass"`
	Hazmat           bool   `json:"hazmat"`
}

type locationSlotRegisteredData struct {
	LocationCode string  `json:"locationCode"`
	ZoneID       string  `json:"zoneId"`
	Role         string  `json:"role"`
	MaxWeightKg  float64 `json:"maxWeightKg"`
	MaxVolumeM3  float64 `json:"maxVolumeM3"`
}

type locationSlotDecommissionedData struct {
	LocationCode string `json:"locationCode"`
}

// LayoutHandlers maps the facility topic's consumed types to their use cases.
func LayoutHandlers(zone *usecases.ApplyZoneRegistered, registered *usecases.ApplyLocationSlotRegistered, decommissioned *usecases.ApplyLocationSlotDecommissioned, logger *slog.Logger) map[string]HandlerFunc {
	logger = defaultLogger(logger)
	return map[string]HandlerFunc{
		TypeZoneRegistered: func(ctx context.Context, e ce.Event) error {
			d, err := decodeData[zoneRegisteredData](e)
			if err != nil {
				return err
			}
			outcome, err := zone.Handle(ctx, e.ID(), usecases.ZoneChange{
				ZoneID: d.ZoneID, SiteCode: d.SiteCode, AreaCode: d.AreaCode, ZoneCode: d.ZoneCode,
				TemperatureClass: d.TemperatureClass, Hazmat: d.Hazmat,
			})
			if err == nil {
				logOutcome(ctx, logger, e, outcome)
			}
			return err
		},
		TypeLocationSlotRegistered: func(ctx context.Context, e ce.Event) error {
			d, err := decodeData[locationSlotRegisteredData](e)
			if err != nil {
				return err
			}
			outcome, err := registered.Handle(ctx, e.ID(), usecases.SlotChange{
				LocationCode: d.LocationCode, ZoneID: d.ZoneID, Role: d.Role, MaxWeightKg: d.MaxWeightKg, MaxVolumeM3: d.MaxVolumeM3,
			})
			if err == nil {
				logOutcome(ctx, logger, e, outcome)
			}
			return err
		},
		TypeLocationSlotDecommissioned: func(ctx context.Context, e ce.Event) error {
			d, err := decodeData[locationSlotDecommissionedData](e)
			if err != nil {
				return err
			}
			outcome, err := decommissioned.Handle(ctx, e.ID(), d.LocationCode)
			if err == nil {
				logOutcome(ctx, logger, e, outcome)
			}
			return err
		},
	}
}

// NewLayoutConsumer reads facility-layout's topic under groupID
// (LAYOUT_CONSUMER_GROUP, never a literal).
func NewLayoutConsumer(brokers []string, groupID string, zone *usecases.ApplyZoneRegistered, registered *usecases.ApplyLocationSlotRegistered, decommissioned *usecases.ApplyLocationSlotDecommissioned, logger *slog.Logger) *Consumer {
	return NewLayoutConsumerForTopic(brokers, LayoutTopic, groupID, zone, registered, decommissioned, logger)
}

// NewLayoutConsumerForTopic is NewLayoutConsumer on an explicit topic.
func NewLayoutConsumerForTopic(brokers []string, topic, groupID string, zone *usecases.ApplyZoneRegistered, registered *usecases.ApplyLocationSlotRegistered, decommissioned *usecases.ApplyLocationSlotDecommissioned, logger *slog.Logger) *Consumer {
	return newConsumer("layout consumer", brokers, topic, groupID, LayoutHandlers(zone, registered, decommissioned, logger), logger)
}
