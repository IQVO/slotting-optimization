package http

import (
	"bytes"
	"io"
	"net/http"
	"strings"
)

// notFoundResource is the 404 of a request path that can never name a resource.
var notFoundResource = problem{status: http.StatusNotFound, slug: "resource-not-found", title: "Resource not found"}

// rejectNUL turns a NUL character in a request into a client error before it
// can reach a Postgres text parameter. PostgreSQL text cannot hold U+0000, and
// a statement that carries one aborts the surrounding transaction (SQLSTATE
// 22021, then 25P02 for the next statement): on the sibling services that
// became a 500 on the real runtime, hidden for as long as the in-memory
// adapters served the contract run (ADR 0011).
//
//   - a NUL in the URL path can never name a resource: 404 resource-not-found,
//     the same answer as any other identifier that does not exist;
//   - a NUL in the query string (raw byte or %00) or in a JSON body (a raw
//     byte or the \u0000 escape) is a malformed request: 400 malformed-request,
//     before the Idempotency-Key middleware records anything.
func rejectNUL(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.ContainsRune(r.URL.Path, 0) || strings.ContainsRune(r.URL.RawPath, 0) || strings.Contains(r.URL.RawPath, "%00") {
			writeProblem(w, r, notFoundResource, "no resource matches the requested path")
			return
		}
		if strings.ContainsRune(r.URL.RawQuery, 0) || strings.Contains(r.URL.RawQuery, "%00") {
			writeError(w, r, errNULQuery)
			return
		}
		if r.Body != nil && r.Body != http.NoBody {
			body, err := io.ReadAll(io.LimitReader(r.Body, maxBodyBytes+1))
			_ = r.Body.Close()
			if err != nil {
				writeError(w, r, errMalformedRequest)
				return
			}
			if containsNUL(body) {
				writeError(w, r, errNULBody)
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(body))
		}
		next.ServeHTTP(w, r)
	})
}

// The malformed-request details of a NUL (both map to malformed-request).
var (
	errNULQuery error = nulError{"a NUL character (U+0000) is not allowed in the query string"}
	errNULBody  error = nulError{"a NUL character (U+0000) is not allowed in a request body"}
)

// nulError is a malformed-request error with its own detail.
type nulError struct{ detail string }

func (e nulError) Error() string { return e.detail }

func (e nulError) Is(target error) bool { return target == errMalformedRequest }

// containsNUL reports whether a JSON document carries U+0000, either as a raw
// 0x00 byte or as the six-character escape \u0000. A backslash that is itself
// escaped (\\u0000, a literal backslash followed by the text u0000) is not a
// NUL: the escape only counts when it is preceded by an even number of
// backslashes.
func containsNUL(body []byte) bool {
	if bytes.IndexByte(body, 0) >= 0 {
		return true
	}
	const escape = `\u0000`
	for from := 0; ; {
		i := bytes.Index(body[from:], []byte(escape))
		if i < 0 {
			return false
		}
		i += from
		slashes := 0
		for j := i - 1; j >= 0 && body[j] == '\\'; j-- {
			slashes++
		}
		if slashes%2 == 0 {
			return true
		}
		from = i + len(escape)
	}
}
