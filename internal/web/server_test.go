package web

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kuarezma/lazylocal/internal/model"
)

type mockController struct {
	mu           sync.Mutex
	services     []model.Service
	exchanges    []model.Exchange
	killedID     string
	launchedID   string
	sweptCount   int
	replayCalled string
}

func (m *mockController) Services() []model.Service {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.services
}

func (m *mockController) Exchanges(serviceID string, onlyErrors bool) []model.Exchange {
	m.mu.Lock()
	defer m.mu.Unlock()
	var res []model.Exchange
	for _, e := range m.exchanges {
		if serviceID != "" && e.ServiceID != serviceID && e.Service != serviceID {
			continue
		}
		if onlyErrors && e.Resp.StatusCode < 400 {
			continue
		}
		res = append(res, e)
	}
	return res
}

func (m *mockController) Replay(ctx context.Context, exchangeID string) (*model.Exchange, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.replayCalled = exchangeID
	for _, e := range m.exchanges {
		if e.ID == exchangeID {
			replayed := e
			replayed.ID = e.ID + "-replayed"
			replayed.ReplayedOf = e.ID
			return &replayed, nil
		}
	}
	return nil, fmt.Errorf("exchange not found: %s", exchangeID)
}

func (m *mockController) KillService(serviceID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if serviceID == "err-kill" {
		return fmt.Errorf("process termination failed")
	}
	m.killedID = serviceID
	return nil
}

func (m *mockController) SweepZombies() (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.sweptCount, nil
}

func (m *mockController) LaunchService(serviceID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if serviceID == "err-launch" {
		return fmt.Errorf("launch script missing")
	}
	m.launchedID = serviceID
	return nil
}

func assertNoWildcardCORS(t *testing.T, resp *http.Response) {
	t.Helper()
	if origin := resp.Header.Get("Access-Control-Allow-Origin"); origin == "*" {
		t.Fatalf("Security Violation: Access-Control-Allow-Origin must NEVER be wildcard '*'!")
	}
}

// 1. GET / 200 OK ve token enjeksiyonu testi
func TestIndexTokenInjection(t *testing.T) {
	ctrl := &mockController{}
	bus := model.NewBus()
	srv := NewServer(ctrl, bus)

	ts := httptest.NewServer(srv)
	defer ts.Close()

	req, err := http.NewRequest(http.MethodGet, ts.URL+"/", nil)
	if err != nil {
		t.Fatalf("failed to create request: %v", err)
	}
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	assertNoWildcardCORS(t, resp)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200 OK, got %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("failed to read body: %v", err)
	}
	content := string(body)

	expectedTokenSnippet := fmt.Sprintf(`window.__LAZYLOCAL_TOKEN__ = "%s"`, srv.Token())
	if !strings.Contains(content, expectedTokenSnippet) {
		t.Fatalf("expected injected token snippet %q, not found in HTML", expectedTokenSnippet)
	}
	if !strings.Contains(content, "hub.localhost") {
		t.Fatalf("expected HTML to contain 'hub.localhost'")
	}
}

