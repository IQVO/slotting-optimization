package config

import (
	"errors"
	"fmt"
)

// Boot errors for the variables the context cannot run without. Each message
// names the variable (ADR 0011).
var (
	ErrMissingDatabaseURL = errors.New("DATABASE_URL is required: this context runs on real Postgres, there is no in-memory mode")
	ErrMissingBrokers     = errors.New("KAFKA_BROKERS is required: this context runs on real Kafka (outbox relay, consumers, /readyz)")
)

// modeKafka is the only publisher and consumer mode (ADR 0011).
const modeKafka = "kafka"

// Runtime is the validated part of the environment that selects the real
// runtime: the brokers and the consumer group of every consumer.
type Runtime struct {
	Brokers []string
	// Groups maps a consumer's group variable (DEMAND_CONSUMER_GROUP, ...)
	// to its value; every consumer always runs, so every entry is present.
	Groups map[string]string
}

// ConsumerEnv names one local-copy consumer's two variables: its mode
// (DEMAND_MODE, ...) and its group (DEMAND_CONSUMER_GROUP, ...). The
// composition root derives the list from its consumer wiring and passes it
// here, so validation and wiring cannot drift apart.
type ConsumerEnv struct {
	ModeEnv  string
	GroupEnv string
}

// LoadRuntime validates the environment before anything is opened or bound:
// DATABASE_URL, KAFKA_BROKERS and every consumer group are mandatory, the
// consumer modes may only be `kafka` (the default), and EVENT_PUBLISHER may
// only be `kafka`. Every error names the variable (ADR 0011).
func LoadRuntime(env func(string) string, specs []ConsumerEnv) (Runtime, error) {
	if env("DATABASE_URL") == "" {
		return Runtime{}, ErrMissingDatabaseURL
	}
	rt := Runtime{Brokers: splitBrokers(env("KAFKA_BROKERS")), Groups: map[string]string{}}
	if len(rt.Brokers) == 0 {
		return Runtime{}, ErrMissingBrokers
	}
	if err := parsePublisherMode(env("EVENT_PUBLISHER")); err != nil {
		return Runtime{}, err
	}
	for _, spec := range specs {
		if err := parseConsumerMode(spec.ModeEnv, env(spec.ModeEnv)); err != nil {
			return Runtime{}, err
		}
		group := env(spec.GroupEnv)
		if group == "" {
			return Runtime{}, fmt.Errorf("%s is required: the %s consumer always runs on real Kafka", spec.GroupEnv, spec.ModeEnv)
		}
		rt.Groups[spec.GroupEnv] = group
	}
	return rt, nil
}

// splitBrokers splits KAFKA_BROKERS on commas, trimming blanks.
func splitBrokers(raw string) []string {
	return splitCSV(raw)
}

// parsePublisherMode validates EVENT_PUBLISHER: unset or "kafka". The log
// sink is gone (ADR 0011); the variable stays because the chart and
// warehouse-infra set it.
func parsePublisherMode(raw string) error {
	switch raw {
	case "", modeKafka:
		return nil
	case "log":
		return errors.New("EVENT_PUBLISHER=log is not supported: this context publishes to real Kafka")
	default:
		return fmt.Errorf("EVENT_PUBLISHER: unknown value %q (want kafka)", raw)
	}
}

// parseConsumerMode accepts the only runtime mode of a local-copy consumer:
// unset (kafka). The permissive and in-memory modes are boot errors because
// every consumer always runs on real Kafka (ADR 0011).
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
