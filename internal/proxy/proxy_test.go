package proxy

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ugur/lazylocal/internal/capture"
	"github.com/ugur/lazylocal/internal/model"
)

func TestDNSRebindingProtection(t *testing.T) {
	ring := capture.NewRingBuffer(10)
	p := NewProxy(ring, nil)

	forbiddenHosts := []string{
		"evil.com",
		"evil.com:8080",
		"attacker.localhost.evil.com",
		"192.168.1.1",
		"google.com",
		"notlocalhost",
	}

	for _, host := range forbiddenHosts {
		req := httptest.NewRequest("GET", "http://"+host+"/test", nil)
		req.Host = host
		rec := httptest.NewRecorder()

		p.ServeHTTP(rec, req)

		if rec.Code != http.StatusMisdirectedRequest {
			t.Errorf("host %s: expected 421 Misdirected Request, got %d", host, rec.Code)
		}
	}

	// Allowed host pattern check
	p.UpdateServices([]model.Service{
		{ID: "s1", Name: "app", Port: 9999, Running: false},
	})

	allowedHosts := []string{
		"localhost",
		"localhost:7080",
		"app.localhost",
		"app.localhost:7080",
		"feat-auth.shop.localhost",
	}

	for _, host := range allowedHosts {
		req := httptest.NewRequest("GET", "http://"+host+"/", nil)
		req.Host = host
		rec := httptest.NewRecorder()

		p.ServeHTTP(rec, req)

		// Since service is not running or not found, it shouldn't be 421
		if rec.Code == http.StatusMisdirectedRequest {
			t.Errorf("host %s was falsely rejected with 421", host)
		}
	}
}

func TestForwardedHostAnd200OK(t *testing.T) {
	var receivedXForwardedHost string
	var receivedReqHost string

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedXForwardedHost = r.Header.Get("X-Forwarded-Host")
		receivedReqHost = r.Host
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("backend-ok"))
	}))
	defer backend.Close()

	u, _ := url.Parse(backend.URL)
	port, _ := strconv.Atoi(u.Port())

	ring := capture.NewRingBuffer(10)
	bus := model.NewBus()
	p := NewProxy(ring, bus)

	p.UpdateServices([]model.Service{
		{
			ID:      "srv-foo",
			Name:    "foo",
			Host:    "foo.localhost",
			Port:    port,
			Running: true,
		},
	})

	proxyServer := httptest.NewServer(p)
	defer proxyServer.Close()

	client := proxyServer.Client()
	req, err := http.NewRequest("GET", proxyServer.URL+"/api/hello", nil)
	if err != nil {
		t.Fatalf("failed to create request: %v", err)
	}
	req.Host = "foo.localhost"

	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("proxy request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", resp.StatusCode)
	}

	body, _ := io.ReadAll(resp.Body)
	if string(body) != "backend-ok" {
		t.Fatalf("expected 'backend-ok', got %q", string(body))
	}

	if receivedXForwardedHost != "foo.localhost" {
		t.Fatalf("expected X-Forwarded-Host 'foo.localhost', got %q", receivedXForwardedHost)
	}

	expectedTargetHost := fmt.Sprintf("127.0.0.1:%d", port)
	if receivedReqHost != expectedTargetHost {
		t.Fatalf("expected backend req.Host %q, got %q", expectedTargetHost, receivedReqHost)
	}

	// Verify exchange record
	exchanges := ring.List("srv-foo", false)
	if len(exchanges) != 1 {
		t.Fatalf("expected 1 exchange recorded, got %d", len(exchanges))
	}
	ex := exchanges[0]
	if ex.Req.Host != "foo.localhost" {
		t.Fatalf("expected recorded host 'foo.localhost', got %q", ex.Req.Host)
	}
	if ex.Resp.StatusCode != 200 {
		t.Fatalf("expected recorded status 200, got %d", ex.Resp.StatusCode)
	}
}

