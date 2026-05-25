# doco-cd-webhook-proxy

A security proxy for [doco-cd](https://doco.cd) webhooks. Receives GitHub push webhooks, validates them, and forwards sanitized requests to an internal doco-cd instance — so doco-cd (and its `docker.sock`) never needs to be exposed to the internet.

## Why

[doco-cd](https://doco.cd) is a GitOps tool that deploys Docker Compose projects by watching Git repositories. It requires `docker.sock` mounted into its container, giving it full root-level access to the Docker host.

Without this proxy, there are two options:

- **Polling** — doco-cd periodically pulls the repo to check for changes. This works but is slow (at least 1 minute delay), causes unnecessary network traffic, and wears out SD cards on low-power devices like Raspberry Pis.
- **Direct webhooks** — fast, but requires exposing doco-cd directly to the internet. While doco-cd validates webhook signatures, it has several limitations as a public-facing endpoint:
  - **No repository allowlist** — anyone who knows the webhook secret can trigger a deployment of *any* repo that doco-cd can pull, not just the ones you intended.
  - **Full Docker access** — a process with `docker.sock` mounted is effectively root on the host. Exposing it to the internet means any vulnerability in its HTTP handling could lead to full host compromise.
  - **Raw request forwarding** — the original HTTP request from the internet reaches doco-cd as-is, including all headers and payload fields. Any parser bug or unexpected field could be exploited.

This proxy closes those gaps: deployments trigger instantly via webhooks, but doco-cd stays on an internal network, unreachable from the internet. The proxy validates the source IP against [GitHub's published CIDR ranges](https://api.github.com/meta), verifies the HMAC signature, enforces a repository allowlist, and **never forwards the original request** — it constructs a new one from scratch using only the validated fields.

## How it works

```
GitHub ──webhook──▶ proxy ──sanitized request──▶ doco-cd (internal)
```

1. Validates the HTTP request (POST method, JSON content type, required headers)
2. Validates the source IP against [GitHub's webhook CIDRs](https://api.github.com/meta)
3. Verifies the HMAC-SHA256 signature (`X-Hub-Signature-256`)
4. Validates the payload (required fields, repository allowlist, clone URL format)
5. Constructs a **new** minimal request to doco-cd — no headers or payload fields are forwarded verbatim

## Configuration

| Variable | Required | Default | Description |
|---|---|---|---|
| `GITHUB_WEBHOOK_SECRET` | yes | | Secret shared with GitHub for signature validation |
| `DOCO_CD_WEBHOOK_SECRET` | yes | | Secret used to sign requests to doco-cd |
| `DOCO_CD_URL` | yes | | Internal doco-cd URL (e.g. `http://doco-cd:80`) |
| `ALLOWED_REPOS` | yes | | Comma-separated repository full names (e.g. `org/repo1,org/repo2`) |
| `LISTEN_ADDR` | no | `:8080` | Address to listen on |
| `TRUSTED_PROXY_CIDRS` | no | | Comma-separated CIDRs of trusted reverse proxies |
| `GITHUB_META_REFRESH_INTERVAL` | no | `1h` | How often to refresh GitHub IP ranges |

Secret variables (`GITHUB_WEBHOOK_SECRET`, `DOCO_CD_WEBHOOK_SECRET`) support a `_FILE` suffix variant for use with Docker secrets or mounted files. For example, setting `GITHUB_WEBHOOK_SECRET_FILE=/run/secrets/github_secret` reads the secret from that file instead of the environment variable directly.

> [!NOTE]
> `GITHUB_WEBHOOK_SECRET` and `DOCO_CD_WEBHOOK_SECRET` should be different values.

## Running with Docker

```yaml
# compose.yaml
services:
  webhook-proxy:
    image: ghcr.io/strayer/doco-cd-webhook-proxy:latest
    ports:
      - "8080:8080"
    environment:
      GITHUB_WEBHOOK_SECRET: ${GITHUB_WEBHOOK_SECRET}
      DOCO_CD_WEBHOOK_SECRET: ${DOCO_CD_WEBHOOK_SECRET}
      DOCO_CD_URL: http://doco-cd:80
      ALLOWED_REPOS: org/repo1,org/repo2

  doco-cd:
    image: ghcr.io/kimdre/doco-cd:latest
    # ... doco-cd configuration
```

The proxy must be able to reach both the internet (to fetch [GitHub's IP ranges](https://api.github.com/meta)) and the internal doco-cd instance. Place it on a shared network with doco-cd, but only expose the proxy's port to the reverse proxy / internet.

## Development Setup

This project uses [mise](https://mise.jdx.dev) for tool management and [prek](https://github.com/j178/prek) for pre-commit hooks.

```sh
mise install      # installs Go, goimports, golangci-lint
prek install      # installs pre-commit hooks
```

Pre-commit hooks run automatically on `git commit`:
- **goimports** — auto-formats Go files
- **golangci-lint** — lints for errors (no auto-fix)

## Design

This proxy is built with a minimal attack surface in mind. The Go application uses only the standard library — no third-party dependencies that could introduce vulnerabilities or supply chain risks. The same principle extends to the build and deployment pipeline: GitHub Actions workflows and Dockerfiles follow security best practices to keep the overall supply chain tight.

## AI Usage Notice

To ensure the responsible use of AI, this project adheres to a strict policy of human oversight. While a Large Language Model (LLM) is used as an assistive tool, its role is limited to implementation based on human-led design. Every line of AI-generated code is then manually reviewed and validated for correctness, security, and quality before being accepted into the codebase. The final authority and accountability for the code rests with the human developer.
