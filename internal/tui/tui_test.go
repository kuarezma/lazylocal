package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/kuarezma/lazylocal/internal/model"
)

func makeTestServices(count int) []model.Service {
	svcs := make([]model.Service, count)
	agents := []string{"claude", "codex", "gemini", ""}
	for i := 0; i < count; i++ {
		svc := model.Service{
			ID:      fmt.Sprintf("svc-%03d", i+1),
			Name:    fmt.Sprintf("app-%d", i+1),
			Host:    fmt.Sprintf("app-%d.localhost", i+1),
			Port:    3000 + i,
			PID:     1000 + i,
			Running: i%4 != 3,
			Agent:   agents[i%len(agents)],
			Repo:    "lazylocal",
			Branch:  "main",
		}
		if i == 0 {
			svc.Pinned = true
		}
		if i == 2 {
			svc.ZombieReason = model.ZombieDeletedCwd
		}
		svcs[i] = svc
	}
	return svcs
}

func makeTestExchanges(count int) []model.Exchange {
	exs := make([]model.Exchange, count)
	methods := []string{"GET", "POST", "PUT", "DELETE"}
	paths := []string{"/api/users", "/api/checkout", "/health", "/login", "/static/app.js"}
	statuses := []int{200, 201, 204, 400, 404, 500}

	for i := 0; i < count; i++ {
		status := statuses[i%len(statuses)]
		ex := model.Exchange{
			ID:         fmt.Sprintf("ex-%04d", i+1),
			ServiceID:  fmt.Sprintf("svc-%03d", (i%10)+1),
			Service:    fmt.Sprintf("app-%d", (i%10)+1),
			TargetPort: 3000 + (i % 10),
			StartedAt:  time.Now().Add(-time.Duration(count-i) * time.Second),
			DurationMs: float64(10 + (i%50)*5),
			Req: model.HTTPRequestData{
				Method:      methods[i%len(methods)],
				Path:        paths[i%len(paths)],
				Headers:     map[string][]string{"Accept": {"application/json"}},
				Body:        []byte(`{"action":"test"}`),
				BodySize:    17,
				ContentType: "application/json",
			},
			Resp: model.HTTPResponseData{
				StatusCode:  status,
				Status:      fmt.Sprintf("%d Status", status),
				Headers:     map[string][]string{"Content-Type": {"application/json"}},
				Body:        []byte(fmt.Sprintf(`{"code":%d,"message":"result"}`, status)),
				BodySize:    35,
				ContentType: "application/json",
			},
		}
		if i > 0 && i%5 == 0 {
			ex.ReplayedOf = exs[i-1].ID
		}
		if status >= 500 {
			ex.Error = "server internal error"
		}
		exs[i] = ex
	}
	return exs
}

// TestTUISnapshot80x24 verifies layout, line count, and width bounds at 80x24.
func TestTUISnapshot80x24(t *testing.T) {
	m := NewModel(80, 24)
	m.NoColor = true
	m.SetServices(makeTestServices(8))
	m.SetExchanges(makeTestExchanges(12))

	rendered := m.Render()
	lines := strings.Split(rendered, "\n")

	if len(lines) != 24 {
		t.Fatalf("expected 24 lines at 80x24, got %d", len(lines))
	}

	for i, line := range lines {
		w := StringWidth(line)
		if w > 80 {
			t.Errorf("line %d width %d exceeds 80: %q", i, w, line)
		}
	}

	// Verify header and core visual elements exist in snapshot
	content := rendered
	if !strings.Contains(content, "lazylocal") {
		t.Errorf("expected 'lazylocal' in snapshot")
	}
	if !strings.Contains(content, "Services") {
		t.Errorf("expected 'Services' in snapshot")
	}
	if !strings.Contains(content, "Traffic") {
		t.Errorf("expected 'Traffic' in snapshot")
	}
	if !strings.Contains(content, "hub.localhost") {
		t.Errorf("expected 'hub.localhost' in snapshot")
	}

	// Test with ANSI color enabled as well
	m.NoColor = false
	renderedColor := m.Render()
	colorLines := strings.Split(renderedColor, "\n")
	if len(colorLines) != 24 {
		t.Fatalf("expected 24 lines with color at 80x24, got %d", len(colorLines))
	}
	for i, line := range colorLines {
		w := StringWidth(line)
		if w > 80 {
			t.Errorf("color line %d width %d exceeds 80", i, w)
		}
	}
}

