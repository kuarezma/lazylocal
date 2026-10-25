//go:build windows

package procs

import (
	"os"
	"testing"
	"time"

	"github.com/ugur/lazylocal/internal/model"
)

func TestKillServiceWindowsRefusesOtherUID(t *testing.T) {
	// If os.Getuid() is -1, test with different UID if currentUID != -1
	currentUID := os.Getuid()
	if currentUID != -1 {
		svc := model.Service{PID: 12345, UID: currentUID + 1}
		if err := KillService(svc, 100*time.Millisecond); err == nil {
			t.Fatal("expected error for foreign UID on Windows")
		}
	}
}

func TestWindowsProcessIsAlive(t *testing.T) {
	// Current process must be alive
	if !isAlive(os.Getpid()) {
		t.Fatalf("current process %d should be reported as alive", os.Getpid())
	}

	// Non-existent PID must not be alive
	if isAlive(99999999) {
		t.Fatal("PID 99999999 should not be reported as alive")
	}
}
