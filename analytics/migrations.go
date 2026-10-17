// Package analytics holds the ANALYTICAL database's schema (ADR 0006): the
// golang-migrate files under migrations/, embedded so cmd/slotting-projector
// applies them without a migrations directory in the image (the same
// approach as the OLTP migrations in internal/adapters/outbound/postgres).
// The analytical database is separate from the OLTP one; it is written only
// by cmd/slotting-projector and read (read-only) by cmd/slotting-reports.
package analytics

import "embed"

// Migrations is the analytical schema: migrations/*.{up,down}.sql.
//
//go:embed migrations/*.sql
var Migrations embed.FS
