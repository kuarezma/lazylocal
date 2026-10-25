package discover

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/ugur/lazylocal/internal/model"
)

// TestHelperProcess is re-executed as a real child process that listens on a
// loopback port; it is a no-op in a normal test run.
func TestHelperProcess(t *testing.T) {
	if os.Getenv("LAZYLOCAL_HELPER") != "listen" {
		return
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	fmt.Printf("port=%d\n", ln.Addr().(*net.TCPAddr).Port)
	time.Sleep(time.Minute)
	os.Exit(0)
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// newMainRepo simulates a plain checkout: <root>/<repo>/.git/HEAD.
func newMainRepo(t *testing.T, repo, headContent string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), repo)
	writeFile(t, filepath.Join(dir, ".git", "HEAD"), headContent)
	return dir
}

// newLinkedWorktree simulates `git worktree add`: the main repo's
// .git/worktrees/<name> metadata plus a ".git" file in the worktree dir.
func newLinkedWorktree(t *testing.T, repo, wtName, branch string) (mainDir, wtDir string) {
	t.Helper()
	root := t.TempDir()
	mainDir = filepath.Join(root, repo)
	wtDir = filepath.Join(root, "worktrees", wtName)
	gitDir := filepath.Join(mainDir, ".git", "worktrees", wtName)
	writeFile(t, filepath.Join(mainDir, ".git", "HEAD"), "ref: refs/heads/main\n")
	writeFile(t, filepath.Join(gitDir, "HEAD"), "ref: refs/heads/"+branch+"\n")
	writeFile(t, filepath.Join(gitDir, "commondir"), "../..\n")
	writeFile(t, filepath.Join(gitDir, "gitdir"), filepath.Join(wtDir, ".git")+"\n")
	writeFile(t, filepath.Join(wtDir, ".git"), "gitdir: "+gitDir+"\n")
	return mainDir, wtDir
}

