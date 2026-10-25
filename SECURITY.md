# Security Architecture & Threat Model

`lazylocal` is a zero-configuration local development reverse proxy, HTTP tap, and dev process manager designed for human developers and autonomous AI coding agents (Claude Code, OpenAI Codex, Gemini CLI, Cursor). Because local development environments handle sensitive credentials, source code, and listening sockets, `lazylocal` implements defense-in-depth security guardrails.

This document describes the security architecture, threat model, protective controls, and the automated security regression test suite.

---

## 1. Loopback-Only Binding (`127.0.0.1`)

### Threat Model
Binding developer proxies or dashboards to `0.0.0.0` or public network interfaces exposes internal development servers, unauthenticated APIs, and source trees to local area networks (LANs), public Wi-Fi networks, and container overlay networks.

### Protective Controls
- **Exclusive Loopback Listener**: `lazylocal` binds exclusively to `127.0.0.1:<port>` via `net.Listen("tcp", "127.0.0.1:port")`.
- **Zero Remote Exposure**: It never binds to `0.0.0.0` or external network adapters.
- **Unprivileged Port**: Default port is `:7080`, requiring no root, `sudo`, or Administrator privileges.

---

## 2. DNS Rebinding Protection (`HTTP 421 Misdirected Request`)

### Threat Model
In a DNS rebinding attack, an attacker controls a public domain (e.g. `attacker.com`) with a low TTL. When a developer visits the attacker's website, the attacker's DNS server resolves initially to their external IP, and shortly thereafter rebinds the domain to `127.0.0.1`. The victim's browser can then make arbitrary HTTP requests to `127.0.0.1` bypassing standard same-origin policies.

### Protective Controls
- **Strict Host Validation in Reverse Proxy**: `internal/proxy/proxy.go` inspects the normalized `Host` header of every incoming request. If the host is not `localhost` or does not end with `.localhost` (RFC 6761), the request is rejected immediately with:
  ```http
  HTTP/421 Misdirected Request
  Misdirected Request: only .localhost domains allowed
  ```
- **Strict Host Validation in Web Hub**: `internal/web/server.go` enforces an allowlist (`hub.localhost`, `apps.localhost`, `localhost`, `127.0.0.1`, `[::1]`). Any request targeting unauthorized domains (such as `evil.com` or arbitrary subdomains) is blocked with `421 Misdirected Request`.

---

## 3. Subdomain-Isolated Constant-Time CSRF Protection

### Threat Model
Since modern browsers resolve all `*.localhost` domains to `127.0.0.1`, an untrusted web application running on another local port or subdomain (e.g., `http://untrusted-app.localhost:7080` or `http://malicious.localhost:3000`) could attempt cross-origin requests against `lazylocal`'s management endpoints to terminate processes or execute replays.

### Protective Controls
- **Dedicated Management Subdomains**: The Web Hub runs exclusively on `hub.localhost` and `apps.localhost`.
- **Strict `Origin` Validation**: Mutating endpoints (`/api/kill`, `/api/replay`, `/api/sweep-zombies`) verify that the incoming `Origin` header matches `hub.localhost` or `apps.localhost`. Requests originating from other subdomains (e.g., `http://otherapp.localhost:7080`) are rejected with `HTTP 403 Forbidden`.
- **Constant-Time CSRF Tokens**: Destructive actions require a cryptographically secure random token passed in the `X-CSRF-Token` header. The token is verified using constant-time comparison (`crypto/subtle.ConstantTimeCompare`) to resist timing side-channel attacks.
- **Zero Token Leakage**: The CSRF token is injected directly into the HTML index response and is **never** broadcasted over the public Server-Sent Events stream (`/api/events`).
- **Defensive HTTP Headers**: Every response from the Web Hub includes:
  * `X-Frame-Options: DENY` (prevents clickjacking via `<iframe>`)
  * `Content-Security-Policy: frame-ancestors 'none'`
  * `X-Content-Type-Options: nosniff`

---

## 4. Sensitive Header Masking & `--redact-memory` Mode

### Threat Model
HTTP traffic passing through local proxies frequently contains bearer tokens, session cookies, and API keys. Displaying these credentials in terminal UIs or web dashboards exposes them to shoulder-surfing, screencasts, and log collection. Additionally, retaining unmasked credentials in process RAM creates a risk of secret extraction via memory dumps or swap inspection.

### Protective Controls
- **Display Masking**: In all UI views (TUI Inspector, Web Hub traffic inspector, JSON API outputs, and SSE event broadcasts), sensitive headers are automatically replaced with `"REDACTED"`:
  * `Authorization`
  * `Cookie`
  * `Set-Cookie`
  * `X-Api-Key`
  * `Proxy-Authorization`
- **Default In-Memory Retention**: By default, unmasked headers are retained in the ring buffer solely to enable 1-key replay (`r`) without requiring the developer to re-authenticate manually.
- **Zero-Secret RAM Retention (`--redact-memory`)**:
  When launched with the `--redact-memory` flag:
  ```sh
  lazylocal --redact-memory
  ```
  `lazylocal` sanitizes `RawReqHeaders` in memory immediately upon capture. Sensitive credentials are wiped from RAM, ensuring zero sensitive tokens reside in memory buffers. When a replay is triggered under `--redact-memory`, the replay request transmits `"REDACTED"` rather than the raw secret.

---

## 5. Safe HTTP Replay Considerations

