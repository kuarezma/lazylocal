package tui

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/kuarezma/lazylocal/internal/model"
)

// ActionType represents commands dispatched by TUI user input.
type ActionType string

const (
	ActionNone         ActionType = "none"
	ActionQuit         ActionType = "quit"
	ActionReplay       ActionType = "replay"
	ActionCopyCurl     ActionType = "copy_curl"
	ActionDiff         ActionType = "diff"
	ActionKillService  ActionType = "kill_service"
	ActionSweepZombies ActionType = "sweep_zombies"
	ActionRefresh      ActionType = "refresh"
)

// Action carries details of the action triggered by HandleKey.
type Action struct {
	Type              ActionType
	ServiceID         string
	ExchangeID        string
	CompareExchangeID string
}

// ANSI escape constants.
const (
	ansiReset    = "\x1b[0m"
	ansiBold     = "\x1b[1m"
	ansiDim      = "\x1b[2m"
	ansiInverse  = "\x1b[7m"
	ansiRed      = "\x1b[31m"
	ansiGreen    = "\x1b[32m"
	ansiYellow   = "\x1b[33m"
	ansiBlue     = "\x1b[34m"
	ansiMagenta  = "\x1b[35m"
	ansiCyan     = "\x1b[36m"
	ansiWhite    = "\x1b[37m"
	ansiGray     = "\x1b[90m"
	ansiBgSelect = "\x1b[48;5;238m"
)

// Style wraps ANSI color formatting with a NoColor toggle.
type Style struct {
	noColor bool
}

func (s Style) Bold(str string) string {
	if s.noColor || str == "" {
		return str
	}
	return ansiBold + str + ansiReset
}

func (s Style) Dim(str string) string {
	if s.noColor || str == "" {
		return str
	}
	return ansiDim + str + ansiReset
}

func (s Style) Green(str string) string {
	if s.noColor || str == "" {
		return str
	}
	return ansiGreen + str + ansiReset
}

func (s Style) Red(str string) string {
	if s.noColor || str == "" {
		return str
	}
	return ansiRed + str + ansiReset
}

func (s Style) Yellow(str string) string {
	if s.noColor || str == "" {
		return str
	}
	return ansiYellow + str + ansiReset
}

func (s Style) Cyan(str string) string {
	if s.noColor || str == "" {
		return str
	}
	return ansiCyan + str + ansiReset
}

func (s Style) Magenta(str string) string {
	if s.noColor || str == "" {
		return str
	}
	return ansiMagenta + str + ansiReset
}

func (s Style) Gray(str string) string {
	if s.noColor || str == "" {
		return str
	}
	return ansiGray + str + ansiReset
}

func (s Style) Selected(str string) string {
	if s.noColor || str == "" {
		return str
	}
	return ansiInverse + ansiBold + str + ansiReset
}

// sanitizeTerm converts ESC (\x1b), DEL (\x7f), and control characters < 0x20
// (excluding \n and \t) into safe \xNN hex representations to prevent terminal
// escape sequence injection (e.g. OSC 52 clipboard hijacking).
func sanitizeTerm(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '\n' || c == '\t' {
			b.WriteByte(c)
		} else if c == 0x1b || c < 0x20 || c == 0x7f {
			fmt.Fprintf(&b, "\\x%02x", c)
		} else {
			b.WriteByte(c)
		}
	}
	return b.String()
}

