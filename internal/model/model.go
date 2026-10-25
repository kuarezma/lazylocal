package model

import (
	"net/http"
	"strings"
	"sync"
	"time"
)

// ZombieReason describes why a listening process is classified as a zombie/orphan.
type ZombieReason string

const (
	ZombieNone          ZombieReason = ""
	ZombieDeletedCwd    ZombieReason = "deleted_cwd"
	ZombieOrphanPPID    ZombieReason = "orphan_ppid"
	ZombieDuplicateWork ZombieReason = "duplicate_worktree"
	ZombieIdleTraffic   ZombieReason = "idle_traffic"
)

// Service represents a discovered or pinned local development server.
type Service struct {
	ID           string       `json:"id"`
	Name         string       `json:"name"`          // Subdomain slug, e.g., "feat-auth.shop" or "matematik"
	Host         string       `json:"host"`          // Full hostname, e.g., "feat-auth.shop.localhost"
	Port         int          `json:"port"`          // Listening TCP port on loopback
	PID          int          `json:"pid"`           // Process ID (0 if pinned & offline)
	PGID         int          `json:"pgid"`          // Process Group ID
	PPID         int          `json:"ppid"`          // Parent Process ID
	UID          int          `json:"uid"`           // Owner User ID
	Command      string       `json:"command"`       // Executable / process name
	Cmd          string       `json:"cmd,omitempty"` // Alias for Command
	Cmdline      string       `json:"cmdline"`       // Full command line if available
	Cwd          string       `json:"cwd"`           // Working directory
	CwdExists    bool         `json:"cwd_exists"`    // False if directory was removed (e.g. deleted git worktree)
	Repo         string       `json:"repo"`          // Git repository root name
	Worktree     string       `json:"worktree"`      // Git worktree directory name
	Branch       string       `json:"branch"`        // Git branch name
	Agent        string       `json:"agent"`         // Detected AI coding agent ("claude", "codex", "gemini", "cursor", etc.)
	Pinned       bool         `json:"pinned"`        // Configured in pins.toml
	Running      bool         `json:"running"`       // Currently listening / healthy
	Category     string       `json:"category"`      // Optional category from pins.toml
	Badge        string       `json:"badge"`         // Optional badge from pins.toml
	Description  string       `json:"description"`   // Optional description from pins.toml
	Color        string       `json:"color"`         // Optional hex color
	AppPath      string       `json:"app_path"`      // Optional macOS .app or launch script path
	HealthURL    string       `json:"health_url"`    // Optional HTTP health check endpoint
	ZombieReason ZombieReason `json:"zombie_reason"` // Non-empty if flagged as zombie
	CPUPercent   float64      `json:"cpu_percent"`
	MemoryMB     float64      `json:"memory_mb"`
	LastTraffic  time.Time    `json:"last_traffic"`
	FirstSeen    time.Time    `json:"first_seen"`
	RequestCount int64        `json:"request_count"`
	ErrorCount   int64        `json:"error_count"` // 4xx + 5xx count
}

// IsZombie returns true if the service has a non-empty ZombieReason.
func (s Service) IsZombie() bool {
	return s.ZombieReason != ZombieNone
}

// HTTPRequestData holds captured HTTP request metadata and truncated body.
type HTTPRequestData struct {
	Method      string              `json:"method"`
	Path        string              `json:"path"`
	Query       string              `json:"query"`
	Host        string              `json:"host"`
	Headers     map[string][]string `json:"headers"` // Sensitive headers masked in display copy
	RawHeaders  http.Header         `json:"-"`       // Unmasked headers kept in memory for faithful Replay
	Body        []byte              `json:"body"`
	BodySize    int64               `json:"body_size"`
	Truncated   bool                `json:"truncated"`
	ContentType string              `json:"content_type"`
}

// HTTPResponseData holds captured HTTP response metadata and truncated body.
type HTTPResponseData struct {
	StatusCode  int                 `json:"status_code"`
	Status      string              `json:"status"`
	Headers     map[string][]string `json:"headers"`
	Body        []byte              `json:"body"`
	BodySize    int64               `json:"body_size"`
	Truncated   bool                `json:"truncated"`
	ContentType string              `json:"content_type"`
}

// Exchange represents a single captured HTTP request/response pair through the reverse proxy.
type Exchange struct {
	ID         string           `json:"id"`
	ServiceID  string           `json:"service_id"`
	Service    string           `json:"service"` // Subdomain name
	TargetPort int              `json:"target_port"`
	StartedAt  time.Time        `json:"started_at"`
	Duration   time.Duration    `json:"duration"`
	DurationMs float64          `json:"duration_ms"`
	Req        HTTPRequestData  `json:"req"`
	Resp       HTTPResponseData `json:"resp"`
	ReqBody    []byte           `json:"req_body,omitempty"`
	RespBody   []byte           `json:"resp_body,omitempty"`
	Method     string           `json:"method,omitempty"`
	Path       string           `json:"path,omitempty"`
	Truncated  bool             `json:"truncated"`
	ReplayedOf string           `json:"replayed_of,omitempty"` // Original Exchange ID if this was triggered via Replay
	Error      string           `json:"error,omitempty"`
	RawReqHeaders http.Header   `json:"-"`                     // Raw request headers kept in memory for replay
}

// IsNonIdempotentMethod returns true if the HTTP method is non-idempotent (POST, PUT, PATCH, DELETE).
func IsNonIdempotentMethod(method string) bool {
	switch strings.ToUpper(strings.TrimSpace(method)) {
	case "POST", "PUT", "PATCH", "DELETE":
		return true
	default:
		return false
	}
}

// EventType enumerates real-time bus events for TUI and Web SSE consumers.
type EventType string

const (
	EventServicesUpdated  EventType = "services_updated"
	EventExchangeRecorded EventType = "exchange_recorded"
	EventServiceKilled    EventType = "service_killed"
)

// Event is published on the Bus whenever services or captured exchanges change.
type Event struct {
	Type      EventType `json:"type"`
	Timestamp time.Time `json:"timestamp"`
	Services  []Service `json:"services,omitempty"`
	Exchange  *Exchange `json:"exchange,omitempty"`
	ServiceID string    `json:"service_id,omitempty"`
}

// Bus is a thread-safe in-memory pub/sub event bus for decoupling Discovery, Proxy, TUI, and Web SSE.
type Bus struct {
	mu   sync.RWMutex
	subs map[chan Event]struct{}
}

// NewBus creates a new pub/sub event bus.
func NewBus() *Bus {
	return &Bus{
		subs: make(map[chan Event]struct{}),
	}
}

// Subscribe returns a buffered channel receiving events and an unsubscribe function.
func (b *Bus) Subscribe(buffer int) (<-chan Event, func()) {
	if buffer <= 0 {
		buffer = 64
	}
	ch := make(chan Event, buffer)
	b.mu.Lock()
	b.subs[ch] = struct{}{}
	b.mu.Unlock()

	var once sync.Once
	unsub := func() {
		once.Do(func() {
			b.mu.Lock()
			delete(b.subs, ch)
			close(ch)
			b.mu.Unlock()
		})
	}
	return ch, unsub
}

// Publish sends an event to all subscribers without blocking slow consumers.
func (b *Bus) Publish(ev Event) {
	if ev.Timestamp.IsZero() {
		ev.Timestamp = time.Now()
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	for ch := range b.subs {
		select {
		case ch <- ev:
		default:
			// Drop if subscriber buffer is full to keep proxy/discovery non-blocking.
		}
	}
}
