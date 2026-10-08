package main

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	inboundmcp "github.com/claudioed/slotting-optimization/internal/adapters/inbound/mcp"
)

func quietLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// newTestServer wires the binary exactly as run() does, over the in-memory
// repositories selected by an empty DATABASE_URL, behind the real router.
func newTestServer(t *testing.T, site string) *httptest.Server {
	t.Helper()
	deps, closeFn, err := buildDeps(context.Background(), quietLogger(), "", "", site)
	if err != nil {
		t.Fatalf("buildDeps: %v", err)
	}
	t.Cleanup(closeFn)
	srv := httptest.NewServer(newRouter(inboundmcp.Handler(inboundmcp.NewServer(deps))))
	t.Cleanup(srv.Close)
	return srv
}

func TestRouter_HealthzIsOpenAndServesOK(t *testing.T) {
	srv := newTestServer(t, "SITE-1")
	resp, err := http.Get(srv.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || string(body) != `{"status":"ok"}` {
		t.Fatalf("GET /healthz = %d %q, want 200 {\"status\":\"ok\"}", resp.StatusCode, body)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", ct)
	}
}

// Both mount points speak Streamable HTTP, with no credentials, and a real
// client can list the four read-only tools and call them end to end.
func TestStreamableHTTP_RootAndMCPPathsServeTools(t *testing.T) {
	srv := newTestServer(t, "SITE-1")
	for _, path := range []string{"/", "/mcp"} {
		t.Run(path, func(t *testing.T) { exerciseEndpoint(t, srv.URL+path) })
	}
}

// exerciseEndpoint connects a real Streamable HTTP client to endpoint, lists
// the tools (four, all read-only) and calls each of them on an empty store.
func exerciseEndpoint(t *testing.T, endpoint string) {
	t.Helper()
	client := sdk.NewClient(&sdk.Implementation{Name: "t", Version: "0"}, nil)
	session, err := client.Connect(context.Background(), &sdk.StreamableClientTransport{Endpoint: endpoint}, nil)
	if err != nil {
		t.Fatalf("connect %s: %v", endpoint, err)
	}
	defer session.Close()

	tools, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	if len(tools.Tools) != 4 {
		t.Fatalf("tools = %d, want 4", len(tools.Tools))
	}
	for _, tool := range tools.Tools {
		if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint {
			t.Fatalf("tool %q is not read-only", tool.Name)
		}
	}

	for _, name := range []string{"list_slot_plans", "get_forward_slots", "get_sku_velocity"} {
		res, err := session.CallTool(context.Background(), &sdk.CallToolParams{Name: name, Arguments: map[string]any{}})
		if err != nil || res.IsError {
			t.Fatalf("%s: err=%v res=%+v", name, err, res)
		}
	}
	res, err := session.CallTool(context.Background(), &sdk.CallToolParams{
		Name: "get_slot_plan", Arguments: map[string]any{"plan_id": "plan-00000000-0000-4000-8000-0000000000ff"},
	})
	if err != nil || !res.IsError {
		t.Fatalf("get_slot_plan on an empty store: err=%v res=%+v, want an isError result", err, res)
	}
}

// The binary reads no API key: requests carrying (or lacking) any
// Authorization header behave identically.
func TestNoAuthIsRequired(t *testing.T) {
	srv := newTestServer(t, "SITE-1")
	body := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"t","version":"0"}}}`
	for _, auth := range []string{"", "Bearer nonsense"} {
		req, _ := http.NewRequest(http.MethodPost, srv.URL+"/mcp", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("initialize with Authorization=%q = %d, want 200", auth, resp.StatusCode)
		}
	}
}

// An unreachable database is a boot error after the bounded retries, never
// a silent fallback to the in-memory repositories.
func TestBuildDeps_BadDatabaseFailsBoot(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // a cancelled context makes bootretry give up at once
	if _, _, err := buildDeps(ctx, quietLogger(), "postgres://u@127.0.0.1:1/db", "postgres://u@127.0.0.1:1/db", "SITE-1"); err == nil {
		t.Fatal("an unreachable database must fail boot")
	}
}

// The default site reaches the site-scoped use cases: with one the site-less
// tools answer, without one they refuse.
func TestBuildDeps_PassesTheDefaultSite(t *testing.T) {
	with, closeWith, err := buildDeps(context.Background(), quietLogger(), "", "", "SITE-7")
	if err != nil {
		t.Fatal(err)
	}
	defer closeWith()
	m, err := with.ListForwardSlots.Handle(context.Background(), "")
	if err != nil || m.Site != "SITE-7" {
		t.Fatalf("forward slots = %+v, %v; want site SITE-7", m, err)
	}

	without, closeWithout, err := buildDeps(context.Background(), quietLogger(), "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	defer closeWithout()
	if _, err := without.ListForwardSlots.Handle(context.Background(), ""); err == nil {
		t.Fatal("no site and no default site must be refused")
	}
}

func TestDefaultSite(t *testing.T) {
	for name, tc := range map[string]struct{ demand, fallback, want string }{
		"neither":     {"", "", ""},
		"demand site": {"SITE-1", "", "SITE-1"},
		"fallback":    {"", "SITE-2", "SITE-2"},
		"demand wins": {"SITE-1", "SITE-2", "SITE-1"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("DEMAND_SITE_ID", tc.demand)
			t.Setenv("DEFAULT_SITE_ID", tc.fallback)
			if got := defaultSite(); got != tc.want {
				t.Fatalf("defaultSite() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestGetenv(t *testing.T) {
	t.Setenv("SO_MCP_TEST_SET", "v")
	t.Setenv("SO_MCP_TEST_EMPTY", "")
	if got := getenv("SO_MCP_TEST_SET", "d"); got != "v" {
		t.Errorf("set: %q", got)
	}
	if got := getenv("SO_MCP_TEST_EMPTY", "d"); got != "d" {
		t.Errorf("empty: %q", got)
	}
}

func TestNewLoggerLevels(t *testing.T) {
	for level, want := range map[string]slog.Level{"debug": slog.LevelDebug, "WARN": slog.LevelWarn, "warning": slog.LevelWarn, "error": slog.LevelError, "": slog.LevelInfo, "bogus": slog.LevelInfo} {
		if got := newLogger(level); !got.Handler().Enabled(context.Background(), want) || (want > slog.LevelDebug && got.Handler().Enabled(context.Background(), want-1)) {
			t.Errorf("newLogger(%q): level %v not the minimum enabled", level, want)
		}
	}
}