// 2. GET /api/services ve GET /api/events (SSE üzerinden yeni eklenen servisin <100ms içinde akıştan okunduğu) testi
func TestServicesAndSSEEventStream(t *testing.T) {
	initialService := model.Service{
		ID:      "srv-shop-1",
		Name:    "shop",
		Host:    "shop.localhost",
		Port:    3000,
		PID:     1234,
		Running: true,
	}
	ctrl := &mockController{
		services: []model.Service{initialService},
	}
	bus := model.NewBus()
	srv := NewServer(ctrl, bus)

	ts := httptest.NewServer(srv)
	defer ts.Close()

	// Step A: Check GET /api/services
	{
		resp, err := ts.Client().Get(ts.URL + "/api/services")
		if err != nil {
			t.Fatalf("failed to fetch services: %v", err)
		}
		defer resp.Body.Close()
		assertNoWildcardCORS(t, resp)

		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200 OK, got %d", resp.StatusCode)
		}

		var list []model.Service
		if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
			t.Fatalf("failed to decode services json: %v", err)
		}
		if len(list) != 1 || list[0].ID != "srv-shop-1" {
			t.Fatalf("expected initial service in list, got: %+v", list)
		}
	}

	// Step B: Connect to SSE and verify <100ms real-time event delivery
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	sseReq, err := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"/api/events", nil)
	if err != nil {
		t.Fatalf("failed to create sse request: %v", err)
	}
	sseResp, err := ts.Client().Do(sseReq)
	if err != nil {
		t.Fatalf("failed to connect to sse: %v", err)
	}
	defer sseResp.Body.Close()
	assertNoWildcardCORS(t, sseResp)

	if sseResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK from SSE, got %d", sseResp.StatusCode)
	}
	if !strings.Contains(sseResp.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("expected text/event-stream, got %s", sseResp.Header.Get("Content-Type"))
	}

	scanner := bufio.NewScanner(sseResp.Body)

	// Consume initial 'init' event
	initReceived := false
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "event: init") {
			initReceived = true
			break
		}
	}
	if !initReceived {
		t.Fatalf("initial 'init' event not received from SSE stream")
	}

	// Publish new service update to bus and measure latency
	newService := model.Service{
		ID:      "srv-auth-2",
		Name:    "feat-auth.shop",
		Host:    "feat-auth.shop.localhost",
		Port:    3001,
		PID:     5678,
		Running: true,
		Branch:  "feat-auth",
		Agent:   "claude",
	}

	start := time.Now()
	bus.Publish(model.Event{
		Type:      model.EventServicesUpdated,
		Timestamp: time.Now().UTC(),
		Services:  []model.Service{initialService, newService},
	})

	receivedCh := make(chan time.Duration, 1)
	errCh := make(chan error, 1)

	go func() {
		for scanner.Scan() {
			line := scanner.Text()
			if strings.Contains(line, "srv-auth-2") {
				receivedCh <- time.Since(start)
				return
			}
		}
		if err := scanner.Err(); err != nil {
			errCh <- err
		}
	}()

	select {
	case elapsed := <-receivedCh:
		if elapsed > 100*time.Millisecond {
			t.Fatalf("SSE latency exceeded: got %v (limit is < 100ms)", elapsed)
		}
		t.Logf("SSE real-time event received in %v (well under 100ms)", elapsed)
	case err := <-errCh:
		t.Fatalf("SSE scanner error: %v", err)
	case <-time.After(500 * time.Millisecond):
		t.Fatalf("timed out waiting for SSE update event (> 500ms)")
	}
}

// 3. Token'sız POST /api/kill -> 403 Forbidden testi
func TestKillMissingTokenForbidden(t *testing.T) {
	ctrl := &mockController{}
	srv := NewServer(ctrl, nil)

	ts := httptest.NewServer(srv)
	defer ts.Close()

	req, err := http.NewRequest(http.MethodPost, ts.URL+"/api/kill?id=srv-1", nil)
	if err != nil {
		t.Fatalf("failed to create request: %v", err)
	}
	// No X-Lazylocal-Token header provided
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	assertNoWildcardCORS(t, resp)

	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403 Forbidden without token, got %d", resp.StatusCode)
	}
	if ctrl.killedID != "" {
		t.Fatalf("controller method should not have been called on forbidden request")
	}
}

// 4. Yabancı Origin (Origin: https://evil.example.com) ile POST /api/kill (geçerli token olsa bile!) -> 403 Forbidden testi
func TestKillDisallowedOriginForbidden(t *testing.T) {
	ctrl := &mockController{}
	srv := NewServer(ctrl, nil)

	ts := httptest.NewServer(srv)
	defer ts.Close()

	disallowedOrigins := []string{
		"https://evil.example.com",
		"http://evil.com",
		"http://attacker.localhost.com",
		"null",
	}

	for _, origin := range disallowedOrigins {
		t.Run(origin, func(t *testing.T) {
			req, err := http.NewRequest(http.MethodPost, ts.URL+"/api/kill?id=srv-1", nil)
			if err != nil {
				t.Fatalf("failed to create request: %v", err)
			}
			req.Header.Set("X-Lazylocal-Token", srv.Token()) // Valid token!
			req.Header.Set("Origin", origin)                 // Malicious origin!

			resp, err := ts.Client().Do(req)
			if err != nil {
				t.Fatalf("request failed: %v", err)
			}
			defer resp.Body.Close()

			assertNoWildcardCORS(t, resp)

			if resp.StatusCode != http.StatusForbidden {
				t.Fatalf("expected 403 Forbidden for origin %q even with valid token, got %d", origin, resp.StatusCode)
			}
			if ctrl.killedID != "" {
				t.Fatalf("controller method should not have been called on disallowed origin")
			}
		})
	}
}

