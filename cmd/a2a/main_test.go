package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2aclient"

	inbounda2a "github.com/claudioed/slotting-optimization/internal/adapters/inbound/a2a"
	"github.com/claudioed/slotting-optimization/internal/adapters/outbound/memory"
)

const testAgentURL = "http://slotting-optimization-a2a.warehouse-systems.svc.cluster.local:8093/"

func quietLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// newTestRouter wires the read use cases (depsOver, the same function run()
// uses) over the in-memory TEST DOUBLES, behind the real router and the real
// Agent Card. The Postgres wiring is proven in main_integration_test.go.
func newTestRouter(site string) http.Handler {
	deps := depsOver(memory.NewSlotPlanRepo(), memory.NewMoveExecutionLedger(), site)
	card := inbounda2a.Card(inbounda2a.Config{AgentURL: testAgentURL, Version: "test"})
	return newRouter(inbounda2a.Handler(inbounda2a.NewExecutor(deps)), card)
}

func newTestServer(t *testing.T, site string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(newTestRouter(site))
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

func TestRouter_ServesTheAgentCardAtTheWellKnownPath(t *testing.T) {
	srv := newTestServer(t, "SITE-1")
	resp, err := http.Get(srv.URL + "/.well-known/agent-card.json")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET agent card: got %d, want 200", resp.StatusCode)
	}
	for _, want := range []string{`"name":"slotting-optimization"`, testAgentURL, `"forward_slot_map"`, `"slot_plan_review"`} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("agent card lacks %s: %s", want, raw)
		}
	}
	if strings.Contains(string(raw), "securitySchemes") {
		t.Errorf("the card must declare no security schemes: %s", raw)
	}
}

// exerciseEndpoint drives a real A2A client against endpoint: both mount
// points (the fleet's <CTX>_A2A_ENDPOINT convention is "/a2a") answer a skill
// over the real JSON-RPC wire, in-band, with no credentials.
func exerciseEndpoint(t *testing.T, endpoint string) {
	t.Helper()
	card := inbounda2a.Card(inbounda2a.Config{AgentURL: endpoint, Version: "test"})
	client, err := a2aclient.NewFromCard(context.Background(), card)
	if err != nil {
		t.Fatalf("client for %s: %v", endpoint, err)
	}
	send := func(args map[string]any) string {
		t.Helper()
		resp, err := client.SendMessage(context.Background(), &a2a.SendMessageRequest{
			Message: a2a.NewMessage(a2a.MessageRoleUser, a2a.NewDataPart(args)),
		})
		if err != nil {
			t.Fatalf("SendMessage %s: %v", endpoint, err)
		}
		msg, ok := resp.(*a2a.Message)
		if !ok || len(msg.Parts) < 2 {
			t.Fatalf("reply = %T, want a message with a data and a text part", resp)
		}
		text, _ := msg.Parts[1].Content.(a2a.Text)
		return string(text)
	}
	if got := send(map[string]any{"skill": "forward_slot_map", "arguments": map[string]any{}}); !strings.Contains(got, "SITE-1") || !strings.Contains(got, "no Approved slot plan") {
		t.Errorf("forward_slot_map on an empty store = %q", got)
	}
	if got := send(map[string]any{"skill": "slot_plan_review", "arguments": map[string]any{}}); !strings.Contains(got, "(404)") {
		t.Errorf("slot_plan_review on an empty store = %q, want the in-band 404 problem", got)
	}
}

func TestJSONRPC_RootAndA2APathsServeSkills(t *testing.T) {
	srv := newTestServer(t, "SITE-1")
	for _, path := range []string{"/", "/a2a"} {
		t.Run(path, func(t *testing.T) { exerciseEndpoint(t, srv.URL+path) })
	}
}

