package main

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"

	"github.com/claudioed/slotting-optimization/internal/application/usecases"
)

// Boot errors for the variables the context cannot run without. Each message
// names the variable (ADR 0011).
var (
	errMissingDatabaseURL = errors.New("DATABASE_URL is required: this context runs on real Postgres, there is no in-memory mode")
	errMissingBrokers     = errors.New("KAFKA_BROKERS is required: this context runs on real Kafka (outbox relay, consumers, /readyz)")
)

// runtimeEnv is the validated part of the environment that selects the real
// runtime: the brokers and the consumer group of every consumer.
type runtimeEnv struct {
	brokers []string
	// groups maps a consumer's group variable (DEMAND_CONSUMER_GROUP, ...) to
	// its value; every consumer always runs, so every entry is present.
	groups map[string]string
}

// loadRuntime validates the environment before anything is opened or bound:
// DATABASE_URL, KAFKA_BROKERS and every consumer group are mandatory, the
// consumer modes may only be `kafka` (the default), and EVENT_PUBLISHER may
// only be `kafka`. Every error names the variable (ADR 0011).
func loadRuntime(env func(string) string) (runtimeEnv, error) {
	if env("DATABASE_URL") == "" {
		return runtimeEnv{}, errMissingDatabaseURL
	}
	rt := runtimeEnv{brokers: splitBrokers(env("KAFKA_BROKERS")), groups: map[string]string{}}
	if len(rt.brokers) == 0 {
		return runtimeEnv{}, errMissingBrokers
	}
	if err := parsePublisherMode(env("EVENT_PUBLISHER")); err != nil {
		return runtimeEnv{}, err
	}
	for _, spec := range consumerSpecs(adapters{}, config{}) {
		if err := parseConsumerMode(spec.modeEnv, env(spec.modeEnv)); err != nil {
			return runtimeEnv{}, err
		}
		group := env(spec.groupEnv)
		if group == "" {
			return runtimeEnv{}, fmt.Errorf("%s is required: the %s consumer always runs on real Kafka", spec.groupEnv, spec.modeEnv)
		}
		rt.groups[spec.groupEnv] = group
	}
	return rt, nil
}

// splitBrokers splits KAFKA_BROKERS on commas, trimming blanks.
func splitBrokers(raw string) []string {
	var out []string
	for _, b := range strings.Split(raw, ",") {
		if b = strings.TrimSpace(b); b != "" {
			out = append(out, b)
		}
	}
	return out
}

// config is the use-case configuration read from the environment.
type config struct {
	defaultSite      string
	onlySite         string
	lookbackDays     int
	forwardZoneCodes []string
	// casePickZoneCodes: CASE_PICK_ZONE_CODES; empty = no case-pick faces.
	casePickZoneCodes []string
}

func configFromEnv(logger *slog.Logger) config {
	site := os.Getenv("DEMAND_SITE_ID")
	return config{
		defaultSite:       getenv("DEMAND_SITE_ID", os.Getenv("DEFAULT_SITE_ID")),
		onlySite:          site,
		lookbackDays:      parseLookbackDays(os.Getenv("LOOKBACK_DAYS"), logger),
		forwardZoneCodes:  parseZoneCodes(os.Getenv("FORWARD_ZONE_CODES")),
		casePickZoneCodes: parseCasePickZoneCodes(os.Getenv("CASE_PICK_ZONE_CODES")),
	}
}

// parseLookbackDays reads LOOKBACK_DAYS; unset, malformed or out of range
// falls back to the use case default (0 = DefaultLookbackDays).
func parseLookbackDays(raw string, logger *slog.Logger) int {
	if raw == "" {
		return 0
	}
	days, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || days < 1 || days > usecases.MaxLookbackDays {
		logger.Warn("invalid LOOKBACK_DAYS; using the default", "value", raw, "default", usecases.DefaultLookbackDays)
		return 0
	}
	return days
}

// parseZoneCodes reads FORWARD_ZONE_CODES (comma separated), defaulting to
// FWD.
func parseZoneCodes(raw string) []string {
	var codes []string
	for _, c := range strings.Split(raw, ",") {
		if c = strings.TrimSpace(c); c != "" {
			codes = append(codes, c)
		}
	}
	if len(codes) == 0 {
		return []string{defaultForwardZoneCode}
	}
	return codes
}

// parseCasePickZoneCodes reads CASE_PICK_ZONE_CODES (comma separated). Unlike
// FORWARD_ZONE_CODES it has no default: unset means no case-pick faces.
func parseCasePickZoneCodes(raw string) []string {
	var codes []string
	for _, c := range strings.Split(raw, ",") {
		if c = strings.TrimSpace(c); c != "" {
			codes = append(codes, c)
		}
	}
	return codes
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