// StripANSI strips all ANSI escape codes from string s.
func StripANSI(s string) string {
	var b strings.Builder
	inEscape := false
	for i := 0; i < len(s); i++ {
		if s[i] == '\x1b' {
			inEscape = true
			continue
		}
		if inEscape {
			if (s[i] >= 'A' && s[i] <= 'Z') || (s[i] >= 'a' && s[i] <= 'z') {
				inEscape = false
			}
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// RuneWidth returns terminal column width for a single rune.
func RuneWidth(r rune) int {
	if r == 0 {
		return 0
	}
	// Wide emojis and symbols (robot, zombie, pin, etc.)
	if (r >= 0x1F300 && r <= 0x1FAFF) || (r >= 0x2600 && r <= 0x27BF) {
		return 2
	}
	// Wide CJK Ideographs
	if (r >= 0x4E00 && r <= 0x9FFF) || (r >= 0x3400 && r <= 0x4DBF) {
		return 2
	}
	// Combining characters
	if r >= 0x0300 && r <= 0x036F {
		return 0
	}
	return 1
}

// StringWidth returns visual terminal column width of a string.
func StringWidth(s string) int {
	clean := StripANSI(s)
	w := 0
	for _, r := range clean {
		w += RuneWidth(r)
	}
	return w
}

// PadOrTruncate adjusts s to exact target visual column width.
func PadOrTruncate(s string, targetWidth int, noColor bool) string {
	if targetWidth <= 0 {
		return ""
	}
	sw := StringWidth(s)
	if sw == targetWidth {
		return s
	}
	if sw < targetWidth {
		return s + strings.Repeat(" ", targetWidth-sw)
	}

	// Truncate to fit within targetWidth
	useEllipsis := targetWidth >= 3
	cutoff := targetWidth
	if useEllipsis {
		cutoff = targetWidth - 1
	}

	var b strings.Builder
	curW := 0
	inEscape := false
	hasEscape := false

	for i := 0; i < len(s); {
		if s[i] == '\x1b' {
			inEscape = true
			hasEscape = true
			b.WriteByte(s[i])
			i++
			continue
		}
		if inEscape {
			b.WriteByte(s[i])
			if (s[i] >= 'A' && s[i] <= 'Z') || (s[i] >= 'a' && s[i] <= 'z') {
				inEscape = false
			}
			i++
			continue
		}

		r, size := utf8.DecodeRuneInString(s[i:])
		rw := RuneWidth(r)
		if curW+rw > cutoff {
			break
		}
		curW += rw
		b.WriteString(s[i : i+size])
		i += size
	}

	if useEllipsis {
		b.WriteString("…")
		curW++
	}
	if curW < targetWidth {
		b.WriteString(strings.Repeat(" ", targetWidth-curW))
	}
	if hasEscape && !noColor {
		b.WriteString(ansiReset)
	}
	return b.String()
}

// Model maintains state and generates two-panel TUI views.
type Model struct {
	mu sync.RWMutex

	Width  int
	Height int

	Services    []model.Service
	SelectedSvc int
	svcScroll   int

	Exchanges  []model.Exchange
	SelectedEx int
	exScroll   int

	FocusPane    int // 0: Services, 1: Traffic
	OnlyErrors   bool
	FilterQuery  string
	Filtering    bool
	ShowDiff     bool
	DiffText     string
	ConfirmSweep bool

	StatusMessage string
	NoColor       bool
}

// NewModel creates an initialized TUI Model.
func NewModel(width, height int) *Model {
	if width <= 0 {
		width = 120
	}
	if height <= 0 {
		height = 32
	}
	return &Model{
		Width:       width,
		Height:      height,
		Services:    make([]model.Service, 0),
		Exchanges:   make([]model.Exchange, 0),
		FocusPane:   0,
		SelectedSvc: 0,
		SelectedEx:  0,
	}
}

// SetServices updates discovered services and bounds the selection.
func (m *Model) SetServices(svcs []model.Service) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.Services = append([]model.Service(nil), svcs...)
	filtered := m.filteredServicesLocked()
	if len(filtered) == 0 {
		m.SelectedSvc = 0
		m.svcScroll = 0
	} else if m.SelectedSvc >= len(filtered) {
		m.SelectedSvc = len(filtered) - 1
	}
	if m.SelectedSvc < 0 {
		m.SelectedSvc = 0
	}
}

// SetExchanges updates recorded HTTP traffic and recalculates diff if active.
func (m *Model) SetExchanges(exs []model.Exchange) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.Exchanges = append([]model.Exchange(nil), exs...)
	filtered := m.filteredExchangesLocked()
	if len(filtered) == 0 {
		m.SelectedEx = 0
		m.exScroll = 0
	} else if m.SelectedEx >= len(filtered) {
		m.SelectedEx = len(filtered) - 1
	}
	if m.SelectedEx < 0 {
		m.SelectedEx = 0
	}
	if m.ShowDiff {
		m.updateDiffLocked()
	}
}

func (m *Model) filteredServicesLocked() []model.Service {
	if m.FilterQuery == "" {
		return m.Services
	}
	q := strings.ToLower(m.FilterQuery)
	var res []model.Service
	for _, s := range m.Services {
		if strings.Contains(strings.ToLower(s.Name), q) ||
			strings.Contains(strings.ToLower(s.Host), q) ||
			strings.Contains(strings.ToLower(s.Repo), q) ||
			strings.Contains(strings.ToLower(s.Worktree), q) ||
			strings.Contains(strings.ToLower(s.Branch), q) ||
			strings.Contains(strings.ToLower(s.Agent), q) ||
			strings.Contains(strings.ToLower(s.Command), q) ||
			strings.Contains(strings.ToLower(string(s.ZombieReason)), q) ||
			strings.Contains(strconv.Itoa(s.Port), q) {
			res = append(res, s)
		}
	}
	return res
}

func (m *Model) filteredExchangesLocked() []model.Exchange {
	q := strings.ToLower(m.FilterQuery)
	var res []model.Exchange
	for _, ex := range m.Exchanges {
		if m.OnlyErrors && ex.Resp.StatusCode < 400 && ex.Error == "" {
			continue
		}
		if q != "" {
			match := strings.Contains(strings.ToLower(ex.Req.Path), q) ||
				strings.Contains(strings.ToLower(ex.Req.Method), q) ||
				strings.Contains(strings.ToLower(ex.Service), q) ||
				strings.Contains(strconv.Itoa(ex.Resp.StatusCode), q) ||
				strings.Contains(strings.ToLower(ex.Error), q)
			if !match {
				continue
			}
		}
		res = append(res, ex)
	}
	return res
}

// HandleKey processes keyboard input and returns an action to execute.
func (m *Model) HandleKey(key string) Action {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Live filtering mode
	if m.Filtering {
		switch key {
		case "enter", "\n", "\r":
			m.Filtering = false
			m.StatusMessage = "Filter: " + m.FilterQuery
			return Action{Type: ActionNone}
		case "esc", "\x1b":
			m.Filtering = false
			m.FilterQuery = ""
			m.StatusMessage = "Filter cleared"
			return Action{Type: ActionNone}
		case "backspace", "\b", "\x7f", "delete":
			if len(m.FilterQuery) > 0 {
				runes := []rune(m.FilterQuery)
				m.FilterQuery = string(runes[:len(runes)-1])
			}
			return Action{Type: ActionNone}
		default:
			if len(key) == 1 || utf8.RuneCountInString(key) == 1 {
				m.FilterQuery += key
			}
			return Action{Type: ActionNone}
		}
	}

	// Zombie sweep confirmation
	if m.ConfirmSweep {
		if key == "Z" || key == "y" || key == "Y" {
			m.ConfirmSweep = false
			m.StatusMessage = "Zombies sweep initiated"
			return Action{Type: ActionSweepZombies}
		}
		m.ConfirmSweep = false
		m.StatusMessage = "Sweep canceled"
		return Action{Type: ActionNone}
	}

	switch key {
	case "q", "ctrl+c":
		return Action{Type: ActionQuit}

	case "j", "down":
		if m.FocusPane == 0 {
			svcs := m.filteredServicesLocked()
			if len(svcs) > 0 && m.SelectedSvc < len(svcs)-1 {
				m.SelectedSvc++
			}
		} else {
			exs := m.filteredExchangesLocked()
			if len(exs) > 0 && m.SelectedEx < len(exs)-1 {
				m.SelectedEx++
				if m.ShowDiff {
					m.updateDiffLocked()
				}
			}
		}
		return Action{Type: ActionNone}

	case "k", "up":
		if m.FocusPane == 0 {
			if m.SelectedSvc > 0 {
				m.SelectedSvc--
			}
		} else {
			if m.SelectedEx > 0 {
				m.SelectedEx--
				if m.ShowDiff {
					m.updateDiffLocked()
				}
			}
		}
		return Action{Type: ActionNone}

	case "tab":
		m.FocusPane = 1 - m.FocusPane
		return Action{Type: ActionNone}

	case "h":
		m.FocusPane = 0
		return Action{Type: ActionNone}

	case "l":
		m.FocusPane = 1
		return Action{Type: ActionNone}

	case "r":
		exs := m.filteredExchangesLocked()
		if len(exs) > 0 && m.SelectedEx >= 0 && m.SelectedEx < len(exs) {
			ex := exs[m.SelectedEx]
			m.StatusMessage = "Replay triggered: " + ex.ID
			return Action{
				Type:       ActionReplay,
				ServiceID:  ex.ServiceID,
				ExchangeID: ex.ID,
			}
		}
		return Action{Type: ActionNone}

	case "c":
		exs := m.filteredExchangesLocked()
		if len(exs) > 0 && m.SelectedEx >= 0 && m.SelectedEx < len(exs) {
			ex := exs[m.SelectedEx]
			m.StatusMessage = "cURL copied for " + ex.ID
			return Action{
				Type:       ActionCopyCurl,
				ServiceID:  ex.ServiceID,
				ExchangeID: ex.ID,
			}
		}
		return Action{Type: ActionNone}

	case "d":
		m.ShowDiff = !m.ShowDiff
		var exID, cmpID string
		if m.ShowDiff {
			exID, cmpID = m.updateDiffLocked()
		}
		return Action{
			Type:              ActionDiff,
			ExchangeID:        exID,
			CompareExchangeID: cmpID,
		}

	case "K":
		svcs := m.filteredServicesLocked()
		if len(svcs) > 0 && m.SelectedSvc >= 0 && m.SelectedSvc < len(svcs) {
			svc := svcs[m.SelectedSvc]
			m.StatusMessage = fmt.Sprintf("Killing %s (PID %d)...", svc.Name, svc.PID)
			return Action{
				Type:      ActionKillService,
				ServiceID: svc.ID,
			}
		}
		return Action{Type: ActionNone}

	case "Z":
		zombies := 0
		for _, s := range m.Services {
			if s.IsZombie() {
				zombies++
			}
		}
		m.ConfirmSweep = true
		m.StatusMessage = fmt.Sprintf("Sweep %d zombie(s)? Press 'Z' or 'y' to confirm, any other key to cancel", zombies)
		return Action{Type: ActionNone}

	case "e":
		m.OnlyErrors = !m.OnlyErrors
		exs := m.filteredExchangesLocked()
		if len(exs) == 0 {
			m.SelectedEx = 0
		} else if m.SelectedEx >= len(exs) {
			m.SelectedEx = len(exs) - 1
		}
		if m.OnlyErrors {
			m.StatusMessage = "Filter: Only 4xx/5xx errors"
		} else {
			m.StatusMessage = "Filter: All HTTP exchanges"
		}
		if m.ShowDiff {
			m.updateDiffLocked()
		}
		return Action{Type: ActionNone}

	case "/":
		m.Filtering = true
		m.StatusMessage = "Filter: "
		return Action{Type: ActionNone}

	case "R", "f5":
		m.StatusMessage = "Refreshed"
		return Action{Type: ActionRefresh}
	}

	return Action{Type: ActionNone}
}

func (m *Model) updateDiffLocked() (string, string) {
	exs := m.filteredExchangesLocked()
	if len(exs) == 0 || m.SelectedEx < 0 || m.SelectedEx >= len(exs) {
		m.DiffText = "(no exchange selected for diff)"
		return "", ""
	}

	curr := exs[m.SelectedEx]
	var prev *model.Exchange

	if curr.ReplayedOf != "" {
		for i := range m.Exchanges {
			if m.Exchanges[i].ID == curr.ReplayedOf {
				prev = &m.Exchanges[i]
				break
			}
		}
	}
	if prev == nil && m.SelectedEx+1 < len(exs) {
		prev = &exs[m.SelectedEx+1]
	}

	if prev == nil {
		m.DiffText = fmt.Sprintf("Exchange %s\n(no previous exchange found to compare with)", curr.ID)
		return curr.ID, ""
	}

	m.DiffText = formatDiff(prev, &curr, m.NoColor)
	return curr.ID, prev.ID
}

func formatDiff(oldEx, newEx *model.Exchange, noColor bool) string {
	st := Style{noColor: noColor}
	var b strings.Builder

	b.WriteString(st.Dim(fmt.Sprintf("--- Original [%s] (%s %s -> %d, %.1fms)\n", oldEx.ID, oldEx.Req.Method, sanitizeTerm(oldEx.Req.Path), oldEx.Resp.StatusCode, oldEx.DurationMs)))
	b.WriteString(st.Bold(fmt.Sprintf("+++ Current  [%s] (%s %s -> %d, %.1fms)\n", newEx.ID, newEx.Req.Method, sanitizeTerm(newEx.Req.Path), newEx.Resp.StatusCode, newEx.DurationMs)))

	b.WriteString(st.Cyan("@@ Status & Timing @@\n"))
	if oldEx.Resp.StatusCode != newEx.Resp.StatusCode {
		b.WriteString(st.Red(fmt.Sprintf("- Status: %d %s\n", oldEx.Resp.StatusCode, oldEx.Resp.Status)))
		b.WriteString(st.Green(fmt.Sprintf("+ Status: %d %s\n", newEx.Resp.StatusCode, newEx.Resp.Status)))
	} else {
		b.WriteString(fmt.Sprintf("  Status: %d %s\n", newEx.Resp.StatusCode, newEx.Resp.Status))
	}

	if oldEx.DurationMs != newEx.DurationMs {
		b.WriteString(st.Red(fmt.Sprintf("- Duration: %.1fms\n", oldEx.DurationMs)))
		b.WriteString(st.Green(fmt.Sprintf("+ Duration: %.1fms\n", newEx.DurationMs)))
	}

	b.WriteString(st.Cyan("@@ Response Body @@\n"))
	oldBody := strings.TrimSpace(sanitizeTerm(string(oldEx.Resp.Body)))
	newBody := strings.TrimSpace(sanitizeTerm(string(newEx.Resp.Body)))
	if oldBody == newBody {
		b.WriteString("  (Response body unchanged)\n")
	} else {
		if oldBody != "" {
			b.WriteString(st.Red(fmt.Sprintf("- %s\n", oldBody)))
		}
		if newBody != "" {
			b.WriteString(st.Green(fmt.Sprintf("+ %s\n", newBody)))
		}
	}

	return b.String()
}

// FormatCurl produces a replayable curl command for an exchange.
func FormatCurl(ex *model.Exchange) string {
	if ex == nil {
		return ""
	}
	host := ex.Req.Host
	if host == "" {
		host = ex.Service + ".localhost"
	}
	url := fmt.Sprintf("http://%s%s", host, ex.Req.Path)
	if ex.Req.Query != "" {
		url += "?" + ex.Req.Query
	}
	var b strings.Builder
	b.WriteString(fmt.Sprintf("curl -X %s '%s'", ex.Req.Method, url))
	for k, vals := range ex.Req.Headers {
		for _, v := range vals {
			b.WriteString(fmt.Sprintf(" -H '%s: %s'", k, v))
		}
	}
	if len(ex.Req.Body) > 0 {
		b.WriteString(fmt.Sprintf(" -d '%s'", string(ex.Req.Body)))
	}
	return b.String()
}

// Render draws the complete screen frame respecting Width and Height.
func (m *Model) Render() string {
	m.mu.Lock()
	defer m.mu.Unlock()

	w := m.Width
	if w <= 0 {
		w = 120
	}
	h := m.Height
	if h <= 0 {
		h = 32
	}
	if w < 40 {
		w = 40
	}
	if h < 8 {
		h = 8
	}

	st := Style{noColor: m.NoColor}

	// Width partitioning: 1 + leftW + 1 + rightW + 1 = w
	leftW := (w - 3) * 38 / 100
	if leftW < 18 {
		leftW = 18
	}
	rightW := w - 3 - leftW
	if rightW < 18 {
		rightW = 18
		leftW = w - 3 - rightW
	}

	// Height partitioning
	// Line 0: Top Header Border
	// Line 1: Panels Title Border
	// Lines 2 .. h-3: Content rows (h - 4 rows)
	// Line h-2: Bottom Divider Border
	// Line h-1: Status / Shortcut Bar
	contentRows := h - 4
	if contentRows < 2 {
		contentRows = 2
	}

	trafficRows := contentRows * 45 / 100
	if trafficRows < 2 {
		trafficRows = 2
	}
	inspectorRows := contentRows - trafficRows - 1
	if inspectorRows < 1 {
		inspectorRows = 1
		trafficRows = contentRows - inspectorRows - 1
	}

	// Aggregated statistics for top bar
	activeCount := 0
	zombieCount := 0
	agentMap := make(map[string]int)
	for _, s := range m.Services {
		if s.IsZombie() {
			zombieCount++
		} else if s.Running {
			activeCount++
		}
		if s.Agent != "" {
			agentMap[s.Agent]++
		}
	}
	var agentNames []string
	for a := range agentMap {
		agentNames = append(agentNames, a)
	}
	sort.Strings(agentNames)
	var agentParts []string
	for _, a := range agentNames {
		agentParts = append(agentParts, fmt.Sprintf("%s(%d)", a, agentMap[a]))
	}

	lines := make([]string, 0, h)

	// --- Line 0: Top Bar Border ---
	// ┌─ ⚡ lazylocal ── 3 active ── 🤖 claude/codex ── 🧟 1 zombie ──── http://hub.localhost ─┐
	hubTag := "http://hub.localhost"
	rightBorder := " ── " + st.Cyan(hubTag) + " ─┐"
	rightBorderW := StringWidth(rightBorder)

	leftBudget := w - rightBorderW
	if leftBudget < 20 {
		leftBudget = 20
	}

	var agentDisplay string
	if len(agentNames) == 0 {
		agentDisplay = "🤖 0"
	} else if w >= 130 {
		agentDisplay = "🤖 " + strings.Join(agentParts, " ")
	} else {
		agentDisplay = "🤖 " + strings.Join(agentNames, "/")
	}

	zombieDisplay := fmt.Sprintf("🧟 %d", zombieCount)
	if w >= 110 {
		sfx := "s"
		if zombieCount == 1 {
			sfx = ""
		}
		zombieDisplay = fmt.Sprintf("🧟 %d zombie%s", zombieCount, sfx)
	}

	headerParts := []string{
		st.Bold("⚡ lazylocal"),
		fmt.Sprintf("%d active", activeCount),
		agentDisplay,
		zombieDisplay,
	}

	leftJoined := "┌─ " + strings.Join(headerParts, " ── ") + " "
	if StringWidth(leftJoined) < leftBudget {
		leftJoined += strings.Repeat("─", leftBudget-StringWidth(leftJoined))
	}
	line0Left := PadOrTruncate(leftJoined, leftBudget, m.NoColor)
	line0 := line0Left + rightBorder
	lines = append(lines, line0)

	// --- Line 1: Panels Header Border ---
	// ├─ Services (3) ───────────────┬─ HTTP Traffic (12) ─────────────────────────┤
	filteredSvcs := m.filteredServicesLocked()
	filteredExs := m.filteredExchangesLocked()

	var svcTitle string
	if m.FocusPane == 0 {
		svcTitle = st.Selected(fmt.Sprintf(" Services (%d) ", len(filteredSvcs)))
	} else {
		svcTitle = st.Bold(fmt.Sprintf(" Services (%d) ", len(filteredSvcs)))
	}
	leftHeader := "─" + svcTitle
	if StringWidth(leftHeader) < leftW {
		leftHeader += strings.Repeat("─", leftW-StringWidth(leftHeader))
	}
	leftHeader = PadOrTruncate(leftHeader, leftW, m.NoColor)

	var exTitle string
	if m.OnlyErrors {
		exTitle = fmt.Sprintf(" Traffic [Errors: %d] ", len(filteredExs))
	} else {
		exTitle = fmt.Sprintf(" Traffic (%d) ", len(filteredExs))
	}
	if m.FocusPane == 1 {
		exTitle = st.Selected(exTitle)
	} else {
		exTitle = st.Bold(exTitle)
	}
	rightHeader := "─" + exTitle
	if StringWidth(rightHeader) < rightW {
		rightHeader += strings.Repeat("─", rightW-StringWidth(rightHeader))
	}
	rightHeader = PadOrTruncate(rightHeader, rightW, m.NoColor)

	line1 := "├" + leftHeader + "┬" + rightHeader + "┤"
	lines = append(lines, line1)

	// --- Auto-scroll viewports ---
	if m.SelectedSvc < m.svcScroll {
		m.svcScroll = m.SelectedSvc
	}
	if m.SelectedSvc >= m.svcScroll+contentRows {
		m.svcScroll = m.SelectedSvc - contentRows + 1
	}
	if m.svcScroll < 0 {
		m.svcScroll = 0
	}

	if m.SelectedEx < m.exScroll {
		m.exScroll = m.SelectedEx
	}
	if m.SelectedEx >= m.exScroll+trafficRows {
		m.exScroll = m.SelectedEx - trafficRows + 1
	}
	if m.exScroll < 0 {
		m.exScroll = 0
	}

	// Pre-generate Inspector / Diff lines
	var inspectorLines []string
	if m.ShowDiff {
		diffSplit := strings.Split(m.DiffText, "\n")
		for _, dl := range diffSplit {
			inspectorLines = append(inspectorLines, dl)
		}
	} else {
		if len(filteredExs) == 0 || m.SelectedEx < 0 || m.SelectedEx >= len(filteredExs) {
			inspectorLines = append(inspectorLines, st.Dim("(no exchange selected)"))
		} else {
			ex := filteredExs[m.SelectedEx]
			inspectorLines = append(inspectorLines, fmt.Sprintf("%s %s  ->  %s  (%.1fms)",
				st.Bold(ex.Req.Method),
				sanitizeTerm(ex.Req.Path),
				formatStatusCode(ex.Resp.StatusCode, st),
				ex.DurationMs,
			))
			inspectorLines = append(inspectorLines, st.Dim(fmt.Sprintf("Host: %s:%d  Started: %s",
				sanitizeTerm(ex.Service)+".localhost",
				ex.TargetPort,
				ex.StartedAt.Format("15:04:05.000"),
			)))
			if len(ex.Req.Headers) > 0 {
				var hKeys []string
				for k := range ex.Req.Headers {
					hKeys = append(hKeys, k)
				}
				sort.Strings(hKeys)
				var hPairs []string
				for _, k := range hKeys {
					vals := ex.Req.Headers[k]
					if len(vals) > 0 {
						hPairs = append(hPairs, fmt.Sprintf("%s: %s", sanitizeTerm(k), sanitizeTerm(vals[0])))
					}
				}
				inspectorLines = append(inspectorLines, st.Dim("Req Headers: "+strings.Join(hPairs, " | ")))
			}
			if len(ex.Req.Body) > 0 {
				bodySnippet := strings.ReplaceAll(sanitizeTerm(string(ex.Req.Body)), "\n", " ")
				inspectorLines = append(inspectorLines, "Req Body: "+bodySnippet)
			}
			if len(ex.Resp.Body) > 0 {
				respSnippet := strings.ReplaceAll(sanitizeTerm(string(ex.Resp.Body)), "\n", " ")
				inspectorLines = append(inspectorLines, "Resp Body: "+respSnippet)
			}
			if ex.Error != "" {
				inspectorLines = append(inspectorLines, st.Red("Error: "+sanitizeTerm(ex.Error)))
			}
		}
	}

	// --- Content Rows (r from 0 to contentRows - 1) ---
	for r := 0; r < contentRows; r++ {
		// 1. Left cell: Service item
		svcIdx := m.svcScroll + r
		var leftText string
		if len(filteredSvcs) == 0 {
			if r == 0 {
				leftText = st.Dim("  (no services discovered)")
			} else {
				leftText = ""
			}
		} else if svcIdx < len(filteredSvcs) {
			svc := filteredSvcs[svcIdx]
			isSelected := (svcIdx == m.SelectedSvc)
			leftText = formatServiceRow(&svc, isSelected, leftW, st)
		}
		leftCell := PadOrTruncate(leftText, leftW, m.NoColor)

		// 2. Right cell: Traffic list (r < trafficRows), Divider (r == trafficRows), Inspector (r > trafficRows)
		var rightCell string
		var midChar, rightEndChar string

		if r < trafficRows {
			midChar = "│"
			rightEndChar = "│"
			exIdx := m.exScroll + r
			var rightText string
			if len(filteredExs) == 0 {
				if r == 0 {
					rightText = st.Dim("  (no traffic recorded)")
				} else {
					rightText = ""
				}
			} else if exIdx < len(filteredExs) {
				ex := filteredExs[exIdx]
				isSelected := (exIdx == m.SelectedEx)
				rightText = formatExchangeRow(&ex, isSelected, rightW, st)
			}
			rightCell = PadOrTruncate(rightText, rightW, m.NoColor)

		} else if r == trafficRows {
			midChar = "├"
			rightEndChar = "┤"
			var inspectTitle string
			if m.ShowDiff {
				inspectTitle = "─ Diff "
			} else {
				if len(filteredExs) > 0 && m.SelectedEx < len(filteredExs) {
					ex := filteredExs[m.SelectedEx]
					inspectTitle = fmt.Sprintf("─ Inspector [%s %s -> %d] ", ex.Req.Method, sanitizeTerm(ex.Req.Path), ex.Resp.StatusCode)
				} else {
					inspectTitle = "─ Inspector "
				}
			}
			inspectTitle = st.Bold(inspectTitle)
			if StringWidth(inspectTitle) < rightW {
				inspectTitle += strings.Repeat("─", rightW-StringWidth(inspectTitle))
			}
			rightCell = PadOrTruncate(inspectTitle, rightW, m.NoColor)

		} else {
			midChar = "│"
			rightEndChar = "│"
			ir := r - trafficRows - 1
			var inspectText string
			if ir < len(inspectorLines) {
				inspectText = "  " + inspectorLines[ir]
			}
			rightCell = PadOrTruncate(inspectText, rightW, m.NoColor)
		}

		line := "│" + leftCell + midChar + rightCell + rightEndChar
		lines = append(lines, line)
	}

	// --- Line h-2: Bottom Divider Border ---
	// ├──────────────────────────────┴─────────────────────────────────────────────┤
	lineH2 := "├" + strings.Repeat("─", leftW) + "┴" + strings.Repeat("─", rightW) + "┤"
	lines = append(lines, lineH2)

	// --- Line h-1: Status / Shortcut Bar ---
	var statusContent string
	if m.Filtering {
		statusContent = fmt.Sprintf(" Filter: %s█   [Enter] Confirm   [Esc] Clear", m.FilterQuery)
	} else if m.ConfirmSweep {
		statusContent = fmt.Sprintf(" ⚠️  Sweep %d zombie(s)? Press 'Z' or 'y' to confirm, any other key to cancel", zombieCount)
	} else if m.StatusMessage != "" {
		statusContent = fmt.Sprintf(" %s   │  [j/k] Move  [Tab] Pane  [r] Replay  [c] cURL  [d] Diff  [q] Quit", m.StatusMessage)
	} else {
		statusContent = " [j/k] Move  [Tab] Pane  [r] Replay  [c] cURL  [d] Diff  [e] 4xx/5xx  [K] Kill  [Z] Sweep Zombies  [/] Filter  [q] Quit"
	}

	lineH1 := "└" + PadOrTruncate("─ "+statusContent+" ", w-2, m.NoColor) + "┘"
	lines = append(lines, lineH1)

	return strings.Join(lines, "\n")
}

func formatServiceRow(s *model.Service, selected bool, width int, st Style) string {
	prefix := "  "
	if selected {
		prefix = "❯ "
	}

	dot := "●"
	if !s.Running {
		dot = "○"
	}

	host := s.Host
	if host == "" {
		host = s.Name + ".localhost"
	}
	portStr := fmt.Sprintf("-> :%d", s.Port)

	var badges []string
	if s.Pinned {
		badges = append(badges, st.Cyan("[📌 PIN]"))
	}
	if s.IsZombie() {
		badges = append(badges, st.Red(fmt.Sprintf("[🧟 ZOMBIE: %s]", s.ZombieReason)))
	}
	if s.Agent != "" {
		badges = append(badges, st.Magenta(fmt.Sprintf("[🤖 %s]", s.Agent)))
	}
	if s.Branch != "" {
		badges = append(badges, st.Dim(fmt.Sprintf("(%s)", s.Branch)))
	}

	coloredDot := dot
	if s.IsZombie() {
		coloredDot = st.Red(dot)
	} else if s.Running {
		coloredDot = st.Green(dot)
	} else {
		coloredDot = st.Dim(dot)
	}

	line := fmt.Sprintf("%s%s %s %s", prefix, coloredDot, host, st.Dim(portStr))
	if len(badges) > 0 {
		line += " " + strings.Join(badges, " ")
	}

	if selected {
		return st.Selected(line)
	}
	return line
}

func formatExchangeRow(ex *model.Exchange, selected bool, width int, st Style) string {
	prefix := "  "
	if selected {
		prefix = "❯ "
	}

	methodStr := formatMethod(ex.Req.Method, st)
	statusStr := formatStatusCode(ex.Resp.StatusCode, st)
	durStr := st.Dim(fmt.Sprintf("%4.0fms", ex.DurationMs))
	if ex.DurationMs >= 1000 {
		durStr = st.Dim(fmt.Sprintf("%4.1fs", ex.DurationMs/1000))
	}

	path := sanitizeTerm(ex.Req.Path)
	if ex.Req.Query != "" {
		path += "?" + sanitizeTerm(ex.Req.Query)
	}

	var badges []string
	if ex.ReplayedOf != "" {
		badges = append(badges, st.Magenta("[REPLAY]"))
	}
	if ex.Error != "" {
		badges = append(badges, st.Red("[ERR]"))
	}

	line := fmt.Sprintf("%s%s %s %s %s", prefix, methodStr, statusStr, durStr, path)
	if len(badges) > 0 {
		line += " " + strings.Join(badges, " ")
	}

	if selected {
		return st.Selected(line)
	}
	return line
}

func formatMethod(m string, st Style) string {
	padded := fmt.Sprintf("%-4s", m)
	switch m {
	case "GET":
		return st.Cyan(padded)
	case "POST":
		return st.Green(padded)
	case "PUT", "PATCH":
		return st.Yellow(padded)
	case "DELETE":
		return st.Red(padded)
	default:
		return st.Bold(padded)
	}
}

func formatStatusCode(code int, st Style) string {
	codeStr := strconv.Itoa(code)
	if code >= 200 && code < 300 {
		return st.Green(codeStr)
	}
	if code >= 300 && code < 400 {
		return st.Cyan(codeStr)
	}
	if code >= 400 && code < 500 {
		return st.Yellow(codeStr)
	}
	if code >= 500 {
		return st.Red(st.Bold(codeStr))
	}
	return codeStr
}
