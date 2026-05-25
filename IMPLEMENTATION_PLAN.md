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

- [x] Load config via `config.Load()`
- [x] Initialize `GitHubIPChecker` (fail if initial fetch fails)
- [x] Initialize `Forwarder`
- [x] Wire up handler and start `http.Server` with timeouts (`ReadHeaderTimeout` 5s, `ReadTimeout` 10s, `WriteTimeout` 30s, `IdleTimeout` 60s, `MaxHeaderBytes` 1 MB)
- [x] Graceful shutdown on `SIGINT`/`SIGTERM`

### 8. Health check endpoint

- [ ] **`GET /healthz`**: Returns `200 OK` when the server is ready to accept requests. No authentication, no IP check — intended for container orchestrators.
- [ ] **Dockerfile `HEALTHCHECK`**: Use the binary itself (e.g. subcommand or dedicated flag) to probe `/healthz`, since `scratch` has no shell or curl.

### 9. End-to-end test

- [ ] **Make GitHub meta URL configurable**: Add optional `GITHUB_META_URL` env var (default `https://api.github.com/meta`) so tests can point at a mock.
- [ ] **E2E test**: Build the binary, start it with a mock `/meta` endpoint (returning `127.0.0.0/8` as hooks CIDR) and a mock doco-cd backend. Send a valid signed webhook, verify the mock doco-cd receives the correct re-signed request with expected headers and payload. Also test rejection cases (bad signature, disallowed repo).
- [ ] **CI integration**: Run the e2e test as part of the PR validation workflow.

### 10. CI/CD — GitHub Actions and Docker image

- [x] **Dockerfile**: Multi-stage build — Go build stage, `scratch` final image. Static binary with `CGO_ENABLED=0`. Non-root user via numeric UID.
- [x] **PR validation workflow** (`.github/workflows/ci.yaml`): Separate jobs for `go test`, `go vet`, goimports, golangci-lint, and Docker build — each visible as its own PR status check.
- [x] **Dev image workflow** (`.github/workflows/build-dev.yaml`): Builds and pushes `dev` tag to `ghcr.io` on main pushes (linux/amd64 + linux/arm64).
- [ ] **Release workflow**: Triggered on `v*` tags, uses `docker/github-builder` for signed/attested multi-platform builds pushed to `ghcr.io` with semver tags.
- [x] **Release notes config** (`.github/release.yml`): Changelog categories by PR label for GitHub auto-generated release notes.

## Not in scope

- HTTPS termination (reverse proxy responsibility)
- Rate limiting (reverse proxy/WAF responsibility)
- `?wait=true` passthrough (GitHub times out after 10s, doco-cd blocks until deployment completes — this mismatch would cause false failures in the GitHub UI)
- Replay protection via `X-GitHub-Delivery` (would require stateful tracking; the IP validation + HMAC + allowlist provide sufficient protection for this use case)
- Persistence or database of any kind

## Review findings (post-implementation)

Issues identified during code review that should be addressed as follow-up work.

### `proxy.go` — Forwarder

- [x] **No context propagation**: `Forward()` uses `http.NewRequest` without context. In-flight forwards cannot be cancelled on server shutdown. The 15s client timeout can exceed the shutdown deadline. Fix: accept and use `r.Context()` via `http.NewRequestWithContext`.
- [x] **Response body not drained**: `defer resp.Body.Close()` without reading the body prevents HTTP keep-alive connection reuse. Under load this exhausts the connection pool. Fix: `io.Copy(io.Discard, resp.Body)` before close.
- [x] **`strings.TrimRight` vs `strings.TrimSuffix`**: Reviewed and kept `strings.TrimRight(docoCDURL, "/")` — for a single-char cutset `/` it correctly strips all trailing slashes, which is the desired behavior. `TrimSuffix` would only remove one slash, breaking URLs like `http://host//`.

### `ipcheck.go` — GitHubIPChecker

- [x] **`checker.Stop()` blocks during mid-fetch**: `refreshLoop`'s `fetch()` creates an `http.Client` with 10s timeout and no context. `Stop()` blocks on `<-c.done` until the fetch completes, adding up to 10s to process exit time. Fix: use a context derived from `stopCh` for the HTTP request.
- [x] **New `http.Client` per fetch**: `fetch()` allocates a fresh `http.Client{Timeout: 10s}` on every call, bypassing connection pooling. Fix: create the client once in `NewGitHubIPChecker`.

### `handler.go` — webhook handler

- [x] **Body read before signature-count check**: The handler reads up to 1MB (line 78) before checking the `X-Hub-Signature-256` header count (line 86). The IP check mitigates this, but promoting the cheap header-count check would reduce resource waste from invalid requests.
- [x] **Silent discard of non-push/non-ping events**: Events like `issues` or `pull_request` return 200 with no log. Operators cannot detect misconfigured webhook subscriptions. Fix: add a debug/info log when dropping unsupported events.
- [x] **Unregistered paths bypass security checks**: Only `/webhook` is registered on the ServeMux. Other paths get the default 404 handler without IP checks or logging. Low severity since the 404 is generic, but path probing goes unrecorded.

### `handler_test.go` — test brittleness

- [x] **Body-too-large tests use wrong signature**: `TestHandler_BodyTooLarge_PingEvent` and `_UnknownEvent` replace the body but leave the signature computed over `validPushPayload`. Tests pass only because body-size is checked before signature. If checks are reordered, tests break for the wrong reason.

### `config.go` — configuration

- [x] **Space-separated `ALLOWED_REPOS` silently fails**: `ALLOWED_REPOS=org/repo org/other` (space instead of comma) becomes one entry `"org/repo org/other"` that never matches. Fail-closed but no startup warning. Consider validating that entries match `owner/repo` format.

## Project structure

```
.
├── .github/
│   ├── release.yml              # changelog categories for auto-generated release notes
│   └── workflows/
│       ├── build-dev.yaml       # build + push dev image on main
│       └── ci.yaml              # PR checks (test, vet, goimports, lint, docker)
├── cmd/
│   └── proxy/
│       └── main.go              # entrypoint, config loading, server setup
├── internal/
│   └── proxy/
│       ├── config.go            # configuration parsing and validation
│       ├── config_test.go
│       ├── handler.go           # HTTP handler (pipeline orchestration)
│       ├── handler_test.go
│       ├── signature.go         # HMAC signing and verification
│       ├── signature_test.go
│       ├── ipcheck.go           # GitHub IP range fetching and validation
│       ├── ipcheck_test.go
│       ├── payload.go           # webhook payload types and construction
│       ├── payload_test.go
│       ├── proxy.go             # outgoing request to doco-cd
│       └── proxy_test.go
├── Dockerfile
├── .dockerignore
├── go.mod
├── README.md
└── IMPLEMENTATION_PLAN.md
```
