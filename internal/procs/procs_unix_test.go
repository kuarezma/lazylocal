//go:build !windows

package procs

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/kuarezma/lazylocal/internal/model"
)

// TestHelperProcess is re-executed as a child process; it is a no-op in a
// normal test run. Mode "ignoreterm" swallows SIGTERM, mode "sleep" keeps the
// default SIGTERM disposition.
func TestHelperProcess(t *testing.T) {
	mode := os.Getenv("LAZYLOCAL_PROCS_HELPER")
	if mode == "" {
		return
	}
	if mode == "ignoreterm" {
		signal.Ignore(syscall.SIGTERM)
	}
	fmt.Println("ready")
	time.Sleep(time.Minute)
	os.Exit(0)
}

type child struct {
	cmd     *exec.Cmd
	reaped  chan struct{} // closed once cmd.Wait has returned
	waitErr error         // valid after reaped is closed
}

func startChild(t *testing.T, mode string, ownGroup bool) *child {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperProcess$")
	cmd.Env = append(os.Environ(), "LAZYLOCAL_PROCS_HELPER="+mode)
	if ownGroup {
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	c := &child{cmd: cmd, reaped: make(chan struct{})}
	// Wait must run after stdout is drained; the reaper goroutine is started
	// once the child reports readiness (signal handlers are installed by then).
	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil || line != "ready\n" {
		_ = cmd.Process.Kill()
		t.Fatalf("helper not ready: line=%q err=%v", line, err)
	}
	go func() {
		c.waitErr = cmd.Wait()
		close(c.reaped)
	}()
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		select {
		case <-c.reaped:
		case <-time.After(2 * time.Second):
		}
	})
	return c
}

func (c *child) service() model.Service {
	pgid := 0
	if p, err := syscall.Getpgid(c.cmd.Process.Pid); err == nil {
		pgid = p
	}
	return model.Service{PID: c.cmd.Process.Pid, PGID: pgid, UID: os.Getuid()}
}

// exitSignal returns the signal that terminated the child, failing if it did not die by a signal.
func (c *child) exitSignal(t *testing.T) syscall.Signal {
	t.Helper()
	select {
	case <-c.reaped:
		err := c.waitErr
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			t.Fatalf("child exit error = %v, want signal death", err)
		}
		ws, ok := exitErr.Sys().(syscall.WaitStatus)
		if !ok || !ws.Signaled() {
			t.Fatalf("child was not killed by a signal: %v", err)
		}
		return ws.Signal()
	case <-time.After(2 * time.Second):
		t.Fatal("child still running")
		return 0
	}
}

func (c *child) stillRunning() bool {
	select {
	case <-c.reaped:
		return false
	case <-time.After(150 * time.Millisecond):
		return true
	}
}

func TestKillServiceRefusesOtherUID(t *testing.T) {
	c := startChild(t, "sleep", false)
	svc := c.service()
	svc.UID = os.Getuid() + 1

	if err := KillService(svc, 100*time.Millisecond); err == nil {
		t.Fatal("expected error for foreign UID")
	}
	if !c.stillRunning() {
		t.Fatal("process owned by another UID must never be signalled")
	}
}

func TestKillServiceSIGTERMStopsCooperativeProcess(t *testing.T) {
	c := startChild(t, "sleep", true)
	start := time.Now()
	if err := KillService(c.service(), 2*time.Second); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed >= time.Second {
		t.Fatalf("cooperative process should die right after SIGTERM, took %v", elapsed)
	}
	if sig := c.exitSignal(t); sig != syscall.SIGTERM {
		t.Fatalf("died by %v, want SIGTERM", sig)
	}
}

func TestKillServiceEscalatesToSIGKILLAfterGracePeriod(t *testing.T) {
	const grace = 100 * time.Millisecond
	c := startChild(t, "ignoreterm", true)
	svc := c.service()
	if svc.PGID != svc.PID {
		t.Fatalf("child should lead its own group: pid=%d pgid=%d", svc.PID, svc.PGID)
	}

	start := time.Now()
	if err := KillService(svc, grace); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed < grace {
		t.Fatalf("returned after %v, before the %v grace period", elapsed, grace)
	}
	if sig := c.exitSignal(t); sig != syscall.SIGKILL {
		t.Fatalf("died by %v, want SIGKILL after SIGTERM was ignored", sig)
	}
}