// The binary reads no API key: requests carrying (or lacking) any
// Authorization header behave identically.
func TestNoAuthIsRequired(t *testing.T) {
	srv := newTestServer(t, "SITE-1")
	body := `{"jsonrpc":"2.0","id":1,"method":"SendMessage","params":{"message":{"messageId":"m1","role":"ROLE_USER","parts":[{"data":{"skill":"launch_rocket"}}]}}}`
	for _, auth := range []string{"", "Bearer nonsense"} {
		req, _ := http.NewRequest(http.MethodPost, srv.URL+"/a2a", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("A2A-Version", "1.0")
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK || !strings.Contains(string(raw), "unknown-skill") {
			t.Fatalf("SendMessage with Authorization=%q = %d %s, want 200 and the in-band unknown-skill problem", auth, resp.StatusCode, raw)
		}
	}
}

// An unreachable database is a boot error after the bounded retries, never
// a silent fallback.
func TestBuildDeps_BadDatabaseFailsBoot(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // a cancelled context makes bootretry give up at once
	if _, _, err := buildDeps(ctx, quietLogger(), "postgres://u@127.0.0.1:1/db", "postgres://u@127.0.0.1:1/db", "SITE-1"); err == nil {
		t.Fatal("an unreachable database must fail boot")
	}
}

func setBootEnv(t *testing.T) {
	t.Helper()
	t.Setenv("DATABASE_URL", "postgres://u@127.0.0.1:1/db?sslmode=disable")
	t.Setenv("A2A_AGENT_URL", testAgentURL)
}

func TestLoadConfigAcceptsACompleteEnvironment(t *testing.T) {
	setBootEnv(t)
	cfg, err := loadConfig(func(k string) string {
		return map[string]string{"DATABASE_URL": "postgres://u@db/x", "A2A_AGENT_URL": " " + testAgentURL + " "}[k]
	})
	if err != nil || cfg.databaseURL != "postgres://u@db/x" || cfg.agentURL != testAgentURL {
		t.Fatalf("config = %+v, %v", cfg, err)
	}
}

// Each missing or rejected value is a boot error that names its variable;
// there is no in-memory runtime, so an empty DATABASE_URL is refused too.
func TestLoadConfigRefusesEachMissingOrRejectedValue(t *testing.T) {
	cases := []struct {
		name, variable, value, want string
	}{
		{"database unset", "DATABASE_URL", "", "DATABASE_URL is required: this context runs on real Postgres"},
		{"agent url unset", "A2A_AGENT_URL", "", "A2A_AGENT_URL is required: the Agent Card must advertise"},
		{"agent url blank", "A2A_AGENT_URL", "   ", "A2A_AGENT_URL is required"},
		{"agent url relative", "A2A_AGENT_URL", "/a2a", `A2A_AGENT_URL="/a2a" is not an absolute http(s) URL`},
		{"agent url schemeless", "A2A_AGENT_URL", "slotting-optimization-a2a:8093", "is not an absolute http(s) URL"},
		{"agent url wrong scheme", "A2A_AGENT_URL", "ftp://host/", "is not an absolute http(s) URL"},
		{"agent url without host", "A2A_AGENT_URL", "http://", "is not an absolute http(s) URL"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := map[string]string{"DATABASE_URL": "postgres://u@db/x", "A2A_AGENT_URL": testAgentURL}
			env[tc.variable] = tc.value
			_, err := loadConfig(func(k string) string { return env[k] })
			if err == nil || !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), tc.variable) {
				t.Fatalf("%s=%q: %v, want a boot error naming the variable and containing %q", tc.variable, tc.value, err, tc.want)
			}
		})
	}
}

// Every problem is reported at once.
func TestLoadConfigReportsEveryProblem(t *testing.T) {
	_, err := loadConfig(func(string) string { return "" })
	if !errors.Is(err, errMissingDatabaseURL) || !errors.Is(err, errNoAgentURL) {
		t.Fatalf("loadConfig(empty) = %v, want both errMissingDatabaseURL and errNoAgentURL", err)
	}
	if _, _, err := buildDeps(context.Background(), quietLogger(), "", "", "SITE-1"); !errors.Is(err, errMissingDatabaseURL) {
		t.Fatalf("buildDeps(\"\") = %v, want errMissingDatabaseURL", err)
	}
}

// run fails fast on a bad configuration: before telemetry, any connection and
// before the listener binds.
func TestRunFailsFastBeforeBindingTheListener(t *testing.T) {
	setBootEnv(t)
	t.Setenv("A2A_AGENT_URL", "")
	l, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	_ = l.Close()
	t.Setenv("A2A_ADDR", addr)

	start := time.Now()
	if err := run(); err == nil || !strings.Contains(err.Error(), "A2A_AGENT_URL is required") {
		t.Fatalf("run = %v, want a boot error naming A2A_AGENT_URL", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("a configuration error must not wait on any dependency")
	}
	if c, derr := (&net.Dialer{Timeout: 200 * time.Millisecond}).DialContext(t.Context(), "tcp", addr); derr == nil {
		_ = c.Close()
		t.Fatal("the listener was bound although the configuration is invalid")
	}
}

// The default site reaches the site-scoped use case: with one the site-less
// skill answers, without one it refuses.
func TestDepsOver_PassesTheDefaultSite(t *testing.T) {
	with := depsOver(memory.NewSlotPlanRepo(), memory.NewMoveExecutionLedger(), "SITE-7")
	m, err := with.ListForwardSlots.Handle(context.Background(), "")
	if err != nil || m.Site != "SITE-7" {
		t.Fatalf("forward slots = %+v, %v; want site SITE-7", m, err)
	}

	without := depsOver(memory.NewSlotPlanRepo(), memory.NewMoveExecutionLedger(), "")
	if _, err := without.ListForwardSlots.Handle(context.Background(), ""); err == nil {
		t.Fatal("no site and no default site must be refused")
	}
}

// The Postgres wiring hands the use cases the READ ports only: the deps carry
// exactly the three read use cases (the adapter's Deps type cannot hold a
// write one -- governance_test in the adapter package pins that).
func TestDepsOver_WiresEveryReadUseCase(t *testing.T) {
	deps := depsOver(memory.NewSlotPlanRepo(), memory.NewMoveExecutionLedger(), "SITE-1")
	if deps.GetPlan == nil || deps.ListPlans == nil || deps.ListForwardSlots == nil {
		t.Fatalf("deps = %+v, want all three read use cases wired", deps)
	}
	if deps.GetPlan.Executions == nil {
		t.Error("GetPlan must carry the execution ledger so slot_plan_review reports execution progress (ADR 0010)")
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
	t.Setenv("SO_A2A_TEST_SET", "v")
	t.Setenv("SO_A2A_TEST_EMPTY", "")
	if got := getenv("SO_A2A_TEST_SET", "d"); got != "v" {
		t.Errorf("set: %q", got)
	}
	if got := getenv("SO_A2A_TEST_EMPTY", "d"); got != "d" {
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
