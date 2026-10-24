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

mise is the single source of truth for tools (Go, goimports, golangci-lint, govulncheck) and tasks:

```sh
mise run format        # goimports -w .
mise run test          # go test -race -cover ./...
mise run lint          # format-check, vet (incl. e2e tag), golangci-lint, govulncheck
mise run e2e           # end-to-end tests (-tags e2e)
mise run run           # run the proxy locally
```

## CI

`.github/workflows/ci.yaml` runs on pull requests and pushes to main, installing tools only via `jdx/mise-action`. Jobs (names are required status checks, don't rename): `Test`, `Vet`, `goimports`, `golangci-lint`, `govulncheck`, `E2E`, `Go version sync`, `Docker build`. The Go version must match in `go.mod`, `mise.toml` and the `Dockerfile`. `pr-labels.yml` (job `require-release-label`, required check) demands one of the labels `breaking`, `feature`, `enhancement`, `bug`, `chore`, `dependencies` on every PR. `release.yaml` (on `v*` tags) first verifies the signed annotated tag, gates the image build on unit and e2e tests, then creates the GitHub Release (notes generated per `.github/release.yml`, `--prerelease` for `-rc` tags); `vulncheck.yaml` scans the published image weekly. Renovate config is `renovate.json5`; Go and the mise-managed dev tools are grouped as "Go toolchain".

## Releasing

Label every PR (`breaking`, `feature`, `enhancement`, `bug`, `chore` or `dependencies`); the labels drive the semver bump and the release notes. To release, run `scripts/release.sh` (`--rc` for a `vX.Y.Z-rc.N` pre-release). It creates and pushes a signed annotated tag; `release.yaml` does the rest.
