package mcp

import (
	"errors"
	"fmt"
	"log/slog"

	"github.com/claudioed/slotting-optimization/internal/application/repository"
	"github.com/claudioed/slotting-optimization/internal/application/usecases"
	"github.com/claudioed/slotting-optimization/internal/domain/slotplan"
)

// errorCatalogue names each typed error a read tool can meet with the same
// stable slug the REST adapter uses as the last segment of its RFC 7807
// "type" URI (internal/adapters/inbound/http/errors.go), in the same match
// order, so a client sees one error vocabulary over both surfaces. Only the
// errors a READ can raise are listed.
var errorCatalogue = []struct {
	target error
	slug   string
}{
	{slotplan.ErrInvalidPlanID, "invalid-plan-id"},
	{slotplan.ErrInvalidSiteID, "invalid-site-id"},
	{usecases.ErrNoSite, "invalid-site-id"},
	{usecases.ErrInvalidLimit, "invalid-limit"},
	{usecases.ErrInvalidCursor, "invalid-cursor"},
	{usecases.ErrInvalidState, "invalid-state"},
	{repository.ErrPlanNotFound, "plan-not-found"},
}

// slugFor returns the REST slug of err; ok is false for an untyped error.
func slugFor(err error) (slug string, ok bool) {
	for _, entry := range errorCatalogue {
		if errors.Is(err, entry.target) {
			return entry.slug, true
		}
	}
	return "", false
}

// toolError builds a tool-level error "<slug>: <detail>". Returned from a
// handler it becomes an isError tool result, never a transport failure.
func toolError(slug, detail string) error {
	return fmt.Errorf("%s: %s", slug, detail)
}

// mapError turns a use-case/repository error into the tool error the model
// sees. Typed errors are prefixed with their REST slug and keep their
// message; anything else is logged and reported generically so
// infrastructure details (DSNs, SQL) never reach the model.
func mapError(err error) error {
	if slug, ok := slugFor(err); ok {
		return toolError(slug, err.Error())
	}
	slog.Error("mcp tool failed with an unexpected error", "error", err)
	return toolError("internal-error", "an unexpected internal error occurred")
}
