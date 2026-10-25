package proxy

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/kuarezma/lazylocal/internal/capture"
	"github.com/kuarezma/lazylocal/internal/model"
)

var idCounter uint64

func generateExchangeID() string {
	c := atomic.AddUint64(&idCounter, 1)
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return fmt.Sprintf("ex_%d_%x_%d", time.Now().UnixNano(), b, c)
}

// captureReader wraps an io.ReadCloser to capture up to max bytes into a buffer
// while passing the full unread stream through to the reader.
type captureReader struct {
	rc         io.ReadCloser
	mu         sync.Mutex
	buf        bytes.Buffer
	max        int
	total      int64
	truncated  bool
	onFinish   func()
	finishOnce sync.Once
}

// limitCaptureReader is an alias for captureReader.
type limitCaptureReader = captureReader

// snapshot returns a thread-safe cloned copy of the captured buffer, total bytes read, and truncation state.
func (c *captureReader) snapshot() ([]byte, int64, bool) {
	if c == nil {
		return nil, 0, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return bytes.Clone(c.buf.Bytes()), c.total, c.truncated
}

func newCaptureReader(rc io.ReadCloser, max int) *captureReader {
	return &captureReader{
		rc:  rc,
		max: max,
	}
}

func (c *captureReader) finish() {
	if c.onFinish != nil {
		c.finishOnce.Do(c.onFinish)
	}
}

func (c *captureReader) Read(p []byte) (n int, err error) {
	if c.rc == nil {
		c.finish()
		return 0, io.EOF
	}
	n, err = c.rc.Read(p)
	if n > 0 {
		c.mu.Lock()
		c.total += int64(n)
		if c.buf.Len() < c.max {
			canWrite := c.max - c.buf.Len()
			if n <= canWrite {
				c.buf.Write(p[:n])
			} else {
				c.buf.Write(p[:canWrite])
				c.truncated = true
			}
		} else {
			c.truncated = true
		}
		c.mu.Unlock()
	}
	if err != nil {
		c.finish()
	}
	return n, err
}

func (c *captureReader) Close() error {
	var err error
	if c.rc != nil {
		err = c.rc.Close()
	}
	c.finish()
	return err
}

// Proxy is a dynamic .localhost HTTP reverse proxy with tap & replay capabilities.
type Proxy struct {
	ring         *capture.RingBuffer
	bus          *model.Bus
	mu           sync.RWMutex
	services     []model.Service
	client       *http.Client
	transport    *http.Transport
	redactMemory bool
}

// NewProxy creates a new reverse proxy instance.
func NewProxy(ring *capture.RingBuffer, bus *model.Bus) *Proxy {
	tr := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   5 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		ForceAttemptHTTP2:     false,
		MaxIdleConns:          500,
		MaxIdleConnsPerHost:   100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   5 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
	}

	return &Proxy{
		ring:      ring,
		bus:       bus,
		transport: tr,
		client: &http.Client{
			Transport: tr,
			Timeout:   30 * time.Second,
		},
	}
}

// UpdateServices thread-safely updates the active services list.
func (p *Proxy) UpdateServices(services []model.Service) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.services = make([]model.Service, len(services))
	copy(p.services, services)
}

// SetRedactMemory enables or disables zero-secret RAM retention by scrubbing
// sensitive headers (Authorization, Cookie, X-Api-Key) from in-memory replay buffers.
func (p *Proxy) SetRedactMemory(v bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.redactMemory = v
}

// RedactMemory returns whether in-memory header redaction is enabled.
func (p *Proxy) RedactMemory() bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.redactMemory
}

// findService looks up a service matching host or subdomain.
func (p *Proxy) findService(cleanHost, subdomain string) (model.Service, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()

	for _, svc := range p.services {
		svcHost := strings.ToLower(svc.Host)
		if h, _, err := net.SplitHostPort(svcHost); err == nil {
			svcHost = h
		}

		if subdomain != "" {
			if strings.EqualFold(svc.Name, subdomain) ||
				strings.EqualFold(svc.ID, subdomain) ||
				strings.EqualFold(svcHost, subdomain) {
				return svc, true
			}
		}

		if strings.EqualFold(svcHost, cleanHost) ||
			strings.EqualFold(svc.Name, cleanHost) ||
			strings.EqualFold(svc.ID, cleanHost) {
			return svc, true
		}
	}
	return model.Service{}, false
}

