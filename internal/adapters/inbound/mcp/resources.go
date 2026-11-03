package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// mimeTypeJSON is the MIME type of every resource this server serves.
const mimeTypeJSON = "application/json"

// forwardSlotsURITemplate is the ONE resource this server offers (Agentic
// Standard v2, AS-3): a scoped read model this context owns, not a dump of a
// table. It answers "where is each SKU slotted right now at this site?" —
// the assignments of the site's Approved plan, with that plan_id and
// approved_at — built from the same ListForwardSlots read use case the
// get_forward_slots tool and GET /forward-slots use. Bounded by
// construction: one site, one Approved plan, JSON text.
const forwardSlotsURITemplate = "warehouse://slotting-optimization/forward-slots/{siteId}"

// registerResources adds the resource surface (read-only by nature, L1; the
// charter keeps resources context data an agent can load without a tool
// call). No resource ever writes: Deps holds read use cases only (ADR 0005).
func (d Deps) registerResources(server *mcp.Server) {
	server.AddResourceTemplate(&mcp.ResourceTemplate{
		URITemplate: forwardSlotsURITemplate,
		Name:        "current forward slot map of a site",
		Description: "The assignments (sku, slot, abc_class, pick_unit, cases_capacity for CASE faces) of a site's Approved slot plan, with that plan_id and approved_at; a site without an Approved plan yields an empty assignment list and no plan_id, not an error.",
		MIMEType:    mimeTypeJSON,
	}, func(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		siteID, err := parseForwardSlotsURI(req.Params.URI)
		if err != nil {
			return nil, err
		}
		m, err := d.ListForwardSlots.Handle(ctx, siteID)
		if err != nil {
			return nil, mapError(err)
		}
		return resourceResult(req.Params.URI, toForwardSlots(m))
	})
}

// resourceResult marshals one read model into the resource's text contents.
func resourceResult(uri string, view any) (*mcp.ReadResourceResult, error) {
	body, err := json.Marshal(view)
	if err != nil {
		return nil, err
	}
	return &mcp.ReadResourceResult{
		Contents: []*mcp.ResourceContents{{
			URI:      uri,
			MIMEType: mimeTypeJSON,
			Text:     string(body),
		}},
	}, nil
}

// parseForwardSlotsURI extracts the siteId from a
// warehouse://slotting-optimization/forward-slots/{siteId} URI. The siteId is
// model-supplied and therefore untrusted: a malformed URI or an empty
// segment is rejected rather than silently defaulted to the service's
// default site (the tool keeps that convenience; a resource URI names its
// exact subject).
func parseForwardSlotsURI(uri string) (string, error) {
	const scheme = "warehouse://slotting-optimization/forward-slots/"
	rest, ok := strings.CutPrefix(uri, scheme)
	if !ok {
		return "", fmt.Errorf("resource uri %q must start with %q", uri, scheme)
	}
	if rest == "" || strings.Contains(rest, "/") {
		return "", fmt.Errorf("resource uri %q must carry exactly one non-empty siteId after %q", uri, scheme)
	}
	return rest, nil
}
