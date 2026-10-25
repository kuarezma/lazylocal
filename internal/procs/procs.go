// Package procs terminates discovered dev servers safely.
package procs

import (
	"time"
)

// DefaultGracePeriod is how long KillService waits after gentle termination before forceful kill.
const DefaultGracePeriod = 3 * time.Second

const pollInterval = 20 * time.Millisecond
