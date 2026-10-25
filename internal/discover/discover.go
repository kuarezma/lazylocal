// Package discover finds local development servers (listening TCP ports owned
// by the current user), resolves their git/worktree identity and flags zombies.
package discover

import (
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kuarezma/lazylocal/internal/model"
)

// Listener is one listening TCP port attributed to a process.
type Listener struct {
	PID     int
	UID     int
	Port    int
	Command string
}

// rawListener is an alias for Listener.
type rawListener = Listener

// ProcInfo is one row of the process table used for agent detection.
type ProcInfo struct {
	PID     int
	PPID    int
	PGID    int
	UID     int
	Command string
	Cmdline string
}

// cwdInfo is the working directory of a process and whether it still exists.
type cwdInfo struct {
	Path   string
	Exists bool
}

// RunFunc runs an external command and returns its stdout.
type RunFunc func(ctx context.Context, name string, args ...string) ([]byte, error)

// Scanner discovers listening dev servers owned by the current user.
type Scanner struct {
	UID     int // only processes owned by this UID are reported
	SelfPID int // lazylocal's own PID, never reported

	run      RunFunc
	goos     string
	procRoot string

	mu        sync.Mutex
	firstSeen map[string]time.Time
}

// NewScanner returns a Scanner for the current user and process.
func NewScanner() *Scanner {
	return &Scanner{
		UID:       os.Getuid(),
		SelfPID:   os.Getpid(),
		run:       defaultRun,
		goos:      runtime.GOOS,
		procRoot:  "/proc",
		firstSeen: make(map[string]time.Time),
	}
}

func defaultRun(ctx context.Context, name string, args ...string) ([]byte, error) {
	out, err := exec.CommandContext(ctx, name, args...).Output()
	var exitErr *exec.ExitError
	// lsof exits 1 when nothing matches or some files were unreadable; stdout is still valid.
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return out, nil
	}
	return out, err
}

// Scan returns the current listening services sorted by port. Names are
// collision-free; zombie classification is left to MarkZombies.
func (s *Scanner) Scan(ctx context.Context) ([]model.Service, error) {
	listeners, err := s.listeners(ctx)
	if err != nil {
		return nil, fmt.Errorf("list listening ports: %w", err)
	}
	table := s.procTable(ctx)

	var kept []Listener
	for _, l := range listeners {
		if l.PID == s.SelfPID || l.PID <= 1 || l.UID != s.UID {
			continue
		}
		if p, ok := table[l.PID]; ok && p.UID != s.UID {
			continue
		}
		kept = append(kept, l)
	}

	cwds := s.cwds(ctx, kept)
	now := time.Now()
	svcs := make([]model.Service, 0, len(kept))
	seen := make(map[string]bool, len(kept))
	for _, l := range kept {
		svc := s.buildService(l, table, cwds[l.PID])
		key := fmt.Sprintf("%d:%d", l.PID, l.Port)
		seen[key] = true
		svc.FirstSeen = s.recordFirstSeen(key, now)
		svcs = append(svcs, svc)
	}
	s.pruneFirstSeen(seen)

	svcs = ResolveNameCollisions(svcs)
	for i := range svcs {
		svcs[i].Host = svcs[i].Name + ".localhost"
	}
	return svcs, nil
}

func (s *Scanner) buildService(l Listener, table map[int]ProcInfo, cwd cwdInfo) model.Service {
	svc := model.Service{
		ID:        fmt.Sprintf("tcp-%d", l.Port),
		Port:      l.Port,
		PID:       l.PID,
		UID:       l.UID,
		Command:   l.Command,
		Cwd:       cwd.Path,
		CwdExists: cwd.Exists,
		Running:   true,
	}
	if p, ok := table[l.PID]; ok {
		svc.PPID, svc.PGID = p.PPID, p.PGID
		svc.Cmdline = p.Cmdline
		if p.Command != "" {
			svc.Command = p.Command
		}
	}
	svc.Agent = DetectAgent(l.PID, table)
	if git := ResolveGitInfo(cwd.Path); git.IsGit {
		svc.Repo, svc.Worktree, svc.Branch = git.Repo, git.Worktree, git.Branch
	}
	svc.Name = BuildServiceName(svc.Repo, svc.Branch, svc.Cwd, svc.Command)
	return svc
}