// 5. Geçerli token + Origin: http://hub.localhost:7080 ile POST /api/kill ve POST /api/replay -> 200 OK testi
func TestValidTokenAndLocalhostOriginSuccess(t *testing.T) {
	exchangeID := "exc-tap-42"
	ctrl := &mockController{
		exchanges: []model.Exchange{
			{
				ID:        exchangeID,
				ServiceID: "srv-shop-1",
				Service:   "shop",
				Req: model.HTTPRequestData{
					Method: "GET",
					Path:   "/api/v1/health",
					Host:   "shop.localhost",
				},
				Resp: model.HTTPResponseData{
					StatusCode: 200,
					Status:     "200 OK",
				},
			},
		},
	}
	srv := NewServer(ctrl, nil)

	ts := httptest.NewServer(srv)
	defer ts.Close()

	allowedOrigin := "http://hub.localhost:7080"

	// Test Kill with allowed Origin and valid token
	{
		req, err := http.NewRequest(http.MethodPost, ts.URL+"/api/kill?id=srv-target-1", nil)
		if err != nil {
			t.Fatalf("failed to create request: %v", err)
		}
		req.Header.Set("X-Lazylocal-Token", srv.Token())
		req.Header.Set("Origin", allowedOrigin)

		resp, err := ts.Client().Do(req)
		if err != nil {
			t.Fatalf("kill request failed: %v", err)
		}
		defer resp.Body.Close()

		assertNoWildcardCORS(t, resp)

		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200 OK for valid kill, got %d", resp.StatusCode)
		}
		if ctrl.killedID != "srv-target-1" {
			t.Fatalf("expected controller to receive srv-target-1, got %q", ctrl.killedID)
		}
		if resp.Header.Get("Access-Control-Allow-Origin") != allowedOrigin {
			t.Fatalf("expected Access-Control-Allow-Origin %q, got %q", allowedOrigin, resp.Header.Get("Access-Control-Allow-Origin"))
		}
	}

	// Test Replay with allowed Origin and valid token
	{
		req, err := http.NewRequest(http.MethodPost, ts.URL+"/api/replay?id="+exchangeID, nil)
		if err != nil {
			t.Fatalf("failed to create request: %v", err)
		}
		req.Header.Set("X-Lazylocal-Token", srv.Token())
		req.Header.Set("Origin", allowedOrigin)

		resp, err := ts.Client().Do(req)
		if err != nil {
			t.Fatalf("replay request failed: %v", err)
		}
		defer resp.Body.Close()

		assertNoWildcardCORS(t, resp)

		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200 OK for valid replay, got %d", resp.StatusCode)
		}
		if ctrl.replayCalled != exchangeID {
			t.Fatalf("expected controller replay for %q, got %q", exchangeID, ctrl.replayCalled)
		}

		var replayed model.Exchange
		if err := json.NewDecoder(resp.Body).Decode(&replayed); err != nil {
			t.Fatalf("failed to decode replay JSON: %v", err)
		}
		if replayed.ReplayedOf != exchangeID {
			t.Fatalf("expected replayed exchange to point to %q, got %q", exchangeID, replayed.ReplayedOf)
		}
	}
}

