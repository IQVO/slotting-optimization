package config

import (
	"io"
	"log/slog"
	"strings"
	"testing"
)

func quietLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestParseLookbackDays(t *testing.T) {
	for raw, want := range map[string]int{"": 0, "7": 7, "\t14": 14, "365": 365, "0": 0, "-1": 0, "366": 0, "abc": 0} {
		if got := parseLookbackDays(raw, quietLogger()); got != want {
			t.Errorf("parseLookbackDays(%q) = %d, want %d", raw, got, want)
		}
	}
}

func TestParseZoneCodes(t *testing.T) {
	cases := map[string]string{"": "FWD", " , ": "FWD", "FWD": "FWD", "FWD, PICK ,": "FWD,PICK"}
	for raw, want := range cases {
		if got := strings.Join(parseZoneCodes(raw), ","); got != want {
			t.Errorf("parseZoneCodes(%q) = %q, want %q", raw, got, want)
		}
	}
}

func TestParseCasePickZoneCodes(t *testing.T) {
	for raw, want := range map[string]string{"": "", " , ": "", "CASEPICK": "CASEPICK", " CASEPICK, BULK ,": "CASEPICK,BULK"} {
		if got := strings.Join(parseCasePickZoneCodes(raw), ","); got != want {
			t.Errorf("parseCasePickZoneCodes(%q) = %q, want %q", raw, got, want)
		}
	}
	if parseCasePickZoneCodes("") != nil {
		t.Error("unset means no case-pick zones, not a default zone")
	}
}

func TestFromEnv(t *testing.T) {
	t.Setenv("CASE_PICK_ZONE_CODES", "")
	t.Setenv("DEMAND_SITE_ID", "")
	t.Setenv("DEFAULT_SITE_ID", "")
	t.Setenv("LOOKBACK_DAYS", "")
	t.Setenv("FORWARD_ZONE_CODES", "")
	cfg := FromEnv(quietLogger())
	if cfg.DefaultSite != "" || cfg.OnlySite != "" || cfg.LookbackDays != 0 || len(cfg.ForwardZoneCodes) != 1 || len(cfg.CasePickZoneCodes) != 0 {
		t.Fatalf("defaults = %+v", cfg)
	}
	t.Setenv("CASE_PICK_ZONE_CODES", "CASEPICK,BULK")
	if cfg = FromEnv(quietLogger()); strings.Join(cfg.CasePickZoneCodes, ",") != "CASEPICK,BULK" {
		t.Fatalf("CASE_PICK_ZONE_CODES = %+v", cfg.CasePickZoneCodes)
	}
	t.Setenv("DEMAND_SITE_ID", "WH1")
	t.Setenv("LOOKBACK_DAYS", "7")
	cfg = FromEnv(quietLogger())
	if cfg.DefaultSite != "WH1" || cfg.OnlySite != "WH1" || cfg.LookbackDays != 7 {
		t.Fatalf("DEMAND_SITE_ID = %+v: it is both the default site and the demand filter", cfg)
	}
	t.Setenv("DEMAND_SITE_ID", "")
	t.Setenv("DEFAULT_SITE_ID", "WH2")
	cfg = FromEnv(quietLogger())
	if cfg.DefaultSite != "WH2" || cfg.OnlySite != "" {
		t.Fatalf("DEFAULT_SITE_ID = %+v: a default site alone must not filter demand", cfg)
	}
}

func TestGetenv(t *testing.T) {
	t.Setenv("SO_TEST_KEY", "")
	if getenv("SO_TEST_KEY", "fallback") != "fallback" {
		t.Fatal("getenv fallback")
	}
	t.Setenv("SO_TEST_KEY", "v")
	if getenv("SO_TEST_KEY", "fallback") != "v" {
		t.Fatal("getenv value")
	}
}

func TestMigrationsDatabaseURL(t *testing.T) {
	const pooled, direct = "postgres://u@pgbouncer:6432/db", "postgres://u@postgres:5432/db"
	t.Setenv("MIGRATIONS_DATABASE_URL", "")
	if got := MigrationsDatabaseURL(pooled); got != pooled {
		t.Errorf("unset: got %q", got)
	}
	t.Setenv("MIGRATIONS_DATABASE_URL", direct)
	if got := MigrationsDatabaseURL(pooled); got != direct {
		t.Errorf("set: got %q", got)
	}
}