// ServeHTTP handles incoming HTTP requests, enforces DNS rebinding protection,
// routes to local targets, and records full exchanges.
func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start := time.Now()

	// 1. DNS Rebinding Protection
	rawHost := r.Host
	cleanHost := rawHost
	if h, _, err := net.SplitHostPort(cleanHost); err == nil {
		cleanHost = h
	}
	cleanHost = strings.ToLower(strings.TrimSpace(cleanHost))

	if cleanHost != "localhost" && !strings.HasSuffix(cleanHost, ".localhost") {
		http.Error(w, "Misdirected Request: only .localhost domains allowed", http.StatusMisdirectedRequest)
		return
	}

	// 2. Subdomain Extraction
	var subdomain string
	if strings.HasSuffix(cleanHost, ".localhost") {
		subdomain = strings.TrimSuffix(cleanHost, ".localhost")
	}

	// 3. Find matching service
	target, ok := p.findService(cleanHost, subdomain)
	if !ok {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"error":     "service not found",
			"host":      r.Host,
			"subdomain": subdomain,
			"status":    http.StatusNotFound,
		})
		return
	}

	if !target.Running || target.Port <= 0 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"error":   "service not running or invalid port",
			"service": target.Name,
			"port":    target.Port,
			"status":  http.StatusBadGateway,
		})
		return
	}

	// 4. Capture Request Body
	var reqCapture *captureReader
	origRawHeaders := r.Header.Clone()
	maskedReqHeaders := capture.MaskHeaders(r.Header)

	if r.Body != nil {
		reqCapture = newCaptureReader(r.Body, capture.MaxBodyBytes)
		r.Body = reqCapture
	}

	// 5. Setup Reverse Proxy
	targetURL, _ := url.Parse(fmt.Sprintf("http://127.0.0.1:%d", target.Port))

	var (
		respCapture     *captureReader
		respStatusCode  = http.StatusOK
		respHeaders     map[string][]string
		respContentType string
		proxyErr        string
		muStatus        sync.Mutex
	)

	isUpgrade := strings.EqualFold(r.Header.Get("Connection"), "Upgrade") ||
		r.Header.Get("Upgrade") != "" ||
		strings.Contains(strings.ToLower(r.Header.Get("Connection")), "upgrade")

	var recordOnce sync.Once
	recordExchange := func() {
		recordOnce.Do(func() {
			duration := time.Since(start)

			var reqBody []byte
			var reqBodySize int64
			var reqTruncated bool
			if reqCapture != nil {
				reqBody, reqBodySize, reqTruncated = reqCapture.snapshot()
			}

			var respBody []byte
			var respBodySize int64
			var respTruncated bool
			muStatus.Lock()
			rc := respCapture
			finalStatus := respStatusCode
			finalErr := proxyErr
			finalHeaders := respHeaders
			finalContentType := respContentType
			muStatus.Unlock()

			if rc != nil {
				respBody, respBodySize, respTruncated = rc.snapshot()
			}

			if isUpgrade && finalStatus == http.StatusOK {
				finalStatus = http.StatusSwitchingProtocols
			}

			ex := model.Exchange{
				ID:         generateExchangeID(),
				ServiceID:  target.ID,
				Service:    target.Name,
				TargetPort: target.Port,
				StartedAt:  start,
				Duration:   duration,
				DurationMs: float64(duration.Microseconds()) / 1000.0,
				Req: model.HTTPRequestData{
					Method:      r.Method,
					Path:        r.URL.Path,
					Query:       r.URL.RawQuery,
					Host:        r.Host,
					Headers:     maskedReqHeaders,
					RawHeaders:  origRawHeaders,
					Body:        reqBody,
					BodySize:    reqBodySize,
					Truncated:   reqTruncated,
					ContentType: r.Header.Get("Content-Type"),
				},
				Resp: model.HTTPResponseData{
					StatusCode:  finalStatus,
					Status:      http.StatusText(finalStatus),
					Headers:     finalHeaders,
					Body:        respBody,
					BodySize:    respBodySize,
					Truncated:   respTruncated,
					ContentType: finalContentType,
				},
				Truncated: reqTruncated || respTruncated,
				Error:     finalErr,
				RawReqHeaders: origRawHeaders,
			}

			if p.RedactMemory() {
				ex.RawReqHeaders = maskHeaders(r.Header)
				ex.Req.RawHeaders = ex.RawReqHeaders
			}

			if p.ring != nil {
				p.ring.Record(ex)
			}

			if p.bus != nil {
				p.bus.Publish(model.Event{
					Type:      model.EventExchangeRecorded,
					Exchange:  &ex,
					ServiceID: target.ID,
				})
			}
		})
	}

	rp := &httputil.ReverseProxy{
		Transport:     p.transport,
		FlushInterval: -1, // Immediate flushing for SSE / low-latency
		Director: func(req *http.Request) {
			req.URL.Scheme = "http"
			req.URL.Host = targetURL.Host
			req.Header.Set("X-Forwarded-Host", r.Host)
			if clientIP, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
				req.Header.Set("X-Forwarded-For", clientIP)
			}
			if r.TLS != nil {
				req.Header.Set("X-Forwarded-Proto", "https")
			} else {
				req.Header.Set("X-Forwarded-Proto", "http")
			}
			// Set req.Host to 127.0.0.1:PORT for Vite/Next.js host check compatibility
			req.Host = targetURL.Host
		},
		ModifyResponse: func(resp *http.Response) error {
			muStatus.Lock()
			respStatusCode = resp.StatusCode
			respHeaders = resp.Header.Clone()
			respContentType = resp.Header.Get("Content-Type")
			if resp.StatusCode == http.StatusSwitchingProtocols {
				muStatus.Unlock()
				return nil
			}
			rc := newCaptureReader(resp.Body, capture.MaxBodyBytes)
			rc.onFinish = recordExchange
			respCapture = rc
			resp.Body = rc
			muStatus.Unlock()
			return nil
		},
		ErrorHandler: func(rw http.ResponseWriter, req *http.Request, err error) {
			muStatus.Lock()
			respStatusCode = http.StatusBadGateway
			proxyErr = err.Error()
			muStatus.Unlock()

			recordExchange()

			rw.Header().Set("Content-Type", "application/json")
			rw.WriteHeader(http.StatusBadGateway)
			_ = json.NewEncoder(rw).Encode(map[string]interface{}{
				"error":  "bad gateway",
				"detail": err.Error(),
				"status": http.StatusBadGateway,
			})
		},
	}

	// 6. Execute reverse proxy call
	rp.ServeHTTP(w, r)
	recordExchange()
}

