package ports

import "time"

// Clock is the service's notion of "now". Use cases stamp events and
// plan windows with it.
type Clock interface {
	Now() time.Time
}
