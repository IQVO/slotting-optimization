package ports

import "context"

// UnitOfWork runs fn as ONE atomic unit: every repository call fn makes
// with the ctx it is handed commits together when fn returns nil, or rolls
// back together when fn returns an error (or panics).
//
// Contract:
//   - fn MUST use the ctx passed to it (not the outer ctx) for every
//     repository call that should participate; the unit of work travels in
//     that ctx.
//   - Do is re-entrant: if ctx already carries an open unit of work, Do
//     JOINS it and the outermost Do owns commit/rollback.
//   - The error fn returns is returned from Do unchanged (errors.Is keeps
//     working); a commit failure is returned wrapped.
type UnitOfWork interface {
	Do(ctx context.Context, fn func(ctx context.Context) error) error
}