func TestParseLsofListen(t *testing.T) {
	const uid = 501
	const output = `p100
cnode
u501
n*:3000
n[::]:3000
n127.0.0.1:3001
p1
claunchd
u0
n*:80
p200
crapportd
u502
n*:49152
p300
cnode
u501
n*:notaport
p400
cpython3
u501
n[::1]:8000
`
	got := ParseLsofListen(output, uid)
	want := []Listener{
		{PID: 100, UID: uid, Port: 3000, Command: "node"},
		{PID: 100, UID: uid, Port: 3001, Command: "node"},
		{PID: 400, UID: uid, Port: 8000, Command: "python3"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ParseLsofListen = %+v, want %+v", got, want)
	}
}

func TestParseLsofListenDropsForeignUID(t *testing.T) {
	const output = "p1\nclaunchd\nu0\nn*:80\np2\ncsshd\nu0\nn*:22\np3\ncother\nu999\nn*:5000\np4\ncbad\nuNaN\nn*:6000\n"
	if got := ParseLsofListen(output, 501); len(got) != 0 {
		t.Fatalf("foreign-UID listeners must be dropped, got %+v", got)
	}
	// root-owned process is only visible when the current user is root itself.
	if got := ParseLsofListen(output, 0); len(got) != 2 {
		t.Fatalf("uid 0 should keep exactly the two uid-0 processes, got %+v", got)
	}
}

func TestParseLsofCwd(t *testing.T) {
	got := ParseLsofCwd("p100\nfcwd\nn/work/a\np200\nfcwd\nn/work/b c\n")
	want := map[int]string{100: "/work/a", 200: "/work/b c"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ParseLsofCwd = %v, want %v", got, want)
	}
}

func TestParsePS(t *testing.T) {
	got := ParsePS("  4001  4100  4001   501 /usr/local/bin/node server.js --port 3001\n   bad line\n    1     0     1     0 /sbin/launchd\n")
	if len(got) != 2 {
		t.Fatalf("want 2 rows, got %v", got)
	}
	p := got[4001]
	if p.PPID != 4100 || p.PGID != 4001 || p.UID != 501 || p.Command != "node" || p.Cmdline != "/usr/local/bin/node server.js --port 3001" {
		t.Fatalf("unexpected row %+v", p)
	}
}

func TestParseProcNetTCP(t *testing.T) {
	const data = `  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
   0: 0100007F:0BB8 00000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 11111 1 0 100 0 0 10 0
   1: 0100007F:1F90 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 22222 1 0 100 0 0 10 0
   2: 0100007F:1F91 0100007F:C350 01 00000000:00000000 00:00000000 00000000  1000        0 33333 1 0 100 0 0 10 0
`
	got := ParseProcNetTCP(data, 1000)
	want := map[string]procNetSocket{"11111": {Port: 3000, UID: 1000}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ParseProcNetTCP = %v, want %v", got, want)
	}
}

func TestResolveGitInfo(t *testing.T) {
	t.Run("main checkout on branch", func(t *testing.T) {
		dir := newMainRepo(t, "shop", "ref: refs/heads/feature/login\n")
		sub := filepath.Join(dir, "web", "src")
		if err := os.MkdirAll(sub, 0o755); err != nil {
			t.Fatal(err)
		}
		got := ResolveGitInfo(sub)
		want := GitInfo{IsGit: true, Repo: "shop", Worktree: "shop", Branch: "feature/login"}
		if got != want {
			t.Fatalf("got %+v, want %+v", got, want)
		}
	})
	t.Run("linked worktree", func(t *testing.T) {
		_, wt := newLinkedWorktree(t, "shop", "feat-auth", "feat/auth")
		got := ResolveGitInfo(wt)
		want := GitInfo{IsGit: true, Repo: "shop", Worktree: "feat-auth", Branch: "feat/auth"}
		if got != want {
			t.Fatalf("got %+v, want %+v", got, want)
		}
	})
	t.Run("relative gitdir in worktree file", func(t *testing.T) {
		mainDir, wt := newLinkedWorktree(t, "shop", "rel", "fix/x")
		rel, err := filepath.Rel(wt, filepath.Join(mainDir, ".git", "worktrees", "rel"))
		if err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Join(wt, ".git"), "gitdir: "+rel+"\n")
		if got := ResolveGitInfo(wt); got.Repo != "shop" || got.Branch != "fix/x" {
			t.Fatalf("got %+v", got)
		}
	})
	t.Run("detached HEAD has empty branch", func(t *testing.T) {
		dir := newMainRepo(t, "shop", "0123456789abcdef0123456789abcdef01234567\n")
		got := ResolveGitInfo(dir)
		if !got.IsGit || got.Branch != "" {
			t.Fatalf("got %+v", got)
		}
	})
	t.Run("deleted worktree dir is not mistaken for parent repo", func(t *testing.T) {
		dir := newMainRepo(t, "shop", "ref: refs/heads/main\n")
		if got := ResolveGitInfo(filepath.Join(dir, ".worktrees", "gone")); got.IsGit {
			t.Fatalf("got %+v, want no git info", got)
		}
	})
	t.Run("not a repo", func(t *testing.T) {
		if got := ResolveGitInfo(t.TempDir()); got.IsGit {
			t.Fatalf("got %+v, want no git info", got)
		}
	})
	t.Run("empty and relative cwd", func(t *testing.T) {
		if ResolveGitInfo("").IsGit || ResolveGitInfo("relative/dir").IsGit {
			t.Fatal("empty/relative cwd must not resolve")
		}
	})
}

