package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/kuarezma/lazylocal/internal/capture"
	"github.com/kuarezma/lazylocal/internal/config"
	"github.com/kuarezma/lazylocal/internal/discover"
	"github.com/kuarezma/lazylocal/internal/model"
	"github.com/kuarezma/lazylocal/internal/procs"
	"github.com/kuarezma/lazylocal/internal/proxy"
	"github.com/kuarezma/lazylocal/internal/tui"
	"github.com/kuarezma/lazylocal/internal/web"
)

const version = "v0.1.0"

// Engine orchestrates discovery, reverse proxying, tap ring buffer, and web dashboard.
type Engine struct {
	mu        sync.RWMutex
	port      int
	interval  time.Duration
	scanner   *discover.Scanner
	ring      *capture.RingBuffer
	bus       *model.Bus
	proxy     *proxy.Proxy
	webServer *web.Server
	pins      []model.Service
	services  []model.Service
	listener  net.Listener
	server    *http.Server
	ctx       context.Context
	cancel    context.CancelFunc
}

// NewEngine creates an initialized Engine instance.
func NewEngine(port int, pinsPath string, interval time.Duration) (*Engine, error) {
	if port < 0 {
		port = 7080
	}
	if interval <= 0 {
		interval = 2 * time.Second
	}

	pins := loadPins(pinsPath)
	bus := model.NewBus()
	ring := capture.NewRingBuffer(capture.DefaultCapacity)
	prx := proxy.NewProxy(ring, bus)

	ctx, cancel := context.WithCancel(context.Background())

	eng := &Engine{
		port:     port,
		interval: interval,
		scanner:  discover.NewScanner(),
		ring:     ring,
		bus:      bus,
		proxy:    prx,
		pins:     pins,
		services: make([]model.Service, 0),
		ctx:      ctx,
		cancel:   cancel,
	}

	eng.webServer = web.NewServer(eng, bus)
	eng.webServer.SetRing(eng.ring)
	return eng, nil
}

// Start binds loopback TCP socket, starts HTTP server and launches background scan loop.
func (e *Engine) Start() error {
	addr := fmt.Sprintf("127.0.0.1:%d", e.port)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("failed to bind loopback address %s: %w", addr, err)
	}

	e.listener = ln
	e.port = ln.Addr().(*net.TCPAddr).Port

	e.server = &http.Server{
		Handler: e,
	}

	go func() {
		if err := e.server.Serve(ln); err != nil && err != http.ErrServerClosed {
			fmt.Fprintf(os.Stderr, "Server error: %v\n", err)
		}
	}()

	// Initial scan
	ctx, cancel := context.WithTimeout(e.ctx, 5*time.Second)
	_, _ = e.ScanAndUpdate(ctx)
	cancel()

	// Periodic background scanner
	if e.interval > 0 {
		go e.scanLoop()
	}

	return nil
}

// Close shuts down the HTTP server and cancels background operations.
func (e *Engine) Close() error {
	e.cancel()
	if e.server != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = e.server.Shutdown(ctx)
	}
	if e.listener != nil {
		return e.listener.Close()
	}
	return nil
}

// Port returns the active listening loopback port.
func (e *Engine) Port() int {
	return e.port
}

// Addr returns the network address string.
func (e *Engine) Addr() string {
	if e.listener != nil {
		return e.listener.Addr().String()
	}
	return fmt.Sprintf("127.0.0.1:%d", e.port)
}

// Bus returns the pub/sub event bus.
func (e *Engine) Bus() *model.Bus {
	return e.bus
}

// Ring returns the HTTP capture ring buffer.
func (e *Engine) Ring() *capture.RingBuffer {
	return e.ring
}

// Proxy returns the underlying reverse proxy.
func (e *Engine) Proxy() *proxy.Proxy {
	return e.proxy
}

// WebServer returns the web dashboard server.
func (e *Engine) WebServer() *web.Server {
	return e.webServer
}

