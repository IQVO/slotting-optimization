package ids

import (
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/claudioed/slotting-optimization/internal/domain/slotplan"
)

func TestUUIDMintsValidDistinctPlanIDs(t *testing.T) {
	a, b := UUID{}.NewPlanID(), UUID{}.NewPlanID()
	if a == b {
		t.Fatal("ids must be distinct")
	}
	if _, err := slotplan.NewPlanID(string(a)); err != nil {
		t.Fatalf("%q is not a valid plan id: %v", a, err)
	}
	if _, err := uuid.Parse(strings.TrimPrefix(string(a), "plan-")); err != nil {
		t.Fatalf("suffix of %q is not a uuid: %v", a, err)
	}
}