func TestBuildServiceName(t *testing.T) {
	tests := []struct {
		name                       string
		repo, branch, cwd, command string
		want                       string
	}{
		{"main branch", "shop", "main", "/x/shop", "node", "shop"},
		{"master branch", "shop", "master", "/x/shop", "node", "shop"},
		{"feature branch", "shop", "feat/Auth_2", "/x/shop", "node", "feat-auth-2.shop"},
		{"repo slug", "Shop App", "fix/x", "/x", "node", "fix-x.shop-app"},
		{"detached", "shop", "", "/x/shop", "node", "shop"},
		{"no repo uses cwd dir", "", "", "/home/u/My Project", "node", "my-project"},
		{"no repo, root cwd uses command", "", "", "/", "/usr/bin/Node", "node"},
		{"nothing", "", "", "", "", "service"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := BuildServiceName(tt.repo, tt.branch, tt.cwd, tt.command); got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestResolveNameCollisions(t *testing.T) {
	in := []model.Service{
		{Name: "shop", Port: 3001, PID: 2},
		{Name: "api", Port: 4000, PID: 3},
		{Name: "shop", Port: 3000, PID: 1},
		{Name: "shop", Port: 3002, PID: 4},
	}
	got := ResolveNameCollisions(in)
	var names []string
	for _, s := range got {
		names = append(names, fmt.Sprintf("%s@%d", s.Name, s.Port))
	}
	want := []string{"shop@3000", "shop-3001@3001", "shop-3002@3002", "api@4000"}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("got %v, want %v", names, want)
	}
	if in[0].Name != "shop" || in[2].Port != 3000 {
		t.Fatal("input slice must not be mutated or reordered")
	}
}

func TestDetectAgent(t *testing.T) {
	table := map[int]ProcInfo{
		10: {PID: 10, PPID: 20, Command: "node", Cmdline: "node server.js"},
		20: {PID: 20, PPID: 30, Command: "zsh", Cmdline: "-zsh"},
		30: {PID: 30, PPID: 1, Command: "node", Cmdline: "node /usr/local/bin/claude --resume"},
		40: {PID: 40, PPID: 1, Command: "node", Cmdline: "node /Users/u/claude-clone/server.js"},
		50: {PID: 50, PPID: 60, Command: "node", Cmdline: "node dev.js"},
		60: {PID: 60, PPID: 50, Command: "sh", Cmdline: "sh"}, // PPID cycle
		70: {PID: 70, PPID: 80, Command: "Cursor Helper", Cmdline: "/Applications/Cursor.app/Contents/Frameworks/Cursor Helper"},
		80: {PID: 80, PPID: 1, Command: "launchd", Cmdline: "/sbin/launchd"},
		90: {PID: 90, PPID: 1, Command: "codex", Cmdline: "codex exec"},
	}
	tests := []struct {
		name string
		pid  int
		want string
	}{
		{"ancestor wrapper script", 10, "claude"},
		{"own command", 90, "codex"},
		{"agent only in path argument", 40, ""},
		{"cycle terminates", 50, ""},
		{"app bundle", 70, "cursor"},
		{"unknown pid", 999, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := DetectAgent(tt.pid, table); got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestClassifyZombie(t *testing.T) {
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	idle := 30 * time.Minute
	base := func() model.Service {
		return model.Service{
			PID: 100, PPID: 50, UID: 501, Command: "node", Cwd: "/w/shop", CwdExists: true,
			Repo: "shop", Branch: "feat", Port: 3001, Running: true,
		}
	}
	with := func(mut func(*model.Service)) model.Service {
		s := base()
		mut(&s)
		return s
	}
	twinLowPort := with(func(s *model.Service) { s.PID, s.Port = 101, 3000 })
	twinNewer := with(func(s *model.Service) {
		s.PID, s.Port, s.FirstSeen = 101, 3009, now
	})

	tests := []struct {
		name string
		svc  model.Service
		all  []model.Service
		want model.ZombieReason
	}{
		{"healthy", base(), nil, model.ZombieNone},
		{"a: deleted cwd", with(func(s *model.Service) { s.CwdExists = false }), nil, model.ZombieDeletedCwd},
		{"a: empty cwd is not deleted cwd zombie", with(func(s *model.Service) { s.Cwd, s.CwdExists = "", false }), nil, model.ZombieNone},
		{"a: deleted cwd wins over orphan", with(func(s *model.Service) { s.CwdExists, s.PPID = false, 1 }), nil, model.ZombieDeletedCwd},
		{"a: pinned offline is ignored", with(func(s *model.Service) { s.Pinned, s.PID, s.CwdExists = true, 0, false }), nil, model.ZombieNone},
		{"b: orphaned node", with(func(s *model.Service) { s.PPID = 1 }), nil, model.ZombieOrphanPPID},
		{"b: orphaned python3.12", with(func(s *model.Service) { s.PPID, s.Command = 1, "Python3.12" }), nil, model.ZombieOrphanPPID},
		{"b: dev runtime via cmdline", with(func(s *model.Service) { s.PPID, s.Command, s.Cmdline = 1, "myapp", "/usr/bin/env node app.js" }), nil, model.ZombieOrphanPPID},
		{"b: launchd child system service", with(func(s *model.Service) { s.PPID, s.Command = 1, "rapportd" }), nil, model.ZombieNone},
		{"b: pinned never orphan", with(func(s *model.Service) { s.PPID, s.Pinned = 1, true }), nil, model.ZombieNone},
		{"c: lower-port twin exists", base(), []model.Service{base(), twinLowPort}, model.ZombieDuplicateWork},
		{"c: newer twin exists", with(func(s *model.Service) { s.FirstSeen = now.Add(-time.Hour) }), []model.Service{base(), twinNewer}, model.ZombieDuplicateWork},
		{"c: preferred twin is not flagged", twinLowPort, []model.Service{base(), twinLowPort}, model.ZombieNone},
		{"c: same pid on two ports is not a duplicate", base(), []model.Service{base(), with(func(s *model.Service) { s.Port = 3000 })}, model.ZombieNone},
		{"c: different cwd is not a duplicate", base(), []model.Service{with(func(s *model.Service) { s.PID, s.Port, s.Cwd = 101, 3000, "/w/other" })}, model.ZombieNone},
		{"c: same cwd different command is not a duplicate", base(), []model.Service{base(), with(func(s *model.Service) { s.PID, s.Port, s.Command = 101, 3000, "python" })}, model.ZombieNone},
		{"d: idle past threshold", with(func(s *model.Service) { s.LastTraffic = now.Add(-time.Hour) }), nil, model.ZombieIdleTraffic},
		{"d: recent traffic", with(func(s *model.Service) { s.LastTraffic = now.Add(-time.Minute) }), nil, model.ZombieNone},
		{"d: never seen traffic", base(), nil, model.ZombieNone},
		{"d: pinned never idle", with(func(s *model.Service) { s.Pinned, s.LastTraffic = true, now.Add(-time.Hour) }), nil, model.ZombieNone},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ClassifyZombie(tt.svc, tt.all, idle, now); got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestMarkZombiesTwinsNeverBothFlagged(t *testing.T) {
	now := time.Now()
	a := model.Service{PID: 1, Port: 3000, Command: "node", Cwd: "/w", CwdExists: true, Repo: "r", Branch: "b", FirstSeen: now}
	b := model.Service{PID: 2, Port: 3001, Command: "node", Cwd: "/w", CwdExists: true, Repo: "r", Branch: "b", FirstSeen: now}
	got := MarkZombies([]model.Service{a, b}, time.Hour, now)
	flagged := 0
	for _, s := range got {
		if s.IsZombie() {
			flagged++
		}
	}
	if flagged != 1 || got[0].IsZombie() || got[1].ZombieReason != model.ZombieDuplicateWork {
		t.Fatalf("want only the higher-port twin flagged, got %+v", got)
	}
}

func TestScanWithFakeRunner(t *testing.T) {
	uid := os.Getuid()
	_, wt := newLinkedWorktree(t, "shop", "feat-auth", "feat/auth")
	gone := filepath.Join(t.TempDir(), "deleted-worktree")

	listen := fmt.Sprintf(`p4001
cnode
u%[1]d
n*:3001
p4002
cnode
u%[1]d
n*:3005
p4003
cnode
u%[1]d
n*:3010
p4004
claunchd
u0
n*:80
p%[2]d
clazylocal
u%[1]d
n*:9999
`, uid, os.Getpid())
	cwds := fmt.Sprintf("p4001\nfcwd\nn%s\np4002\nfcwd\nn%s\np4003\nfcwd\nn%s\n", wt, wt, gone)
	ps := fmt.Sprintf(`4001 4100 4001 %[1]d node server.js
4002 4100 4002 %[1]d node worker.js
4003 1 4003 %[1]d node old.js
4100 1 4100 %[1]d claude
4004 1 4004 0 /sbin/launchd
`, uid)

	s := NewScanner()
	s.goos = "darwin"
	s.run = func(_ context.Context, name string, args ...string) ([]byte, error) {
		joined := strings.Join(args, " ")
		switch {
		case name == "ps":
			return []byte(ps), nil
		case name == "lsof" && strings.Contains(joined, "-iTCP"):
			return []byte(listen), nil
		case name == "lsof" && strings.Contains(joined, "-d cwd"):
			return []byte(cwds), nil
		}
		return nil, fmt.Errorf("unexpected command %s %s", name, joined)
	}

	got, err := s.Scan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, svc := range got {
		names = append(names, fmt.Sprintf("%s@%d", svc.Name, svc.Port))
	}
	want := []string{"feat-auth.shop@3001", "feat-auth.shop-3005@3005", "deleted-worktree@3010"}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("services = %v, want %v (foreign-UID and self PID must be absent)", names, want)
	}
	first := got[0]
	if first.Host != "feat-auth.shop.localhost" || first.Repo != "shop" || first.Worktree != "feat-auth" ||
		first.Branch != "feat/auth" || first.Agent != "claude" || first.PPID != 4100 || !first.CwdExists || first.FirstSeen.IsZero() {
		t.Fatalf("unexpected first service %+v", first)
	}
	if got[2].CwdExists {
		t.Fatalf("deleted cwd must be reported as missing: %+v", got[2])
	}

	marked := MarkZombies(got, time.Hour, time.Now())
	if marked[2].ZombieReason != model.ZombieDeletedCwd {
		t.Fatalf("deleted worktree server should be a zombie, got %q", marked[2].ZombieReason)
	}
}

// TestG2ScannerFindsWorktreeServer is acceptance gate G2: a real child process
// listening on a loopback port from inside a git worktree is discovered by
// name "<branch>.<repo>" within 2 seconds.
func TestG2ScannerFindsWorktreeServer(t *testing.T) {
	if runtime.GOOS == "darwin" {
		if _, err := exec.LookPath("lsof"); err != nil {
			t.Skip("lsof not available")
		}
	}
	_, wt := newLinkedWorktree(t, "shop", "feat-auth", "feat/auth")

	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperProcess$")
	cmd.Env = append(os.Environ(), "LAZYLOCAL_HELPER=listen")
	cmd.Dir = wt
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})

	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil {
		t.Fatalf("helper did not report its port: %v", err)
	}
	var port int
	if _, err := fmt.Sscanf(strings.TrimSpace(line), "port=%d", &port); err != nil {
		t.Fatalf("bad helper output %q: %v", line, err)
	}

	scanner := NewScanner()
	deadline := time.Now().Add(2 * time.Second)
	var seen []string
	for {
		svcs, err := scanner.Scan(context.Background())
		if err != nil {
			t.Fatalf("Scan: %v", err)
		}
		seen = seen[:0]
		for _, svc := range svcs {
			seen = append(seen, fmt.Sprintf("%s@%d(pid %d)", svc.Name, svc.Port, svc.PID))
			if svc.Port != port {
				continue
			}
			if svc.Name != "feat-auth.shop" || svc.PID != cmd.Process.Pid || !svc.CwdExists || svc.Branch != "feat/auth" || svc.Repo != "shop" {
				t.Fatalf("port %d found but wrong identity: %+v", port, svc)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("port %d (pid %d) not discovered within 2s; saw %v", port, cmd.Process.Pid, seen)
		}
		time.Sleep(100 * time.Millisecond)
	}
}