// TestTUISnapshot160x48 verifies layout, line count, and width bounds at 160x48.
func TestTUISnapshot160x48(t *testing.T) {
	m := NewModel(160, 48)
	m.NoColor = true
	m.SetServices(makeTestServices(30))
	m.SetExchanges(makeTestExchanges(60))

	rendered := m.Render()
	lines := strings.Split(rendered, "\n")

	if len(lines) != 48 {
		t.Fatalf("expected 48 lines at 160x48, got %d", len(lines))
	}

	for i, line := range lines {
		w := StringWidth(line)
		if w > 160 {
			t.Errorf("line %d width %d exceeds 160: %q", i, w, line)
		}
	}
}

// TestTUIEmptyStateNoPanic verifies rendering and all key presses on an empty model.
func TestTUIEmptyStateNoPanic(t *testing.T) {
	m := NewModel(80, 24)
	m.SetServices(nil)
	m.SetExchanges(nil)

	// Render empty
	out := m.Render()
	lines := strings.Split(out, "\n")
	if len(lines) != 24 {
		t.Fatalf("expected 24 lines on empty state, got %d", len(lines))
	}

	// Exercise all supported keys
	keys := []string{
		"j", "k", "down", "up",
		"tab", "h", "l",
		"r", "c", "d", "K", "Z", "e",
		"/", "enter", "esc", "backspace",
		"R", "f5", "q", "ctrl+c", "unknown",
	}

	for _, k := range keys {
		act := m.HandleKey(k)
		_ = act
	}

	// Must still render cleanly
	outAfter := m.Render()
	if len(strings.Split(outAfter, "\n")) != 24 {
		t.Fatalf("expected 24 lines after empty key handling")
	}
}

