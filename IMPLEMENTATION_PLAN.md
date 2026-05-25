# Implementation Plan

## Overview

A single-binary Go application (stdlib only) that receives GitHub push webhooks, validates them, and forwards sanitized requests to an internal doco-cd instance.

```
GitHub ──webhook──▶ proxy ──new request──▶ doco-cd (internal network)
```

## Configuration

All configuration via environment variables. Secrets support `_FILE` suffix variants for Docker secrets (e.g. `GITHUB_WEBHOOK_SECRET_FILE=/run/secrets/github_secret`).

| Variable | Required | Default | Description |
|---|---|---|---|
| `GITHUB_WEBHOOK_SECRET` | yes | | Secret shared with GitHub for incoming signature validation |
| `DOCO_CD_WEBHOOK_SECRET` | yes | | Secret for signing outgoing requests to doco-cd |
| `DOCO_CD_URL` | yes | | Internal doco-cd URL (e.g. `http://doco-cd:80`) |
| `ALLOWED_REPOS` | yes | | Comma-separated repo full names (e.g. `org/repo1,org/repo2`) |
| `LISTEN_ADDR` | no | `:8080` | Address to listen on |
| `TRUSTED_PROXY_CIDRS` | no | | Comma-separated CIDRs of trusted reverse proxies |
| `GITHUB_META_REFRESH_INTERVAL` | no | `1h` | How often to refresh GitHub IP ranges |

### Startup validation

- Fail if any required variable is missing or empty
- Fail if `ALLOWED_REPOS` is empty after parsing
- Warn if `GITHUB_WEBHOOK_SECRET` and `DOCO_CD_WEBHOOK_SECRET` are identical
- Canonicalize `ALLOWED_REPOS` entries to lowercase and trim whitespace
- Parse and validate `TRUSTED_PROXY_CIDRS` if provided

## Request Processing Pipeline

### 1. HTTP hardening

- Accept only `POST` method on `/webhook` endpoint
- Require `Content-Type: application/json`
- Enforce max body size (1 MB)
- Return generic error responses (no internal details leaked)

### 2. Source IP validation

- Extract client IP from `RemoteAddr`
- If `RemoteAddr` is within `TRUSTED_PROXY_CIDRS`, use the last untrusted IP from `X-Forwarded-For` instead
- Check client IP against GitHub's `hooks` CIDRs (both IPv4 and IPv6)
- Reject if not matched

### 3. Event type routing

- Read `X-GitHub-Event` header
- `ping` → return `200 OK` immediately (GitHub sends this on webhook setup), do not forward
- `push` → continue pipeline
- Anything else → return `200 OK`, do not forward

### 4. HMAC signature verification

- Require exactly one `X-Hub-Signature-256` header
- Require `sha256=` prefix followed by valid hex digest
- Verify against raw request body bytes using `hmac.Equal` (constant-time comparison)
- Only accept SHA-256, not legacy SHA-1

### 5. Payload parsing and validation

- Parse JSON body into a strict struct containing only:
  - `ref` (string)
  - `before` (string)
  - `after` (string)
  - `repository.name` (string)
  - `repository.full_name` (string)
  - `repository.clone_url` (string)
  - `pusher.name` (string)
  - `pusher.email` (string)
- Reject if required fields are missing or empty

### 6. Repository allowlist check

- Lowercase `repository.full_name` from payload
- Check against canonicalized allowlist
- Reject if not matched

### 7. Construct and send request to doco-cd

- Marshal a new JSON payload from the validated fields only
- Compute HMAC-SHA256 over the marshaled bytes using `DOCO_CD_WEBHOOK_SECRET`
- Send `POST` to `{DOCO_CD_URL}/v1/webhook` with:
  - `Content-Type: application/json`
  - `X-GitHub-Event: push`
  - `X-Hub-Signature-256: sha256={computed}`
- Use a dedicated `http.Client` with:
  - Timeout (e.g. 15s)
  - No redirect following (`CheckRedirect` returns error)

### 8. Response handling

- Forward doco-cd's HTTP status code back to the caller
- Do not forward response headers or body from doco-cd

## GitHub IP Range Management

