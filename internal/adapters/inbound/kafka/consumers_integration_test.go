//go:build integration

package kafka_test

import (
	"context"
	"testing"
	"time"

	kafkaconsumer "github.com/claudioed/slotting-optimization/internal/adapters/inbound/kafka"
	"github.com/claudioed/slotting-optimization/internal/adapters/outbound/postgres"
	"github.com/claudioed/slotting-optimization/internal/application/usecases"
	"github.com/claudioed/slotting-optimization/internal/testing/kafkatest"
	"github.com/claudioed/slotting-optimization/internal/testing/pgtest"
)

// realIntake is the consumer use cases over a real Postgres.
func realIntake(t *testing.T) (usecases.Intake, *postgres.DemandLedger, *postgres.ProfileDirectory, *postgres.SlotCatalogue) {
	t.Helper()
	pool := pgtest.NewPool(t)
	return usecases.Intake{UoW: postgres.NewUnitOfWork(pool), Processed: postgres.NewProcessedEventRepo(pool)},
		postgres.NewDemandLedger(pool), postgres.NewProfileDirectory(pool), postgres.NewSlotCatalogue(pool)
}

// runConsumer starts c and stops it (and waits for it) when the test ends.
func runConsumer(t *testing.T, c *kafkaconsumer.Consumer) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = c.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		<-done
		_ = c.Close()
	})
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestDemandConsumer_RealKafkaAndPostgres(t *testing.T) {
	in, demand, _, _ := realIntake(t)
	topic := kafkatest.Topic(t, "warehouse.order-management.events")
	kafkatest.Produce(t, topic,
		kafkatest.Message{Key: "ord-1", Value: demandMsg("e1", "ord-1", 1, "SITE-1", "SKU-1", 12, "ACTIVE")},
		kafkatest.Message{Key: "ord-1", Value: []byte(legacyFlat)},
		kafkatest.Message{Key: "ord-1", Value: demandMsg("e1", "ord-1", 1, "SITE-1", "SKU-1", 12, "ACTIVE")}, // redelivery
		kafkatest.Message{Key: "ord-2", Value: demandMsg("e2", "ord-2", 1, "SITE-1", "SKU-2", 3, "ACTIVE")},
	)
	uc := &usecases.ApplyDemandChanged{Intake: in, Demand: demand}
	runConsumer(t, kafkaconsumer.NewDemandConsumerForTopic(kafkatest.Brokers(t), topic, "itest-demand-"+topic, uc, testLogger()))

	eventually(t, "both orders in the demand ledger", func() bool {
		v, err := demand.Velocity(context.Background(), "SITE-1", window[0], window[1])
		return err == nil && len(v) == 2
	})
	v, _ := demand.Velocity(context.Background(), "SITE-1", window[0], window[1])
	if v[0].SKU != "SKU-1" || v[0].Picks != 1 || v[0].Units != 12 {
		t.Fatalf("velocity = %+v: the redelivered event must count once", v)
	}
}

func TestProductAndLayoutConsumers_RealKafkaAndPostgres(t *testing.T) {
	in, _, profiles, catalogue := realIntake(t)
	productTopic := kafkatest.Topic(t, "warehouse.product-master.events")
	layoutTopic := kafkatest.Topic(t, "warehouse.facility.events")
	kafkatest.Produce(t, productTopic,
		kafkatest.Message{Key: "SKU-9", Value: classifiedMsg("p1", "SKU-9", classifiedHazmat)},
		kafkatest.Message{Key: "SKU-9", Value: physicalMsg("p2", typeMeasured, "SKU-9", measured)},
	)
	const reg = "com.warehouse.wms.facility-layout.locationslot.LocationSlotRegistered"
	kafkatest.Produce(t, layoutTopic,
		kafkatest.Message{Key: "WH1-FWD-AMB", Value: zoneMsg("z1", "WH1-FWD-AMB", `{"zoneId":"WH1-FWD-AMB","siteCode":"WH1","zoneCode":"FWD","temperatureClass":"Ambient"}`)},
		kafkatest.Message{Key: "L-1", Value: slotMsg("s1", reg, "L-1", `{"locationCode":"L-1","zoneId":"WH1-FWD-AMB","maxWeightKg":40,"maxVolumeM3":0.5}`)},
	)
	classified := &usecases.ApplyProductClassified{Intake: in, Profiles: profiles}
	physical := &usecases.ApplyPhysicalProfile{Intake: in, Profiles: profiles}
	runConsumer(t, kafkaconsumer.NewProductConsumerForTopic(kafkatest.Brokers(t), productTopic, "itest-product-"+productTopic, classified, physical, testLogger()))
	runConsumer(t, kafkaconsumer.NewLayoutConsumerForTopic(kafkatest.Brokers(t), layoutTopic, "itest-layout-"+layoutTopic,
		&usecases.ApplyZoneRegistered{Intake: in, Catalogue: catalogue},
		&usecases.ApplyLocationSlotRegistered{Intake: in, Catalogue: catalogue},
		&usecases.ApplyLocationSlotDecommissioned{Intake: in, Catalogue: catalogue}, testLogger()))

	eventually(t, "the measured profile", func() bool {
		p, err := profiles.Get(context.Background(), "SKU-9")
		return err == nil && p.Version == 5 && p.Unit != nil
	})
	eventually(t, "the forward slot", func() bool {
		s, err := catalogue.ForwardSlots(context.Background(), "WH1", []string{"FWD"})
		return err == nil && len(s) == 1 && s[0].Code == "L-1"
	})
	p, _ := profiles.Get(context.Background(), "SKU-9")
	if p.Unit.WeightG != 1720 || len(p.HandlingTags) != 2 {
		t.Fatalf("profile = %+v: classification and measurement must merge", p)
	}
}