// TestTUILargeStateKeysNoPanic verifies 200 services + 500 records and all keyboard actions.
func TestTUILargeStateKeysNoPanic(t *testing.T) {
	m := NewModel(120, 32)
	svcs := makeTestServices(200)
	exs := makeTestExchanges(500)

	m.SetServices(svcs)
	m.SetExchanges(exs)

	out := m.Render()
	if len(strings.Split(out, "\n")) != 32 {
		t.Fatalf("expected 32 lines on large state, got %d", len(strings.Split(out, "\n")))
	}

	// 1. Move down / up in Services pane
	m.FocusPane = 0
	m.HandleKey("j")
	if m.SelectedSvc != 1 {
		t.Errorf("expected SelectedSvc 1, got %d", m.SelectedSvc)
	}
	m.HandleKey("down")
	if m.SelectedSvc != 2 {
		t.Errorf("expected SelectedSvc 2, got %d", m.SelectedSvc)
	}
	m.HandleKey("k")
	if m.SelectedSvc != 1 {
		t.Errorf("expected SelectedSvc 1, got %d", m.SelectedSvc)
	}
	m.HandleKey("up")
	if m.SelectedSvc != 0 {
		t.Errorf("expected SelectedSvc 0, got %d", m.SelectedSvc)
	}

	// 2. Kill service
	actKill := m.HandleKey("K")
	if actKill.Type != ActionKillService || actKill.ServiceID != svcs[0].ID {
		t.Errorf("expected ActionKillService with ID %s, got %+v", svcs[0].ID, actKill)
	}

	// 3. Tab switch to Traffic pane
	m.HandleKey("tab")
	if m.FocusPane != 1 {
		t.Errorf("expected FocusPane 1 after tab, got %d", m.FocusPane)
	}

	// 4. Move down in Traffic pane
	m.HandleKey("j")
	if m.SelectedEx != 1 {
		t.Errorf("expected SelectedEx 1, got %d", m.SelectedEx)
	}

	// 5. Replay action (non-idempotent requires confirmation on first 'r', executes on second 'r')
	actPrompt := m.HandleKey("r")
	if actPrompt.Type != ActionNone {
		t.Errorf("expected ActionNone prompt on first 'r' for %s, got %+v", exs[1].ID, actPrompt)
	}
	actReplay := m.HandleKey("r")
	if actReplay.Type != ActionReplay || actReplay.ExchangeID != exs[1].ID {
		t.Errorf("expected ActionReplay for %s, got %+v", exs[1].ID, actReplay)
	}

	// 6. cURL copy action
	actCurl := m.HandleKey("c")
	if actCurl.Type != ActionCopyCurl || actCurl.ExchangeID != exs[1].ID {
		t.Errorf("expected ActionCopyCurl for %s, got %+v", exs[1].ID, actCurl)
	}
	curlCmd := FormatCurl(&exs[1])
	if !strings.HasPrefix(curlCmd, "curl -X") {
		t.Errorf("expected valid curl command, got: %s", curlCmd)
	}

	// 7. Diff action toggle
	actDiff := m.HandleKey("d")
	if actDiff.Type != ActionDiff || !m.ShowDiff {
		t.Errorf("expected ActionDiff with ShowDiff=true, got %+v", actDiff)
	}
	if m.DiffText == "" {
		t.Errorf("expected non-empty DiffText")
	}

	// Diff toggle off
	actDiffOff := m.HandleKey("d")
	if actDiffOff.Type != ActionDiff || m.ShowDiff {
		t.Errorf("expected ShowDiff=false after second toggle")
	}

	// 8. Error filter toggle
	m.HandleKey("e")
	if !m.OnlyErrors {
		t.Errorf("expected OnlyErrors true")
	}
	m.HandleKey("e")
	if m.OnlyErrors {
		t.Errorf("expected OnlyErrors false after second toggle")
	}

	// 9. Zombie sweep confirmation workflow
	actSweepInit := m.HandleKey("Z")
	if actSweepInit.Type != ActionNone || !m.ConfirmSweep {
		t.Errorf("expected ConfirmSweep true and ActionNone, got %+v", actSweepInit)
	}
	actSweepConfirm := m.HandleKey("Z")
	if actSweepConfirm.Type != ActionSweepZombies || m.ConfirmSweep {
		t.Errorf("expected ActionSweepZombies and ConfirmSweep false, got %+v", actSweepConfirm)
	}

	// Test 'y' confirmation
	m.HandleKey("Z")
	actYConfirm := m.HandleKey("y")
	if actYConfirm.Type != ActionSweepZombies {
		t.Errorf("expected ActionSweepZombies on 'y' confirm, got %+v", actYConfirm)
	}

	// Test cancel confirmation
	m.HandleKey("Z")
	actCancel := m.HandleKey("n")
	if actCancel.Type != ActionNone || m.ConfirmSweep {
		t.Errorf("expected sweep to be canceled")
	}

	// 10. Live filter workflow
	m.HandleKey("/")
	if !m.Filtering {
		t.Errorf("expected Filtering true")
	}
	m.HandleKey("u")
	m.HandleKey("s")
	m.HandleKey("e")
	m.HandleKey("r")
	if m.FilterQuery != "user" {
		t.Errorf("expected FilterQuery 'user', got %q", m.FilterQuery)
	}
	m.HandleKey("backspace")
	if m.FilterQuery != "use" {
		t.Errorf("expected FilterQuery 'use', got %q", m.FilterQuery)
	}
	m.HandleKey("enter")
	if m.Filtering {
		t.Errorf("expected Filtering false after enter")
	}

	// Clear filter via Esc
	m.HandleKey("/")
	m.HandleKey("esc")
	if m.Filtering || m.FilterQuery != "" {
		t.Errorf("expected FilterQuery empty and Filtering false after esc")
	}

	// 11. Quit action
	actQuit := m.HandleKey("q")
	if actQuit.Type != ActionQuit {
		t.Errorf("expected ActionQuit, got %+v", actQuit)
	}

	actQuitCtrlC := m.HandleKey("ctrl+c")
	if actQuitCtrlC.Type != ActionQuit {
		t.Errorf("expected ActionQuit on ctrl+c, got %+v", actQuitCtrlC)
	}

	// Render after all actions
	finalRender := m.Render()
	if len(strings.Split(finalRender, "\n")) != 32 {
		t.Errorf("expected 32 lines in final render")
	}
}

