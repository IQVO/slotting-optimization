package usecases

import (
	"context"
	"fmt"
	"time"

	"github.com/claudioed/slotting-optimization/internal/application/ports"
	"github.com/claudioed/slotting-optimization/internal/domain/packlevel"
	"github.com/claudioed/slotting-optimization/internal/domain/slotplan"
)

// PackLevelConsumer namespaces the claims of the product-master pack-level
// consumer in ports.ProcessedEvents. It is not a Kafka consumer group id.
const PackLevelConsumer = "slotting-pack-level"

// PackLevelDeclaredChange is a decoded ProductPackLevelDeclared payload,
// reduced to what slotting acts on. Dims are the DECLARED dimensions, nil when
// product-master omitted them.
type PackLevelDeclaredChange struct {
	SKU           string
	Level         string
	GTIN          string
	UnitsPerPack  int64
	Dims          *packlevel.Dims
	EffectiveFrom time.Time
	Version       int64
}

// PackLevelRetiredChange is a decoded ProductPackLevelRetired payload.
type PackLevelRetiredChange struct {
	SKU         string
	Level       string
	GTIN        string
	EffectiveTo time.Time
	Version     int64
}

// PackLevelMeasuredChange is a decoded ProductPackLevelMeasured payload.
type PackLevelMeasuredChange struct {
	SKU     string
	Level   string
	GTIN    string
	Dims    packlevel.Dims
	Version int64
}

// ApplyPackLevelDeclared applies a ProductPackLevelDeclared to the local
// view.
type ApplyPackLevelDeclared struct {
	Intake
	PackLevels ports.PackLevelView
}

// Handle runs the use case. A version not greater than the stored one is
// OutcomeStale (a replay or a late message); a payload that can never be
// applied is ErrInvalidEvent. Re-declaring the same GTIN keeps its
// measurement; a new GTIN starts unmeasured.
func (uc *ApplyPackLevelDeclared) Handle(ctx context.Context, eventID string, c PackLevelDeclaredChange) (Outcome, error) {
	if _, err := slotplan.NewSKU(c.SKU); err != nil {
		return "", invalid(err)
	}
	incoming, err := packlevel.New(c.SKU, c.Level, c.GTIN, c.UnitsPerPack, c.Dims, c.EffectiveFrom, c.Version)
	if err != nil {
		return "", invalid(err)
	}
	return uc.apply(ctx, PackLevelConsumer, eventID, func(ctx context.Context) (Outcome, error) {
		cur, _, err := uc.PackLevels.Get(ctx, incoming.SKU, incoming.Level)
		if err != nil {
			return "", fmt.Errorf("read pack level: %w", err)
		}
		if !incoming.Supersedes(cur) {
			return OutcomeStale, nil
		}
		if err := uc.PackLevels.Save(ctx, incoming.Carry(cur)); err != nil {
			return "", fmt.Errorf("save pack level: %w", err)
		}
		return OutcomeApplied, nil
	})
}

// ApplyPackLevelRetired applies a ProductPackLevelRetired to the local view.
type ApplyPackLevelRetired struct {
	Intake
	PackLevels ports.PackLevelView
}

// Handle runs the use case. A retirement of a GTIN that is not the stored one
// (it was superseded already) or of a level never seen is OutcomeIgnored, one
// with a version not greater than the stored one is OutcomeStale; neither
// changes anything.
func (uc *ApplyPackLevelRetired) Handle(ctx context.Context, eventID string, c PackLevelRetiredChange) (Outcome, error) {
	level, err := validLevel(c.SKU, c.Level, c.GTIN, c.Version)
	if err != nil {
		return "", invalid(err)
	}
	return uc.apply(ctx, PackLevelConsumer, eventID, func(ctx context.Context) (Outcome, error) {
		cur, found, err := uc.PackLevels.Get(ctx, c.SKU, level)
		if err != nil {
			return "", fmt.Errorf("read pack level: %w", err)
		}
		if !found {
			return OutcomeIgnored, nil
		}
		retired, ok := cur.Retire(c.GTIN, c.EffectiveTo, c.Version)
		if !ok {
			return staleOrIgnored(cur, c.GTIN), nil
		}
		if err := uc.PackLevels.Save(ctx, retired); err != nil {
			return "", fmt.Errorf("save pack level: %w", err)
		}
		return OutcomeApplied, nil
	})
}

// ApplyPackLevelMeasured applies a ProductPackLevelMeasured to the local view:
// the measurement of the stored GTIN replaces the declared dimensions as the
// effective ones.
type ApplyPackLevelMeasured struct {
	Intake
	PackLevels ports.PackLevelView
}

// Handle runs the use case. A measurement of a level never seen or of a GTIN
// that is not the stored one is OutcomeIgnored; a version not greater than the
// stored one is OutcomeStale.
func (uc *ApplyPackLevelMeasured) Handle(ctx context.Context, eventID string, c PackLevelMeasuredChange) (Outcome, error) {
	level, err := validLevel(c.SKU, c.Level, c.GTIN, c.Version)
	if err != nil {
		return "", invalid(err)
	}
	if err := c.Dims.Validate(); err != nil {
		return "", invalid(err)
	}
	return uc.apply(ctx, PackLevelConsumer, eventID, func(ctx context.Context) (Outcome, error) {
		cur, found, err := uc.PackLevels.Get(ctx, c.SKU, level)
		if err != nil {
			return "", fmt.Errorf("read pack level: %w", err)
		}
		if !found {
			return OutcomeIgnored, nil
		}
		measured, ok := cur.Measure(c.GTIN, c.Dims, c.Version)
		if !ok {
			return staleOrIgnored(cur, c.GTIN), nil
		}
		if err := uc.PackLevels.Save(ctx, measured); err != nil {
			return "", fmt.Errorf("save pack level: %w", err)
		}
		return OutcomeApplied, nil
	})
}

// staleOrIgnored classifies a change the domain refused: for the stored GTIN
// it is a replay or a late message (stale), for any other GTIN it concerns a
// level that was superseded already (ignored).
func staleOrIgnored(cur packlevel.PackLevel, gtin string) Outcome {
	if gtin == cur.GTIN {
		return OutcomeStale
	}
	return OutcomeIgnored
}

// validLevel validates the SKU, level, GTIN and version every retirement and
// measurement carries, through the same domain constructor a declaration uses.
func validLevel(sku, level, gtin string, version int64) (packlevel.Level, error) {
	if _, err := slotplan.NewSKU(sku); err != nil {
		return "", err
	}
	probe, err := packlevel.New(sku, level, gtin, 1, nil, time.Time{}, version)
	if err != nil {
		return "", err
	}
	return probe.Level, nil
}
