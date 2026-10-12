package http

import (
	"errors"
	"net/http"

	"github.com/claudioed/slotting-optimization/internal/application/repository"
	"github.com/claudioed/slotting-optimization/internal/application/usecases"
	"github.com/claudioed/slotting-optimization/internal/domain/slotplan"
)

// problemBaseURI is the namespace of this service's RFC 7807 `type` URIs.
const problemBaseURI = "https://errors.slotting-optimization.warehouse-systems.dev/"

// errMalformedRequest marks a body or parameter the adapter itself rejects
// (bad JSON, unknown field, wrong type).
var errMalformedRequest = errors.New("request body is not valid JSON for this operation")

// problem is the fixed (status, slug, title) of one error category; the
// detail comes from the error at write time.
type problem struct {
	status int
	slug   string
	title  string
}

var internalProblem = problem{http.StatusInternalServerError, "internal-error", "Internal server error"}

// Problems the idempotency middleware writes itself, before any use case runs.
var (
	idempotencyKeyRequired = problem{http.StatusBadRequest, "idempotency-key-required", "Idempotency-Key header is required"}
	idempotencyKeyReused   = problem{http.StatusUnprocessableEntity, "idempotency-key-reused", "Idempotency-Key was already used with a different request"}
)

// problemCatalogue maps every typed error to its problem, in match order.
// Slugs are the catalogue of .claude/rules/rest-api.md.
var problemCatalogue = []struct {
	err error
	p   problem
}{
	{errMalformedRequest, problem{http.StatusBadRequest, "malformed-request", "Malformed request"}},
	{slotplan.ErrInvalidPlanID, problem{http.StatusBadRequest, "invalid-plan-id", "Plan id is invalid"}},
	{slotplan.ErrInvalidSiteID, problem{http.StatusBadRequest, "invalid-site-id", "Site id is invalid"}},
	{usecases.ErrNoSite, problem{http.StatusBadRequest, "invalid-site-id", "Site id is invalid"}},
	{usecases.ErrInvalidLookback, problem{http.StatusBadRequest, "invalid-lookback", "Lookback is invalid"}},
	{usecases.ErrInvalidLimit, problem{http.StatusBadRequest, "invalid-limit", "Limit is invalid"}},
	{usecases.ErrInvalidCursor, problem{http.StatusBadRequest, "invalid-cursor", "Cursor is invalid"}},
	{usecases.ErrInvalidState, problem{http.StatusBadRequest, "invalid-state", "State is invalid"}},
	{slotplan.ErrRejectReasonTooLong, problem{http.StatusBadRequest, "reject-reason-too-long", "Reject reason is too long"}},
	{repository.ErrPlanNotFound, problem{http.StatusNotFound, "plan-not-found", "Slot plan not found"}},
	{slotplan.ErrNotDraft, problem{http.StatusConflict, "plan-not-draft", "Only a Draft plan can be approved or rejected"}},
	{repository.ErrApprovedPlanConflict, problem{http.StatusConflict, "approved-plan-conflict", "Another plan of this site was approved at the same time"}},
	{repository.ErrConcurrentModification, problem{http.StatusConflict, "concurrent-modification", "The resource was modified by another request; re-fetch the latest version and retry"}},
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
