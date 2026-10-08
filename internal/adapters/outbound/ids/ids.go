// Package ids is the adapter of ports.IDGenerator: "plan-" plus a UUID v4.
package ids

import (
	"github.com/google/uuid"

	"github.com/claudioed/slotting-optimization/internal/application/ports"
	"github.com/claudioed/slotting-optimization/internal/domain/slotplan"
)

// UUID mints plan ids as "plan-<uuid v4>".
type UUID struct{}

var _ ports.IDGenerator = UUID{}

// NewPlanID implements ports.IDGenerator.
func (UUID) NewPlanID() slotplan.PlanID { return slotplan.PlanID("plan-" + uuid.NewString()) }