- Fetch `https://api.github.com/meta` on startup — fail if unreachable or unparseable
- Parse only the `hooks` key (array of CIDR strings)
- Support both IPv4 and IPv6 CIDRs
- Refresh periodically (default 1h) in a background goroutine
- On refresh failure: log error, retain last-known-good ranges
- Use `ETag`/`If-None-Match` headers for efficient polling

## HTTP Server Configuration

- `ReadHeaderTimeout`: 5s
- `ReadTimeout`: 10s
- `WriteTimeout`: 30s
- `IdleTimeout`: 60s
- `MaxHeaderBytes`: 1 MB
- Graceful shutdown on `SIGINT`/`SIGTERM`

## Logging

- Structured log output using `log/slog`
- Log on: startup config (without secrets), IP validation failures, signature failures, allowlist rejections, forwarding results, GitHub meta refresh events
- No sensitive data in logs (no secrets, no full payloads)

## Development Method

This project follows **test-driven development (TDD)**. For each unit of functionality:

1. Write a failing test that defines the expected behavior
2. Write the minimal implementation to make the test pass
3. Refactor while keeping tests green

Every source file has a corresponding `_test.go` file. Tests use only the standard library `testing` package and `net/http/httptest` — no external test frameworks.

### Session workflow

Each Claude Code session implements exactly **one** item from the TODO list below. Do not continue to the next item in the same session. The workflow per session is:

1. Identify the next unchecked item in the TODO list
2. Implement it following TDD (tests first, then implementation)
3. Run `go test ./... -cover` and `go vet ./...` to verify
4. Update the TODO checkboxes in this file to mark completed items
5. Run `/codex:review --background` for a code review
6. The user will then clear the session and start fresh for the next item

### Test strategy by component

| Component | Test approach |
|---|---|
| `config` | Table-driven tests with env var manipulation. Cover required/optional/missing/invalid/`_FILE` variants, identical secret warning, allowlist canonicalization. |
| `signature` | Verify HMAC computation and constant-time comparison. Test valid signatures, wrong signatures, missing header, malformed header, SHA-1 rejection. |
| `ipcheck` | Mock HTTP server returning GitHub `/meta` JSON. Test CIDR parsing (IPv4 + IPv6), IP matching, trusted proxy + `X-Forwarded-For` extraction, refresh with last-known-good fallback, startup failure on unreachable endpoint. |
| `payload` | Parse valid/invalid/incomplete JSON payloads. Verify only expected fields are extracted. Test output payload construction and JSON marshaling. |
| `proxy` | `httptest.Server` as mock doco-cd. Verify outgoing request has correct path, headers, signature, and reconstructed body. Test timeout and non-2xx handling. |
| `handler` | Integration-style tests using `httptest.Server` as both the proxy endpoint and a mock doco-cd backend. Full pipeline tests: valid request end-to-end, wrong method, wrong content type, bad IP, bad signature, disallowed repo, ping event, unknown event. |

### Coverage goal

Full line and branch coverage for all security-critical paths (signature verification, IP validation, allowlist enforcement). Use `go test -cover` to verify.

## Implementation TODO

Each item follows TDD: write failing test first, then implement, then refactor.

### 1. `config` — no internal dependencies, everything else needs it

- [x] Define `Config` struct and `Load()` function
- [x] Required env vars: `GITHUB_WEBHOOK_SECRET`, `DOCO_CD_WEBHOOK_SECRET`, `DOCO_CD_URL`, `ALLOWED_REPOS` — fail if missing/empty
- [x] Optional env vars: `LISTEN_ADDR` (default `:8080`), `TRUSTED_PROXY_CIDRS`, `GITHUB_META_REFRESH_INTERVAL` (default `1h`)
- [x] `_FILE` suffix support for secret vars (read file contents, trim whitespace)
- [x] `ALLOWED_REPOS` canonicalization: lowercase, trim whitespace, reject if empty after parsing
- [x] `TRUSTED_PROXY_CIDRS` parsing and validation
- [x] Warn if `GITHUB_WEBHOOK_SECRET` and `DOCO_CD_WEBHOOK_SECRET` are identical
- [x] Startup log with redacted secrets
- [x] Reject non-positive `GITHUB_META_REFRESH_INTERVAL`

### 2. `signature` — no internal dependencies

- [x] `Compute(message, key []byte) string` — returns `sha256=<hex>` HMAC-SHA256
- [x] `Verify(message, key []byte, header string) error` — parse header, require `sha256=` prefix, reject `sha1=`, constant-time compare via `hmac.Equal`