// BenchmarkRender200Services500Exchanges proves rendering 200 services and 500 records is well under 16ms (target < 2ms).
func BenchmarkRender200Services500Exchanges(b *testing.B) {
	m := NewModel(120, 32)
	m.SetServices(makeTestServices(200))
	m.SetExchanges(makeTestExchanges(500))

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = m.Render()
	}
}

// TestTerminalEscapeSanitization verifies H4: untrusted Path containing terminal escapes (OSC 52)
// is sanitized so that raw \x1b]52 never appears in Render() output.
func TestTerminalEscapeSanitization(t *testing.T) {
	m := NewModel(120, 32)
	evilPayload := "/test\x1b]52;c;evil\x07"
	m.SetExchanges([]model.Exchange{
		{
			ID:      "ex-evil",
			Service: "evilapp",
			Req: model.HTTPRequestData{
				Method: "GET",
				Path:   evilPayload,
			},
			Resp: model.HTTPResponseData{
				StatusCode: 200,
			},
		},
	})
	m.FocusPane = 1
	m.SelectedEx = 0

	out := m.Render()
	if strings.Contains(out, "\x1b]52") {
		t.Fatalf("Security failure: raw ANSI escape sequence \\x1b]52 found in Render() output!")
	}
	if !strings.Contains(out, `\x1b]52`) {
		t.Fatalf("Expected sanitized string representation in Render() output, got: %s", out)
	}
}

func TestTUI_ANSISanitization(t *testing.T) {
	TestTerminalEscapeSanitization(t)
}

func TestTUIReplayConfirmation(t *testing.T) {
	m := NewModel(120, 32)
	exs := []model.Exchange{
		{
			ID:      "ex-get",
			Service: "api",
			Method:  "GET",
			Path:    "/health",
			Req: model.HTTPRequestData{
				Method: "GET",
				Path:   "/health",
			},
		},
		{
			ID:      "ex-post",
			Service: "api",
			Method:  "POST",
			Path:    "/orders",
			Req: model.HTTPRequestData{
				Method: "POST",
				Path:   "/orders",
			},
		},
	}
	m.SetExchanges(exs)
	m.FocusPane = 1

	// 1. GET request replays immediately on first 'r'
	m.SelectedEx = 0
	act := m.HandleKey("r")
	if act.Type != ActionReplay || act.ExchangeID != "ex-get" {
		t.Fatalf("expected immediate ActionReplay for GET, got %+v", act)
	}

	// 2. POST request: first 'r' prompts confirmation
	m.SelectedEx = 1
	actPrompt := m.HandleKey("r")
	if actPrompt.Type != ActionNone {
		t.Fatalf("expected ActionNone prompt for POST, got %+v", actPrompt)
	}
	if !strings.Contains(m.StatusMessage, "Confirm replay of POST /orders?") {
		t.Fatalf("expected confirmation status prompt, got %q", m.StatusMessage)
	}

	// Any other key cancels
	m.HandleKey("k")
	if m.pendingConfirmExchangeID != "" {
		t.Fatalf("expected pendingConfirmExchangeID to be cleared after 'k'")
	}

	// 3. POST request: prompt on 'r', then confirm with 'y'
	m.SelectedEx = 1
	m.HandleKey("r")
	actY := m.HandleKey("y")
	if actY.Type != ActionReplay || actY.ExchangeID != "ex-post" {
		t.Fatalf("expected ActionReplay on 'y' confirmation, got %+v", actY)
	}

	// 4. POST request: prompt on 'r', then confirm with 'r'
	m.SelectedEx = 1
	m.HandleKey("r")
	actR := m.HandleKey("r")
	if actR.Type != ActionReplay || actR.ExchangeID != "ex-post" {
		t.Fatalf("expected ActionReplay on 'r' re-press, got %+v", actR)
	}
}
