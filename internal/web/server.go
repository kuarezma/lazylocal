package web

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/kuarezma/lazylocal/internal/capture"
	"github.com/kuarezma/lazylocal/internal/model"
)

//go:embed static/index.html
var indexHTML []byte

// Controller defines the orchestration methods exposed to the Web Hub.
type Controller interface {
	Services() []model.Service
	Exchanges(serviceID string, onlyErrors bool) []model.Exchange
	Replay(ctx context.Context, exchangeID string) (*model.Exchange, error)
	KillService(serviceID string) error
	SweepZombies() (int, error)
	LaunchService(serviceID string) error
}

// Server provides the embedded lazylocal Hub dashboard and JSON/SSE APIs.
type Server struct {
	ctrl  Controller
	bus   *model.Bus
	ring  *capture.RingBuffer
	token string
	mux   *http.ServeMux
}

// IsNonIdempotentMethod returns true if the HTTP method is non-idempotent (POST, PUT, PATCH, DELETE).
func IsNonIdempotentMethod(method string) bool {
	return model.IsNonIdempotentMethod(method)
}

// SetRing sets the capture ring buffer used for exchange lookups and confirmation checks.
func (s *Server) SetRing(ring *capture.RingBuffer) {
	s.ring = ring
}

// NewServer creates a new Web Hub Server with an in-memory CSRF token.
func NewServer(ctrl Controller, bus *model.Bus) *Server {
	if bus == nil {
		bus = model.NewBus()
	}
	s := &Server{
		ctrl:  ctrl,
		bus:   bus,
		token: generateCSRFToken(),
		mux:   http.NewServeMux(),
	}
	s.registerRoutes()
	return s
}

// Token returns the active CSRF protection token.
func (s *Server) Token() string {
	return s.token
}

// Handler returns the underlying http.Handler.
func (s *Server) Handler() http.Handler {
	return s
}

// isAllowedHost verifies that the Host header matches allowed local endpoints.
func isAllowedHost(host string) bool {
	h := host
	if sh, _, err := net.SplitHostPort(h); err == nil {
		h = sh
	}
	h = strings.ToLower(strings.TrimSpace(h))
	h = strings.TrimPrefix(h, "[")
	h = strings.TrimSuffix(h, "]")
	switch h {
	case "hub.localhost", "apps.localhost", "localhost", "127.0.0.1", "::1":
		return true
	default:
		return false
	}
}

// ServeHTTP delegates request handling to the internal multiplexer.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !isAllowedHost(r.Host) {
		http.Error(w, "Misdirected Request: invalid Host header", http.StatusMisdirectedRequest)
		return
	}
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Content-Security-Policy", "frame-ancestors 'none'")
	if r.Method == http.MethodPost && r.Body != nil {
		r.Body = http.MaxBytesReader(w, r.Body, 64*1024)
	}
	s.mux.ServeHTTP(w, r)
}

func generateCSRFToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return hex.EncodeToString([]byte(fmt.Sprintf("%d", time.Now().UnixNano())))
	}
	return hex.EncodeToString(b)
}

// isAllowedOrigin checks if the origin belongs to allowed local loopback origins.
func isAllowedOrigin(origin string) bool {
	if origin == "" {
		return true
	}
	target := origin
	if !strings.Contains(target, "://") {
		target = "http://" + target
	}
	u, err := url.Parse(target)
	if err != nil {
		return false
	}
	hostname := strings.ToLower(u.Hostname())
	if hostname == "" {
		return false
	}
	hostname = strings.TrimPrefix(hostname, "[")
	hostname = strings.TrimSuffix(hostname, "]")
	switch hostname {
	case "hub.localhost", "apps.localhost", "localhost", "127.0.0.1", "::1":
		return true
	default:
		return false
	}
}

