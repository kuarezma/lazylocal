# Security Architecture & Threat Model

[![CI](https://github.com/kuarezma/lazylocal/actions/workflows/ci.yml/badge.svg)](https://github.com/kuarezma/lazylocal/actions/workflows/ci.yml)

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

## 4. In-Memory Header & Body Redaction (`--redact-memory`, default) and Zero-Body Mode (`--no-body`)

### Threat Model
HTTP traffic passing through local proxies frequently contains bearer tokens, session cookies, passwords, and API keys in headers and request/response bodies. Displaying these credentials in terminal UIs or web dashboards exposes them to shoulder-surfing, screencasts, and log collection. Additionally, retaining unmasked credentials in process RAM creates a risk of secret extraction via memory dumps, process inspection, or swap files.

### Protective Controls
- **Display Masking**: In all UI views (TUI Inspector, Web Hub traffic inspector, JSON API outputs, and SSE event broadcasts), sensitive headers are automatically replaced with `"REDACTED"`:
  * `Authorization`
  * `Cookie`
  * `Set-Cookie`
  * `X-Api-Key`
  * `Proxy-Authorization`
- **Default-On In-Memory Redaction (`--redact-memory` enabled by default)**:
  By default out-of-the-box (`--redact-memory=true`), `lazylocal` scrubs sensitive credentials immediately upon capture from all in-memory buffers (`RawReqHeaders`, `Req.RawHeaders`, `Req.Body`, `Resp.Body`, `ReqBody`, and `RespBody`).
- **Transient Buffer Zeroing (`clear()`)**:
  When body capture is active, `captureReader` buffers streamed payload bytes up to 64 KB. Immediately after taking a snapshot and executing redaction, `recordExchange` calls `reqCapture.zero()` and `rc.zero()`, locking the buffer, calling `clear(buf.Bytes())` to zero out all byte slices, and resetting buffer length so unredacted transient bytes do not linger in heap memory.
- **Zero-Body Mode (`--no-body`)**:
  For environments processing highly sensitive data or production PII where buffering bodies in memory is prohibited, passing `--no-body` bypasses `captureReader` entirely, capturing zero request or response body bytes into memory (`len(ex.Req.Body) == 0` and `len(ex.Resp.Body) == 0`) while allowing live HTTP traffic to stream through unmodified.
- **Opt-In Raw Secrets Retention (`--allow-raw-secrets`)**:
  Developers who explicitly need unredacted token re-transmission during instant replay can opt-in using `--allow-raw-secrets` (or `--redact-memory=false`):
  ```sh
  lazylocal --allow-raw-secrets
  ```
- **Automatic Sensitive Body Field & Value Pattern Redaction**:
  Whenever `--redact-memory` is active (default), `redactSensitiveBody` parses and sanitizes request and response bodies:
  * **Protected Keys**: Any JSON key or form-urlencoded field name matching (case-insensitively, ignoring hyphens and underscores): `password`, `passwd`, `pass`, `secret`, `client_secret`, `token`, `access_token`, `refresh_token`, `id_token`, `api_key`, `apikey`, `private_key`, `authorization`, `credit_card`, `card_number`, `cvv`, `ssn`.
  * **Deep JSON Traversal**: Valid JSON structures (objects and nested arrays) are recursively walked, replacing matched sensitive values with `"REDACTED"` while preserving surrounding non-sensitive fields.
  * **Secret Value Pattern Redaction**: Token value patterns are scrubbed even under custom/unknown key names or in free-text and plain-text responses:
    - JWT tokens (`eyJ...`)
    - API key formats (OpenAI `sk-...`, GitHub `ghp_...`/`gho_...`/`github_pat_...`, Slack `xox...`, AWS `AKIA...`, Google `AIza...`)
    - Bearer and Basic authentication headers in text (`Bearer ...`, `Basic ...`)
    - PEM private keys (`-----BEGIN ... PRIVATE KEY-----`)
  * **Fast-Path Screening (`mayContainSecrets`)**: Binary payloads and large files that lack key-value separators (`:`, `=`) and known credential token prefixes are fast-pathed in microseconds without regex overhead.
  * **Regex Fallback for Form-URLencoded & Truncated Payloads**: Form-urlencoded bodies (`application/x-www-form-urlencoded`) and truncated JSON payloads are sanitized via regex fallback, replacing sensitive values with `REDACTED` or `"REDACTED"`.
  * **Stream Preservation**: Proxied live HTTP traffic between client and server is transmitted unmodified; only the ring buffer and TUI/Web inspection copies are scrubbed in RAM.

---

## 5. Non-Idempotent HTTP Replay Protection (`POST`, `PUT`, `PATCH`, `DELETE`)

### Threat Model
The instant replay feature (`r` key in TUI or Replay button in Web Hub) re-executes captured HTTP requests against the local backend service. Replaying stateful non-idempotent requests (`POST`, `PUT`, `PATCH`, `DELETE`) without confirmation can trigger unintended database mutations, duplicate financial transactions, or secondary deletions.

### Protective Controls
- **Method Classification**: `IsNonIdempotentMethod` identifies stateful HTTP methods (`POST`, `PUT`, `PATCH`, `DELETE`). Idempotent requests (`GET`, `HEAD`, `OPTIONS`) remain replayable instantly without interruption.
- **Web API Confirmation Gate (`409 Conflict`)**:
  In `/api/replay`, replaying any non-idempotent exchange requires explicit confirmation via `?confirm=true` query parameter or `X-Confirm-Replay: true` header. Requests lacking this flag are rejected with `HTTP 409 Conflict`:
  ```json
  {
    "error": "non-idempotent replay requires confirmation",
    "requires_confirmation": true,
    "method": "POST"
  }
  ```
- **Interactive TUI Confirmation Flow**:
  In the TUI, pressing `r` on a `POST`, `PUT`, `PATCH`, or `DELETE` exchange stages the exchange in `pendingConfirmExchangeID` and prompts:
  `"Confirm replay of [METHOD] [PATH]? Press 'y' or 'r' again to confirm, any other key to cancel"`
  Pressing `y` or `r` a second time executes the replay; any other key cancels immediately.
- **Web UI Confirmation Dialog**:
  In the Web Hub, clicking Replay on a stateful method presents a confirmation dialog (`window.confirm`) explaining the potential mutation before transmitting `&confirm=true` and `X-Confirm-Replay: true`.

---

## 6. Safe Process Termination, Preview Mode & TOCTOU Protection

### Threat Model
Autonomous zombie process hunters and termination tools could accidentally terminate unrelated user processes, system daemons, or parent AI coding agent shells if process IDs (PIDs) are reused by the OS (Time-of-Check to Time-of-Use / TOCTOU race conditions).

### Protective Controls
- **Safe-by-Default Preview Mode for `lazylocal sweep`**:
  Running `lazylocal sweep` without `--yes` (or `-y`) runs in safe preview mode: it lists candidate zombie processes, details the heuristic reason, and explicitly informs the user without terminating anything:
  ```text
  Preview mode (no processes killed). Re-run with 'lazylocal sweep --yes' to terminate these processes.
  ```
  Processes are only terminated when `--yes` (or `-y`) is explicitly passed.
- **PID Identity Re-Verification (TOCTOU Race Prevention)**:
  Before sending termination signals (`KillService`), `lazylocal` re-inspects the live running process via OS process tables (`ps -p <pid> -o comm=` on Unix, `tasklist /FI "PID eq <pid>"` on Windows). If the live command does not match `svc.Cmd` / `svc.Command`, termination is aborted immediately with a `PID reuse detected (TOCTOU violation)` error.
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
  * **Unix**: Sends `SIGTERM`, waits a graceful grace period (Default: 3s), and only escalates to `SIGKILL` if the process fails to exit.
  * **Windows**: Executes `taskkill /PID <pid> /T`, waits gracefully, and escalates to `taskkill /F /PID <pid> /T` with fallback to `Process.Kill()`.

---

## 7. Terminal Escape Sequence Sanitization (`sanitizeTerm`)

### Threat Model
Untrusted HTTP traffic captured by the proxy (URLs, headers, JSON payloads) could contain malicious ANSI escape sequences, OSC commands (e.g., OSC 52 clipboard hijacking, OSC 0 window title tampering), or CSI cursor repositioning sequences. When rendered in a terminal emulator, these could execute escape sequence injection attacks or alter terminal display state.

### Protective Controls
- **`sanitizeTerm` Pipeline**: Before rendering captured HTTP exchanges in the TUI, text strings are passed through `sanitizeTerm`.
- **Sequence Stripping**: Control characters, raw escape sequences (`\x1b]52`), and non-printable bytes are escaped to printable strings (e.g. `\x1b]52`), preventing terminal hijacking and clipboard manipulation.

---

## 8. Cryptographic Release Verification & CI Vulnerability Auditing

### Protective Controls
- **Cryptographic SHA-256 Checksum Verification**:
  Official installers (`install.sh` and `install.ps1`) automatically download the signed release `checksums.txt` alongside downloaded binaries and verify the SHA-256 hash before granting executable permissions or moving files to destination PATH directories. If hash calculation fails or mismatches, the temporary binary is immediately deleted and installation aborts.
- **Continuous Automated Vulnerability Auditing (`govulncheck`)**:
  GitHub Actions CI enforces official Go vulnerability scanning via `govulncheck` on Go 1.24, blocking builds if known CVE vulnerabilities exist in transitive dependencies.
- **Race Condition Detection (`-race`)**:
  All unit, integration, and security tests run under the Go race detector in CI across Linux and macOS.

---

## 9. Known Limitations & Residual Risks

While `lazylocal` implements defense-in-depth security layers, users and operators should be aware of the following engineering boundaries:

1. **Heuristic Redaction Boundaries**:
   Field-name matching (`password`, `token`, `secret`, `api_key`, etc.) and token-prefix regexes (`eyJ...`, `sk-...`, `ghp_...`, `AKIA...`, `Bearer ...`, PEM keys) cover standard credentials and common industry formats, but cannot mathematically guarantee detection of custom, unformatted, or proprietary secret formats. Users handling production PII, confidential medical data, or unformatted secrets should run `lazylocal --no-body` to disable body capture in memory completely.

2. **Transient Stream Buffering**:
   When body capture is enabled (up to 64 KB), `captureReader` buffers bytes while reading the stream before `recordExchange` scrubs sensitive fields and zeroes (`clear()`) the temporary buffer. While the memory is zeroed immediately after snapshot and redaction, unredacted bytes exist in the temporary buffer during the transfer window. Using `--no-body` completely bypasses `captureReader` and prevents any body buffering.

3. **Release Checksum Trust Model**:
   Installers (`install.sh` and `install.ps1`) are strictly fail-closed: if `checksums.txt` cannot be downloaded or hash verification fails, installation aborts immediately. Hashes are fetched over HTTPS from GitHub Releases. For higher security environments independent of GitHub release assets, users can pin the hash directly using `EXPECTED_SHA256=<hash>`, or use `go install github.com/kuarezma/lazylocal/cmd/lazylocal@v0.1.0` to verify against the immutable Google Go Checksum Database (`sum.golang.org`) transparency log.

4. **Verified CI & `govulncheck` Evidence**:
   Every commit and pull request triggers GitHub Actions CI verifying `go vet ./...`, `go test -v -race ./...` across Ubuntu Linux, macOS, and Windows on both Go 1.23 and 1.24, alongside official `govulncheck ./...` dependency auditing. Live pipeline results can be audited at [https://github.com/kuarezma/lazylocal/actions/workflows/ci.yml](https://github.com/kuarezma/lazylocal/actions/workflows/ci.yml).

---

## 10. Security Regression Test Suite

All security controls are continuously verified in CI via automated Go regression tests:

| Security Control | Test Function | Test File | Description |
| :--- | :--- | :--- | :--- |
| **DNS Rebinding Protection (Proxy)** | `TestProxyDNSRebindingProtection` | `internal/proxy/proxy_test.go` | Rejects non-`.localhost` domains with `HTTP 421 Misdirected Request` |
| **Header Masking & Replay** | `TestProxyHeaderMaskingAndReplay` | `internal/proxy/proxy_test.go` | Verifies UI masking and faithful replay preservation |
| **In-Memory Header Redaction** | `TestProxyRedactMemory` | `internal/proxy/proxy_test.go` | Verifies default in-memory header redaction under `--redact-memory` |
| **Sensitive Body Field Redaction** | `TestProxyBodyRedaction` | `internal/proxy/proxy_test.go` | Verifies recursive JSON and form-urlencoded body redaction for passwords, API keys, and tokens |
| **Pattern Redaction & Zero-Body Mode** | `TestProxyPatternAndNoBodyRedaction` | `internal/proxy/proxy_test.go` | Verifies secret pattern scrubbing (JWT, sk-, ghp-, Bearer) and --no-body zero capture mode |
| **Non-Idempotent Replay Confirmation** | `TestServerReplayConfirmationGuard` | `internal/web/server_test.go` | Enforces `409 Conflict` on unconfirmed POST/PUT/DELETE replay in Web API |
| **TUI Replay Confirmation Flow** | `TestTUIReplayConfirmation` | `internal/tui/tui_test.go` | Verifies interactive confirmation prompts on POST/PUT/DELETE before dispatching replay |
| **TOCTOU Process Identity Re-Verification** | `TestKillServiceTOCTOUPIDReuseMismatch` | `internal/procs/procs_unix_test.go` | Verifies live PID command check prevents PID reuse race conditions |
| **Security Headers & Cross-Subdomain CSRF** | `TestServerSecurityHeadersAndCSRF` | `internal/web/server_test.go` | Enforces origin validation, constant-time CSRF tokens, and security headers |
| **DNS Rebinding Protection (Web Hub)** | `TestServerDNSRebindingProtection` | `internal/web/server_test.go` | Verifies `HTTP 421` on foreign Host headers and `frame-ancestors 'none'` |
| **Terminal ANSI Sanitization** | `TestTUI_ANSISanitization` | `internal/tui/tui_test.go` | Prevents OSC 52 and terminal escape sequence injection |
| **Zombie Process Classifier** | `TestZombieClassifier` | `internal/discover/discover_test.go` | Verifies deterministic heuristic detection and twin-conflict resolution |

To run the full security test suite:
```sh
go test -v -race -run "^(TestProxyDNSRebindingProtection|TestProxyHeaderMaskingAndReplay|TestProxyRedactMemory|TestProxyBodyRedaction|TestProxyPatternAndNoBodyRedaction|TestServerReplayConfirmationGuard|TestTUIReplayConfirmation|TestKillServiceTOCTOUPIDReuseMismatch|TestServerSecurityHeadersAndCSRF|TestServerDNSRebindingProtection|TestTUI_ANSISanitization|TestZombieClassifier)$" ./...
```
