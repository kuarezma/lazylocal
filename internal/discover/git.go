package discover

import (
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/kuarezma/lazylocal/internal/model"
)

// GitInfo is the git identity of a working directory.
type GitInfo struct {
	IsGit    bool
	Repo     string // repository name (main checkout dir name, or bare repo name)
	Worktree string // name of the worktree directory containing .git
	Branch   string // current branch; empty when HEAD is detached
}

const (
	maxGitWalk  = 64
	headRefPref = "ref: refs/heads/"
)

// ResolveGitInfo walks up from cwd looking for a ".git" directory or a
// linked-worktree ".git" file ("gitdir: ..."), reading HEAD directly without
// spawning git. A cwd that no longer exists yields no git info, so a deleted
// worktree is never mistaken for its parent repository.
func ResolveGitInfo(cwd string) GitInfo {
	if cwd == "" || !filepath.IsAbs(cwd) {
		return GitInfo{}
	}
	if st, err := os.Stat(cwd); err != nil || !st.IsDir() {
		return GitInfo{}
	}
	dir := filepath.Clean(cwd)
	for i := 0; i < maxGitWalk; i++ {
		if info, ok := readGitEntry(dir); ok {
			return info
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return GitInfo{}
}

func readGitEntry(dir string) (GitInfo, bool) {
	dotGit := filepath.Join(dir, ".git")
	st, err := os.Stat(dotGit)
	if err != nil {
		return GitInfo{}, false
	}
	info := GitInfo{IsGit: true, Worktree: filepath.Base(dir)}
	if st.IsDir() {
		info.Repo = filepath.Base(dir)
		info.Branch = readBranch(dotGit)
		return info, true
	}
	data, err := os.ReadFile(dotGit)
	if err != nil {
		return GitInfo{}, false
	}
	gitDir, ok := parseGitdirFile(string(data), dir)
	if !ok {
		return GitInfo{}, false
	}
	info.Repo = repoNameFromGitDir(gitDir, dir)
	info.Branch = readBranch(gitDir)
	return info, true
}

func parseGitdirFile(content, base string) (string, bool) {
	first, _, _ := strings.Cut(content, "\n")
	path, ok := strings.CutPrefix(strings.TrimSpace(first), "gitdir:")
	if !ok {
		return "", false
	}
	path = strings.TrimSpace(path)
	if path == "" {
		return "", false
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(base, path)
	}
	return filepath.Clean(path), true
}

// repoNameFromGitDir names the repository a linked worktree belongs to.
// Linked worktrees carry a "commondir" file pointing at the main .git dir;
// submodules do not, so they fall back to the checkout directory name.
func repoNameFromGitDir(gitDir, checkoutDir string) string {
	data, err := os.ReadFile(filepath.Join(gitDir, "commondir"))
	if err != nil {
		return filepath.Base(checkoutDir)
	}
	common := strings.TrimSpace(string(data))
	if !filepath.IsAbs(common) {
		common = filepath.Join(gitDir, common)
	}
	common = filepath.Clean(common)
	if filepath.Base(common) == ".git" {
		return filepath.Base(filepath.Dir(common))
	}
	return strings.TrimSuffix(filepath.Base(common), ".git") // bare repository
}

func readBranch(gitDir string) string {
	data, err := os.ReadFile(filepath.Join(gitDir, "HEAD"))
	if err != nil {
		return ""
	}
	first, _, _ := strings.Cut(string(data), "\n")
	branch, ok := strings.CutPrefix(strings.TrimSpace(first), headRefPref)
	if !ok {
		return ""
	}
	return branch
}

// slugify lowercases s and reduces it to alphanumerics joined by single dashes.
func slugify(s string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			if dash && b.Len() > 0 {
				b.WriteByte('-')
			}
			dash = false
			b.WriteRune(r)
			continue
		}
		dash = true
	}
	return b.String()
}

// BuildServiceName derives the URL-safe service name:
//   - repo on main/master (or detached HEAD): "<repo>"
//   - repo on another branch: "<branch>.<repo>"
//   - no repo: the cwd directory name, else the command name.
func BuildServiceName(repo, branch, cwd, command string) string {
	if repoSlug := slugify(repo); repoSlug != "" {
		branchSlug := slugify(branch)
		if branchSlug == "" || branch == "main" || branch == "master" {
			return repoSlug
		}
		return branchSlug + "." + repoSlug
	}
	if cwd != "" {
		if base := slugify(filepath.Base(cwd)); base != "" {
			return base
		}
	}
	if name := slugify(filepath.Base(command)); name != "" && command != "" {
		return name
	}
	return "service"
}

// ResolveNameCollisions returns a copy of svcs sorted by port in which every
// name is unique: the lowest port keeps the name, later ones become
// "<name>-<port>".
func ResolveNameCollisions(svcs []model.Service) []model.Service {
	out := make([]model.Service, len(svcs))
	copy(out, svcs)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Port != out[j].Port {
			return out[i].Port < out[j].Port
		}
		return out[i].PID < out[j].PID
	})
	taken := make(map[string]bool, len(out))
	for i := range out {
		name := out[i].Name
		if taken[name] {
			name = name + "-" + strconv.Itoa(out[i].Port)
		}
		taken[name] = true
		out[i].Name = name
	}
	return out
}
