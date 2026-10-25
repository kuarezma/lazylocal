package procs

import (
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/kuarezma/lazylocal/internal/model"
)

func TestKillServiceRefusesInvalidPID(t *testing.T) {
	for _, pid := range []int{-1, 0, 1} {
		t.Run(fmt.Sprintf("pid=%d", pid), func(t *testing.T) {
			svc := model.Service{PID: pid, PGID: pid, UID: os.Getuid()}
			if err := KillService(svc, 100*time.Millisecond); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func TestKillServiceRefusesSelf(t *testing.T) {
	svc := model.Service{PID: os.Getpid(), UID: os.Getuid()}
	if err := KillService(svc, 100*time.Millisecond); err == nil {
		t.Fatal("expected error when targeting lazylocal itself")
	}
}
