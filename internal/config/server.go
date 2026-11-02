package config

import (
	"log/slog"
	"os"
	"time"
)

// DefaultShutdownDrainDelay is how long shutdown waits, after flipping
// /readyz to not-ready, before closing the listener (SHUTDOWN_DRAIN_DELAY
// overrides it; "0" disables it).
const DefaultShutdownDrainDelay = 5 * time.Second

// Server is the process configuration read from the environment: the listen
// address, the log level, the telemetry identity and endpoint, and the
// shutdown drain delay.
type Server struct {
	HTTPAddr           string
	LogLevel           string
	ServiceVersion     string
	OTLPEndpoint       string
	ShutdownDrainDelay time.Duration
}

// ServerFromEnv reads the process settings (HTTP_ADDR, LOG_LEVEL,
// SERVICE_VERSION, OTEL_EXPORTER_OTLP_ENDPOINT, SHUTDOWN_DRAIN_DELAY). An
// empty OTLPEndpoint means the telemetry default.
func ServerFromEnv(logger *slog.Logger) Server {
	return Server{
		HTTPAddr:           getenv("HTTP_ADDR", ":8080"),
		LogLevel:           os.Getenv("LOG_LEVEL"),
		ServiceVersion:     getenv("SERVICE_VERSION", "dev"),
		OTLPEndpoint:       os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"),
		ShutdownDrainDelay: parseShutdownDrainDelay(os.Getenv("SHUTDOWN_DRAIN_DELAY"), logger),
	}
}

// parseShutdownDrainDelay reads SHUTDOWN_DRAIN_DELAY ("0" disables; unset,
// negative or unparsable = the default).
func parseShutdownDrainDelay(raw string, logger *slog.Logger) time.Duration {
	if raw == "" {
		return DefaultShutdownDrainDelay
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d < 0 {
		logger.Warn("ignoring invalid SHUTDOWN_DRAIN_DELAY", "value", raw, "default", DefaultShutdownDrainDelay.String())
		return DefaultShutdownDrainDelay
	}
	return d
}
