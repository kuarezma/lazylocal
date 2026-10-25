package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/kuarezma/lazylocal/internal/model"
)

func captureStdout(t *testing.T, fn func() error) (string, error) {
	t.Helper()
	oldStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe failed: %v", err)
	}
	os.Stdout = w

	var buf bytes.Buffer
	done := make(chan struct{})
	go func() {
		_, _ = io.Copy(&buf, r)
		_ = r.Close()
		close(done)
	}()

	runErr := fn()
	_ = w.Close()
	os.Stdout = oldStdout
	<-done

	return buf.String(), runErr
}

// 1. --version çıktısını ve süresini (<50ms) doğrular
func TestVersionOutputAndLatency(t *testing.T) {
	start := time.Now()

	rawOut, _ := captureStdout(t, func() error {
		printVersion()
		return nil
	})

	duration := time.Since(start)
	output := strings.TrimSpace(rawOut)

	expected := fmt.Sprintf("lazylocal %s (%s/%s)", version, runtime.GOOS, runtime.GOARCH)
	if output != expected {
		t.Fatalf("expected version %q, got %q", expected, output)
	}

	if duration > 50*time.Millisecond {
		t.Fatalf("version output took too long: %v (must be <50ms)", duration)
	}
}

// 2. Uçtan Uca G5 & G7 Entegrasyon Testi
func TestEndToEndLazyLocal(t *testing.T) {
	// A. Geçici sahte backend HTTP sunucusu (1. istekte 500, 2. istekte 200 döner)
	callCount := 0
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		if callCount == 1 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error": "checkout transaction failed"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status": "checkout success"}`))
	}))
	defer backend.Close()

	backendPort := backend.Listener.Addr().(*net.TCPAddr).Port

	// B. lazylocal birleşik HTTP sunucusunu 127.0.0.1:0 üzerinde başlat
	eng, err := NewEngine(0, "pins.toml", 0)
	if err != nil {
		t.Fatalf("failed to create engine: %v", err)
	}
	if err := eng.Start(); err != nil {
		t.Fatalf("failed to start engine: %v", err)
	}
	defer eng.Close()

	// C. Aktif shop servisini ekle (10 varsayılan pin + shop = 11 servis)
	shopSvc := model.Service{
		ID:      "srv-shop-1",
		Name:    "shop",
		Host:    "shop.localhost",
		Port:    backendPort,
		PID:     os.Getpid(),
		Running: true,
	}
	merged := mergeAndClassify(eng.pins, []model.Service{shopSvc})
	eng.SetServices(merged)

	client := &http.Client{Timeout: 5 * time.Second}

	// D. Host: hub.localhost ile GET /api/services -> 200 OK ve 10 pin + shop doğrula
	{
		req, err := http.NewRequest(http.MethodGet, fmt.Sprintf("http://%s/api/services", eng.Addr()), nil)
		if err != nil {
			t.Fatalf("failed to create hub request: %v", err)
		}
		req.Host = "hub.localhost"

		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("GET /api/services failed: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200 OK from hub.localhost, got %d", resp.StatusCode)
		}

		var svcs []model.Service
		if err := json.NewDecoder(resp.Body).Decode(&svcs); err != nil {
			t.Fatalf("failed to decode services JSON: %v", err)
		}

		if len(svcs) < 11 {
			t.Fatalf("expected at least 11 services (10 default pins + 1 active shop), got %d", len(svcs))
		}

		foundShop := false
		foundMatematik := false
		for _, s := range svcs {
			if s.Name == "shop" && s.Running && s.Port == backendPort {
				foundShop = true
			}
			if s.Name == "matematik" && s.Pinned {
				foundMatematik = true
			}
		}
		if !foundShop {
			t.Fatalf("expected active 'shop' service in /api/services list")
		}
		if !foundMatematik {
			t.Fatalf("expected pinned 'matematik' in /api/services list")
		}
	}

	// E. Host: shop.localhost ile POST /checkout çağır -> 500 al, kaydın yakalandığını doğrula
	var recordedExID string
	{
		checkoutPayload := `{"item": "book", "qty": 2}`
		req, err := http.NewRequest(http.MethodPost, fmt.Sprintf("http://%s/checkout", eng.Addr()), strings.NewReader(checkoutPayload))
		if err != nil {
			t.Fatalf("failed to create checkout request: %v", err)
		}
		req.Host = "shop.localhost"
		req.Header.Set("Content-Type", "application/json")

		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("POST /checkout failed: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusInternalServerError {
			t.Fatalf("expected 500 Internal Server Error, got %d", resp.StatusCode)
		}

		bodyBytes, _ := io.ReadAll(resp.Body)
		if !strings.Contains(string(bodyBytes), "checkout transaction failed") {
			t.Fatalf("unexpected body from 500 error: %s", string(bodyBytes))
		}

		// Ring buffer'dan yakalanan kaydı doğrula
		exchanges := eng.Exchanges("", false)
		if len(exchanges) == 0 {
			t.Fatalf("expected at least 1 captured exchange in ring buffer")
		}
		recordedExID = exchanges[0].ID
		if exchanges[0].Service != "shop" || exchanges[0].Resp.StatusCode != 500 {
			t.Fatalf("unexpected exchange recorded: %+v", exchanges[0])
		}
	}

	// F. Replay çağrıldığında 2. denemede 200 OK döndüğünü doğrula
	{
		replayed, err := eng.Replay(context.Background(), recordedExID)
		if err != nil {
			t.Fatalf("Replay failed: %v", err)
		}
		if replayed.Resp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200 OK on replay, got %d", replayed.Resp.StatusCode)
		}
		if !strings.Contains(string(replayed.Resp.Body), "checkout success") {
			t.Fatalf("expected 'checkout success' in replay response, got %s", string(replayed.Resp.Body))
		}
		if replayed.ReplayedOf != recordedExID {
			t.Fatalf("expected ReplayedOf %q, got %q", recordedExID, replayed.ReplayedOf)
		}
	}

	// G. Host: evil.com ile istek atıldığında 421 Misdirected Request döndüğünü doğrula
	{
		req, err := http.NewRequest(http.MethodGet, fmt.Sprintf("http://%s/any-path", eng.Addr()), nil)
		if err != nil {
			t.Fatalf("failed to create evil request: %v", err)
		}
		req.Host = "evil.com"

		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("evil request failed: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusMisdirectedRequest {
			t.Fatalf("expected 421 Misdirected Request for Host: evil.com, got %d", resp.StatusCode)
		}
	}
}

// 3. CLI Subcommand Tests
func TestCLIURLCommand(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("failed to get working dir: %v", err)
	}

	// Test --json output
	{
		rawOut, err := captureStdout(t, func() error {
			return runURL([]string{"--cwd", wd, "--json", "--port", "7080"})
		})
		if err != nil {
			t.Fatalf("runURL failed: %v", err)
		}

		var res map[string]any
		if err := json.Unmarshal([]byte(rawOut), &res); err != nil {
			t.Fatalf("failed to unmarshal JSON: %v", err)
		}

		urlVal, ok := res["url"].(string)
		if !ok || !strings.Contains(urlVal, ".localhost:7080") {
			t.Fatalf("unexpected URL in JSON output: %v", res)
		}
	}

	// Test plain output
	{
		rawOut, err := captureStdout(t, func() error {
			return runURL([]string{"--cwd", wd, "--port", "7080"})
		})
		if err != nil {
			t.Fatalf("runURL plain failed: %v", err)
		}

		line := strings.TrimSpace(rawOut)
		if !strings.HasPrefix(line, "http://") || !strings.Contains(line, ".localhost:7080") {
			t.Fatalf("unexpected URL string: %q", line)
		}
	}
}

func TestCLIListCommand(t *testing.T) {
	rawOut, err := captureStdout(t, func() error {
		return runList([]string{"--json"})
	})
	if err != nil {
		t.Fatalf("runList failed: %v", err)
	}

	var svcs []model.Service
	if err := json.Unmarshal([]byte(rawOut), &svcs); err != nil {
		t.Fatalf("failed to unmarshal JSON from runList: %v", err)
	}

	if len(svcs) < 10 {
		t.Fatalf("expected at least 10 pinned services in list, got %d", len(svcs))
	}
}

func TestCLISweepDryRun(t *testing.T) {
	output, err := captureStdout(t, func() error {
		return runSweep([]string{"--dry-run"})
	})
	if err != nil {
		t.Fatalf("runSweep --dry-run failed: %v", err)
	}

	// Should either say no zombies or dry-run mode
	if !strings.Contains(output, "No zombie dev processes") && !strings.Contains(output, "Dry-run mode") {
		t.Fatalf("unexpected sweep dry run output: %q", output)
	}
}