// 6. SweepZombies & Launch endpoints and exchanges endpoint coverage
func TestSweepZombiesLaunchAndExchanges(t *testing.T) {
	ctrl := &mockController{
		sweptCount: 3,
		exchanges: []model.Exchange{
			{
				ID:        "e1",
				ServiceID: "s1",
				Resp:      model.HTTPResponseData{StatusCode: 200},
			},
			{
				ID:        "e2",
				ServiceID: "s1",
				Resp:      model.HTTPResponseData{StatusCode: 500},
			},
		},
	}
	srv := NewServer(ctrl, nil)

	ts := httptest.NewServer(srv)
	defer ts.Close()

	// Sweep Zombies
	{
		req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/sweep-zombies", nil)
		req.Header.Set("X-Lazylocal-Token", srv.Token())
		req.Header.Set("Origin", "http://127.0.0.1:7080")

		resp, err := ts.Client().Do(req)
		if err != nil {
			t.Fatalf("sweep request failed: %v", err)
		}
		defer resp.Body.Close()
		assertNoWildcardCORS(t, resp)

		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200 OK, got %d", resp.StatusCode)
		}
		var result map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&result)
		if int(result["swept_count"].(float64)) != 3 {
			t.Fatalf("expected 3 zombies swept, got %v", result["swept_count"])
		}
	}

	// Launch Service
	{
		req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/launch?id=pinned-service-9", nil)
		req.Header.Set("X-Lazylocal-Token", srv.Token())
		req.Header.Set("Origin", "http://hub.localhost:7080")

		resp, err := ts.Client().Do(req)
		if err != nil {
			t.Fatalf("launch request failed: %v", err)
		}
		defer resp.Body.Close()
		assertNoWildcardCORS(t, resp)

		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200 OK, got %d", resp.StatusCode)
		}
		if ctrl.launchedID != "pinned-service-9" {
			t.Fatalf("expected launchedID pinned-service-9, got %q", ctrl.launchedID)
		}
	}

	// GET /api/exchanges with errors=1 filter
	{
		resp, err := ts.Client().Get(ts.URL + "/api/exchanges?service_id=s1&errors=1")
		if err != nil {
			t.Fatalf("exchanges fetch failed: %v", err)
		}
		defer resp.Body.Close()
		assertNoWildcardCORS(t, resp)

		var errExchanges []model.Exchange
		_ = json.NewDecoder(resp.Body).Decode(&errExchanges)
		if len(errExchanges) != 1 || errExchanges[0].ID != "e2" {
			t.Fatalf("expected only error exchange e2, got: %+v", errExchanges)
		}
	}
}

// 7. Security verification of invalid/mismatched tokens
func TestInvalidTokenForbidden(t *testing.T) {
	ctrl := &mockController{}
	srv := NewServer(ctrl, nil)

	ts := httptest.NewServer(srv)
	defer ts.Close()

	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/kill?id=srv-1", nil)
	req.Header.Set("X-Lazylocal-Token", "invalid-token-1234567890abcdef")
	req.Header.Set("Origin", "http://localhost:7080")

	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	assertNoWildcardCORS(t, resp)

	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403 Forbidden with invalid token, got %d", resp.StatusCode)
	}
}

// 8. H1 DNS Rebinding: Host allowlist returns 421 Misdirected Request for disallowed hosts
func TestHostAllowlistMisdirectedRequest(t *testing.T) {
	ctrl := &mockController{}
	srv := NewServer(ctrl, nil)
	ts := httptest.NewServer(srv)
	defer ts.Close()

	disallowedHosts := []string{
		"evil.com",
		"foo.localhost",
		"attacker.localhost:7080",
		"evil.com:8080",
		"192.168.1.100",
		"sub.hub.localhost",
	}

	for _, host := range disallowedHosts {
		t.Run(host, func(t *testing.T) {
			req, err := http.NewRequest(http.MethodGet, ts.URL+"/", nil)
			if err != nil {
				t.Fatalf("failed to create request: %v", err)
			}
			req.Host = host
			resp, err := ts.Client().Do(req)
			if err != nil {
				t.Fatalf("request failed: %v", err)
			}
			defer resp.Body.Close()

			if resp.StatusCode != http.StatusMisdirectedRequest {
				t.Fatalf("expected 421 Misdirected Request for Host %q, got %d", host, resp.StatusCode)
			}
		})
	}

	allowedHosts := []string{
		"hub.localhost",
		"apps.localhost",
		"localhost",
		"127.0.0.1",
		"[::1]",
		"hub.localhost:7080",
		"localhost:7080",
		"127.0.0.1:7080",
	}

	for _, host := range allowedHosts {
		t.Run("allowed_"+host, func(t *testing.T) {
			req, err := http.NewRequest(http.MethodGet, ts.URL+"/", nil)
			if err != nil {
				t.Fatalf("failed to create request: %v", err)
			}
			req.Host = host
			resp, err := ts.Client().Do(req)
			if err != nil {
				t.Fatalf("request failed: %v", err)
			}
			defer resp.Body.Close()

			if resp.StatusCode == http.StatusMisdirectedRequest {
				t.Fatalf("expected Host %q to be allowed, got 421", host)
			}
			if resp.Header.Get("X-Frame-Options") != "DENY" {
				t.Fatalf("expected X-Frame-Options DENY, got %q", resp.Header.Get("X-Frame-Options"))
			}
			if resp.Header.Get("Content-Security-Policy") != "frame-ancestors 'none'" {
				t.Fatalf("expected Content-Security-Policy frame-ancestors 'none', got %q", resp.Header.Get("Content-Security-Policy"))
			}
		})
	}
}