// scanLoop runs periodic discovery ticks.
func (e *Engine) scanLoop() {
	ticker := time.NewTicker(e.interval)
	defer ticker.Stop()

	for {
		select {
		case <-e.ctx.Done():
			return
		case <-ticker.C:
			ctx, cancel := context.WithTimeout(e.ctx, 4*time.Second)
			_, _ = e.ScanAndUpdate(ctx)
			cancel()
		}
	}
}

// ScanAndUpdate scans listening ports, merges with pins, flags zombies and updates state.
func (e *Engine) ScanAndUpdate(ctx context.Context) ([]model.Service, error) {
	scanned, err := e.scanner.Scan(ctx)
	if err != nil {
		return nil, err
	}

	all := mergeAndClassify(e.pins, scanned)

	e.mu.Lock()
	e.services = all
	e.mu.Unlock()

	e.proxy.UpdateServices(all)

	if e.bus != nil {
		e.bus.Publish(model.Event{
			Type:     model.EventServicesUpdated,
			Services: all,
		})
	}

	return all, nil
}

// SetServices replaces active services directly (useful for tests and manual updates).
func (e *Engine) SetServices(svcs []model.Service) {
	all := make([]model.Service, len(svcs))
	copy(all, svcs)
	all = discover.MarkZombies(all, 0, time.Now())

	e.mu.Lock()
	e.services = all
	e.mu.Unlock()

	e.proxy.UpdateServices(all)

	if e.bus != nil {
		e.bus.Publish(model.Event{
			Type:     model.EventServicesUpdated,
			Services: all,
		})
	}
}

// Services returns a copy of current active and pinned services.
func (e *Engine) Services() []model.Service {
	e.mu.RLock()
	defer e.mu.RUnlock()
	res := make([]model.Service, len(e.services))
	copy(res, e.services)
	return res
}

// Exchanges returns captured HTTP traffic from the ring buffer.
func (e *Engine) Exchanges(serviceID string, onlyErrors bool) []model.Exchange {
	return e.ring.List(serviceID, onlyErrors)
}

// Replay re-executes an exchange by ID.
func (e *Engine) Replay(ctx context.Context, exchangeID string) (*model.Exchange, error) {
	return e.proxy.Replay(ctx, exchangeID)
}

// KillService terminates a service by ID or name using SIGTERM then SIGKILL.
func (e *Engine) KillService(serviceID string) error {
	e.mu.RLock()
	var target *model.Service
	for _, s := range e.services {
		if s.ID == serviceID || s.Name == serviceID {
			cp := s
			target = &cp
			break
		}
	}
	e.mu.RUnlock()

	if target == nil {
		return fmt.Errorf("service not found: %s", serviceID)
	}
	if target.PID <= 0 {
		return fmt.Errorf("service %s has no active process (PID: %d)", serviceID, target.PID)
	}

	if err := procs.KillService(*target, 3*time.Second); err != nil {
		return err
	}

	if e.bus != nil {
		e.bus.Publish(model.Event{
			Type:      model.EventServiceKilled,
			ServiceID: serviceID,
		})
	}

	// Trigger immediate rescan
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_, _ = e.ScanAndUpdate(ctx)
	}()

	return nil
}

// SweepZombies terminates all processes classified as zombies.
func (e *Engine) SweepZombies() (int, error) {
	e.mu.RLock()
	var zombies []model.Service
	for _, s := range e.services {
		if s.IsZombie() && s.PID > 0 {
			zombies = append(zombies, s)
		}
	}
	e.mu.RUnlock()

	count := 0
	var lastErr error
	for _, z := range zombies {
		if err := procs.KillService(z, 2*time.Second); err == nil {
			count++
		} else {
			lastErr = err
		}
	}

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_, _ = e.ScanAndUpdate(ctx)
	}()

	if count == 0 && lastErr != nil {
		return 0, lastErr
	}
	return count, nil
}