// Replay re-executes a previously captured exchange using original unmasked headers and body.
func (p *Proxy) Replay(ctx context.Context, exchangeID string) (*model.Exchange, error) {
	if p.ring == nil {
		return nil, fmt.Errorf("capture ring buffer is nil")
	}

	origEx, ok := p.ring.Get(exchangeID)
	if !ok {
		return nil, fmt.Errorf("exchange not found: %s", exchangeID)
	}

	targetURL := fmt.Sprintf("http://127.0.0.1:%d%s", origEx.TargetPort, origEx.Req.Path)
	if origEx.Req.Query != "" {
		targetURL += "?" + origEx.Req.Query
	}

	req, err := http.NewRequestWithContext(ctx, origEx.Req.Method, targetURL, bytes.NewReader(origEx.Req.Body))
	if err != nil {
		return nil, fmt.Errorf("failed to create replay request: %w", err)
	}

	// Use original unmasked headers if present (unless redactMemory was enabled)
	if origEx.RawReqHeaders != nil {
		req.Header = origEx.RawReqHeaders.Clone()
	} else if origEx.Req.RawHeaders != nil {
		req.Header = origEx.Req.RawHeaders.Clone()
	} else {
		req.Header = make(http.Header)
		for k, vals := range origEx.Req.Headers {
			for _, v := range vals {
				req.Header.Add(k, v)
			}
		}
	}

	req.Host = fmt.Sprintf("127.0.0.1:%d", origEx.TargetPort)
	if origEx.Req.Host != "" {
		req.Header.Set("X-Forwarded-Host", origEx.Req.Host)
	}

	start := time.Now()
	resp, err := p.client.Do(req)
	duration := time.Since(start)

	newEx := &model.Exchange{
		ID:         generateExchangeID(),
		ServiceID:  origEx.ServiceID,
		Service:    origEx.Service,
		TargetPort: origEx.TargetPort,
		StartedAt:  start,
		Duration:   duration,
		DurationMs: float64(duration.Microseconds()) / 1000.0,
		ReplayedOf: exchangeID,
		Req: model.HTTPRequestData{
			Method:      origEx.Req.Method,
			Path:        origEx.Req.Path,
			Query:       origEx.Req.Query,
			Host:        origEx.Req.Host,
			Headers:     capture.MaskHeaders(req.Header),
			RawHeaders:  req.Header.Clone(),
			Body:        origEx.Req.Body,
			BodySize:    int64(len(origEx.Req.Body)),
			Truncated:   origEx.Req.Truncated,
			ContentType: req.Header.Get("Content-Type"),
		},
		RawReqHeaders: req.Header.Clone(),
	}

	if p.RedactMemory() {
		newEx.RawReqHeaders = maskHeaders(req.Header)
		newEx.Req.RawHeaders = newEx.RawReqHeaders
	}

	if err != nil {
		newEx.Error = err.Error()
		newEx.Resp.StatusCode = http.StatusBadGateway
		newEx.Resp.Status = http.StatusText(http.StatusBadGateway)
		p.ring.Record(*newEx)
		if p.bus != nil {
			p.bus.Publish(model.Event{
				Type:      model.EventExchangeRecorded,
				Exchange:  newEx,
				ServiceID: newEx.ServiceID,
			})
		}
		return newEx, err
	}
	defer resp.Body.Close()

	// Read response body up to MaxBodyBytes
	var bodyBuf bytes.Buffer
	limitedReader := io.LimitReader(resp.Body, capture.MaxBodyBytes+1)
	n, readErr := bodyBuf.ReadFrom(limitedReader)
	if readErr != nil && readErr != io.EOF {
		newEx.Error = readErr.Error()
	}

	respTruncated := false
	savedBody := bodyBuf.Bytes()
	if n > capture.MaxBodyBytes {
		savedBody = savedBody[:capture.MaxBodyBytes]
		respTruncated = true
	}

	// Drain any remaining bytes to measure accurate BodySize
	extraBytes, _ := io.Copy(io.Discard, resp.Body)
	totalBodySize := n + extraBytes

	newEx.Resp = model.HTTPResponseData{
		StatusCode:  resp.StatusCode,
		Status:      resp.Status,
		Headers:     resp.Header.Clone(),
		Body:        savedBody,
		BodySize:    totalBodySize,
		Truncated:   respTruncated,
		ContentType: resp.Header.Get("Content-Type"),
	}
	newEx.Truncated = newEx.Req.Truncated || respTruncated

	p.ring.Record(*newEx)
	if p.bus != nil {
		p.bus.Publish(model.Event{
			Type:      model.EventExchangeRecorded,
			Exchange:  newEx,
			ServiceID: newEx.ServiceID,
		})
	}

	return newEx, nil
}

