package http

import "testing"

func TestReadinessGateIsNilSafeAndDefaultsToReady(t *testing.T) {
	var nilGate *Readiness
	nilGate.SetNotReady() // must not panic
	if !nilGate.Ready() {
		t.Fatal("a nil gate is ready")
	}
	g := &Readiness{}
	if !g.Ready() {
		t.Fatal("the zero value is ready")
	}
	g.SetNotReady()
	if g.Ready() {
		t.Fatal("not ready after SetNotReady")
	}
}

func TestCORSOriginsDefaultAndOverride(t *testing.T) {
	t.Setenv("CORS_ALLOWED_ORIGINS", "")
	if got := corsAllowedOrigins(); len(got) != 1 || got[0] != "http://localhost:5173" {
		t.Fatalf("default = %v", got)
	}
	t.Setenv("CORS_ALLOWED_ORIGINS", "http://a.example, http://b.example")
	if got := corsAllowedOrigins(); len(got) != 2 || got[1] != "http://b.example" {
		t.Fatalf("override = %v", got)
	}
}
