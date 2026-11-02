// Package config parses the api's environment: the boot validation of the
// real runtime (Runtime, ADR 0011), the use-case configuration (Config) and
// the process settings (Server). Moved out of cmd/api so the parsing is a
// tested unit of its own; every error names the variable it refused.
package config

import (
	"log/slog"
	"os"
	"strconv"
	"strings"

	"github.com/claudioed/slotting-optimization/internal/application/usecases"
)

// defaultForwardZoneCode is the zone code of the forward pick slots when
// FORWARD_ZONE_CODES is unset.
const defaultForwardZoneCode = "FWD"

// Config is the use-case configuration read from the environment.
type Config struct {
	DefaultSite      string
	OnlySite         string
	LookbackDays     int
	ForwardZoneCodes []string
	// CasePickZoneCodes: CASE_PICK_ZONE_CODES; empty = no case-pick faces.
	CasePickZoneCodes []string
}

// FromEnv reads the use-case configuration (DEMAND_SITE_ID, DEFAULT_SITE_ID,
// LOOKBACK_DAYS, FORWARD_ZONE_CODES, CASE_PICK_ZONE_CODES).
func FromEnv(logger *slog.Logger) Config {
	site := os.Getenv("DEMAND_SITE_ID")
	return Config{
		DefaultSite:       getenv("DEMAND_SITE_ID", os.Getenv("DEFAULT_SITE_ID")),
		OnlySite:          site,
		LookbackDays:      parseLookbackDays(os.Getenv("LOOKBACK_DAYS"), logger),
		ForwardZoneCodes:  parseZoneCodes(os.Getenv("FORWARD_ZONE_CODES")),
		CasePickZoneCodes: parseCasePickZoneCodes(os.Getenv("CASE_PICK_ZONE_CODES")),
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
	codes := splitCSV(raw)
	if len(codes) == 0 {
		return []string{defaultForwardZoneCode}
	}
	return codes
}

// parseCasePickZoneCodes reads CASE_PICK_ZONE_CODES (comma separated). Unlike
// FORWARD_ZONE_CODES it has no default: unset means no case-pick faces.
func parseCasePickZoneCodes(raw string) []string {
	return splitCSV(raw)
}

// splitCSV splits a comma-separated variable, trimming blanks.
func splitCSV(raw string) []string {
	var out []string
	for _, c := range strings.Split(raw, ",") {
		if c = strings.TrimSpace(c); c != "" {
			out = append(out, c)
		}
	}
	return out
}

// getenv returns key's value or fallback when it is unset or blank.
func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// MigrationsDatabaseURL returns MIGRATIONS_DATABASE_URL when set (a direct
// DSN: golang-migrate's advisory lock does not survive PgBouncer transaction
// pooling), else databaseURL.
func MigrationsDatabaseURL(databaseURL string) string {
	return getenv("MIGRATIONS_DATABASE_URL", databaseURL)
}
