package main

import (
	"net/http"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv"
	"github.com/go-chi/chi/v5"
	"github.com/riandyrn/otelchi"
	otelchimetric "github.com/riandyrn/otelchi/metric"
)

// a2aServiceName labels the a2a binary for logs/spans/metrics, distinct
// from the api and mcp services' names.
const a2aServiceName = "slotting-optimization-a2a"

// newRouter wraps the A2A handler in a small chi router so the binary is
// deployable behind Kubernetes probes:
//
//   - GET /healthz -> 200 {"status":"ok"} (open; liveness/readiness probes).
//   - GET /.well-known/agent-card.json -> the Agent Card, the discovery contract
//     (ADR 0013).
//   - /  and /a2a  -> the A2A JSON-RPC handler. Both paths are served so the
//     fleet's <CTX>_A2A_ENDPOINT convention (".../a2a") and a root-mounted
//     endpoint (what the Kong route rewrites to) both work.
//
// No auth middleware is ever added here (TestNoAuthMiddlewareReintroduced).
func newRouter(a2aHandler http.Handler, card *a2a.AgentCard) http.Handler {
	r := chi.NewRouter()
	r.Use(otelchi.Middleware(a2aServiceName, otelchi.WithChiRoutes(r)))
	r.Use(otelchimetric.NewServerRequestDuration(otelchimetric.NewBaseConfig(a2aServiceName)))
	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	r.Handle(a2asrv.WellKnownAgentCardPath, a2asrv.NewStaticAgentCardHandler(card))
	r.Handle("/a2a", a2aHandler)
	r.Handle("/", a2aHandler)
	return r
}
