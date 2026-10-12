package http

import (
	"net/http"
	"sync/atomic"
)

// Readiness is a process-wide, thread-safe readiness gate: GET /readyz
// reflects it, separately from /healthz (liveness, never flipped by
// shutdown). The zero value is READY. Graceful shutdown flips it FIRST, so a
// readinessProbe stops routing new traffic before the listener closes.
type Readiness struct {
	notReady atomic.Bool
}

// SetNotReady flips the gate to not-ready (nil-safe).
func (g *Readiness) SetNotReady() {
	if g == nil {
		return
	}
	g.notReady.Store(true)
}

// Ready reports whether the gate currently says ready (nil = ready).
func (g *Readiness) Ready() bool {
	if g == nil {
		return true
	}
	return !g.notReady.Load()
}

// handleReadyz: 200 {"status":"ready"} while ready, 503
// {"status":"not_ready"} once SetNotReady was called.
func (s *Server) handleReadyz(w http.ResponseWriter, _ *http.Request) {
	if !s.Readiness.Ready() {
		writeJSON(w, http.StatusServiceUnavailable, statusBody{Status: "not_ready"})
		return
	}
	writeJSON(w, http.StatusOK, statusBody{Status: "ready"})
}
