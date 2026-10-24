package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/claudioed/slotting-optimization/internal/application/ports"
	"github.com/claudioed/slotting-optimization/internal/domain/packlevel"
	"github.com/claudioed/slotting-optimization/internal/domain/planning"
	"github.com/claudioed/slotting-optimization/internal/domain/slotplan"
)

// PackLevelView is the pgxpool-backed ports.PackLevelView: the local copy of
// product-master's pack levels, one row per (SKU, level).
type PackLevelView struct {
	pool *pgxpool.Pool
}

var _ ports.PackLevelView = (*PackLevelView)(nil)

// NewPackLevelView constructs a PackLevelView over pool.
func NewPackLevelView(pool *pgxpool.Pool) *PackLevelView { return &PackLevelView{pool: pool} }

// dimsColumns are the four nullable columns of one dimension set.
type dimsColumns struct {
	length, width, height, weight *int64
}

func (c dimsColumns) dims() *packlevel.Dims {
	if c.length == nil || c.width == nil || c.height == nil || c.weight == nil {
		return nil
	}
	return &packlevel.Dims{LengthMM: *c.length, WidthMM: *c.width, HeightMM: *c.height, WeightG: *c.weight}
}

func columnsOf(d *packlevel.Dims) dimsColumns {
	if d == nil {
		return dimsColumns{}
	}
	return dimsColumns{&d.LengthMM, &d.WidthMM, &d.HeightMM, &d.WeightG}
}

// Get implements ports.PackLevelView.
func (v *PackLevelView) Get(ctx context.Context, sku string, level packlevel.Level) (packlevel.PackLevel, bool, error) {
	var (
		l             packlevel.PackLevel
		lvl           string
		declared      dimsColumns
		measured      dimsColumns
		effectiveFrom time.Time
		effectiveTo   *time.Time
	)
	err := queryFor(ctx, v.pool).QueryRow(ctx, `
		SELECT sku, level, gtin, units_per_pack,
		       declared_length_mm, declared_width_mm, declared_height_mm, declared_weight_g,
		       measured_length_mm, measured_width_mm, measured_height_mm, measured_weight_g,
		       effective_from, effective_to, version
		FROM pack_levels WHERE sku = $1 AND level = $2
	`, sku, string(level)).Scan(&l.SKU, &lvl, &l.GTIN, &l.UnitsPerPack,
		&declared.length, &declared.width, &declared.height, &declared.weight,
		&measured.length, &measured.width, &measured.height, &measured.weight,
		&effectiveFrom, &effectiveTo, &l.Version)
	if errors.Is(err, pgx.ErrNoRows) {
		return packlevel.PackLevel{}, false, nil
	}
	if err != nil {
		return packlevel.PackLevel{}, false, fmt.Errorf("read pack level: %w", err)
	}
	l.Level = packlevel.Level(lvl)
	l.Declared, l.Measured = declared.dims(), measured.dims()
	l.EffectiveFrom = effectiveFrom.UTC()
	if effectiveTo != nil {
		end := effectiveTo.UTC()
		l.EffectiveTo = &end
	}
	return l, true, nil
}

// Save implements ports.PackLevelView: an upsert keyed (sku, level).
func (v *PackLevelView) Save(ctx context.Context, l packlevel.PackLevel) error {
	var effectiveTo *time.Time
	if l.EffectiveTo != nil {
		end := l.EffectiveTo.UTC()
		effectiveTo = &end
	}
	d, m := columnsOf(l.Declared), columnsOf(l.Measured)
	_, err := queryFor(ctx, v.pool).Exec(ctx, `
		INSERT INTO pack_levels (sku, level, gtin, units_per_pack,
		    declared_length_mm, declared_width_mm, declared_height_mm, declared_weight_g,
		    measured_length_mm, measured_width_mm, measured_height_mm, measured_weight_g,
		    effective_from, effective_to, version)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15)
		ON CONFLICT (sku, level) DO UPDATE
		SET gtin = EXCLUDED.gtin, units_per_pack = EXCLUDED.units_per_pack,
		    declared_length_mm = EXCLUDED.declared_length_mm, declared_width_mm = EXCLUDED.declared_width_mm,
		    declared_height_mm = EXCLUDED.declared_height_mm, declared_weight_g = EXCLUDED.declared_weight_g,
		    measured_length_mm = EXCLUDED.measured_length_mm, measured_width_mm = EXCLUDED.measured_width_mm,
		    measured_height_mm = EXCLUDED.measured_height_mm, measured_weight_g = EXCLUDED.measured_weight_g,
		    effective_from = EXCLUDED.effective_from, effective_to = EXCLUDED.effective_to,
		    version = EXCLUDED.version, updated_at = now()
	`, l.SKU, string(l.Level), l.GTIN, l.UnitsPerPack,
		d.length, d.width, d.height, d.weight, m.length, m.width, m.height, m.weight,
		l.EffectiveFrom.UTC(), effectiveTo, l.Version)
	if err != nil {
		return fmt.Errorf("save pack level: %w", err)
	}
	return nil
}

// ActiveCases implements ports.PackLevelView.
func (v *PackLevelView) ActiveCases(ctx context.Context, skus []slotplan.SKU, at time.Time) (map[slotplan.SKU]planning.CaseSize, error) {
	keys := make([]string, len(skus))
	for i, s := range skus {
		keys[i] = string(s)
	}
	rows, err := queryFor(ctx, v.pool).Query(ctx, `
		SELECT sku, units_per_pack,
		       COALESCE(measured_length_mm, declared_length_mm)
		         * COALESCE(measured_width_mm, declared_width_mm)
		         * COALESCE(measured_height_mm, declared_height_mm),
		       COALESCE(measured_weight_g, declared_weight_g)
		FROM pack_levels
		WHERE level = 'CASE' AND sku = ANY($1)
		  AND effective_from <= $2 AND (effective_to IS NULL OR effective_to > $2)
		  AND COALESCE(measured_length_mm, declared_length_mm) IS NOT NULL
	`, keys, at.UTC())
	if err != nil {
		return nil, fmt.Errorf("active case levels: %w", err)
	}
	defer rows.Close()
	out := make(map[slotplan.SKU]planning.CaseSize, len(skus))
	for rows.Next() {
		var sku string
		var c planning.CaseSize
		if err := rows.Scan(&sku, &c.UnitsPerPack, &c.VolumeMM3, &c.WeightG); err != nil {
			return nil, fmt.Errorf("scan active case level: %w", err)
		}
		out[slotplan.SKU(sku)] = c
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read active case levels: %w", err)
	}
	return out, nil
}
