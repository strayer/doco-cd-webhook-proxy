package proxy

import (
	"io"
	"log/slog"
	"mime"
	"net/http"
	"strings"
)

const maxBodySize = 1 << 20 // 1 MB

type rejectReason string

const (
	rejectIP             rejectReason = "ip"
	rejectSignature      rejectReason = "signature"
	rejectSignatureCount rejectReason = "signature_count"
	rejectAllowlist      rejectReason = "allowlist"
	rejectPayload        rejectReason = "payload"
	rejectBodyTooLarge   rejectReason = "body_too_large"
	rejectBodyRead       rejectReason = "body_read"
	rejectMethod         rejectReason = "method"
	rejectContentType    rejectReason = "content_type"
	rejectMissingEvent   rejectReason = "missing_event"
	rejectForwardError   rejectReason = "forward_error"
	rejectCloneURL       rejectReason = "clone_url"
)

type handlerResult struct {
	status    int
	reason    rejectReason
	forwarded bool
}

func NewHandler(cfg *Config, checker *GitHubIPChecker, forwarder *Forwarder) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/webhook", &webhookHandler{
		cfg:       cfg,
		checker:   checker,
		forwarder: forwarder,
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		slog.Info("request to unregistered path", "path", r.URL.Path, "method", r.Method)
		w.WriteHeader(http.StatusNotFound)
	})
	return mux
}

type webhookHandler struct {
	cfg       *Config
	checker   *GitHubIPChecker
	forwarder *Forwarder
}

func (h *webhookHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	result := h.handle(r)
	w.WriteHeader(result.status)
}

func (h *webhookHandler) handle(r *http.Request) handlerResult {
	if r.Method != http.MethodPost {
		return handlerResult{status: http.StatusMethodNotAllowed, reason: rejectMethod}
	}

	clientIP := ExtractClientIP(r, h.cfg.TrustedProxyCIDRs)
	if !h.checker.Check(clientIP) {
		slog.Warn("request from disallowed IP", "ip", clientIP)
		return handlerResult{status: http.StatusForbidden, reason: rejectIP}
	}

	mediaType, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if !strings.EqualFold(mediaType, "application/json") {
		return handlerResult{status: http.StatusUnsupportedMediaType, reason: rejectContentType}
	}

	event := r.Header.Get("X-GitHub-Event")
	if event == "" {
		return handlerResult{status: http.StatusBadRequest, reason: rejectMissingEvent}
	}

	if len(r.Header.Values("X-Hub-Signature-256")) != 1 {
		slog.Warn("signature verification failed", "error", "expected exactly one X-Hub-Signature-256 header")
		return handlerResult{status: http.StatusForbidden, reason: rejectSignatureCount}
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, maxBodySize+1))
	if err != nil {
		return handlerResult{status: http.StatusBadRequest, reason: rejectBodyRead}
	}
	if len(body) > maxBodySize {
		return handlerResult{status: http.StatusRequestEntityTooLarge, reason: rejectBodyTooLarge}
	}
	sigHeader := r.Header.Get("X-Hub-Signature-256")
	if err := VerifySignature(body, []byte(h.cfg.GitHubWebhookSecret), sigHeader); err != nil {
		slog.Warn("signature verification failed", "error", err)
		return handlerResult{status: http.StatusForbidden, reason: rejectSignature}
	}

	if event == "ping" {
		return handlerResult{status: http.StatusOK}
	}

	if event != "push" {
		slog.Info("ignoring unsupported event type", "event", event)
		return handlerResult{status: http.StatusOK}
	}

	payload, err := ParsePayload(body)
	if err != nil {
		slog.Warn("payload parsing failed", "error", err)
		return handlerResult{status: http.StatusBadRequest, reason: rejectPayload}
	}

	if !h.repoAllowed(payload.Repository.FullName) {
		slog.Warn("repository not in allowlist", "repo", payload.Repository.FullName)
		return handlerResult{status: http.StatusForbidden, reason: rejectAllowlist}
	}

	expectedCloneURL := "https://github.com/" + strings.ToLower(payload.Repository.FullName) + ".git"
	if strings.ToLower(payload.Repository.CloneURL) != expectedCloneURL {
		slog.Warn("clone_url mismatch", "repo", payload.Repository.FullName, "clone_url", payload.Repository.CloneURL)
		return handlerResult{status: http.StatusForbidden, reason: rejectCloneURL}
	}

	statusCode, err := h.forwarder.Forward(r.Context(), payload, h.cfg.DocoCDURL, []byte(h.cfg.DocoCDWebhookSecret))
	if err != nil {
		slog.Error("failed to forward to doco-cd", "error", err)
		return handlerResult{status: http.StatusBadGateway, reason: rejectForwardError}
	}

	slog.Info("forwarded webhook", "repo", payload.Repository.FullName, "status", statusCode)
	return handlerResult{status: statusCode, forwarded: true}
}

func (h *webhookHandler) repoAllowed(fullName string) bool {
	normalized := strings.ToLower(fullName)
	for _, allowed := range h.cfg.AllowedRepos {
		if allowed == normalized {
			return true
		}
	}
	return false
}
