package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	inboundhttp "github.com/claudioed/slotting-optimization/internal/adapters/inbound/http"
)

func routerFor(s *inboundhttp.Server) http.Handler { return inboundhttp.NewRouter(s) }

type reply struct {
	id   string
	body string
}

// call performs one request against srv and fails unless it returned want.
func call(t *testing.T, srv *httptest.Server, method, path string, want int) reply {
	t.Helper()
	req, err := http.NewRequest(method, srv.URL+path, strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != want {
		t.Fatalf("%s %s = %d %s, want %d", method, path, resp.StatusCode, raw, want)
	}
	var parsed struct {
		PlanID string `json:"planId"`
	}
	_ = json.Unmarshal(raw, &parsed)
	return reply{id: parsed.PlanID, body: string(raw)}
}