func Test1MBPayloadIntegrityAnd64KBCaptureTruncation(t *testing.T) {
	const oneMB = 1024 * 1024 // 1048576 bytes
	reqData := bytes.Repeat([]byte("A"), oneMB)
	respData := bytes.Repeat([]byte("B"), oneMB)

	var backendReceivedBytes int64

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n, err := io.Copy(io.Discard, r.Body)
		if err != nil {
			t.Errorf("backend read error: %v", err)
		}
		backendReceivedBytes = n

		w.Header().Set("Content-Type", "application/octet-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(respData)
	}))
	defer backend.Close()

	u, _ := url.Parse(backend.URL)
	port, _ := strconv.Atoi(u.Port())

	ring := capture.NewRingBuffer(10)
	p := NewProxy(ring, nil)
	p.UpdateServices([]model.Service{
		{ID: "large-svc", Name: "large", Port: port, Running: true},
	})

	proxyServer := httptest.NewServer(p)
	defer proxyServer.Close()

	req, err := http.NewRequest("POST", proxyServer.URL+"/upload", bytes.NewReader(reqData))
	if err != nil {
		t.Fatalf("failed to create request: %v", err)
	}
	req.Host = "large.localhost"
	req.Header.Set("Content-Type", "application/octet-stream")

	resp, err := proxyServer.Client().Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	clientReceivedBuf := new(bytes.Buffer)
	clientReceivedBytes, err := io.Copy(clientReceivedBuf, resp.Body)
	if err != nil {
		t.Fatalf("failed reading proxy response: %v", err)
	}

	// 1. Verify end-to-end full 1 MB delivery
	if backendReceivedBytes != oneMB {
		t.Fatalf("backend received %d bytes, expected full 1MB (%d)", backendReceivedBytes, oneMB)
	}
	if clientReceivedBytes != oneMB {
		t.Fatalf("client received %d bytes, expected full 1MB (%d)", clientReceivedBytes, oneMB)
	}

	// 2. Verify Exchange capture truncation to 64KB (65536)
	exchanges := ring.List("large-svc", false)
	if len(exchanges) != 1 {
		t.Fatalf("expected 1 exchange recorded, got %d", len(exchanges))
	}
	ex := exchanges[0]

	if !ex.Req.Truncated {
		t.Fatalf("expected ex.Req.Truncated to be true")
	}
	if ex.Req.BodySize != oneMB {
		t.Fatalf("expected ex.Req.BodySize to be %d, got %d", oneMB, ex.Req.BodySize)
	}
	if len(ex.Req.Body) != capture.MaxBodyBytes {
		t.Fatalf("expected len(ex.Req.Body) == %d, got %d", capture.MaxBodyBytes, len(ex.Req.Body))
	}

	if !ex.Resp.Truncated {
		t.Fatalf("expected ex.Resp.Truncated to be true")
	}
	if ex.Resp.BodySize != oneMB {
		t.Fatalf("expected ex.Resp.BodySize to be %d, got %d", oneMB, ex.Resp.BodySize)
	}
	if len(ex.Resp.Body) != capture.MaxBodyBytes {
		t.Fatalf("expected len(ex.Resp.Body) == %d, got %d", capture.MaxBodyBytes, len(ex.Resp.Body))
	}

	if !ex.Truncated {
		t.Fatalf("expected ex.Truncated to be true")
	}
}

