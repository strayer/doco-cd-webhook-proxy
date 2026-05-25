# Agent Instructions

Read `README.md` for project context.

## Rules

- **No external dependencies.** stdlib only. This is a hard security constraint, not a preference.
- **TDD.** Write failing tests first, then implement. Every `.go` file gets a `_test.go` file.
- **Tests use only `testing` and `net/http/httptest`.** No test frameworks.
- **Never forward the original request.** Always construct a new HTTP request from validated fields. This is the core security property of the proxy.
- **Fail closed.** Missing config, unparseable IP ranges, empty allowlist → refuse to start or reject the request. Never fall back to permissive behavior.
- **No secrets in logs.** Log config on startup but redact secret values. Never log full payloads.
- **Generic error responses.** Do not leak internal state, config, or backend details to the caller.
- **Constant-time signature comparison.** Use `hmac.Equal`, never `==`.
- **Raw body for HMAC.** Verify signature against the raw request body bytes, not re-serialized JSON.
- **Sign outgoing bytes.** Compute the outgoing HMAC over the exact marshaled JSON bytes sent to doco-cd.

## Style

- Entrypoint in `cmd/proxy/`, all logic in `internal/proxy/`.
- Use `log/slog` for structured logging.
- Table-driven tests with descriptive subtest names.
- No comments unless the "why" is non-obvious.
- `gofmt`/`goimports` formatting assumed — don't fight it.

## Commits

Use [Conventional Commits](https://www.conventionalcommits.org/) for all commit messages.

## Pre-commit hooks

This project uses [prek](https://github.com/j178/prek) to run pre-commit hooks. Hooks are defined in `.pre-commit-config.yaml` as `repo: local` system hooks:

1. **goimports** — auto-formats staged `.go` files in-place (`-w`). May modify files during commit.
2. **golangci-lint** — lints against `HEAD` (`--new-from-rev=HEAD`). No auto-fix — fails the commit on violations.

If a commit fails due to goimports reformatting, re-stage the changes and commit again. If golangci-lint fails, fix the reported issues before committing.

## Build & test

```sh
go build ./...
go test ./... -cover
go vet ./...
```
