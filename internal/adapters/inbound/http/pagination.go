package http

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/claudioed/slotting-optimization/internal/adapters/inbound/http/page"
	"github.com/claudioed/slotting-optimization/internal/domain/planning"
	"github.com/claudioed/slotting-optimization/internal/domain/slotplan"
)

// errUnknownQueryParameter backs the 400 unknown-query-parameter problem
// (REST guideline 4.2): a retired paging parameter must fail loudly.
var errUnknownQueryParameter = fmt.Errorf("unknown query parameter")

func errUnknownQuery(name string) error {
	return fmt.Errorf("%w: %q is not a parameter of this operation", errUnknownQueryParameter, name)
}

// pageParams validates the paging query of a collection GET: no undeclared
// parameter (filters are the declared ones besides limit and cursor), a single
// integer limit in 1-500 (default 100) and at most one cursor. The cursor's
// content is decoded by whoever owns its format.
func pageParams(r *http.Request, filters ...string) (int, error) {
	if name := page.Unknown(r, filters...); name != "" {
		return 0, errUnknownQuery(name)
	}
	if len(r.URL.Query()["cursor"]) > 1 {
		return 0, page.ErrInvalidCursor
	}
	return page.Limit(r)
}

// velocityCursor is the opaque cursor after row v: Sort carries picks and
// units, ID the sku, the keyset of the (picks DESC, units DESC, sku ASC) order.
func velocityCursor(v planning.SkuVelocity) string {
	return page.Encode(page.Position{
		Sort: strconv.FormatInt(v.Picks, 10) + ":" + strconv.FormatInt(v.Units, 10),
		ID:   string(v.SKU),
	})
}

// velocityAfterParam decodes the request's cursor into the row the page
// starts after; nil is the first page.
func velocityAfterParam(r *http.Request) (*planning.SkuVelocity, error) {
	pos, err := page.After(r)
	if err != nil || pos == nil {
		return nil, err
	}
	picks, units, ok := strings.Cut(pos.Sort, ":")
	p, perr := strconv.ParseInt(picks, 10, 64)
	u, uerr := strconv.ParseInt(units, 10, 64)
	if !ok || perr != nil || uerr != nil || p < 0 || u < 0 {
		return nil, page.ErrInvalidCursor
	}
	return &planning.SkuVelocity{SKU: slotplan.SKU(pos.ID), Picks: p, Units: u}, nil
}
