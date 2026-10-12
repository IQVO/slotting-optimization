package http

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
)

// maxBodyBytes bounds a request body; every body here is a few hundred bytes.
const maxBodyBytes = 64 << 10

type statusBody struct {
	Status string `json:"status"`
}

// problemDetail is the RFC 7807 application/problem+json body.
type problemDetail struct {
	Type     string `json:"type"`
	Title    string `json:"title"`
	Status   int    `json:"status"`
	Detail   string `json:"detail"`
	Instance string `json:"instance"`
}

// writeJSON encodes v as the response body with the given status code.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// decodeOptionalJSON strictly decodes r's body into dest: unknown fields,
// trailing data and malformed JSON are errMalformedRequest. An empty body is
// valid and leaves dest untouched (every body here is optional).
func decodeOptionalJSON(r *http.Request, dest any) error {
	dec := json.NewDecoder(io.LimitReader(r.Body, maxBodyBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dest); err != nil {
		if errors.Is(err, io.EOF) {
			return nil
		}
		return fmt.Errorf("%w: %v", errMalformedRequest, err)
	}
	if dec.More() {
		return fmt.Errorf("%w: trailing data after the JSON body", errMalformedRequest)
	}
	return nil
}

// writeError writes err as its RFC 7807 problem. An unmapped error is a 500
// whose detail never leaks the internal message (it is logged instead).
func writeError(w http.ResponseWriter, r *http.Request, err error) {
	p := problemFor(err)
	detail := err.Error()
	if p == internalProblem {
		slog.ErrorContext(r.Context(), "request failed", "error", err, "method", r.Method, "path", r.URL.Path)
		detail = "an unexpected error occurred"
	}
	writeProblem(w, r, p, detail)
}

// writeProblem writes p with an explicit detail.
func writeProblem(w http.ResponseWriter, r *http.Request, p problem, detail string) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(p.status)
	_ = json.NewEncoder(w).Encode(problemDetail{
		Type:     problemBaseURI + p.slug,
		Title:    p.title,
		Status:   p.status,
		Detail:   detail,
		Instance: r.URL.EscapedPath(),
	})
}