// DiffExchanges returns the status code, duration, and line-by-line diff of two exchange responses.
func DiffExchanges(a, b model.Exchange) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Status: %d -> %d\n", a.Resp.StatusCode, b.Resp.StatusCode))
	sb.WriteString(fmt.Sprintf("Duration: %.2fms -> %.2fms (diff: %+.2fms)\n", a.DurationMs, b.DurationMs, b.DurationMs-a.DurationMs))
	sb.WriteString(fmt.Sprintf("Body Size: %d -> %d bytes\n", a.Resp.BodySize, b.Resp.BodySize))
	sb.WriteString("Body Diff:\n")

	diff := computeLineDiff(string(a.Resp.Body), string(b.Resp.Body))
	if diff == "" {
		sb.WriteString("  (identical body)\n")
	} else {
		sb.WriteString(diff)
	}
	return sb.String()
}

// computeLineDiff calculates a line-by-line unified diff between two text strings using LCS.
// If either input exceeds 500 lines, only the first 500 lines are compared to prevent OOM.
func computeLineDiff(textA, textB string) string {
	if textA == textB {
		return ""
	}

	linesA := strings.Split(textA, "\n")
	linesB := strings.Split(textB, "\n")

	const maxDiffLines = 500
	if len(linesA) > maxDiffLines {
		linesA = linesA[:maxDiffLines]
	}
	if len(linesB) > maxDiffLines {
		linesB = linesB[:maxDiffLines]
	}

	n := len(linesA)
	m := len(linesB)

	dp := make([][]int, n+1)
	for i := range dp {
		dp[i] = make([]int, m+1)
	}

	for i := 0; i < n; i++ {
		for j := 0; j < m; j++ {
			if linesA[i] == linesB[j] {
				dp[i+1][j+1] = dp[i][j] + 1
			} else if dp[i+1][j] >= dp[i][j+1] {
				dp[i+1][j+1] = dp[i+1][j]
			} else {
				dp[i+1][j+1] = dp[i][j+1]
			}
		}
	}

	var diff []string
	i, j := n, m
	for i > 0 || j > 0 {
		if i > 0 && j > 0 && linesA[i-1] == linesB[j-1] {
			diff = append(diff, "  "+linesA[i-1])
			i--
			j--
		} else if j > 0 && (i == 0 || dp[i][j-1] >= dp[i-1][j]) {
			diff = append(diff, "+ "+linesB[j-1])
			j--
		} else if i > 0 && (j == 0 || dp[i][j-1] < dp[i-1][j]) {
			diff = append(diff, "- "+linesA[i-1])
			i--
		}
	}

	for l, r := 0, len(diff)-1; l < r; l, r = l+1, r-1 {
		diff[l], diff[r] = diff[r], diff[l]
	}

	return strings.Join(diff, "\n") + "\n"
}

