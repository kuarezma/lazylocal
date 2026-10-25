//go:build windows

package procs

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"syscall"
	"time"

	"github.com/kuarezma/lazylocal/internal/model"
)

// KillService stops svc on Windows: gentle termination via taskkill (/T) first,
// then forceful termination via taskkill (/F /T) and os.Process.Kill if it is still
// alive after gracePeriod (DefaultGracePeriod when <= 0).
//
// It refuses to touch processes owned by another UID, PID <= 1, and lazylocal itself.
func KillService(svc model.Service, gracePeriod time.Duration) error {
	currentUID := os.Getuid()
	if svc.UID != -1 && currentUID != -1 && svc.UID != currentUID {
		return fmt.Errorf("refusing to kill pid %d: owned by uid %d, not current uid %d", svc.PID, svc.UID, currentUID)
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

	pidStr := strconv.Itoa(svc.PID)

	// Check if already gone
	if !isAlive(svc.PID) {
		return nil
	}

	// 1. Try gentle termination first: taskkill /PID <pid> /T
	_ = exec.Command("taskkill", "/PID", pidStr, "/T").Run()

	if waitGone(svc.PID, gracePeriod) {
		return nil
	}

	// 2. Escalate to forceful termination: taskkill /F /PID <pid> /T
	_ = exec.Command("taskkill", "/F", "/PID", pidStr, "/T").Run()

	// Fallback directly through os.FindProcess -> p.Kill()
	if p, err := os.FindProcess(svc.PID); err == nil {
		_ = p.Kill()
	}

	if !waitGone(svc.PID, gracePeriod) {
		return fmt.Errorf("pid %d still alive after taskkill /F", svc.PID)
	}
	return nil
}

// waitGone polls until process with pid no longer exists or timeout elapses.
func waitGone(pid int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for {
		if !isAlive(pid) {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(pollInterval)
	}
}

// isAlive checks whether a process is still active on Windows.
func isAlive(pid int) bool {
	h, err := syscall.OpenProcess(syscall.PROCESS_QUERY_INFORMATION, false, uint32(pid))
	if err != nil {
		if errors.Is(err, syscall.ERROR_ACCESS_DENIED) {
			return true
		}
		return false
	}
	defer syscall.CloseHandle(h)

	var exitCode uint32
	if err := syscall.GetExitCodeProcess(h, &exitCode); err != nil {
		return false
	}
	const stillActive = 259
	return exitCode == stillActive
}
