package telemetry

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestNewMetricsHandler_ServesGoAndProcessMetrics(t *testing.T) {
	h, err := NewMetricsHandler()
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body, _ := io.ReadAll(rec.Body)
	if rec.Code != http.StatusOK || !strings.Contains(string(body), "go_goroutines") {
		t.Fatalf("metrics = %d %s", rec.Code, body)
	}
}

func TestSetup_NeverBlocksOnAMissingCollector(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	start := time.Now()
	shutdown, err := Setup(ctx, "slotting-optimization", "test", "127.0.0.1:1")
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("Setup waited on the Collector")
	}
	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), time.Second)
	defer cancelShutdown()
	_ = shutdown(shutdownCtx)
	t.Setenv("ENVIRONMENT", "")
	if Environment() != "local" {
		t.Fatalf("Environment() = %q", Environment())
	}
	t.Setenv("ENVIRONMENT", "kind")
	if Environment() != "kind" {
		t.Fatalf("Environment() = %q", Environment())
	}
}