// LaunchService launches a pinned service via macOS .app or shell script.
func (e *Engine) LaunchService(serviceID string) error {
	e.mu.RLock()
	var target *model.Service
	for _, s := range e.services {
		if s.ID == serviceID || s.Name == serviceID {
			cp := s
			target = &cp
			break
		}
	}
	e.mu.RUnlock()

	if target == nil {
		return fmt.Errorf("service not found: %s", serviceID)
	}
	if target.AppPath == "" {
		return fmt.Errorf("service %s has no configured app_path", serviceID)
	}

	var cmd *exec.Cmd
	if runtime.GOOS == "darwin" && strings.HasSuffix(target.AppPath, ".app") {
		cmd = exec.Command("open", target.AppPath)
	} else if runtime.GOOS == "windows" {
		cmd = exec.Command("cmd", "/c", "start", "", target.AppPath)
	} else if runtime.GOOS == "linux" {
		cmd = exec.Command("xdg-open", target.AppPath)
	} else {
		cmd = exec.Command("sh", target.AppPath)
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to launch %s: %w", target.AppPath, err)
	}
	return nil
}

// ServeHTTP implements the unified HTTP entry point.
// Enforces DNS rebinding protection and routes between Web Hub and Reverse Proxy.
func (e *Engine) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	rawHost := r.Host
	cleanHost := rawHost
	if h, _, err := net.SplitHostPort(cleanHost); err == nil {
		cleanHost = h
	}
	cleanHost = strings.ToLower(strings.TrimSpace(cleanHost))

	// Host Header validation (DNS Rebinding protection)
	if cleanHost != "localhost" && cleanHost != "127.0.0.1" && !strings.HasSuffix(cleanHost, ".localhost") {
		http.Error(w, "Misdirected Request: only .localhost domains allowed", http.StatusMisdirectedRequest)
		return
	}

	// Route to Web Hub if host is hub.localhost, apps.localhost, localhost or 127.0.0.1
	if cleanHost == "hub.localhost" || cleanHost == "apps.localhost" || cleanHost == "localhost" || cleanHost == "127.0.0.1" {
		e.webServer.ServeHTTP(w, r)
		return
	}

	// Any other *.localhost -> Reverse Proxy
	e.proxy.ServeHTTP(w, r)
}

func mergeAndClassify(pins []model.Service, scanned []model.Service) []model.Service {
	merged := make([]model.Service, len(pins))
	copy(merged, pins)

	pinPortMap := make(map[int]int)
	for i, p := range merged {
		if p.Port > 0 {
			pinPortMap[p.Port] = i
		}
	}

	for _, s := range scanned {
		if idx, ok := pinPortMap[s.Port]; ok {
			merged[idx].Running = true
			merged[idx].PID = s.PID
			merged[idx].PPID = s.PPID
			merged[idx].PGID = s.PGID
			merged[idx].UID = s.UID
			merged[idx].Command = s.Command
			merged[idx].Cmdline = s.Cmdline
			merged[idx].Cwd = s.Cwd
			merged[idx].CwdExists = s.CwdExists
			merged[idx].Repo = s.Repo
			merged[idx].Worktree = s.Worktree
			merged[idx].Branch = s.Branch
			merged[idx].Agent = s.Agent
			merged[idx].FirstSeen = s.FirstSeen
		} else {
			merged = append(merged, s)
		}
	}

	return discover.MarkZombies(merged, 0, time.Now())
}

func loadPins(path string) []model.Service {
	defaults := config.DefaultPins()
	var loaded []model.Service

	if path != "" {
		if svcs, err := config.LoadPins(path); err == nil && len(svcs) > 0 {
			loaded = svcs
		}
	} else {
		if _, err := os.Stat("pins.toml"); err == nil {
			if svcs, err := config.LoadPins("pins.toml"); err == nil && len(svcs) > 0 {
				loaded = svcs
			}
		}
	}

	if len(loaded) == 0 {
		return defaults
	}

	byID := make(map[string]model.Service)
	for _, d := range defaults {
		byID[d.ID] = d
	}
	for _, p := range loaded {
		byID[p.ID] = p
	}

	var res []model.Service
	for _, d := range defaults {
		if p, ok := byID[d.ID]; ok {
			res = append(res, p)
			delete(byID, d.ID)
		}
	}
	for _, p := range loaded {
		if _, ok := byID[p.ID]; ok {
			res = append(res, p)
			delete(byID, p.ID)
		}
	}
	return res
}

