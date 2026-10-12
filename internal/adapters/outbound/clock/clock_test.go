package clock

import (
	"testing"
	"time"
)

func TestSystemIsUTCNow(t *testing.T) {
	before := time.Now()
	got := System{}.Now()
	if got.Location() != time.UTC || got.Before(before.Add(-time.Second)) || got.After(time.Now().Add(time.Second)) {
		t.Fatalf("Now = %v", got)
	}
}

func TestSystemNowHasMicrosecondPrecision(t *testing.T) {
	clk := System{}
	for range 50 {
		if got := clk.Now(); got.Nanosecond()%1000 != 0 {
			t.Fatalf("Now = %v has sub-microsecond precision", got)
		}
	}
}
