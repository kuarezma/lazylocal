<div align="center">

```
  _                     _                 _ 
 | |                   | |               | |
 | | __ _ _____   _ ___| | ___   ___ __ _| |
 | |/ _` |_  / | | / __| |/ _ \ / __/ _` | |
 | | (_| |/ /| |_| \__ \ | (_) | (_| (_| | |
 |_|\__,_/___|\__, |___/_|\___/ \___\__,_|_|
               __/ |                        
              |___/   Zero-Config .localhost Dev Proxy
```

# lazylocal

**Stop hunting ports. Start debugging in your browser.**

Zero-config `.localhost` subdomains • In-flight HTTP tap & instant replay • Autonomous zombie process hunter • Built for human developers and multi-agent AI coding loops.

[![CI](https://github.com/kuarezma/lazylocal/actions/workflows/ci.yml/badge.svg)](https://github.com/kuarezma/lazylocal/actions/workflows/ci.yml)
[![Go Report Card](https://goreportcard.com/badge/github.com/kuarezma/lazylocal)](https://goreportcard.com/report/github.com/kuarezma/lazylocal)
[![Go Version](https://img.shields.io/badge/go-%3E%3D1.22-blue.svg)](https://golang.org)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](https://opensource.org/licenses/MIT)
[![npm](https://img.shields.io/npm/v/lazylocal.svg)](https://www.npmjs.com/package/lazylocal)
[![Platform](https://img.shields.io/badge/platform-macOS%20%7C%20Linux%20%7C%20Windows-lightgrey.svg)]()

[English](#-why-lazylocal) • [Türkçe Dokümantasyon](#-türkçe-özet)

</div>

---

## ⚡ 10-Second Quick Start

```sh
# Run instantly with zero install (macOS, Linux, Windows):
npx lazylocal

# Windows (PowerShell one-liner):
irm https://raw.githubusercontent.com/kuarezma/lazylocal/main/install.ps1 | iex

# macOS / Linux (curl installer):
curl -fsSL https://raw.githubusercontent.com/kuarezma/lazylocal/main/install.sh | sh

# Or install via Homebrew (macOS & Linux):
brew install kuarezma/tap/lazylocal

# Or with Go:
go install github.com/kuarezma/lazylocal/cmd/lazylocal@latest
```

Open your browser to **`http://hub.localhost:7080`** or view your terminal for the interactive ANSI TUI!

---

## 💡 Why lazylocal?

Modern web development is a port nightmare:
- Next.js grabs `:3000`, Vite grabs `:5173`, FastAPI grabs `:8000`, and Tauri grabs `:1420`.
- AI coding agents (Claude Code, Codex, Gemini, Cursor) spawn background servers in parallel git worktrees on unpredictable ports.
- Terminal tabs get closed, leaving orphaned processes consuming RAM and throwing `EADDRINUSE`.
- Testing a webhook or a failed API call requires manually writing complex `curl` commands or clicking around web apps.

**`lazylocal` solves this forever in a single zero-dependency binary (<12 MB):**

```text
┌─ ⚡ lazylocal ── 3 active ── 🤖 claude/codex ── 🧟 1 zombie ──── http://hub.localhost ─┐
├─ Services (3) ───────────────┬─ Traffic (14) ────────────────────────────────────────┤
│ ❯ ● shop.localhost -> :3000  │ ❯ POST 500  42ms /checkout                           │
│   ● doc-center.localhost     │   GET  200  12ms /api/items                           │
│   ○ matematik.localhost [PIN]│   GET  200   4ms /favicon.ico                         │
│   ● [ZOMBIE: deleted_cwd]    ├─ Inspector [POST /checkout -> 500] ───────────────────┤
│                              │   Host: shop.localhost:3000  Started: 04:32:11.450    │
│                              │   Req Body: {"cart_id": "test_cart_42", "qty": 1}     │
│                              │   Resp Body: {"error": "checkout transaction failed"} │
├──────────────────────────────┴───────────────────────────────────────────────────────┤
└─ [j/k] Move  [Tab] Pane  [r] Replay  [c] cURL  [d] Diff  [K] Kill  [Z] Sweep  [q] Quit ─┘
```

---

## 🚀 4 Killer Features

### 1. Zero-Config `.localhost` Subdomains (RFC 6761)
macOS, modern Linux, and Windows (Windows 10/11) resolve `*.localhost` to `127.0.0.1` natively.
- No editing `/etc/hosts` or `C:\Windows\System32\drivers\etc\hosts`
- No `sudo` or Administrator permissions required (runs on unprivileged `:7080`)
- No local DNS servers or dnsmasq to configure
- Native platform scanner: `lsof` (macOS), `/proc/net/tcp` (Linux), and `netstat -ano -p tcp` + `tasklist` (Windows)
- Automatic Git repository and branch naming:
  * Repo on `main`: `http://shop.localhost:7080`
  * Worktree on `feat-auth`: `http://feat-auth.shop.localhost:7080`

### 2. In-Flight HTTP Tap & 1-Key Replay (`r`)
Every request and response routed through `lazylocal` is captured in a 500-entry in-memory ring buffer.
- Sensitive headers (`Authorization`, `Cookie`, `X-Api-Key`) are redacted in UI views.
- Original raw headers and request bodies are preserved in memory for faithful replay.
- Press **`r`** in the TUI (or click Replay in the Web Hub) to immediately re-fire the exact request after updating your code.
- Press **`d`** to view a live diff between the original failed response (500) and the replayed fix (200 OK).

### 3. Autonomous Zombie Dev Process Cleaner (`Z`)
Background dev servers left behind by closed terminal tabs or deleted git worktrees waste system memory and block ports.
`lazylocal` classifies zombies using 4 deterministic heuristic rules:
1. `deleted_cwd`: Working directory was deleted (e.g. branch worktree removed).
2. `orphan_ppid`: Parent process terminated (`PPID=1`) and executable is a dev runner (`node`, `vite`, `python`, `go`).
3. `duplicate_worktree`: Multiple instances serving the same repo, branch, and directory.
4. `idle_traffic`: Dead server with no HTTP traffic.

Press **`Z`** to sweep all zombies in 1 second.
- On macOS & Linux: issues `SIGTERM`, waits gracefully, and escalates to `SIGKILL` only if unresponsive.
- On Windows: gracefully executes `taskkill /PID <pid> /T`, waits gracefully, and escalates to `taskkill /F /PID <pid> /T` with fallback to `Process.Kill()`.

### 4. Deterministic Multi-Agent URL Routing (`lazylocal url`)
When coordinating AI agents (Claude Code, OpenAI Codex, Gemini, Cursor) across multiple worktrees, pass `lazylocal url`:
```sh
$ lazylocal url
http://feat-auth.shop.localhost:7080

$ lazylocal url --json
{
  "service": "feat-auth.shop",
  "host": "feat-auth.shop.localhost",
  "port": 7080,
  "url": "http://feat-auth.shop.localhost:7080",
  "repo": "shop",
  "branch": "feat-auth"
}
```
Agents never collide over ports or overwrite each other's state.

---

## 📊 Comparison Matrix

| Feature | `lazylocal` | `portless` | `lsport` / `lsof` | `traefik` |
| :--- | :---: | :---: | :---: | :---: |
| **Zero-Config `.localhost`** | **Native RFC 6761** | Needs custom DNS | ❌ None | ❌ Complex config |
| **No Root / No `sudo`** | **Yes (`:7080`)** | ❌ Needs root/:80 | **Yes** | ❌ Root / Docker |
| **Git Branch / Worktree Identity** | **Automatic** | ❌ Port numbers only | ❌ None | ❌ Manual |
| **AI Agent Detection (Claude/Codex/Cursor)** | **Automatic** | ❌ None | ❌ None | ❌ None |
| **HTTP Tap & Instant Replay (`r`)** | **Built-in Ring Buffer** | ❌ None | ❌ None | ⚠️ Plugin required |
| **Live Response Diff View (`d`)** | **Built-in** | ❌ None | ❌ None | ❌ None |
| **Zombie Process Sweep (`Z`)** | **1-Key SIGTERM/KILL** | ❌ None | ❌ Manual `kill -9` | ❌ None |
| **Dual Interface (TUI + Web Hub)** | **Both included** | ❌ CLI only | ❌ CLI only | ⚠️ Web UI only |
| **Single Static Binary** | **< 12 MB (Go stdlib)** | ❌ Node.js runtime | ❌ Shell script | ❌ > 100 MB |
| **External Dependencies** | **0 (Zero)** | 15+ npm packages | 0 | Dozens |

---

## 📦 Installation

### Option 1: Zero-Install via NPX (macOS, Linux, Windows)
```sh
npx lazylocal
```

### Option 2: Windows PowerShell One-Liner
```powershell
irm https://raw.githubusercontent.com/kuarezma/lazylocal/main/install.ps1 | iex
```

### Option 3: Homebrew (macOS & Linux)
```sh
brew install kuarezma/tap/lazylocal
```

### Option 4: Go Install
```sh
go install github.com/kuarezma/lazylocal/cmd/lazylocal@latest
```

### Option 5: Standalone Binary (GitHub Releases)
Download precompiled binaries for macOS (Apple Silicon / Intel), Linux (x86_64 / arm64), and Windows (amd64 / arm64) from [Releases](https://github.com/kuarezma/lazylocal/releases).

**macOS / Linux:**
```sh
curl -fsSL https://raw.githubusercontent.com/kuarezma/lazylocal/main/install.sh | sh
```

---

## 🛠️ CLI Usage & Flags

```sh
# Start lazylocal reverse proxy and interactive TUI (default):
lazylocal

# Start in background headless mode (ideal for CI, servers, background daemons):
lazylocal --headless --port 7080

# List discovered services as a formatted table or JSON:
lazylocal ls
lazylocal ls --json

# Print deterministic .localhost URL for current git branch:
lazylocal url
lazylocal url --json

# Sweep zombie processes (defaults to safe preview; terminate with --yes):
lazylocal sweep
lazylocal sweep --yes
lazylocal sweep --dry-run

# Print version:
lazylocal version
```

### Command Flags:
- `--port <int>`: Reverse proxy and Web Hub loopback port (default: `7080`).
- `--pins <path>`: Path to custom pinned services configuration (default: `pins.toml`).
- `--interval <dur>`: Background scanner polling interval (default: `2s`).
- `--headless`: Run in background headless mode without launching the TUI.
- `--no-body`: Disable capturing HTTP request and response bodies in memory.
- `--redact-memory`: Scrub sensitive headers and bodies from in-memory replay buffers (default: `true`).
- `--allow-raw-secrets`: Allow storing unredacted secrets in memory for faithful replay (disables redact-memory).
- `--version`: Print version information and exit.

---

## 🌐 Web Hub Dashboard (`http://hub.localhost:7080`)

`lazylocal` embeds a fast, responsive single-page web dashboard directly inside the binary:
- **Real-Time SSE Stream**: Automatically updates when services appear, crash, or change.
- **Service Cards**: Shows memory, CPU, port, process ID, Git branch, and detected AI agent badge.
- **Traffic Log**: View method, status code, duration, and inspect request/response payloads.
- **1-Click Replay & Process Kill**: Terminate misbehaving servers or replay requests with constant-time CSRF token verification.

---

## 🛡️ Security Architecture

For complete threat models, architecture diagrams, and the automated regression suite, see [**SECURITY.md**](SECURITY.md).

1. **Strict Loopback Binding (`127.0.0.1`)**:
   `lazylocal` binds exclusively to `127.0.0.1`. It never listens on `0.0.0.0` or public network interfaces.
2. **DNS Rebinding Protection (HTTP 421)**:
   The reverse proxy strictly validates incoming `Host` headers. Any request targeting external domains (e.g., `evil.com`) is immediately rejected with `421 Misdirected Request`.
3. **Subdomain-Isolated Constant-Time CSRF Tokens & Origin Allowlist**:
   Destructive API endpoints (`/api/kill`, `/api/replay`, `/api/sweep-zombies`) enforce constant-time CSRF token comparison (`crypto/subtle`) and validate that incoming `Origin` headers strictly match loopback or `*.localhost`.
4. **In-Memory Header & Body Redaction (`--redact-memory`, default) and Zero-Body Mode (`--no-body`)**:
   Sensitive headers (`Authorization`, `Cookie`, `X-Api-Key`, etc.) and sensitive body fields (`password`, `token`, `secret`, `api_key`, `cvv`, etc. in JSON and form-urlencoded payloads, as well as secret token patterns like JWTs, OpenAI `sk-...`, GitHub `ghp_...`, and Bearer tokens) are scrubbed to `"REDACTED"` in memory by default. Transient buffers are zeroed (`clear()`) after snapshot. Users handling production PII or unstructured secrets can disable body capture entirely via `--no-body`. Opt-out for faithful raw secrets replay is available via `--allow-raw-secrets`.
5. **Non-Idempotent Replay Confirmation Guard**:
   Stateful methods (`POST`, `PUT`, `PATCH`, `DELETE`) require explicit user confirmation via Web UI dialog, TUI interactive prompt, or `?confirm=true` / `X-Confirm-Replay: true` header (`HTTP 409 Conflict` guard).
6. **Safe Sweep Preview Mode & TOCTOU Protection**:
   `lazylocal sweep` defaults to preview mode without killing processes unless `--yes` / `-y` is provided. Process termination validates live PID command names before signaling, preventing PID reuse race conditions.
7. **AI Agent Shell Protection & Graceful Zombie Sweep**:
   Process termination checks `PGID == PID` before signaling process groups, protecting parent AI agent shells (Claude Code, Cursor, Codex).
8. **Cryptographic SHA-256 Installers & CI `govulncheck`**:
   `install.sh` and `install.ps1` cryptographically verify release SHA-256 hashes against `checksums.txt`, and GitHub Actions CI enforces `govulncheck` vulnerability scanning on every push.
9. **Terminal Escape Sanitization**:
   Untrusted HTTP headers and paths are stripped of ANSI/OSC/CSI control sequences to prevent terminal emulator injection.

---

## ⚡ Optional: Clean URLs on Port 80 (macOS Packet Filter)

To omit `:7080` and use clean URLs like `http://shop.localhost/` without running `lazylocal` as root:

```sh
# Forward port 80 to 7080 on loopback (one-time command):
echo "rdr pass on lo0 inet proto tcp from any to 127.0.0.1 port 80 -> 127.0.0.1 port 7080" | sudo pfctl -ef -

# To remove the rule later:
sudo pfctl -F all -f /etc/pf.conf
```

---

## 🇹🇷 Türkçe Özet

### lazylocal Nedir?
**`lazylocal`**, yerel geliştirme süreçlerinde port arama ve çakışma sorunlarını kökten çözen, sıfır bağımlılıklı (`Go stdlib`), yüksek performanslı bir ters proxy ve geliştirici araç setidir.

### Öne Çıkan Yetenekler
1. **Sıfır Yapılandırma `.localhost` Alt Alan Adları**:
   macOS, Linux ve Windows (10/11) üzerinde `/etc/hosts` düzenlemeden veya `sudo` / Yönetici izni gerekmeden her servis otomatik olarak `<dal>.<proje>.localhost:7080` adresini alır. Windows üzerinde `netstat -ano -p tcp` ve `tasklist` ile yerel süreçler sıfır yapılandırmayla taranır.
2. **Trafik Yakalama & Anında Tekrar (`r`)**:
   Tüm HTTP istekleri ve yanıtları yakalanır. Terminal TUI'de **`r`** tuşuna basılarak hatalı (500) istekler orijinal gövde ve başlıklarla tekrar çalıştırılır; **`d`** ile yanıt farkı (diff) anlık incelenir.
3. **Otonom Zombi Süreç Temizleyici (`Z`)**:
   Kapanmış terminal sekmelerinden veya silinmiş Git worktree'lerinden arta kalan yetim Node/Python/Go geliştirici süreçlerini tespit eder. Unix'te `SIGTERM`/`SIGKILL`, Windows'ta `taskkill /PID /T` ve `taskkill /F /PID /T` süreç ağacı sonlandırması ile tek hamlede temizler.
4. **Yapay Zeka Ajan Desteği (`lazylocal url`)**:
   Claude Code, Codex, Gemini ve Cursor gibi araçların aynı port için çakışmasını engeller; her çalışma alanına deterministik adres sağlar.
5. **İki Panel TUI & Web Hub Panosu (`hub.localhost`)**:
   Terminalden çıkmadan ANSI TUI ile veya tarayıcıdan modern Web Hub ile tam kontrol.

---

## 📄 License

MIT License © 2026 Uğur Yaşayan. See [LICENSE](LICENSE) for details.