func printVersion() {
	fmt.Printf("lazylocal %s (%s/%s)\n", version, runtime.GOOS, runtime.GOARCH)
}

func main() {
	if len(os.Args) > 1 {
		arg1 := os.Args[1]
		if arg1 == "version" || arg1 == "--version" || arg1 == "-version" || arg1 == "-v" {
			printVersion()
			os.Exit(0)
		}
		switch arg1 {
		case "list", "ls":
			if err := runList(os.Args[2:]); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
			return
		case "url":
			if err := runURL(os.Args[2:]); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
			return
		case "sweep":
			if err := runSweep(os.Args[2:]); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
			return
		case "help", "--help", "-h":
			printUsage()
			os.Exit(0)
		}
	}

	fs := flag.NewFlagSet("lazylocal", flag.ExitOnError)
	portFlag := fs.Int("port", 7080, "Reverse proxy and Web Hub loopback port")
	pinsFlag := fs.String("pins", "", "Path to pins.toml configuration")
	intervalFlag := fs.Duration("interval", 2*time.Second, "Discovery scanner periodic interval")
	headlessFlag := fs.Bool("headless", false, "Run in background headless mode (no TUI)")
	redactMemory := fs.Bool("redact-memory", true, "Scrub sensitive headers and bodies from in-memory replay buffers (default: true)")
	allowRawSecrets := fs.Bool("allow-raw-secrets", false, "Allow storing unredacted secrets in memory (disables redact-memory)")
	noBody := fs.Bool("no-body", false, "Disable capturing HTTP request and response bodies in memory")
	versionFlag := fs.Bool("version", false, "Print version and exit")

	if err := fs.Parse(os.Args[1:]); err != nil {
		os.Exit(1)
	}

	if *versionFlag {
		printVersion()
		os.Exit(0)
	}

	eng, err := NewEngine(*portFlag, *pinsFlag, *intervalFlag)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to initialize lazylocal: %v\n", err)
		os.Exit(1)
	}

	if *noBody {
		eng.Proxy().SetCaptureBodies(false)
	}

	if *allowRawSecrets || !*redactMemory {
		proxySrv := eng.Proxy()
		proxySrv.SetRedactMemory(false)
	}

	if err := eng.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to start lazylocal: %v\n", err)
		os.Exit(1)
	}
	defer eng.Close()

	if *headlessFlag || !isTTY() {
		runHeadless(eng, *intervalFlag)
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := runTUI(ctx, eng); err != nil {
		fmt.Fprintf(os.Stderr, "TUI error: %v\n", err)
	}
}

func runHeadless(eng *Engine, interval time.Duration) {
	fmt.Printf("⚡ lazylocal %s (%s/%s) [Headless Mode]\n", version, runtime.GOOS, runtime.GOARCH)
	fmt.Printf("• Web Hub Dashboard: http://hub.localhost:%d\n", eng.Port())
	fmt.Printf("• Reverse Proxy:     *.localhost:%d -> local dev servers\n", eng.Port())
	fmt.Printf("• Scan Interval:     %s\n", interval)
	fmt.Printf("• Bound Address:     %s (loopback only)\n", eng.Addr())
	fmt.Println("Press Ctrl+C to terminate.")

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh

	fmt.Println("\nShutting down lazylocal...")
	_ = eng.Close()
}

