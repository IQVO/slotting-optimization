package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	inboundkafka "github.com/claudioed/slotting-optimization/internal/adapters/inbound/kafka"
	"github.com/claudioed/slotting-optimization/internal/adapters/outbound/telemetry"
	"github.com/claudioed/slotting-optimization/internal/application/ports"
	"github.com/claudioed/slotting-optimization/internal/application/usecases"
)

// worker is a started background loop and the channel closed once it
// returned.
type worker struct {
	name string
	done chan struct{}
}

// modeKafka is the only consumer mode (DEMAND_MODE, PRODUCT_MODE,
// LAYOUT_MODE, RELOCATION_MODE, PACK_LEVEL_MODE): the names stay because docs and
// warehouse-infra set them, the legacy values are refused.
const modeKafka = "kafka"

// parseConsumerMode accepts the only runtime mode of a local-copy consumer:
// "" (the default) or "kafka". `permissive` and `off` are refused: the
// consumers always run.
func parseConsumerMode(name, raw string) error {
	switch raw {
	case "", modeKafka:
		return nil
	case "permissive", "off":
		return fmt.Errorf("%s=%s is not supported: this context runs on real Kafka", name, raw)
	default:
		return fmt.Errorf("%s: unknown mode %q (want kafka)", name, raw)
	}
}

// consumerSpec is one local-copy consumer: its mode and group variables and
// how to build it once both are resolved.
type consumerSpec struct {
	modeEnv, groupEnv string
	build             func(brokers []string, group string, logger *slog.Logger) *inboundkafka.Consumer
}

func consumerSpecs(ad adapters, cfg config) []consumerSpec {
	intake := ad.intake()
	return []consumerSpec{
		{"DEMAND_MODE", "DEMAND_CONSUMER_GROUP", func(brokers []string, group string, logger *slog.Logger) *inboundkafka.Consumer {
			uc := &usecases.ApplyDemandChanged{Intake: intake, Demand: ad.demand, OnlySite: cfg.onlySite}
			return inboundkafka.NewDemandConsumer(brokers, group, uc, logger)
		}},
		{"PRODUCT_MODE", "PRODUCT_CONSUMER_GROUP", func(brokers []string, group string, logger *slog.Logger) *inboundkafka.Consumer {
			classified := &usecases.ApplyProductClassified{Intake: intake, Profiles: ad.profiles}
			physical := &usecases.ApplyPhysicalProfile{Intake: intake, Profiles: ad.profiles}
			return inboundkafka.NewProductConsumer(brokers, group, classified, physical, logger)
		}},
		{"LAYOUT_MODE", "LAYOUT_CONSUMER_GROUP", func(brokers []string, group string, logger *slog.Logger) *inboundkafka.Consumer {
			return inboundkafka.NewLayoutConsumer(brokers, group,
				&usecases.ApplyZoneRegistered{Intake: intake, Catalogue: ad.catalogue},
				&usecases.ApplyLocationSlotRegistered{Intake: intake, Catalogue: ad.catalogue},
				&usecases.ApplyLocationSlotDecommissioned{Intake: intake, Catalogue: ad.catalogue}, logger)
		}},
		{"RELOCATION_MODE", "RELOCATION_CONSUMER_GROUP", func(brokers []string, group string, logger *slog.Logger) *inboundkafka.Consumer {
			uc := &usecases.RecordRelocation{Intake: intake, Plans: ad.plans, Executions: ad.executions, Metrics: relocationMetrics()}
			return inboundkafka.NewStockConsumer(brokers, group, uc, logger)
		}},
		{"PACK_LEVEL_MODE", "PACK_LEVEL_CONSUMER_GROUP", func(brokers []string, group string, logger *slog.Logger) *inboundkafka.Consumer {
			return inboundkafka.NewPackLevelConsumer(brokers, group, inboundkafka.PackLevelUseCases{
				Declared: &usecases.ApplyPackLevelDeclared{Intake: intake, PackLevels: ad.packLevels},
				Retired:  &usecases.ApplyPackLevelRetired{Intake: intake, PackLevels: ad.packLevels},
				Measured: &usecases.ApplyPackLevelMeasured{Intake: intake, PackLevels: ad.packLevels},
			}, logger)
		}},
	}
}

// relocationMetrics is the StockRelocated consumer's outcome counter, or
// nil (record nothing) in the impossible case the instrument cannot be
// registered; never a typed nil inside the interface.
func relocationMetrics() ports.RelocationMetrics {
	m, err := telemetry.NewRelocationMetrics()
	if err != nil {
		return nil
	}
	return m
}

// startConsumers starts every consumer; they are always on. The readers dial
// lazily inside Run, so a broker outage never blocks boot.
func startConsumers(ad adapters, rt runtimeEnv, logger *slog.Logger) ([]*worker, func()) {
	ctx, stop := context.WithCancel(context.Background())
	var (
		workers []*worker
		closers []func() error
	)
	for _, spec := range consumerSpecs(ad, configFromEnv(logger)) {
		group := rt.groups[spec.groupEnv]
		consumer := spec.build(rt.brokers, group, logger)
		w := &worker{name: spec.modeEnv + " consumer", done: make(chan struct{})}
		workers = append(workers, w)
		closers = append(closers, consumer.Close)
		go func() {
			defer close(w.done)
			logger.Info("consumer running", "consumer", consumer.Name, "topic", consumer.Topic, "group_id", group, "brokers", rt.brokers)
			if err := consumer.Run(ctx); !errors.Is(err, context.Canceled) {
				logger.Error("consumer stopped", "consumer", consumer.Name, "error", err)
			}
		}()
	}
	return workers, func() {
		stop()
		for _, closeFn := range closers {
			_ = closeFn()
		}
	}
}