func (s *Scanner) recordFirstSeen(key string, now time.Time) time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	if t, ok := s.firstSeen[key]; ok {
		return t
	}
	s.firstSeen[key] = now
	return now
}

func (s *Scanner) pruneFirstSeen(live map[string]bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for k := range s.firstSeen {
		if !live[k] {
			delete(s.firstSeen, k)
		}
	}
}

func (s *Scanner) listeners(ctx context.Context) ([]Listener, error) {
	switch s.goos {
	case "darwin":
		out, err := s.run(ctx, "lsof", "-nP", "-iTCP", "-sTCP:LISTEN", "-F", "pcnu")
		if err != nil {
			return nil, err
		}
		return ParseLsofListen(string(out), s.UID), nil
	case "linux":
		return s.linuxListeners()
	case "windows":
		return s.windowsListeners(ctx)
	default:
		return nil, fmt.Errorf("unsupported platform %q", s.goos)
	}
}

func (s *Scanner) windowsListeners(ctx context.Context) ([]Listener, error) {
	out, err := s.run(ctx, "netstat", "-ano", "-p", "tcp")
	if err != nil {
		return nil, err
	}
	return ParseNetstatWindows(string(out), s.UID), nil
}

func (s *Scanner) procTable(ctx context.Context) map[int]ProcInfo {
	if s.goos == "windows" {
		out, err := s.run(ctx, "tasklist", "/FO", "CSV", "/NH")
		if err != nil {
			return map[int]ProcInfo{}
		}
		return ParseTasklistCSV(string(out))
	}
	out, err := s.run(ctx, "ps", "-ww", "-axo", "pid=,ppid=,pgid=,uid=,command=")
	if err != nil {
		return map[int]ProcInfo{}
	}
	return ParsePS(string(out))
}

func (s *Scanner) cwds(ctx context.Context, ls []Listener) map[int]cwdInfo {
	pids := make([]int, 0, len(ls))
	seen := make(map[int]bool, len(ls))
	for _, l := range ls {
		if !seen[l.PID] {
			seen[l.PID] = true
			pids = append(pids, l.PID)
		}
	}
	sort.Ints(pids)
	if len(pids) == 0 {
		return map[int]cwdInfo{}
	}
	if s.goos == "linux" {
		return s.linuxCwds(pids)
	}
	if s.goos == "windows" {
		return s.windowsCwds(pids)
	}
	ids := make([]string, len(pids))
	for i, pid := range pids {
		ids[i] = strconv.Itoa(pid)
	}
	out, err := s.run(ctx, "lsof", "-a", "-p", strings.Join(ids, ","), "-d", "cwd", "-Fn")
	if err != nil {
		return map[int]cwdInfo{}
	}
	res := make(map[int]cwdInfo)
	for pid, path := range ParseLsofCwd(string(out)) {
		_, statErr := os.Stat(path)
		exists := true
		if statErr != nil && errors.Is(statErr, os.ErrNotExist) {
			exists = false
		}
		res[pid] = cwdInfo{Path: path, Exists: exists}
	}
	return res
}

func (s *Scanner) windowsCwds(pids []int) map[int]cwdInfo {
	return windowsCwdsMap(pids)
}

// ParseLsofListen parses `lsof -nP -iTCP -sTCP:LISTEN -F pcnu` output. Only
// processes owned by currentUID are returned; every other UID (root, system
// services, unparsable UID) is dropped. Each (pid, port) pair appears once.
func ParseLsofListen(output string, currentUID int) []Listener {
	type proc struct {
		pid   int
		uid   int
		cmd   string
		ports []int
	}
	var (
		out  []Listener
		seen = make(map[[2]int]bool)
		cur  = proc{uid: -1}
	)
	flush := func() {
		if cur.pid > 0 && cur.uid == currentUID {
			for _, port := range cur.ports {
				key := [2]int{cur.pid, port}
				if seen[key] {
					continue
				}
				seen[key] = true
				out = append(out, Listener{PID: cur.pid, UID: cur.uid, Port: port, Command: cur.cmd})
			}
		}
		cur = proc{uid: -1}
	}

	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimRight(line, "\r")
		if len(line) < 2 {
			continue
		}
		val := line[1:]
		switch line[0] {
		case 'p':
			flush()
			if pid, err := strconv.Atoi(val); err == nil {
				cur.pid = pid
			}
		case 'c':
			cur.cmd = val
		case 'u':
			if uid, err := strconv.Atoi(val); err == nil {
				cur.uid = uid
			}
		case 'n':
			if port, ok := parseListenPort(val); ok {
				cur.ports = append(cur.ports, port)
			}
		}
	}
	flush()
	return out
}