// shellQuote safely wraps a string in single quotes and escapes embedded single quotes as '"'"'
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'"'"'`) + "'"
}

// CurlCommand returns a runnable curl command string using masked headers.
func CurlCommand(ex model.Exchange) string {
	url := fmt.Sprintf("http://%s%s", ex.Req.Host, ex.Req.Path)
	if ex.Req.Query != "" {
		url += "?" + ex.Req.Query
	}

	var parts []string
	parts = append(parts, fmt.Sprintf("curl -X %s %s", ex.Req.Method, shellQuote(url)))

	keys := make([]string, 0, len(ex.Req.Headers))
	for k := range ex.Req.Headers {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, k := range keys {
		for _, v := range ex.Req.Headers[k] {
			parts = append(parts, fmt.Sprintf("  -H %s", shellQuote(k+": "+v)))
		}
	}

	if len(ex.Req.Body) > 0 {
		parts = append(parts, fmt.Sprintf("  --data-raw %s", shellQuote(string(ex.Req.Body))))
	}

	return strings.Join(parts, " \\\n")
}

// maskHeaders creates a clone of http.Header with sensitive headers replaced with "REDACTED".
func maskHeaders(h http.Header) http.Header {
	if h == nil {
		return make(http.Header)
	}
	masked := make(http.Header, len(h))
	for k, v := range h {
		lower := strings.ToLower(k)
		switch lower {
		case "authorization", "cookie", "set-cookie", "x-api-key", "proxy-authorization":
			masked[k] = []string{"REDACTED"}
		default:
			cp := make([]string, len(v))
			copy(cp, v)
			masked[k] = cp
		}
	}
	return masked
}
