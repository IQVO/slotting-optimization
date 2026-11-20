package http

import (
	"errors"
	"net/http"

	"github.com/claudioed/slotting-optimization/internal/adapters/inbound/http/page"
	"github.com/claudioed/slotting-optimization/internal/application/repository"
	"github.com/claudioed/slotting-optimization/internal/application/usecases"
	"github.com/claudioed/slotting-optimization/internal/domain/slotplan"
)

// problemBaseURI is the namespace of this service's RFC 7807 `type` URIs.
const problemBaseURI = "https://errors.slotting-optimization.warehouse-systems.dev/"

// errMalformedRequest marks a body or parameter the adapter itself rejects
// (bad JSON, unknown field, wrong type).
var errMalformedRequest = errors.New("request body is not valid JSON for this operation")

// problem is the fixed (status, slug, title, class) of one error category; the
// detail comes from the error at write time. class is the replay class the
// Idempotency-Key middleware reads (replay_class.go, ADR 0016): every problem
// literal of this package lives in this file and declares one.
type problem struct {
	status int
	slug   string
	title  string
	class  replayClass
}

var internalProblem = problem{http.StatusInternalServerError, "internal-error", "Internal server error", classRetryable}

// Problems the middlewares write themselves, before any use case runs. The
// three Idempotency-Key answers are a function of the request alone and are
// written outside the store, so nothing is ever kept under their key.
var (
	idempotencyKeyRequired = problem{http.StatusBadRequest, "idempotency-key-required", "Idempotency-Key header is required", classFinal}
	idempotencyKeyInvalid  = problem{http.StatusBadRequest, "idempotency-key-invalid", "Idempotency-Key header is invalid", classFinal}
	idempotencyKeyReused   = problem{http.StatusConflict, "idempotency-key-reused", "Idempotency-Key was already used with a different request", classFinal}
	// notFoundResource is the 404 of a request path that can never name a
	// resource (a NUL in the path, nul.go); it is written before the
	// idempotency middleware, so nothing is stored under any key.
	notFoundResource = problem{http.StatusNotFound, "resource-not-found", "Resource not found", classFinal}
	// dependencyUnavailable is the 503 of GET /readyz (readiness.go): a 5xx is
	// never stored; the class is nominal.
	dependencyUnavailable = problem{http.StatusServiceUnavailable, "dependency-unavailable", "A runtime dependency is unavailable", classRetryable}
	// The reports reader (cmd/slotting-reports, a read-only binary on the
	// analytical database) answers these on GET routes: never behind the
	// idempotency middleware, never stored; the classes are nominal.
	invalidReportRange       = problem{http.StatusBadRequest, "invalid-report-range", "from and to must form a valid RFC 3339 range of at most 366 days", classFinal}
	invalidReportGranularity = problem{http.StatusBadRequest, "invalid-report-granularity", "granularity must be \"day\"", classFinal}
	reportStoreError         = problem{http.StatusInternalServerError, "report-store-error", "The report could not be served", classRetryable}
)

// problemCatalogue maps every typed error to its problem, in match order.
// Slugs are the catalogue of .claude/rules/rest-api.md. The class of each
// entry is decided in ADR 0016; a slug shared by several errors has ONE class
// (invalid-site-id mixes a request-only format check with ErrNoSite, so it is
// RETRYABLE).
var problemCatalogue = []struct {
	err error
	p   problem
}{
	{errMalformedRequest, problem{http.StatusBadRequest, "malformed-request", "Malformed request", classFinal}},
	{slotplan.ErrInvalidPlanID, problem{http.StatusBadRequest, "invalid-plan-id", "Plan id is invalid", classFinal}},
	{slotplan.ErrInvalidSiteID, problem{http.StatusBadRequest, "invalid-site-id", "Site id is invalid", classRetryable}},
	{usecases.ErrNoSite, problem{http.StatusBadRequest, "invalid-site-id", "Site id is invalid", classRetryable}},
	{usecases.ErrInvalidLookback, problem{http.StatusBadRequest, "invalid-lookback", "Lookback is invalid", classFinal}},
	{errUnknownQueryParameter, problem{http.StatusBadRequest, "unknown-query-parameter", "Unknown query parameter", classFinal}},
	{page.ErrInvalidLimit, problem{http.StatusBadRequest, "invalid-limit", "Limit is invalid", classFinal}},
	{page.ErrInvalidCursor, problem{http.StatusBadRequest, "invalid-cursor", "Cursor is invalid", classFinal}},
	{usecases.ErrInvalidLimit, problem{http.StatusBadRequest, "invalid-limit", "Limit is invalid", classFinal}},
	{usecases.ErrInvalidCursor, problem{http.StatusBadRequest, "invalid-cursor", "Cursor is invalid", classFinal}},
	{usecases.ErrInvalidState, problem{http.StatusBadRequest, "invalid-state", "State is invalid", classFinal}},
	{slotplan.ErrRejectReasonTooLong, problem{http.StatusBadRequest, "reject-reason-too-long", "Reject reason is too long", classFinal}},
	{repository.ErrPlanNotFound, problem{http.StatusNotFound, "plan-not-found", "Slot plan not found", classFinal}},
	{slotplan.ErrNotDraft, problem{http.StatusConflict, "plan-not-draft", "Only a Draft plan can be approved or rejected", classFinal}},
	{repository.ErrApprovedPlanConflict, problem{http.StatusConflict, "approved-plan-conflict", "Another plan of this site was approved at the same time", classRetryable}},
	{repository.ErrConcurrentModification, problem{http.StatusConflict, "concurrent-modification", "The resource was modified by another request; re-fetch the latest version and retry", classRetryable}},
}

// problemFor returns the problem of the first catalogue entry err matches,
// or the internal-error problem.
func problemFor(err error) problem {
	for _, entry := range problemCatalogue {
		if errors.Is(err, entry.err) {
			return entry.p
		}
	}
	return internalProblem
}