// verifySecurity enforces G4 gate: checks Origin allowlist and CSRF token constant-time equality.
func (s *Server) verifySecurity(w http.ResponseWriter, r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin != "" {
		if !isAllowedOrigin(origin) {
			http.Error(w, "Forbidden: disallowed origin", http.StatusForbidden)
			return false
		}
		// Explicit origin only, never wildcard '*'
		w.Header().Set("Access-Control-Allow-Origin", origin)
		w.Header().Set("Vary", "Origin")
	}

	gotToken := r.Header.Get("X-Lazylocal-Token")
	if subtle.ConstantTimeCompare([]byte(gotToken), []byte(s.token)) != 1 {
		http.Error(w, "Forbidden: invalid CSRF token", http.StatusForbidden)
		return false
	}

	return true
}

func (s *Server) handleCORSPreflight(w http.ResponseWriter, r *http.Request) bool {
	if r.Method != http.MethodOptions {
		return false
	}
	origin := r.Header.Get("Origin")
	if origin != "" && isAllowedOrigin(origin) {
		w.Header().Set("Access-Control-Allow-Origin", origin)
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-Lazylocal-Token")
		w.Header().Set("Vary", "Origin")
		w.WriteHeader(http.StatusNoContent)
		return true
	}
	http.Error(w, "Forbidden: disallowed origin", http.StatusForbidden)
	return true
}

func (s *Server) registerRoutes() {
	s.mux.HandleFunc("/", s.handleIndex)
	s.mux.HandleFunc("/api/services", s.handleServices)
	s.mux.HandleFunc("/api/exchanges", s.handleExchanges)
	s.mux.HandleFunc("/api/events", s.handleEvents)
	s.mux.HandleFunc("/api/replay", s.handleReplay)
	s.mux.HandleFunc("/api/kill", s.handleKill)
	s.mux.HandleFunc("/api/sweep-zombies", s.handleSweepZombies)
	s.mux.HandleFunc("/api/launch", s.handleLaunch)
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" && r.URL.Path != "/index.html" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	html := strings.ReplaceAll(string(indexHTML), "{{CSRF_TOKEN}}", s.token)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	if r.Method == http.MethodGet {
		_, _ = w.Write([]byte(html))
	}
}