// parseListenPort extracts the port from "*:3000", "127.0.0.1:3000" or "[::1]:3000".
func parseListenPort(name string) (int, bool) {
	if strings.Contains(name, "->") {
		return 0, false
	}
	idx := strings.LastIndexByte(name, ':')
	if idx < 0 {
		return 0, false
	}
	port, err := strconv.Atoi(name[idx+1:])
	if err != nil || port < 1 || port > 65535 {
		return 0, false
	}
	return port, true
}

// ParseLsofCwd parses `lsof -a -p <pids> -d cwd -Fn` output into pid -> cwd.
func ParseLsofCwd(output string) map[int]string {
	res := make(map[int]string)
	pid := 0
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimRight(line, "\r")
		if len(line) < 2 {
			continue
		}
		switch line[0] {
		case 'p':
			pid, _ = strconv.Atoi(line[1:])
		case 'n':
			if pid > 0 {
				res[pid] = line[1:]
			}
		}
	}
	return res
}

// ParsePS parses `ps -axo pid=,ppid=,pgid=,uid=,command=` output.
func ParsePS(output string) map[int]ProcInfo {
	table := make(map[int]ProcInfo)
	for _, line := range strings.Split(output, "\n") {
		var nums [4]int
		rest := line
		ok := true
		for i := range nums {
			rest = strings.TrimLeft(rest, " \t")
			end := strings.IndexAny(rest, " \t")
			if end < 0 {
				ok = false
				break
			}
			n, err := strconv.Atoi(rest[:end])
			if err != nil {
				ok = false
				break
			}
			nums[i] = n
			rest = rest[end:]
		}
		cmdline := strings.TrimSpace(rest)
		if !ok || cmdline == "" {
			continue
		}
		table[nums[0]] = ProcInfo{
			PID: nums[0], PPID: nums[1], PGID: nums[2], UID: nums[3],
			Command: filepath.Base(strings.Fields(cmdline)[0]),
			Cmdline: cmdline,
		}
	}
	return table
}

// linuxListeners joins /proc/net/tcp{,6} listening sockets to owning processes via /proc/<pid>/fd.
func (s *Scanner) linuxListeners() ([]Listener, error) {
	sockets := make(map[string]procNetSocket)
	for _, name := range []string{"tcp", "tcp6"} {
		data, err := os.ReadFile(filepath.Join(s.procRoot, "net", name))
		if err != nil {
			if name == "tcp" {
				return nil, err
			}
			continue
		}
		for inode, sock := range ParseProcNetTCP(string(data), s.UID) {
			sockets[inode] = sock
		}
	}
	entries, err := os.ReadDir(s.procRoot)
	if err != nil {
		return nil, err
	}
	var out []Listener
	seen := make(map[[2]int]bool)
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		fdDir := filepath.Join(s.procRoot, e.Name(), "fd")
		fds, err := os.ReadDir(fdDir)
		if err != nil {
			continue // not ours or already gone
		}
		for _, fd := range fds {
			link, err := os.Readlink(filepath.Join(fdDir, fd.Name()))
			if err != nil || !strings.HasPrefix(link, "socket:[") {
				continue
			}
			sock, ok := sockets[strings.TrimSuffix(strings.TrimPrefix(link, "socket:["), "]")]
			key := [2]int{pid, sock.Port}
			if !ok || seen[key] {
				continue
			}
			seen[key] = true
			comm, _ := os.ReadFile(filepath.Join(s.procRoot, e.Name(), "comm"))
			out = append(out, Listener{PID: pid, UID: sock.UID, Port: sock.Port, Command: strings.TrimSpace(string(comm))})
		}
	}
	return out, nil
}

