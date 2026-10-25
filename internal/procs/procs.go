// Package procs terminates discovered dev servers safely.
package procs

import (
	"time"

	"github.com/kuarezma/lazylocal/internal/model"
)

// DefaultGracePeriod is how long KillService waits after gentle termination before forceful kill.
const DefaultGracePeriod = 3 * time.Second

const pollInterval = 20 * time.Millisecond

// Kill stops svc using DefaultGracePeriod.
func Kill(svc model.Service) error {
	return KillService(svc, DefaultGracePeriod)
}