func (s *Server) handleServices(w http.ResponseWriter, r *http.Request) {
	if s.handleCORSPreflight(w, r) {
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	origin := r.Header.Get("Origin")
	if origin != "" {
		if !isAllowedOrigin(origin) {
			http.Error(w, "Forbidden: disallowed origin", http.StatusForbidden)
			return
		}
		w.Header().Set("Access-Control-Allow-Origin", origin)
		w.Header().Set("Vary", "Origin")
	}

	services := s.ctrl.Services()
	if services == nil {
		services = []model.Service{}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(services)
}

func (s *Server) handleExchanges(w http.ResponseWriter, r *http.Request) {
	if s.handleCORSPreflight(w, r) {
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	origin := r.Header.Get("Origin")
	if origin != "" {
		if !isAllowedOrigin(origin) {
			http.Error(w, "Forbidden: disallowed origin", http.StatusForbidden)
			return
		}
		w.Header().Set("Access-Control-Allow-Origin", origin)
		w.Header().Set("Vary", "Origin")
	}

	serviceID := r.URL.Query().Get("service_id")
	errParam := r.URL.Query().Get("errors")
	onlyErrors := errParam == "1" || strings.EqualFold(errParam, "true")

	exchanges := s.ctrl.Exchanges(serviceID, onlyErrors)
	if exchanges == nil {
		exchanges = []model.Exchange{}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(exchanges)
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	origin := r.Header.Get("Origin")
	if origin != "" {
		if !isAllowedOrigin(origin) {
			http.Error(w, "Forbidden: disallowed origin", http.StatusForbidden)
			return
		}
		w.Header().Set("Access-Control-Allow-Origin", origin)
		w.Header().Set("Vary", "Origin")
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming unsupported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	// Emit initial state snapshot immediately
	services := s.ctrl.Services()
	if services == nil {
		services = []model.Service{}
	}
	initPayload := map[string]any{
		"type":      "init",
		"timestamp": time.Now().UTC(),
		"services":  services,
	}
	initBytes, _ := json.Marshal(initPayload)
	fmt.Fprintf(w, "event: init\ndata: %s\n\n", initBytes)
	flusher.Flush()

	ch, unsub := s.bus.Subscribe(64)
	defer unsub()

	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-ch:
			if !ok {
				return
			}
			evBytes, err := json.Marshal(ev)
			if err != nil {
				continue
			}
			eventType := string(ev.Type)
			if eventType == "" {
				eventType = "message"
			}
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", eventType, evBytes)
			flusher.Flush()
		}
	}
}

func (s *Server) handleReplay(w http.ResponseWriter, r *http.Request) {
	if s.handleCORSPreflight(w, r) {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.verifySecurity(w, r) {
		return
	}
	if r.Body != nil {
		r.Body = http.MaxBytesReader(w, r.Body, 64*1024)
	}

	exchangeID := r.URL.Query().Get("id")
	if exchangeID == "" && r.Body != nil {
		r.Body = http.MaxBytesReader(w, r.Body, 64*1024)
		var body struct {
			ID string `json:"id"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		exchangeID = body.ID
	}
	if exchangeID == "" {
		http.Error(w, "Missing exchange id", http.StatusBadRequest)
		return
	}

	// Look up exchange in ring buffer or controller
	var origEx model.Exchange
	var found bool
	if s.ring != nil {
		origEx, found = s.ring.Get(exchangeID)
	}
	if !found && s.ctrl != nil {
		for _, e := range s.ctrl.Exchanges("", false) {
			if e.ID == exchangeID {
				origEx = e
				found = true
				break
			}
		}
	}

	if found {
		method := origEx.Method
		if method == "" {
			method = origEx.Req.Method
		}
		if IsNonIdempotentMethod(method) {
			confirmed := r.URL.Query().Get("confirm") == "true" || r.Header.Get("X-Confirm-Replay") == "true"
			if !confirmed {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusConflict)
				_ = json.NewEncoder(w).Encode(map[string]any{
					"error":                  "non-idempotent replay requires confirmation",
					"requires_confirmation": true,
					"method":                 method,
				})
				return
			}
		}
	}

	ex, err := s.ctrl.Replay(r.Context(), exchangeID)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(ex)
}

func (s *Server) handleKill(w http.ResponseWriter, r *http.Request) {
	if s.handleCORSPreflight(w, r) {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.verifySecurity(w, r) {
		return
	}
	if r.Body != nil {
		r.Body = http.MaxBytesReader(w, r.Body, 64*1024)
	}

	serviceID := r.URL.Query().Get("id")
	if serviceID == "" && r.Body != nil {
		r.Body = http.MaxBytesReader(w, r.Body, 64*1024)
		var body struct {
			ID string `json:"id"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		serviceID = body.ID
	}
	if serviceID == "" {
		http.Error(w, "Missing service id", http.StatusBadRequest)
		return
	}

	err := s.ctrl.KillService(serviceID)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "service_id": serviceID})
}

func (s *Server) handleSweepZombies(w http.ResponseWriter, r *http.Request) {
	if s.handleCORSPreflight(w, r) {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.verifySecurity(w, r) {
		return
	}
	if r.Body != nil {
		r.Body = http.MaxBytesReader(w, r.Body, 64*1024)
	}

	count, err := s.ctrl.SweepZombies()
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "swept_count": count})
}

func (s *Server) handleLaunch(w http.ResponseWriter, r *http.Request) {
	if s.handleCORSPreflight(w, r) {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.verifySecurity(w, r) {
		return
	}
	if r.Body != nil {
		r.Body = http.MaxBytesReader(w, r.Body, 64*1024)
	}

	serviceID := r.URL.Query().Get("id")
	if serviceID == "" && r.Body != nil {
		r.Body = http.MaxBytesReader(w, r.Body, 64*1024)
		var body struct {
			ID string `json:"id"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		serviceID = body.ID
	}
	if serviceID == "" {
		http.Error(w, "Missing service id", http.StatusBadRequest)
		return
	}

	err := s.ctrl.LaunchService(serviceID)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "service_id": serviceID})
}
