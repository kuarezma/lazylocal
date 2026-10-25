package discover

import (
	"path/filepath"
	"strings"
	"time"

	"github.com/kuarezma/lazylocal/internal/model"
)

var agentNames = []string{"claude", "codex", "gemini", "cursor", "aider", "opencode", "antigravity"}

const maxAncestorDepth = 64

// DetectAgent returns the AI coding agent that owns pid: the first match of
// an agent name in the executable (or its first script argument) of the
// process itself, then of each ancestor up the PPID chain. Only executable and
// script base names are compared, so a path like ".../claude-clone/server.js"
// does not count.
func DetectAgent(pid int, procTable map[int]ProcInfo) string {
	visited := make(map[int]bool)
	for depth := 0; depth < maxAncestorDepth && pid > 0 && !visited[pid]; depth++ {
		visited[pid] = true
		p, ok := procTable[pid]
		if !ok {
			return ""
		}
		if agent := agentInProc(p); agent != "" {
			return agent
		}
		pid = p.PPID
	}
	return ""
}

func agentInProc(p ProcInfo) string {
	candidates := []string{p.Command}
	fields := strings.Fields(p.Cmdline)
	for i := 0; i < len(fields) && i < 2; i++ {
		candidates = append(candidates, fields[i])
	}
	for _, c := range candidates {
		base := strings.ToLower(filepath.Base(c))
		for _, agent := range agentNames {
			if strings.Contains(base, agent) {
				return agent
			}
		}
	}
	return ""
}

var (
	devExact    = []string{"go", "vite", "next", "ruby", "cargo", "node", "bun", "deno", "npm", "pnpm", "yarn", "php", "uvicorn", "gunicorn"}
	devPrefixes = []string{"python", "node", "next-server", "ruby", "vite"}
)

// isDevCommand reports whether the process looks like a dev runtime rather
// than a system service.
func isDevCommand(svc model.Service) bool {
	candidates := []string{svc.Command}
	fields := strings.Fields(svc.Cmdline)
	for i := 0; i < len(fields) && i < 2; i++ {
		candidates = append(candidates, fields[i])
	}
	for _, c := range candidates {
		base := strings.ToLower(filepath.Base(c))
		for _, name := range devExact {
			if base == name {
				return true
			}
		}
		for _, prefix := range devPrefixes {
			if strings.HasPrefix(base, prefix) {
				return true
			}
		}
	}
	return false
}

// ClassifyZombie applies the zombie rules in order: deleted cwd, orphaned
// dev server, duplicate work in the same repo/branch/cwd, idle traffic.
func ClassifyZombie(svc model.Service, all []model.Service, idleThreshold time.Duration, now time.Time) model.ZombieReason {
	if svc.Pinned && svc.PID == 0 {
		return model.ZombieNone // pinned and offline: nothing to hunt
	}
	if svc.Cwd != "" && !svc.CwdExists {
		return model.ZombieDeletedCwd
	}
	if svc.PPID == 1 && !svc.Pinned && isDevCommand(svc) {
		return model.ZombieOrphanPPID
	}
	if hasPreferredTwin(svc, all) {
		return model.ZombieDuplicateWork
	}
	if idleThreshold > 0 && !svc.Pinned && !svc.LastTraffic.IsZero() && now.Sub(svc.LastTraffic) > idleThreshold {
		return model.ZombieIdleTraffic
	}
	return model.ZombieNone
}

// hasPreferredTwin reports whether another process serves the same
// repo+branch+cwd+command and should be kept instead of svc.
func hasPreferredTwin(svc model.Service, all []model.Service) bool {
	if svc.Repo == "" || svc.Cwd == "" || svc.Command == "" {
		return false
	}
	for _, other := range all {
		if other.PID <= 0 || other.PID == svc.PID {
			continue
		}
		if other.Repo != svc.Repo || other.Branch != svc.Branch || other.Cwd != svc.Cwd || other.Command != svc.Command {
			continue
		}
		if preferred(other, svc) {
			return true
		}
	}
	return false
}

// preferred reports whether a should be kept over b: the newer process wins,
// and when age is unknown or equal the lower port wins. It is antisymmetric,
// so two twins are never both flagged.
func preferred(a, b model.Service) bool {
	if !a.FirstSeen.IsZero() && !b.FirstSeen.IsZero() && !a.FirstSeen.Equal(b.FirstSeen) {
		return a.FirstSeen.After(b.FirstSeen)
	}
	if a.Port != b.Port {
		return a.Port < b.Port
	}
	return a.PID < b.PID
}

// MarkZombies returns a copy of svcs with ZombieReason filled in.
func MarkZombies(svcs []model.Service, idleThreshold time.Duration, now time.Time) []model.Service {
	out := make([]model.Service, len(svcs))
	copy(out, svcs)
	for i := range out {
		out[i].ZombieReason = ClassifyZombie(out[i], svcs, idleThreshold, now)
	}
	return out
}
