package telemetry

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// NewMetricsHandler returns the GET /metrics scrape handler over a private
// registry holding the Go runtime and process collectors (the OTLP pipeline
// in Setup carries the HTTP/business metrics; this endpoint keeps the
// pod scrapeable without a Collector).
func NewMetricsHandler() (http.Handler, error) {
	registry := prometheus.NewRegistry()
	if err := registry.Register(collectors.NewGoCollector()); err != nil {
		return nil, err
	}
	if err := registry.Register(collectors.NewProcessCollector(collectors.ProcessCollectorOpts{})); err != nil {
		return nil, err
	}
	return promhttp.HandlerFor(registry, promhttp.HandlerOpts{}), nil
}
