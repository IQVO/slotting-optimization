package http

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/claudioed/slotting-optimization/internal/domain/slotplan"
)

func requestWithPlanID(raw string) *http.Request {
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("planId", raw)
	r := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
	return r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx))
}

func TestPlanIDParamUnescapesAndRejectsBadEscapes(t *testing.T) {
	got, err := planIDParam(requestWithPlanID("plan-a%20b"))
	if err != nil || got != "plan-a b" {
		t.Fatalf("got %q, %v", got, err)
	}
	if _, err := planIDParam(requestWithPlanID("plan-%zz")); !errors.Is(err, slotplan.ErrInvalidPlanID) {
		t.Fatalf("err = %v", err)
	}
}

func TestProblemForUnmappedErrorIsInternal(t *testing.T) {
	if p := problemFor(errors.New("boom")); p != internalProblem {
		t.Fatalf("problem = %+v", p)
	}
}

func TestCaptureRecordsTheFirstStatusAndDefaultsTo200(t *testing.T) {
	c := &capture{header: http.Header{}}
	_, _ = c.Write([]byte("x"))
	c.WriteHeader(http.StatusTeapot) // too late: the first status wins
	if c.code != http.StatusOK || c.body.String() != "x" {
		t.Fatalf("code %d body %q", c.code, c.body.String())
	}
	d := &capture{header: http.Header{}}
	d.WriteHeader(http.StatusCreated)
	d.WriteHeader(http.StatusOK)
	if d.code != http.StatusCreated {
		t.Fatalf("code %d", d.code)
	}
}

func TestStoredHeadersAreCopiedAndGarbageIsIgnored(t *testing.T) {
	dst := http.Header{}
	copyStoredHeaders(dst, []byte(`{"Location":["/slot-plans/plan-1"],"Content-Type":["application/json"]}`))
	if dst.Get("Location") != "/slot-plans/plan-1" || dst.Get("Content-Type") != "application/json" {
		t.Fatalf("headers = %v", dst)
	}
	empty := http.Header{}
	copyStoredHeaders(empty, nil)
	copyStoredHeaders(empty, []byte("not json"))
	if len(empty) != 0 {
		t.Fatalf("headers = %v", empty)
	}
	if errMissingOutcome.Error() == "" {
		t.Fatal("the defensive error needs a message")
	}
}
