//go:build !windows

package procs

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/kuarezma/lazylocal/internal/model"
)

// verifyProcessIdentity prevents TOCTOU race conditions / PID reuse by verifying
// that the process still exists and its executable command matches svc.Cmd.
func verifyProcessIdentity(svc model.Service) error {
	cmdStr := svc.Cmd
	if cmdStr == "" {
		cmdStr = svc.Command
	}
	if cmdStr == "" || svc.PID <= 1 {
		return nil
	}

	out, err := exec.Command("ps", "-p", strconv.Itoa(svc.PID), "-o", "comm=").Output()
	if err != nil {
		return fmt.Errorf("process pid %d no longer exists or cannot be inspected: %w", svc.PID, err)
	}
	liveComm := strings.TrimSpace(string(out))
	if liveComm == "" {
		return fmt.Errorf("process pid %d has terminated (empty command name)", svc.PID)
	}

	expected := strings.TrimSpace(cmdStr)
	fields := strings.Fields(expected)
	if len(fields) == 0 {
		return nil
	}

	expectedBase := strings.ToLower(filepath.Base(fields[0]))
	liveBase := strings.ToLower(filepath.Base(liveComm))

	if expectedBase != liveBase &&
		!strings.Contains(strings.ToLower(expected), liveBase) &&
		!strings.Contains(strings.ToLower(liveComm), expectedBase) {
		return fmt.Errorf("PID reuse detected (TOCTOU violation): pid %d expected command %q but found %q", svc.PID, cmdStr, liveComm)
	}
	return nil
}

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

	if err := verifyProcessIdentity(svc); err != nil {
		if strings.Contains(err.Error(), "no longer exists") || strings.Contains(err.Error(), "has terminated") {
			return nil // already gone
		}
		return err
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
