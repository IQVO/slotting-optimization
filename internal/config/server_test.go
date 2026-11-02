package config

import (
	"testing"
	"time"
)

func TestShutdownDrainDelay(t *testing.T) {
	cases := map[string]time.Duration{"": DefaultShutdownDrainDelay, "0": 0, "2s": 2 * time.Second, "x": DefaultShutdownDrainDelay, "-1s": DefaultShutdownDrainDelay}
	for raw, want := range cases {
		t.Setenv("SHUTDOWN_DRAIN_DELAY", raw)
		if got := parseShutdownDrainDelay(raw, quietLogger()); got != want {
			t.Errorf("parseShutdownDrainDelay(%q) = %v, want %v", raw, got, want)
		}
	}
}

func TestServerFromEnv(t *testing.T) {
	for _, k := range []string{"HTTP_ADDR", "LOG_LEVEL", "SERVICE_VERSION", "OTEL_EXPORTER_OTLP_ENDPOINT", "SHUTDOWN_DRAIN_DELAY"} {
		t.Setenv(k, "")
	}
	srv := ServerFromEnv(quietLogger())
	if srv.HTTPAddr != ":8080" || srv.LogLevel != "" || srv.ServiceVersion != "dev" || srv.OTLPEndpoint != "" || srv.ShutdownDrainDelay != DefaultShutdownDrainDelay {
		t.Fatalf("defaults = %+v", srv)
	}
	t.Setenv("HTTP_ADDR", ":9090")
	t.Setenv("LOG_LEVEL", "debug")
	t.Setenv("SERVICE_VERSION", "1.2.3")
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "collector:4317")
	t.Setenv("SHUTDOWN_DRAIN_DELAY", "0")
	if srv = ServerFromEnv(quietLogger()); srv.HTTPAddr != ":9090" || srv.LogLevel != "debug" || srv.ServiceVersion != "1.2.3" || srv.OTLPEndpoint != "collector:4317" || srv.ShutdownDrainDelay != 0 {
		t.Fatalf("overrides = %+v", srv)
	}
}