func runList(args []string) error {
	fs := flag.NewFlagSet("list", flag.ContinueOnError)
	jsonFlag := fs.Bool("json", false, "Output in JSON format")
	pinsPath := fs.String("pins", "", "Path to pins.toml configuration")
	if err := fs.Parse(args); err != nil {
		return err
	}

	pins := loadPins(*pinsPath)
	scanner := discover.NewScanner()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	scanned, err := scanner.Scan(ctx)
	if err != nil {
		return fmt.Errorf("scan failed: %w", err)
	}

	all := mergeAndClassify(pins, scanned)

	if *jsonFlag {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(all)
	}

	printServicesTable(all)
	return nil
}

func runURL(args []string) error {
	fs := flag.NewFlagSet("url", flag.ContinueOnError)
	jsonFlag := fs.Bool("json", false, "Output in JSON format")
	cwdFlag := fs.String("cwd", "", "Target working directory")
	portFlag := fs.Int("port", 7080, "Reverse proxy port")
	if err := fs.Parse(args); err != nil {
		return err
	}

	targetDir := *cwdFlag
	if targetDir == "" {
		var err error
		targetDir, err = os.Getwd()
		if err != nil {
			return fmt.Errorf("could not determine current directory: %w", err)
		}
	}
	targetDir = filepath.Clean(targetDir)

	git := discover.ResolveGitInfo(targetDir)
	serviceName := discover.BuildServiceName(git.Repo, git.Branch, targetDir, "")
	host := serviceName + ".localhost"

	portSuffix := ""
	if *portFlag != 80 && *portFlag > 0 {
		portSuffix = fmt.Sprintf(":%d", *portFlag)
	}
	fullURL := fmt.Sprintf("http://%s%s", host, portSuffix)

	if *jsonFlag {
		out := map[string]any{
			"service":  serviceName,
			"host":     host,
			"port":     *portFlag,
			"url":      fullURL,
			"repo":     git.Repo,
			"branch":   git.Branch,
			"worktree": git.Worktree,
			"cwd":      targetDir,
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(out)
	}

	fmt.Println(fullURL)
	return nil
}

func runSweep(args []string) error {
	fs := flag.NewFlagSet("sweep", flag.ContinueOnError)
	dryRun := fs.Bool("dry-run", false, "List zombies without terminating them")
	yes := fs.Bool("yes", false, "Confirm and terminate zombie processes")
	fs.BoolVar(yes, "y", false, "Alias for --yes")
	pinsPath := fs.String("pins", "", "Path to pins.toml configuration")
	if err := fs.Parse(args); err != nil {
		return err
	}

	pins := loadPins(*pinsPath)
	scanner := discover.NewScanner()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	scanned, err := scanner.Scan(ctx)
	if err != nil {
		return fmt.Errorf("scan failed: %w", err)
	}

	all := mergeAndClassify(pins, scanned)

	var zombies []model.Service
	for _, s := range all {
		if s.IsZombie() && s.PID > 0 {
			zombies = append(zombies, s)
		}
	}

	if len(zombies) == 0 {
		fmt.Println("No zombie dev processes detected. Workspace is clean.")
		return nil
	}

	fmt.Printf("Detected %d zombie dev process(es):\n", len(zombies))
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "PID\tPORT\tNAME\tREASON\tCOMMAND\tCWD")
	for _, z := range zombies {
		fmt.Fprintf(w, "%d\t%d\t%s\t%s\t%s\t%s\n",
			z.PID, z.Port, z.Name, z.ZombieReason, z.Command, z.Cwd)
	}
	_ = w.Flush()

	if *dryRun {
		fmt.Println("\n(Dry-run mode: no processes were terminated)")
		return nil
	}

	if !*yes {
		fmt.Println("\nPreview mode (no processes killed). Re-run with 'lazylocal sweep --yes' to terminate these processes.")
		return nil
	}

	fmt.Println("\nTerminating zombie processes...")
	killed := 0
	for _, z := range zombies {
		if err := procs.KillService(z, 3*time.Second); err != nil {
			fmt.Fprintf(os.Stderr, "  ✗ Failed to kill PID %d (%s): %v\n", z.PID, z.Name, err)
		} else {
			fmt.Printf("  ✓ Terminated PID %d (%s) on port %d\n", z.PID, z.Name, z.Port)
			killed++
		}
	}
	fmt.Printf("Successfully swept %d/%d zombies.\n", killed, len(zombies))
	return nil
}