// 9. C1 Cross-Subdomain CSRF: Origin: http://otherapp.localhost:7080 -> 403 Forbidden
func TestCrossSubdomainCSRFProtection(t *testing.T) {
	ctrl := &mockController{}
	srv := NewServer(ctrl, nil)
	ts := httptest.NewServer(srv)
	defer ts.Close()

	disallowedOrigins := []string{
		"http://otherapp.localhost:7080",
		"http://untrusted-app.localhost:7080",
		"http://foo.localhost:3000",
		"http://evil.com",
	}

	for _, origin := range disallowedOrigins {
		t.Run("post_kill_"+origin, func(t *testing.T) {
			req, err := http.NewRequest(http.MethodPost, ts.URL+"/api/kill?id=srv-1", nil)
			if err != nil {
				t.Fatalf("failed to create request: %v", err)
			}
			req.Header.Set("X-Lazylocal-Token", srv.Token()) // valid token
			req.Header.Set("Origin", origin)

			resp, err := ts.Client().Do(req)
			if err != nil {
				t.Fatalf("request failed: %v", err)
			}
			defer resp.Body.Close()

			if resp.StatusCode != http.StatusForbidden {
				t.Fatalf("expected 403 Forbidden for origin %q, got %d", origin, resp.StatusCode)
			}
		})

		t.Run("get_services_"+origin, func(t *testing.T) {
			req, err := http.NewRequest(http.MethodGet, ts.URL+"/api/services", nil)
			if err != nil {
				t.Fatalf("failed to create request: %v", err)
			}
			req.Header.Set("Origin", origin)

			resp, err := ts.Client().Do(req)
			if err != nil {
				t.Fatalf("request failed: %v", err)
			}
			defer resp.Body.Close()

			if resp.StatusCode != http.StatusForbidden {
				t.Fatalf("expected 403 Forbidden for GET /api/services from origin %q, got %d", origin, resp.StatusCode)
			}
		})
	}

	// Verify SSE init event does NOT expose the token
	{
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()

		sseReq, err := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"/api/events", nil)
		if err != nil {
			t.Fatalf("failed to create sse request: %v", err)
		}
		sseResp, err := ts.Client().Do(sseReq)
		if err != nil {
			t.Fatalf("failed to connect to sse: %v", err)
		}
		defer sseResp.Body.Close()

		scanner := bufio.NewScanner(sseResp.Body)
		for scanner.Scan() {
			line := scanner.Text()
			if strings.HasPrefix(line, "data: ") {
				dataJSON := strings.TrimPrefix(line, "data: ")
				var payload map[string]any
				if err := json.Unmarshal([]byte(dataJSON), &payload); err == nil {
					if payload["type"] == "init" {
						if _, hasToken := payload["token"]; hasToken {
							t.Fatalf("Security violation: SSE init message must not contain token!")
						}
						break
					}
				}
			}
		}
	}
}

func TestServerDNSRebindingProtection(t *testing.T) {
	TestHostAllowlistMisdirectedRequest(t)
}

func TestServerSecurityHeadersAndCSRF(t *testing.T) {
	TestCrossSubdomainCSRFProtection(t)
}