### 3. `payload` — no internal dependencies

- [x] Define incoming `GitHubPushEvent` struct with only the allowed fields
- [x] `Parse(body []byte) (GitHubPushEvent, error)` — unmarshal and validate required fields are non-empty
- [x] `GitHubPushEvent.Marshal() ([]byte, error)` — construct clean outgoing JSON from validated fields

### 4. `ipcheck` — no internal dependencies (config values passed in as params)

- [x] `GitHubIPChecker` struct with `Check(ip string) bool`
- [x] Fetch and parse `https://api.github.com/meta` — extract `hooks` CIDRs (IPv4 + IPv6)
- [x] Fail startup if initial fetch fails or returns no CIDRs
- [x] Background refresh goroutine with configurable interval
- [x] `ETag`/`If-None-Match` for efficient polling
- [x] On refresh failure: log error, keep last-known-good ranges
- [x] `ExtractClientIP(r *http.Request, trustedProxyCIDRs []*net.IPNet) string` — use `RemoteAddr`, fall back to last untrusted `X-Forwarded-For` entry when `RemoteAddr` is in trusted proxy CIDRs

### 5. `proxy` — depends on `signature` and `payload` types

- [x] `Forwarder` struct wrapping an `http.Client` (15s timeout, no redirects)
- [x] `Forward(event GitHubPushEvent, docoCDURL string, secret []byte) (statusCode int, err error)`
- [x] Construct `POST` to `{DOCO_CD_URL}/v1/webhook` with correct headers and HMAC signature over marshaled bytes
- [x] Return doco-cd's status code; do not forward headers or body

### 6. `handler` — depends on everything above

- [x] `NewHandler(cfg Config, checker *GitHubIPChecker, forwarder *Forwarder) http.Handler`
- [x] `POST /webhook` only — reject other methods/paths
- [x] Require `Content-Type: application/json`
- [x] Enforce max body size (1 MB)
- [x] Event routing: `ping` → 200, unknown events → 200, `push` → continue
- [x] Source IP validation via `ipcheck`
- [x] HMAC signature verification against raw body
- [x] Payload parsing and validation
- [x] Repository allowlist check (case-insensitive)
- [x] Forward to doco-cd and return its status code
- [x] Generic error responses throughout (no internal details)
- [x] Integration tests: full pipeline end-to-end, wrong method, wrong content type, bad IP, bad signature, disallowed repo, ping event, unknown event

### 7. `cmd/proxy/main.go` — ties everything together

- [ ] Load config via `config.Load()`
- [ ] Initialize `GitHubIPChecker` (fail if initial fetch fails)
- [ ] Initialize `Forwarder`
- [ ] Wire up handler and start `http.Server` with timeouts (`ReadHeaderTimeout` 5s, `ReadTimeout` 10s, `WriteTimeout` 30s, `IdleTimeout` 60s, `MaxHeaderBytes` 1 MB)
- [ ] Graceful shutdown on `SIGINT`/`SIGTERM`

## Not in scope

- HTTPS termination (reverse proxy responsibility)
- Rate limiting (reverse proxy/WAF responsibility)
- `?wait=true` passthrough (GitHub times out after 10s, doco-cd blocks until deployment completes — this mismatch would cause false failures in the GitHub UI)
- Replay protection via `X-GitHub-Delivery` (would require stateful tracking; the IP validation + HMAC + allowlist provide sufficient protection for this use case)
- Persistence or database of any kind

## Project structure

```
.
├── cmd/
│   └── proxy/
│       └── main.go          # entrypoint, config loading, server setup
├── internal/
│   └── proxy/
│       ├── config.go        # configuration parsing and validation
│       ├── config_test.go
│       ├── handler.go       # HTTP handler (pipeline orchestration)
│       ├── handler_test.go
│       ├── signature.go     # HMAC signing and verification
│       ├── signature_test.go
│       ├── ipcheck.go       # GitHub IP range fetching and validation
│       ├── ipcheck_test.go
│       ├── payload.go       # webhook payload types and construction
│       ├── payload_test.go
│       ├── proxy.go         # outgoing request to doco-cd
│       └── proxy_test.go
├── go.mod
├── README.md
└── IMPLEMENTATION_PLAN.md
```