func printServicesTable(svcs []model.Service) {
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
	fmt.Fprintln(w, "NAME\tPORT\tPID\tAGENT\tBRANCH/REPO\tSTATUS\tZOMBIE")

	for _, s := range svcs {
		portStr := "-"
		if s.Port > 0 {
			portStr = strconv.Itoa(s.Port)
		}
		pidStr := "-"
		if s.PID > 0 {
			pidStr = strconv.Itoa(s.PID)
		}
		agentStr := "-"
		if s.Agent != "" {
			agentStr = s.Agent
		}
		branchRepo := "-"
		if s.Repo != "" || s.Branch != "" {
			if s.Branch != "" && s.Repo != "" {
				branchRepo = fmt.Sprintf("%s (%s)", s.Branch, s.Repo)
			} else if s.Branch != "" {
				branchRepo = s.Branch
			} else {
				branchRepo = s.Repo
			}
		}
		status := "OFFLINE"
		if s.Running {
			status = "RUNNING"
		}
		zombie := "-"
		if s.Pinned {
			zombie = "[PIN]"
		} else if s.IsZombie() {
			zombie = fmt.Sprintf("[ZOMBIE: %s]", s.ZombieReason)
		}

		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			s.Name, portStr, pidStr, agentStr, branchRepo, status, zombie)
	}
	_ = w.Flush()
}

func printUsage() {
	fmt.Println(`lazylocal - Zero-config local development reverse proxy with HTTP tap & replay

USAGE:
  lazylocal [command] [flags]

COMMANDS:
  (default)     Start the reverse proxy, Web Hub dashboard, and interactive TUI
  list, ls      List all discovered dev servers and pinned applications (--json)
  url           Print deterministic .localhost URL for current git branch (--json)
  sweep         Detect and terminate zombie dev processes (--yes / -y, --dry-run)
  version       Print version and build information

FLAGS:
  --port <int>         Reverse proxy and Web Hub loopback port (default: 7080)
  --pins <path>        Path to pins.toml configuration (default: pins.toml or builtin)
  --interval <dur>     Discovery scanner periodic interval (default: 2s)
  --headless           Run in background headless mode without TUI
  --no-body            Disable capturing HTTP request and response bodies in memory
  --redact-memory      Scrub sensitive headers and bodies from in-memory replay buffers (default: true)
  --allow-raw-secrets  Allow storing unredacted secrets in memory for faithful replay (disables redact-memory)
  --version, -v        Print version and exit`)
}

func isTTY() bool {
	fi, err := os.Stdout.Stat()
	if err != nil {
		return false
	}
	if (fi.Mode() & os.ModeCharDevice) == 0 {
		return false
	}
	sfi, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return (sfi.Mode() & os.ModeCharDevice) != 0
}

func getTerminalSize() (int, int) {
	if runtime.GOOS != "windows" {
		out, err := exec.Command("stty", "size").Output()
		if err == nil {
			var rows, cols int
			if _, err := fmt.Sscanf(strings.TrimSpace(string(out)), "%d %d", &rows, &cols); err == nil {
				if cols >= 40 && rows >= 8 {
					return cols, rows
				}
			}
		}
	}
	return 120, 32
}