### Threat Model
The instant replay feature (`r` key in TUI or Replay button in Web Hub) re-executes captured HTTP requests against the local backend service.

### Operational Safety Guidelines
- **Idempotent Requests (`GET`, `HEAD`, `OPTIONS`)**: Inherently safe to replay repeatedly during debugging.
- **Non-Idempotent Requests (`POST`, `PUT`, `PATCH`, `DELETE`)**: Replaying stateful requests may cause secondary mutations in backend databases (e.g., duplicate orders, duplicate Stripe charges if pointing to live test keys, or unintended record deletions).
- **Recommendation**: Ensure your local backend APIs utilize idempotency keys (`Idempotency-Key`) or mock external payment and webhook providers during development.

---

## 6. Safe Process Termination & Zombie Sweep Guardrails

### Threat Model
Autonomous zombie hunters and process termination tools can accidentally kill unrelated user processes, system services, or the parent AI agent shell running the development session.

### Protective Controls
- **Deterministic Heuristic Classification**: Processes are only classified as zombies under 4 strict criteria:
  1. `deleted_cwd`: Process current working directory has been deleted on disk.
  2. `orphan_ppid`: Parent process is init/launchd (`PPID == 1`) and the process command matches known dev runtimes (`node`, `vite`, `python`, `go`). System daemons (e.g., `rapportd`) are explicitly exempt.
  3. `duplicate_worktree`: Multiple instances serve the identical git repo, branch, and directory; the preferred/lowest port is kept alive and only the redundant twin is flagged.
  4. `idle_traffic`: Dead server with no HTTP traffic for over 30 minutes. Pinned services are never marked idle.
- **UID Match Enforcement**: `lazylocal` only targets processes belonging to the current user (`UID == os.Getuid()`). It strictly refuses to terminate processes owned by other users or `root`.
- **Self-Protection**: `lazylocal` explicitly checks its own PID (`os.Getpid()`) and refuses to terminate itself.
- **Parent AI Agent Shell Protection**:
  Before issuing process group signals, `lazylocal` verifies `PGID == PID`. If the target process is not the process group leader (for example, a dev command running in the same process group as Claude Code, Cursor, or a bash terminal), `lazylocal` signals **only** the target child PID, preserving the parent AI agent session and terminal shell.
- **Graceful Escalation**:
  * **Unix**: Sends `SIGTERM`, waits a graceful grace period (500ms), and only escalates to `SIGKILL` if the process fails to exit.
  * **Windows**: Executes `taskkill /PID <pid> /T`, waits gracefully, and escalates to `taskkill /F /PID <pid> /T` with fallback to `Process.Kill()`.
- **Dry-Run Auditing**: Developers and agents can run `lazylocal sweep --dry-run` to preview candidate processes before any kill signals are issued.

---

## 7. Terminal Escape Sequence Sanitization (`sanitizeTerm`)

### Threat Model
Untrusted HTTP traffic captured by the proxy (URLs, headers, JSON payloads) could contain malicious ANSI escape sequences, OSC commands (e.g., OSC 52 clipboard hijacking, OSC 0 window title tampering), or CSI cursor repositioning sequences. When rendered in a terminal emulator, these could execute escape sequence injection attacks or alter terminal display state.

### Protective Controls
- **`sanitizeTerm` Pipeline**: Before rendering captured HTTP exchanges in the TUI, text strings are passed through `sanitizeTerm`.
- **Sequence Stripping**: Control characters, raw escape sequences (`\x1b]52`), and non-printable bytes are escaped to printable strings (e.g. `\x1b]52`), preventing terminal hijacking and clipboard manipulation.

---

## 8. Security Regression Test Suite

All security controls are continuously verified in CI via automated Go regression tests:

| Security Control | Test Function | Test File | Description |
| :--- | :--- | :--- | :--- |
| **DNS Rebinding Protection (Proxy)** | `TestProxyDNSRebindingProtection` | `internal/proxy/proxy_test.go` | Rejects non-`.localhost` domains with `HTTP 421 Misdirected Request` |
| **Header Masking & Replay** | `TestProxyHeaderMaskingAndReplay` | `internal/proxy/proxy_test.go` | Verifies UI masking and faithful replay preservation |
| **Zero-Secret RAM Retention** | `TestProxyRedactMemory` | `internal/proxy/proxy_test.go` | Verifies in-memory redaction and sanitized replay under `--redact-memory` |
| **Security Headers & Cross-Subdomain CSRF** | `TestServerSecurityHeadersAndCSRF` | `internal/web/server_test.go` | Enforces origin validation, constant-time CSRF tokens, and security headers |
| **DNS Rebinding Protection (Web Hub)** | `TestServerDNSRebindingProtection` | `internal/web/server_test.go` | Verifies `HTTP 421` on foreign Host headers and `frame-ancestors 'none'` |
| **Terminal ANSI Sanitization** | `TestTUI_ANSISanitization` | `internal/tui/tui_test.go` | Prevents OSC 52 and terminal escape sequence injection |
| **Zombie Process Classifier** | `TestZombieClassifier` | `internal/discover/discover_test.go` | Verifies deterministic heuristic detection and twin-conflict resolution |

To run the full security test suite:
```sh
go test -v -race -run "^(TestProxyDNSRebindingProtection|TestProxyHeaderMaskingAndReplay|TestProxyRedactMemory|TestServerSecurityHeadersAndCSRF|TestServerDNSRebindingProtection|TestTUI_ANSISanitization|TestZombieClassifier)$" ./...
```
