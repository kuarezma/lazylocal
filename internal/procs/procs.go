// Package procs terminates discovered dev servers safely.
package procs

import (
	"errors"
	"fmt"
	"os"
	"syscall"
	"time"

	"github.com/ugur/lazylocal/internal/model"
)

// DefaultGracePeriod is how long KillService waits after SIGTERM before SIGKILL.
const DefaultGracePeriod = 3 * time.Second

const pollInterval = 20 * time.Millisecond

// KillService stops svc: SIGTERM first, then SIGKILL if it is still alive
// after gracePeriod (DefaultGracePeriod when <= 0). The signal goes to the
// whole process group when PGID > 1, otherwise to the PID alone.
//
// It refuses to touch processes owned by another UID, PID <= 1, lazylocal
// itself, and lazylocal's own process group.
func KillService(svc model.Service, gracePeriod time.Duration) error {
	if svc.UID != os.Getuid() {
		return fmt.Errorf("refusing to kill pid %d: owned by uid %d, not current uid %d", svc.PID, svc.UID, os.Getuid())
	}
	if svc.PID <= 1 {
		return fmt.Errorf("refusing to kill invalid pid %d", svc.PID)
	}
	if svc.PID == os.Getpid() {
		return errors.New("refusing to kill lazylocal itself")
	}
	if gracePeriod <= 0 {
		gracePeriod = DefaultGracePeriod
	}

	ownPGID := syscall.Getpgrp()
	target := svc.PID
	if svc.PGID > 1 && svc.PGID == svc.PID && svc.PGID != ownPGID {
		target = -svc.PGID
	}

	if err := syscall.Kill(target, syscall.SIGTERM); err != nil {
		if errors.Is(err, syscall.ESRCH) {
			return nil // already gone
		}
		return fmt.Errorf("send SIGTERM to %d: %w", target, err)
	}
	if waitGone(target, gracePeriod) {
		return nil
	}

	if err := syscall.Kill(target, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		return fmt.Errorf("send SIGKILL to %d: %w", target, err)
	}
	if !waitGone(target, gracePeriod) {
		return fmt.Errorf("pid %d still alive after SIGKILL", svc.PID)
	}
	return nil
}

// waitGone polls until target no longer exists or timeout elapses.
func waitGone(target int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for {
		if !alive(target) {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(pollInterval)
	}
}

// alive reports whether a process (or any member of a group, for negative
// target) exists. EPERM means it exists but is not signalable by us.
func alive(target int) bool {
	err := syscall.Kill(target, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