func readKey(r io.Reader) (string, error) {
	var buf [16]byte
	n, err := r.Read(buf[:])
	if err != nil {
		return "", err
	}
	if n == 0 {
		return "", nil
	}

	if buf[0] == '\x1b' {
		if n == 1 {
			return "esc", nil
		}
		if n >= 3 && buf[1] == '[' {
			switch buf[2] {
			case 'A':
				return "up", nil
			case 'B':
				return "down", nil
			case 'C':
				return "right", nil
			case 'D':
				return "left", nil
			case 'Z':
				return "shift-tab", nil
			}
		}
		return "esc", nil
	}

	switch buf[0] {
	case '\x03':
		return "ctrl+c", nil
	case '\r', '\n':
		return "enter", nil
	case '\t':
		return "tab", nil
	case '\x7f', '\b':
		return "backspace", nil
	default:
		return string(buf[:n]), nil
	}
}

func copyToClipboard(text string) {
	if runtime.GOOS == "darwin" {
		cmd := exec.Command("pbcopy")
		cmd.Stdin = strings.NewReader(text)
		_ = cmd.Run()
	} else if runtime.GOOS == "linux" {
		cmd := exec.Command("xclip", "-selection", "clipboard")
		cmd.Stdin = strings.NewReader(text)
		_ = cmd.Run()
	} else if runtime.GOOS == "windows" {
		cmd := exec.Command("clip")
		cmd.Stdin = strings.NewReader(text)
		_ = cmd.Run()
	}
}

func runTUI(ctx context.Context, eng *Engine) error {
	origState, err := exec.Command("stty", "-g").Output()
	hasRaw := err == nil
	if hasRaw {
		_ = exec.Command("stty", "raw", "-echo").Run()
		defer func() {
			_ = exec.Command("stty", strings.TrimSpace(string(origState))).Run()
		}()
	}

	os.Stdout.WriteString("\x1b[?1049h\x1b[?25l")
	defer func() {
		os.Stdout.WriteString("\x1b[?25h\x1b[?1049l\x1b[0m")
	}()

	w, h := getTerminalSize()
	ui := tui.NewModel(w, h)
	ui.SetServices(eng.Services())
	ui.SetExchanges(eng.Exchanges("", false))

	os.Stdout.WriteString("\x1b[H" + ui.Render())

	eventCh, unsub := eng.Bus().Subscribe(128)
	defer unsub()

	keyCh := make(chan string, 32)
	go func() {
		for {
			key, err := readKey(os.Stdin)
			if err != nil {
				return
			}
			if key != "" {
				keyCh <- key
			}
		}
	}()

	sigCh := make(chan os.Signal, 2)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil

		case <-sigCh:
			return nil

		case key := <-keyCh:
			act := ui.HandleKey(key)
			switch act.Type {
			case tui.ActionQuit:
				return nil
			case tui.ActionReplay:
				go func(id string) {
					_, _ = eng.Replay(context.Background(), id)
				}(act.ExchangeID)
			case tui.ActionKillService:
				go func(id string) {
					_ = eng.KillService(id)
				}(act.ServiceID)
			case tui.ActionSweepZombies:
				go func() {
					_, _ = eng.SweepZombies()
				}()
			case tui.ActionRefresh:
				go func() {
					c, cancel := context.WithTimeout(context.Background(), 3*time.Second)
					_, _ = eng.ScanAndUpdate(c)
					cancel()
				}()
			case tui.ActionCopyCurl:
				if ex, ok := eng.Ring().Get(act.ExchangeID); ok {
					curlCmd := proxy.CurlCommand(ex)
					copyToClipboard(curlCmd)
				}
			}
			os.Stdout.WriteString("\x1b[H" + ui.Render())

		case ev := <-eventCh:
			switch ev.Type {
			case model.EventServicesUpdated:
				ui.SetServices(eng.Services())
			case model.EventExchangeRecorded:
				ui.SetExchanges(eng.Exchanges("", false))
			case model.EventServiceKilled:
				ui.SetServices(eng.Services())
			}
			os.Stdout.WriteString("\x1b[H" + ui.Render())

		case <-ticker.C:
			curW, curH := getTerminalSize()
			if curW != ui.Width || curH != ui.Height {
				ui.Width = curW
				ui.Height = curH
				os.Stdout.WriteString("\x1b[H" + ui.Render())
			}
		}
	}
}