func TestKillServiceNeverSignalsOwnProcessGroup(t *testing.T) {
	c := startChild(t, "sleep", false) // shares the test process's group
	svc := c.service()
	if svc.PGID != syscall.Getpgrp() {
		t.Skipf("child unexpectedly in another group (%d != %d)", svc.PGID, syscall.Getpgrp())
	}
	if err := KillService(svc, 2*time.Second); err != nil {
		t.Fatal(err)
	}
	if sig := c.exitSignal(t); sig != syscall.SIGTERM {
		t.Fatalf("died by %v, want SIGTERM", sig)
	}
	// Reaching this line proves the test process itself survived.
}

func TestKillServiceAlreadyGoneIsNotAnError(t *testing.T) {
	c := startChild(t, "sleep", true)
	svc := c.service()
	_ = c.cmd.Process.Kill()
	c.exitSignal(t)
	if err := KillService(svc, 100*time.Millisecond); err != nil {
		t.Fatalf("already-dead process should not error: %v", err)
	}
}

func TestKillServiceOnlySignalsPIDWhenPGIDNotLeader(t *testing.T) {
	leader := startChild(t, "sleep", true)
	leaderSvc := leader.service()
	if leaderSvc.PGID != leaderSvc.PID {
		t.Fatalf("leader must lead its group: pid=%d pgid=%d", leaderSvc.PID, leaderSvc.PGID)
	}

	// Start member process in the leader's process group
	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperProcess$")
	cmd.Env = append(os.Environ(), "LAZYLOCAL_PROCS_HELPER=sleep")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pgid: leaderSvc.PID}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Skipf("cannot set pgid to sibling (platform permission): %v", err)
		return
	}
	member := &child{cmd: cmd, reaped: make(chan struct{})}
	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil || line != "ready\n" {
		_ = cmd.Process.Kill()
		t.Fatalf("member helper not ready: line=%q err=%v", line, err)
	}
	go func() {
		member.waitErr = cmd.Wait()
		close(member.reaped)
	}()
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		select {
		case <-member.reaped:
		case <-time.After(2 * time.Second):
		}
	})

	memberSvc := member.service()
	if memberSvc.PGID != leaderSvc.PID {
		t.Fatalf("member PGID must be %d, got %d", leaderSvc.PID, memberSvc.PGID)
	}
	if memberSvc.PID == memberSvc.PGID {
		t.Fatalf("member PID must differ from PGID: pid=%d pgid=%d", memberSvc.PID, memberSvc.PGID)
	}

	// Killing member should NOT send signal to group, only to member.PID
	if err := KillService(memberSvc, 2*time.Second); err != nil {
		t.Fatal(err)
	}
	if sig := member.exitSignal(t); sig != syscall.SIGTERM {
		t.Fatalf("member died by %v, want SIGTERM", sig)
	}

	// Leader must STILL be running!
	if !leader.stillRunning() {
		t.Fatal("group leader was killed! Signal must only go to PID when PGID != PID")
	}
}

func TestKillServiceTOCTOUPIDReuseMismatch(t *testing.T) {
	c := startChild(t, "sleep", true)
	svc := c.service()
	svc.Cmd = "completely-different-process-name-12345"

	err := KillService(svc, 1*time.Second)
	if err == nil {
		t.Fatal("expected KillService to fail with PID reuse error when svc.Cmd mismatches live process")
	}
	if !strings.Contains(err.Error(), "PID reuse detected") && !strings.Contains(err.Error(), "TOCTOU") {
		t.Fatalf("expected TOCTOU PID reuse error, got %v", err)
	}

	if !c.stillRunning() {
		t.Fatal("child was killed despite TOCTOU mismatch!")
	}
}