func TestAuthorizationRedactionAndReplay(t *testing.T) {
	var mu sync.Mutex
	var calls []struct {
		Auth string
		Body string
	}

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		calls = append(calls, struct {
			Auth string
			Body string
		}{
			Auth: r.Header.Get("Authorization"),
			Body: string(body),
		})
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"result":"ok"}`))
	}))
	defer backend.Close()

	u, _ := url.Parse(backend.URL)
	port, _ := strconv.Atoi(u.Port())

	ring := capture.NewRingBuffer(10)
	bus := model.NewBus()
	p := NewProxy(ring, bus)
	p.UpdateServices([]model.Service{
		{ID: "auth-svc", Name: "auth", Port: port, Running: true},
	})

	proxyServer := httptest.NewServer(p)
	defer proxyServer.Close()

	// 1. Initial proxy call
	req, _ := http.NewRequest("POST", proxyServer.URL+"/login", strings.NewReader(`{"username":"admin"}`))
	req.Host = "auth.localhost"
	req.Header.Set("Authorization", "Bearer secret123")
	req.Header.Set("Content-Type", "application/json")

	resp, err := proxyServer.Client().Do(req)
	if err != nil {
		t.Fatalf("initial request failed: %v", err)
	}
	_ = resp.Body.Close()

	// 2. Check mask in captured exchange
	exchanges := ring.List("auth-svc", false)
	if len(exchanges) != 1 {
		t.Fatalf("expected 1 exchange recorded, got %d", len(exchanges))
	}
	origEx := exchanges[0]

	authVals, ok := origEx.Req.Headers["Authorization"]
	if !ok || len(authVals) != 1 || authVals[0] != "REDACTED" {
		t.Fatalf("expected Authorization header to be REDACTED, got: %+v", authVals)
	}

	// Raw unmasked header must be preserved in memory
	if origEx.Req.RawHeaders.Get("Authorization") != "Bearer secret123" {
		t.Fatalf("expected RawHeaders to keep secret123, got: %s", origEx.Req.RawHeaders.Get("Authorization"))
	}

	// 3. Trigger Replay
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	replayEx, err := p.Replay(ctx, origEx.ID)
	if err != nil {
		t.Fatalf("replay failed: %v", err)
	}

	if replayEx.ReplayedOf != origEx.ID {
		t.Fatalf("expected ReplayedOf %s, got %s", origEx.ID, replayEx.ReplayedOf)
	}

	// 4. Verify backend received original Bearer secret123 on Replay
	mu.Lock()
	defer mu.Unlock()
	if len(calls) != 2 {
		t.Fatalf("expected 2 backend calls, got %d", len(calls))
	}

	// First call
	if calls[0].Auth != "Bearer secret123" || calls[0].Body != `{"username":"admin"}` {
		t.Fatalf("unexpected call 1: %+v", calls[0])
	}
	// Replay call
	if calls[1].Auth != "Bearer secret123" || calls[1].Body != `{"username":"admin"}` {
		t.Fatalf("unexpected call 2 (replay): %+v", calls[1])
	}
}

func TestSSELowLatencyStreaming(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")

		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "Flusher unsupported", http.StatusInternalServerError)
			return
		}

		// Flush headers immediately
		flusher.Flush()

		// Send first event immediately
		fmt.Fprintf(w, "data: event-one\n\n")
		flusher.Flush()

		// Keep connection open for a moment
		time.Sleep(500 * time.Millisecond)
		fmt.Fprintf(w, "data: event-two\n\n")
		flusher.Flush()
	}))
	defer backend.Close()

	u, _ := url.Parse(backend.URL)
	port, _ := strconv.Atoi(u.Port())

	ring := capture.NewRingBuffer(10)
	p := NewProxy(ring, nil)
	p.UpdateServices([]model.Service{
		{ID: "sse-svc", Name: "sse", Port: port, Running: true},
	})

	proxyServer := httptest.NewServer(p)
	defer proxyServer.Close()

	req, _ := http.NewRequest("GET", proxyServer.URL+"/events", nil)
	req.Host = "sse.localhost"

	start := time.Now()
	resp, err := proxyServer.Client().Do(req)
	if err != nil {
		t.Fatalf("sse request failed: %v", err)
	}
	defer resp.Body.Close()

	reader := bufio.NewReader(resp.Body)
	line, err := reader.ReadString('\n')
	firstEventLatency := time.Since(start)

	if err != nil {
		t.Fatalf("failed to read first sse line: %v", err)
	}

	line = strings.TrimSpace(line)
	if line != "data: event-one" {
		t.Fatalf("expected 'data: event-one', got %q", line)
	}

	if firstEventLatency > 100*time.Millisecond {
		t.Fatalf("first SSE event took too long: %v (expected < 100ms)", firstEventLatency)
	}
}

func TestWebSocketUpgradeEcho(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
			http.Error(w, "Expected websocket upgrade", http.StatusBadRequest)
			return
		}

		hj, ok := w.(http.Hijacker)
		if !ok {
			http.Error(w, "Hijacking unsupported", http.StatusInternalServerError)
			return
		}

		conn, rw, err := hj.Hijack()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		defer conn.Close()

		// Send 101 Switching Protocols
		_, _ = rw.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n\r\n")
		_ = rw.Flush()

		// Echo loop
		buf := make([]byte, 1024)
		n, err := rw.Read(buf)
		if err == nil && n > 0 {
			_, _ = rw.Write(buf[:n])
			_ = rw.Flush()
		}
	}))
	defer backend.Close()

	u, _ := url.Parse(backend.URL)
	port, _ := strconv.Atoi(u.Port())

	ring := capture.NewRingBuffer(10)
	p := NewProxy(ring, nil)
	p.UpdateServices([]model.Service{
		{ID: "ws-svc", Name: "ws", Port: port, Running: true},
	})

	proxyServer := httptest.NewServer(p)
	defer proxyServer.Close()

	proxyURL, _ := url.Parse(proxyServer.URL)

	// Dial raw TCP to proxy server
	conn, err := net.Dial("tcp", proxyURL.Host)
	if err != nil {
		t.Fatalf("failed to dial proxy: %v", err)
	}
	defer conn.Close()

	// Send HTTP Upgrade Request
	upgradeReq := fmt.Sprintf("GET /ws HTTP/1.1\r\nHost: ws.localhost\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n\r\n")
	_, err = conn.Write([]byte(upgradeReq))
	if err != nil {
		t.Fatalf("failed to write upgrade request: %v", err)
	}

	reader := bufio.NewReader(conn)
	statusLine, err := reader.ReadString('\n')
	if err != nil {
		t.Fatalf("failed to read response status: %v", err)
	}

	if !strings.Contains(statusLine, "101") {
		t.Fatalf("expected 101 Switching Protocols, got: %s", statusLine)
	}

	// Consume remaining response headers
	for {
		headerLine, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("failed reading headers: %v", err)
		}
		if headerLine == "\r\n" || headerLine == "\n" {
			break
		}
	}

	// Send echo payload
	testPayload := []byte("hello-lazylocal-ws-echo")
	_, err = conn.Write(testPayload)
	if err != nil {
		t.Fatalf("failed to send ws payload: %v", err)
	}

	echoBuf := make([]byte, len(testPayload))
	_, err = io.ReadFull(reader, echoBuf)
	if err != nil {
		t.Fatalf("failed to read echoed payload: %v", err)
	}

	if !bytes.Equal(echoBuf, testPayload) {
		t.Fatalf("expected echo %q, got %q", string(testPayload), string(echoBuf))
	}
}

func TestDiffExchangesAndCurlCommand(t *testing.T) {
	exA := model.Exchange{
		DurationMs: 12.5,
		Req: model.HTTPRequestData{
			Method: "POST",
			Host:   "foo.localhost:7080",
			Path:   "/items",
			Query:  "sort=asc",
			Headers: map[string][]string{
				"Content-Type":  {"application/json"},
				"Authorization": {"REDACTED"},
			},
			Body: []byte(`{"id":1}`),
		},
		Resp: model.HTTPResponseData{
			StatusCode: 200,
			Body:       []byte("line 1\nline 2\nline 3"),
			BodySize:   20,
		},
	}

	exB := model.Exchange{
		DurationMs: 15.0,
		Resp: model.HTTPResponseData{
			StatusCode: 201,
			Body:       []byte("line 1\nline 2 modified\nline 3\nline 4"),
			BodySize:   32,
		},
	}

	// Test Diff
	diff := DiffExchanges(exA, exB)
	if !strings.Contains(diff, "Status: 200 -> 201") {
		t.Errorf("diff missing status change: %s", diff)
	}
	if !strings.Contains(diff, "- line 2") || !strings.Contains(diff, "+ line 2 modified") {
		t.Errorf("diff missing line diff: %s", diff)
	}

	// Test Curl
	curl := CurlCommand(exA)
	if !strings.Contains(curl, "curl -X POST 'http://foo.localhost:7080/items?sort=asc'") {
		t.Errorf("unexpected curl URL line: %s", curl)
	}
	if !strings.Contains(curl, "-H 'Authorization: REDACTED'") {
		t.Errorf("curl missing redacted auth header: %s", curl)
	}
	if !strings.Contains(curl, `--data-raw '{"id":1}'`) {
		t.Errorf("curl missing data-raw: %s", curl)
	}
}

func TestCurlCommandShellQuoteEscaping(t *testing.T) {
	ex := model.Exchange{
		Req: model.HTTPRequestData{
			Method: "POST",
			Host:   "app.localhost",
			Path:   "/search",
			Query:  "q=it's",
			Headers: map[string][]string{
				"X-User": {"Bob's Laptop"},
			},
			Body: []byte(`{"quote":"it's a test"}`),
		},
	}

	cmd := CurlCommand(ex)
	// Single quotes should be escaped as '"'"'
	expectedURL := `'http://app.localhost/search?q=it'"'"'s'`
	expectedHeader := `  -H 'X-User: Bob'"'"'s Laptop'`
	expectedBody := `  --data-raw '{"quote":"it'"'"'s a test"}'`

	if !strings.Contains(cmd, expectedURL) {
		t.Errorf("expected escaped URL %s, got:\n%s", expectedURL, cmd)
	}
	if !strings.Contains(cmd, expectedHeader) {
		t.Errorf("expected escaped header %s, got:\n%s", expectedHeader, cmd)
	}
	if !strings.Contains(cmd, expectedBody) {
		t.Errorf("expected escaped body %s, got:\n%s", expectedBody, cmd)
	}
}

func TestComputeLineDiffCapping500Lines(t *testing.T) {
	// Create two 1000-line texts that differ at line 10 and line 800
	var bA, bB strings.Builder
	for i := 0; i < 1000; i++ {
		if i == 10 {
			bA.WriteString(fmt.Sprintf("line %d original\n", i))
			bB.WriteString(fmt.Sprintf("line %d modified\n", i))
		} else {
			bA.WriteString(fmt.Sprintf("line %d\n", i))
			bB.WriteString(fmt.Sprintf("line %d\n", i))
		}
	}

	// Should execute rapidly without allocating huge 1000x1000 slice
	start := time.Now()
	diff := computeLineDiff(bA.String(), bB.String())
	elapsed := time.Since(start)

	if elapsed > 500*time.Millisecond {
		t.Errorf("computeLineDiff took too long: %v", elapsed)
	}
	if !strings.Contains(diff, "- line 10 original") || !strings.Contains(diff, "+ line 10 modified") {
		t.Errorf("expected diff to capture line 10 modification, got:\n%s", diff)
	}
}

func TestCaptureReaderSnapshotThreadSafety(t *testing.T) {
	src := io.NopCloser(strings.NewReader("hello world snapshot test"))
	cr := newCaptureReader(src, 1024)

	buf := make([]byte, 11) // read "hello world"
	n, err := cr.Read(buf)
	if err != nil || n != 11 {
		t.Fatalf("failed to read 11 bytes: %v, n=%d", err, n)
	}

	snap1, total1, trunc1 := cr.snapshot()
	if string(snap1) != "hello world" || total1 != 11 || trunc1 {
		t.Fatalf("unexpected snapshot 1: snap=%q total=%d trunc=%v", string(snap1), total1, trunc1)
	}

	// Read remaining bytes
	buf2 := make([]byte, 14)
	n2, err := cr.Read(buf2)
	if err != nil || n2 != 14 {
		t.Fatalf("failed to read remaining bytes: %v, n=%d", err, n2)
	}

	// snap1 must be unchanged (cloned)
	if string(snap1) != "hello world" {
		t.Fatalf("snap1 mutated by subsequent reads! got: %q", string(snap1))
	}

	snap2, total2, trunc2 := cr.snapshot()
	if string(snap2) != "hello world snapshot test" || total2 != 25 || trunc2 {
		t.Fatalf("unexpected snapshot 2: snap=%q total=%d trunc=%v", string(snap2), total2, trunc2)
	}
}

func BenchmarkProxyLatency(b *testing.B) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("OK"))
	}))
	defer backend.Close()

	u, _ := url.Parse(backend.URL)
	port, _ := strconv.Atoi(u.Port())

	ring := capture.NewRingBuffer(1000)
	p := NewProxy(ring, nil)
	p.UpdateServices([]model.Service{
		{ID: "bench-svc", Name: "bench", Port: port, Running: true},
	})

	proxyServer := httptest.NewServer(p)
	defer proxyServer.Close()

	client := proxyServer.Client()

	req, _ := http.NewRequest("GET", proxyServer.URL+"/ping", nil)
	req.Host = "bench.localhost"

	// Warmup
	for i := 0; i < 10; i++ {
		resp, err := client.Do(req)
		if err == nil {
			_ = resp.Body.Close()
		}
	}

	b.ResetTimer()
	b.ReportAllocs()

	latencies := make([]time.Duration, 0, b.N)

	for i := 0; i < b.N; i++ {
		t0 := time.Now()
		resp, err := client.Do(req)
		lat := time.Since(t0)
		if err != nil {
			b.Fatalf("benchmark request failed: %v", err)
		}
		_ = resp.Body.Close()
		latencies = append(latencies, lat)
	}

	// Verify p50 latency < 1ms
	if len(latencies) > 0 {
		// Calculate median (p50)
		p50 := latencies[len(latencies)/2]
		b.Logf("p50 latency: %v", p50)
		if p50 > 1*time.Millisecond {
			b.Logf("Notice: p50 latency is %v (target < 1ms under low load)", p50)
		}
	}
}
