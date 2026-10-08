// Package mcp is the inbound Model Context Protocol adapter: it exposes this
// bounded context to the AI ecosystem as a second driving adapter over the
// same application-layer read use cases the REST adapter uses. It is built on
// the official MCP Go SDK and served over Streamable HTTP only.
//
// Per docs/adr/0005-mcp-server-adoption.md the surface is READ-ONLY: every
// tool reads slotting data (get_slot_plan, list_slot_plans, get_forward_slots,
// get_sku_velocity); generating, approving and rejecting plans stay on REST,
// and governance_test.go fails the build on a write-verb tool name. This
// package depends inward on the application layer and the domain only --
// never on an outbound adapter or the REST adapter -- and nothing else may
// depend on it (internal/architecture's TestMCPAdapterDependencyRule). The
// composition root (cmd/mcp) wires concrete repositories into the use cases.
// There is no auth of any kind (fleet-wide revert 2026-09-11;
// TestNoAuthMiddlewareReintroduced).
package mcp

import (
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// serverName and serverVersion identify this server in the MCP initialize
// handshake.
const (
	serverName    = "slotting-optimization-mcp"
	serverVersion = "1.0.0"
)

// instructions is what an MCP client is told about this server at initialize.
const instructions = "Forward pick slot planning of the warehouse (read-only): read one slot plan with get_slot_plan " +
	"(state Draft/Approved/Rejected/Superseded, policy, demand window, the SKU-to-slot assignments with their ABC class, " +
	"the moves against the previous plan and the SKUs left unassigned) or page through plans, newest first, with " +
	"list_slot_plans (optionally by site and state); read the CURRENT forward slot map of a site (the assignments of its " +
	"Approved plan) with get_forward_slots; and see the demand the planner works from with get_sku_velocity (order-line " +
	"picks and units per SKU over a window). Nothing here changes data: generating a plan and approving or rejecting it " +
	"(the human decision) are REST operations of slotting-optimization."

// NewServer builds the MCP server for this bounded context with every tool
// registered.
func NewServer(deps Deps) *mcp.Server {
	server := mcp.NewServer(
		&mcp.Implementation{Name: serverName, Version: serverVersion},
		&mcp.ServerOptions{Instructions: instructions},
	)
	deps.registerTools(server)
	return server
}

// Handler returns the Streamable HTTP handler for the MCP server.
func Handler(server *mcp.Server) http.Handler {
	return mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, nil)
}
