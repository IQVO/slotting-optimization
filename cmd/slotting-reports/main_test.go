package main

import (
	"context"
	"errors"
	"testing"
)

func TestRun_FailsFastWithoutTheAnalyticalDatabase(t *testing.T) {
	t.Setenv("ANALYTICS_DATABASE_URL", "")
	if err := run(); !errors.Is(err, errMissingAnalyticsURL) {
		t.Fatalf("run = %v", err)
	}
}

func TestRun_RejectsAMalformedDSN(t *testing.T) {
	t.Setenv("ANALYTICS_DATABASE_URL", "postgres://%zz")
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "127.0.0.1:1")
	if err := run(); err == nil {
		t.Fatal("run accepted a malformed DSN")
	}
}

func TestGetenv(t *testing.T) {
	t.Setenv("SLOTTING_REPORTS_TEST_KEY", "")
	if getenv("SLOTTING_REPORTS_TEST_KEY", "d") != "d" {
		t.Fatal("unset must fall back")
	}
	t.Setenv("SLOTTING_REPORTS_TEST_KEY", "v")
	if getenv("SLOTTING_REPORTS_TEST_KEY", "d") != "v" {
		t.Fatal("set must win")
	}
}

func TestNewLogger_LevelMapping(t *testing.T) {
	for level, enabledDebug := range map[string]bool{"debug": true, "info": false, "": false, "nonsense": false} {
		if got := newLogger(level).Enabled(context.Background(), -4); got != enabledDebug {
			t.Errorf("level %q: debug enabled = %v, want %v", level, got, enabledDebug)
		}
	}
	if newLogger("warn").Enabled(context.Background(), 0) || !newLogger("warning").Enabled(context.Background(), 4) || newLogger("error").Enabled(context.Background(), 4) {
		t.Error("warn/error level mapping is wrong")
	}
}