func (s *Scanner) linuxCwds(pids []int) map[int]cwdInfo {
	const deletedSuffix = " (deleted)"
	res := make(map[int]cwdInfo, len(pids))
	for _, pid := range pids {
		link, err := os.Readlink(filepath.Join(s.procRoot, strconv.Itoa(pid), "cwd"))
		if err != nil {
			continue
		}
		if path, deleted := strings.CutSuffix(link, deletedSuffix); deleted {
			res[pid] = cwdInfo{Path: path, Exists: false}
			continue
		}
		_, statErr := os.Stat(link)
		exists := true
		if statErr != nil && errors.Is(statErr, os.ErrNotExist) {
			exists = false
		}
		res[pid] = cwdInfo{Path: link, Exists: exists}
	}
	return res
}

type procNetSocket struct {
	Port int
	UID  int
}

// ParseProcNetTCP parses /proc/net/tcp or /proc/net/tcp6 and returns LISTEN
// sockets owned by currentUID, keyed by socket inode.
func ParseProcNetTCP(data string, currentUID int) map[string]procNetSocket {
	const stateListen = "0A"
	res := make(map[string]procNetSocket)
	for i, line := range strings.Split(data, "\n") {
		fields := strings.Fields(line)
		if i == 0 || len(fields) < 10 || fields[3] != stateListen {
			continue
		}
		uid, err := strconv.Atoi(fields[7])
		if err != nil || uid != currentUID {
			continue
		}
		idx := strings.LastIndexByte(fields[1], ':')
		if idx < 0 {
			continue
		}
		port, err := strconv.ParseUint(fields[1][idx+1:], 16, 16)
		if err != nil || port == 0 {
			continue
		}
		res[fields[9]] = procNetSocket{Port: int(port), UID: uid}
	}
	return res
}

// ParseNetstatWindows parses Windows `netstat -ano -p tcp` output.
// Lines matching LISTENING TCP sockets are parsed into rawListeners.
func ParseNetstatWindows(output string, currentUID int) []rawListener {
	var out []rawListener
	seen := make(map[[2]int]bool)

	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 5 {
			continue
		}
		if !strings.EqualFold(fields[0], "TCP") {
			continue
		}
		if !strings.EqualFold(fields[3], "LISTENING") {
			continue
		}
		port, ok := parseListenPort(fields[1])
		if !ok {
			continue
		}
		pid, err := strconv.Atoi(fields[4])
		if err != nil || pid <= 0 {
			continue
		}
		key := [2]int{pid, port}
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, rawListener{
			PID:  pid,
			UID:  currentUID,
			Port: port,
		})
	}
	return out
}

// ParseTasklistCSV parses `tasklist /FO CSV /NH` output into a map of PID -> ProcInfo.
func ParseTasklistCSV(output string) map[int]ProcInfo {
	table := make(map[int]ProcInfo)
	r := csv.NewReader(strings.NewReader(output))
	r.FieldsPerRecord = -1
	r.LazyQuotes = true
	records, err := r.ReadAll()
	if err != nil {
		for _, line := range strings.Split(output, "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			lr := csv.NewReader(strings.NewReader(line))
			if recs, err := lr.ReadAll(); err == nil && len(recs) > 0 {
				records = append(records, recs[0])
			}
		}
	}
	for _, rec := range records {
		if len(rec) < 2 {
			continue
		}
		pid, err := strconv.Atoi(rec[1])
		if err != nil || pid <= 0 {
			continue
		}
		rawName := strings.TrimSpace(rec[0])
		cmd := rawName
		if strings.HasSuffix(strings.ToLower(cmd), ".exe") {
			cmd = cmd[:len(cmd)-4]
		}
		table[pid] = ProcInfo{
			PID:     pid,
			UID:     os.Getuid(),
			Command: cmd,
			Cmdline: rawName,
		}
	}
	return table
}
