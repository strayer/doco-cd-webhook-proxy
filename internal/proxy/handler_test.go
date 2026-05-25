package proxy

import (
	"bytes"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func testConfig(docoCDURL string) *Config {
	return &Config{
		GitHubWebhookSecret:       "gh-secret",
		DocoCDWebhookSecret:       "cd-secret",
		DocoCDURL:                 docoCDURL,
		AllowedRepos:              []string{"org/repo"},
		ListenAddr:                ":8080",
		GitHubMetaRefreshInterval: time.Hour,
	}
}

func testChecker(t *testing.T, allowedCIDRs ...string) *GitHubIPChecker {
	t.Helper()
	cidrs := make([]*net.IPNet, 0, len(allowedCIDRs))
	for _, c := range allowedCIDRs {
		_, cidr, err := net.ParseCIDR(c)
		if err != nil {
			t.Fatalf("invalid CIDR %q: %v", c, err)
		}
		cidrs = append(cidrs, cidr)
	}
	return &GitHubIPChecker{
		cidrs:  cidrs,
		stopCh: make(chan struct{}),
		done:   make(chan struct{}),
	}
}

const validPushPayload = `{
	"ref": "refs/heads/main",
	"before": "aaaa",
	"after": "bbbb",
	"repository": {
		"name": "repo",
		"full_name": "org/repo",
		"clone_url": "https://github.com/org/repo.git"
	},
	"pusher": {
		"name": "user",
		"email": "user@example.com"
	}
}`

func validPushRequest(t *testing.T) *http.Request {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/webhook", strings.NewReader(validPushPayload))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-GitHub-Event", "push")
	req.Header.Set("X-Hub-Signature-256", ComputeSignature([]byte(validPushPayload), []byte("gh-secret")))
	req.RemoteAddr = "127.0.0.1:12345"
	return req
}

func failingBackend(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("backend should not have been called")
	}))
}

func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	handler := slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})
	slog.SetDefault(slog.New(handler))
	t.Cleanup(func() {
		slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	})
	return &buf
}

func assertReason(t *testing.T, h *webhookHandler, r *http.Request, wantReason rejectReason) {
	t.Helper()
	result := h.handle(r)
	if result.reason != wantReason {
		t.Errorf("expected reason %q, got %q (status %d)", wantReason, result.reason, result.status)
	}
}

func TestHandler_WrongMethod(t *testing.T) {
	backend := failingBackend(t)
	defer backend.Close()
	h := &webhookHandler{cfg: testConfig(backend.URL), checker: testChecker(t, "127.0.0.0/8"), forwarder: NewForwarder()}

	methods := []string{"GET", "PUT", "DELETE", "PATCH"}
	for _, m := range methods {
		t.Run(m, func(t *testing.T) {
			req := httptest.NewRequest(m, "/webhook", nil)
			assertReason(t, h, req, rejectMethod)
		})
	}
}

func TestHandler_WrongPath(t *testing.T) {
	backend := failingBackend(t)
	defer backend.Close()
	h := NewHandler(testConfig(backend.URL), testChecker(t, "127.0.0.0/8"), NewForwarder())
	logs := captureLogs(t)

	req := httptest.NewRequest("POST", "/other", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("expected %d, got %d", http.StatusNotFound, rr.Code)
	}
	if rr.Body.Len() != 0 {
		t.Errorf("expected empty response body for unregistered path, got %q", rr.Body.String())
	}
	if !strings.Contains(logs.String(), "request to unregistered path") {
		t.Error("expected log message about unregistered path")
	}
}

func TestHandler_WrongContentType(t *testing.T) {
	backend := failingBackend(t)
	defer backend.Close()
	h := &webhookHandler{cfg: testConfig(backend.URL), checker: testChecker(t, "127.0.0.0/8"), forwarder: NewForwarder()}

	types := []string{"text/plain", "application/json-patch+json", "application/jsonanything", "multipart/form-data"}
	for _, ct := range types {
		t.Run(ct, func(t *testing.T) {
			req := validPushRequest(t)
			req.Header.Set("Content-Type", ct)
			assertReason(t, h, req, rejectContentType)
		})
	}
}

func TestHandler_WrongContentType_BadIP(t *testing.T) {
	backend := failingBackend(t)
	defer backend.Close()
	h := &webhookHandler{cfg: testConfig(backend.URL), checker: testChecker(t, "127.0.0.0/8"), forwarder: NewForwarder()}

	req := validPushRequest(t)
	req.Header.Set("Content-Type", "text/plain")
	req.RemoteAddr = "10.0.0.1:12345"
	assertReason(t, h, req, rejectIP)
}

func TestHandler_ContentTypeCaseInsensitive(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()
	h := &webhookHandler{cfg: testConfig(backend.URL), checker: testChecker(t, "127.0.0.0/8"), forwarder: NewForwarder()}

	types := []string{"Application/JSON", "APPLICATION/JSON", "application/json; charset=utf-8"}
	for _, ct := range types {
		t.Run(ct, func(t *testing.T) {
			req := validPushRequest(t)
			req.Header.Set("Content-Type", ct)
			result := h.handle(req)
			if result.reason == rejectContentType {
				t.Errorf("should accept content type %q but got rejection", ct)
			}
		})
	}
}

func TestHandler_MissingContentType(t *testing.T) {
	backend := failingBackend(t)
	defer backend.Close()
	h := &webhookHandler{cfg: testConfig(backend.URL), checker: testChecker(t, "127.0.0.0/8"), forwarder: NewForwarder()}

	req := validPushRequest(t)
	req.Header.Del("Content-Type")
	assertReason(t, h, req, rejectContentType)
}

func TestHandler_MissingEventHeader(t *testing.T) {
	backend := failingBackend(t)
	defer backend.Close()
	h := &webhookHandler{cfg: testConfig(backend.URL), checker: testChecker(t, "127.0.0.0/8"), forwarder: NewForwarder()}

	req := validPushRequest(t)
	req.Header.Del("X-GitHub-Event")
	assertReason(t, h, req, rejectMissingEvent)
}

func TestHandler_PingEvent(t *testing.T) {
	backend := failingBackend(t)
	defer backend.Close()
	h := &webhookHandler{cfg: testConfig(backend.URL), checker: testChecker(t, "127.0.0.0/8"), forwarder: NewForwarder()}

	body := `{}`
	req := validPushRequest(t)
	req.Header.Set("X-GitHub-Event", "ping")
	req.Body = io.NopCloser(strings.NewReader(body))
	req.Header.Set("X-Hub-Signature-256", ComputeSignature([]byte(body), []byte("gh-secret")))
	result := h.handle(req)
	if result.status != http.StatusOK {
		t.Errorf("expected %d, got %d (reason: %s)", http.StatusOK, result.status, result.reason)
	}
	if result.forwarded {
		t.Error("ping should not be forwarded")
	}
}

func TestHandler_PingEvent_BadSignature(t *testing.T) {
	backend := failingBackend(t)
	defer backend.Close()
	h := &webhookHandler{cfg: testConfig(backend.URL), checker: testChecker(t, "127.0.0.0/8"), forwarder: NewForwarder()}

	req := validPushRequest(t)
	req.Header.Set("X-GitHub-Event", "ping")
	req.Header.Set("X-Hub-Signature-256", ComputeSignature([]byte("tampered"), []byte("gh-secret")))
	assertReason(t, h, req, rejectSignature)
}

func TestHandler_PingEvent_BadIP(t *testing.T) {
	backend := failingBackend(t)
	defer backend.Close()
	h := &webhookHandler{cfg: testConfig(backend.URL), checker: testChecker(t, "127.0.0.0/8"), forwarder: NewForwarder()}
	logs := captureLogs(t)

	req := validPushRequest(t)
	req.Header.Set("X-GitHub-Event", "ping")
	req.RemoteAddr = "10.0.0.1:12345"
	assertReason(t, h, req, rejectIP)

	if !strings.Contains(logs.String(), "request from disallowed IP") {
		t.Error("expected log message about disallowed IP")
	}
}

func TestHandler_UnknownEvent(t *testing.T) {
	backend := failingBackend(t)
	defer backend.Close()
	h := &webhookHandler{cfg: testConfig(backend.URL), checker: testChecker(t, "127.0.0.0/8"), forwarder: NewForwarder()}

	events := []string{"issues", "pull_request", "release", "star"}
	for _, e := range events {
		t.Run(e, func(t *testing.T) {
			logs := captureLogs(t)
			body := `{}`
			req := validPushRequest(t)
			req.Header.Set("X-GitHub-Event", e)
			req.Body = io.NopCloser(strings.NewReader(body))
			req.Header.Set("X-Hub-Signature-256", ComputeSignature([]byte(body), []byte("gh-secret")))
			result := h.handle(req)
			if result.status != http.StatusOK {
				t.Errorf("expected %d, got %d (reason: %s)", http.StatusOK, result.status, result.reason)
			}
			if result.forwarded {
				t.Error("unknown event should not be forwarded")
			}
			if !strings.Contains(logs.String(), "ignoring unsupported event type") {
				t.Error("expected log message about unsupported event type")
			}
		})
	}
}

func TestHandler_UnknownEvent_BadSignature(t *testing.T) {
	backend := failingBackend(t)
	defer backend.Close()
	h := &webhookHandler{cfg: testConfig(backend.URL), checker: testChecker(t, "127.0.0.0/8"), forwarder: NewForwarder()}

	req := validPushRequest(t)
	req.Header.Set("X-GitHub-Event", "issues")
	req.Header.Set("X-Hub-Signature-256", ComputeSignature([]byte("tampered"), []byte("gh-secret")))
	assertReason(t, h, req, rejectSignature)
}

func TestHandler_UnknownEvent_BadIP(t *testing.T) {
	backend := failingBackend(t)
	defer backend.Close()
	h := &webhookHandler{cfg: testConfig(backend.URL), checker: testChecker(t, "127.0.0.0/8"), forwarder: NewForwarder()}
	logs := captureLogs(t)

	req := validPushRequest(t)
	req.Header.Set("X-GitHub-Event", "issues")
	req.RemoteAddr = "10.0.0.1:12345"
	assertReason(t, h, req, rejectIP)

	if !strings.Contains(logs.String(), "request from disallowed IP") {
		t.Error("expected log message about disallowed IP")
	}
}

func TestHandler_BadIP(t *testing.T) {
	backend := failingBackend(t)
	defer backend.Close()
	h := &webhookHandler{cfg: testConfig(backend.URL), checker: testChecker(t, "192.0.2.0/24"), forwarder: NewForwarder()}
	logs := captureLogs(t)

	req := validPushRequest(t)
	req.RemoteAddr = "10.0.0.1:12345"
	assertReason(t, h, req, rejectIP)

	if !strings.Contains(logs.String(), "request from disallowed IP") {
		t.Error("expected log message about disallowed IP")
	}
}

func TestHandler_BadSignature(t *testing.T) {
	backend := failingBackend(t)
	defer backend.Close()
	h := &webhookHandler{cfg: testConfig(backend.URL), checker: testChecker(t, "127.0.0.0/8"), forwarder: NewForwarder()}
	logs := captureLogs(t)

	req := validPushRequest(t)
	req.Header.Set("X-Hub-Signature-256", ComputeSignature([]byte(validPushPayload), []byte("wrong-secret")))
	assertReason(t, h, req, rejectSignature)

	if !strings.Contains(logs.String(), "signature verification failed") {
		t.Error("expected log message about signature failure")
	}
}

func TestHandler_MissingSignature(t *testing.T) {
	backend := failingBackend(t)
	defer backend.Close()
	h := &webhookHandler{cfg: testConfig(backend.URL), checker: testChecker(t, "127.0.0.0/8"), forwarder: NewForwarder()}
	logs := captureLogs(t)

	req := validPushRequest(t)
	req.Header.Del("X-Hub-Signature-256")
	assertReason(t, h, req, rejectSignatureCount)

	if !strings.Contains(logs.String(), "signature verification failed") {
		t.Error("expected log message about signature failure")
	}
}

func TestHandler_DuplicateSignatureHeaders(t *testing.T) {
	backend := failingBackend(t)
	defer backend.Close()
	h := &webhookHandler{cfg: testConfig(backend.URL), checker: testChecker(t, "127.0.0.0/8"), forwarder: NewForwarder()}
	logs := captureLogs(t)

	req := validPushRequest(t)
	req.Header.Add("X-Hub-Signature-256", "sha256=0000000000000000000000000000000000000000000000000000000000000000")
	assertReason(t, h, req, rejectSignatureCount)

	if !strings.Contains(logs.String(), "signature verification failed") {
		t.Error("expected log message about signature failure")
	}
}

func TestHandler_InvalidPayload(t *testing.T) {
	backend := failingBackend(t)
	defer backend.Close()
	h := &webhookHandler{cfg: testConfig(backend.URL), checker: testChecker(t, "127.0.0.0/8"), forwarder: NewForwarder()}
	logs := captureLogs(t)

	body := `{"ref": ""}`
	req := validPushRequest(t)
	req.Body = io.NopCloser(strings.NewReader(body))
	req.Header.Set("X-Hub-Signature-256", ComputeSignature([]byte(body), []byte("gh-secret")))
	assertReason(t, h, req, rejectPayload)

	if !strings.Contains(logs.String(), "payload parsing failed") {
		t.Error("expected log message about payload failure")
	}
}

func TestHandler_DisallowedRepo(t *testing.T) {
	backend := failingBackend(t)
	defer backend.Close()
	h := &webhookHandler{cfg: testConfig(backend.URL), checker: testChecker(t, "127.0.0.0/8"), forwarder: NewForwarder()}
	logs := captureLogs(t)

	body := `{
		"ref": "refs/heads/main",
		"before": "aaaa",
		"after": "bbbb",
		"repository": {
			"name": "other",
			"full_name": "org/other",
			"clone_url": "https://github.com/org/other.git"
		},
		"pusher": {
			"name": "user",
			"email": "user@example.com"
		}
	}`
	req := validPushRequest(t)
	req.Body = io.NopCloser(strings.NewReader(body))
	req.Header.Set("X-Hub-Signature-256", ComputeSignature([]byte(body), []byte("gh-secret")))
	assertReason(t, h, req, rejectAllowlist)

	if !strings.Contains(logs.String(), "repository not in allowlist") {
		t.Error("expected log message about allowlist rejection")
	}
}

func TestHandler_BodyTooLarge(t *testing.T) {
	backend := failingBackend(t)
	defer backend.Close()
	h := &webhookHandler{cfg: testConfig(backend.URL), checker: testChecker(t, "127.0.0.0/8"), forwarder: NewForwarder()}

	largeBody := strings.Repeat("x", 1<<20+1)
	req := validPushRequest(t)
	req.Body = io.NopCloser(strings.NewReader(largeBody))
	req.Header.Set("X-Hub-Signature-256", ComputeSignature([]byte(largeBody), []byte("gh-secret")))
	assertReason(t, h, req, rejectBodyTooLarge)
}

func TestHandler_BodyTooLarge_PingEvent(t *testing.T) {
	backend := failingBackend(t)
	defer backend.Close()
	h := &webhookHandler{cfg: testConfig(backend.URL), checker: testChecker(t, "127.0.0.0/8"), forwarder: NewForwarder()}

	largeBody := strings.Repeat("x", 1<<20+1)
	req := validPushRequest(t)
	req.Header.Set("X-GitHub-Event", "ping")
	req.Body = io.NopCloser(strings.NewReader(largeBody))
	req.Header.Set("X-Hub-Signature-256", ComputeSignature([]byte(largeBody), []byte("gh-secret")))
	assertReason(t, h, req, rejectBodyTooLarge)
}

func TestHandler_BodyTooLarge_UnknownEvent(t *testing.T) {
	backend := failingBackend(t)
	defer backend.Close()
	h := &webhookHandler{cfg: testConfig(backend.URL), checker: testChecker(t, "127.0.0.0/8"), forwarder: NewForwarder()}

	largeBody := strings.Repeat("x", 1<<20+1)
	req := validPushRequest(t)
	req.Header.Set("X-GitHub-Event", "issues")
	req.Body = io.NopCloser(strings.NewReader(largeBody))
	req.Header.Set("X-Hub-Signature-256", ComputeSignature([]byte(largeBody), []byte("gh-secret")))
	assertReason(t, h, req, rejectBodyTooLarge)
}

func TestHandler_CloneURLMismatch(t *testing.T) {
	backend := failingBackend(t)
	defer backend.Close()
	h := &webhookHandler{cfg: testConfig(backend.URL), checker: testChecker(t, "127.0.0.0/8"), forwarder: NewForwarder()}
	logs := captureLogs(t)

	body := `{
		"ref": "refs/heads/main",
		"before": "aaaa",
		"after": "bbbb",
		"repository": {
			"name": "repo",
			"full_name": "org/repo",
			"clone_url": "https://evil.com/malware.git"
		},
		"pusher": {
			"name": "user",
			"email": "user@example.com"
		}
	}`
	req := validPushRequest(t)
	req.Body = io.NopCloser(strings.NewReader(body))
	req.Header.Set("X-Hub-Signature-256", ComputeSignature([]byte(body), []byte("gh-secret")))
	assertReason(t, h, req, rejectCloneURL)

	if !strings.Contains(logs.String(), "clone_url mismatch") {
		t.Error("expected log message about clone_url mismatch")
	}
}

func TestHandler_CloneURLMismatch_SubpathAttack(t *testing.T) {
	backend := failingBackend(t)
	defer backend.Close()
	h := &webhookHandler{cfg: testConfig(backend.URL), checker: testChecker(t, "127.0.0.0/8"), forwarder: NewForwarder()}

	body := `{
		"ref": "refs/heads/main",
		"before": "aaaa",
		"after": "bbbb",
		"repository": {
			"name": "repo",
			"full_name": "org/repo",
			"clone_url": "https://github.com/org/repo/../evil/malware.git"
		},
		"pusher": {
			"name": "user",
			"email": "user@example.com"
		}
	}`
	req := validPushRequest(t)
	req.Body = io.NopCloser(strings.NewReader(body))
	req.Header.Set("X-Hub-Signature-256", ComputeSignature([]byte(body), []byte("gh-secret")))
	assertReason(t, h, req, rejectCloneURL)
}

func TestHandler_GenericErrorBodies(t *testing.T) {
	backend := failingBackend(t)
	defer backend.Close()
	h := NewHandler(testConfig(backend.URL), testChecker(t, "127.0.0.0/8"), NewForwarder())

	tests := []struct {
		name   string
		mutate func(r *http.Request)
	}{
		{
			name: "bad signature",
			mutate: func(r *http.Request) {
				r.Header.Set("X-Hub-Signature-256", ComputeSignature([]byte(validPushPayload), []byte("wrong-secret")))
			},
		},
		{
			name: "bad IP",
			mutate: func(r *http.Request) {
				r.RemoteAddr = "10.0.0.1:12345"
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := validPushRequest(t)
			tt.mutate(req)
			rr := httptest.NewRecorder()
			h.ServeHTTP(rr, req)
			body := rr.Body.String()
			for _, leak := range []string{"gh-secret", "cd-secret", "localhost", "doco"} {
				if strings.Contains(strings.ToLower(body), leak) {
					t.Errorf("response body leaks internal detail %q: %s", leak, body)
				}
			}
		})
	}
}

func TestHandler_EndToEnd(t *testing.T) {
	var receivedBody []byte
	var receivedHeaders http.Header
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedHeaders = r.Header.Clone()
		var err error
		receivedBody, err = io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("failed to read body: %v", err)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()

	cfg := testConfig(backend.URL)
	h := &webhookHandler{cfg: cfg, checker: testChecker(t, "127.0.0.0/8"), forwarder: NewForwarder()}

	req := validPushRequest(t)
	result := h.handle(req)

	if result.status != http.StatusOK {
		t.Fatalf("expected %d, got %d (reason: %s)", http.StatusOK, result.status, result.reason)
	}
	if !result.forwarded {
		t.Fatal("expected request to be forwarded")
	}

	if receivedHeaders.Get("Content-Type") != "application/json" {
		t.Errorf("expected Content-Type application/json, got %q", receivedHeaders.Get("Content-Type"))
	}
	if receivedHeaders.Get("X-GitHub-Event") != "push" {
		t.Errorf("expected X-GitHub-Event push, got %q", receivedHeaders.Get("X-GitHub-Event"))
	}

	sig := receivedHeaders.Get("X-Hub-Signature-256")
	if err := VerifySignature(receivedBody, []byte("cd-secret"), sig); err != nil {
		t.Errorf("outgoing signature verification failed: %v", err)
	}

	event, err := ParsePayload(receivedBody)
	if err != nil {
		t.Fatalf("failed to parse forwarded payload: %v", err)
	}
	if event.Repository.FullName != "org/repo" {
		t.Errorf("expected repo org/repo, got %q", event.Repository.FullName)
	}
}

func TestHandler_EndToEnd_BackendError(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer backend.Close()

	cfg := testConfig(backend.URL)
	h := &webhookHandler{cfg: cfg, checker: testChecker(t, "127.0.0.0/8"), forwarder: NewForwarder()}

	req := validPushRequest(t)
	result := h.handle(req)

	if result.status != http.StatusInternalServerError {
		t.Errorf("expected %d, got %d (reason: %s)", http.StatusInternalServerError, result.status, result.reason)
	}
}

func TestHandler_EndToEnd_BackendUnreachable(t *testing.T) {
	cfg := testConfig("http://127.0.0.1:1")
	h := &webhookHandler{cfg: cfg, checker: testChecker(t, "127.0.0.0/8"), forwarder: NewForwarder()}

	req := validPushRequest(t)
	result := h.handle(req)

	if result.reason != rejectForwardError {
		t.Errorf("expected reason %q, got %q", rejectForwardError, result.reason)
	}
	if result.status != http.StatusBadGateway {
		t.Errorf("expected %d, got %d", http.StatusBadGateway, result.status)
	}
}

func TestHandler_RepoAllowlistCaseInsensitive(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()

	cfg := testConfig(backend.URL)
	h := &webhookHandler{cfg: cfg, checker: testChecker(t, "127.0.0.0/8"), forwarder: NewForwarder()}

	body := `{
		"ref": "refs/heads/main",
		"before": "aaaa",
		"after": "bbbb",
		"repository": {
			"name": "Repo",
			"full_name": "Org/Repo",
			"clone_url": "https://github.com/Org/Repo.git"
		},
		"pusher": {
			"name": "user",
			"email": "user@example.com"
		}
	}`
	req := validPushRequest(t)
	req.Body = io.NopCloser(strings.NewReader(body))
	req.Header.Set("X-Hub-Signature-256", ComputeSignature([]byte(body), []byte("gh-secret")))
	result := h.handle(req)

	if result.reason == rejectAllowlist {
		t.Error("case-insensitive repo name should be allowed")
	}
	if !result.forwarded {
		t.Errorf("expected request to be forwarded, got reason %q", result.reason)
	}
}

func TestHandler_HealthzGET(t *testing.T) {
	backend := failingBackend(t)
	defer backend.Close()
	h := NewHandler(testConfig(backend.URL), testChecker(t, "127.0.0.0/8"), NewForwarder())

	req := httptest.NewRequest("GET", "/healthz", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected %d, got %d", http.StatusOK, rr.Code)
	}
}

func TestHandler_HealthzNoAuth(t *testing.T) {
	backend := failingBackend(t)
	defer backend.Close()
	h := NewHandler(testConfig(backend.URL), testChecker(t, "192.0.2.0/24"), NewForwarder())

	req := httptest.NewRequest("GET", "/healthz", nil)
	req.RemoteAddr = "10.0.0.1:12345"
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("healthz should not check IP, expected %d, got %d", http.StatusOK, rr.Code)
	}
}

func TestHandler_HealthzRejectsNonGET(t *testing.T) {
	backend := failingBackend(t)
	defer backend.Close()
	h := NewHandler(testConfig(backend.URL), testChecker(t, "127.0.0.0/8"), NewForwarder())

	methods := []string{"POST", "PUT", "DELETE", "PATCH"}
	for _, m := range methods {
		t.Run(m, func(t *testing.T) {
			req := httptest.NewRequest(m, "/healthz", nil)
			rr := httptest.NewRecorder()
			h.ServeHTTP(rr, req)
			if rr.Code != http.StatusMethodNotAllowed {
				t.Errorf("expected %d for %s, got %d", http.StatusMethodNotAllowed, m, rr.Code)
			}
		})
	}
}

func TestHandler_DoesNotForwardResponseBody(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Internal", "secret-info")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("internal backend response"))
	}))
	defer backend.Close()

	cfg := testConfig(backend.URL)
	h := NewHandler(cfg, testChecker(t, "127.0.0.0/8"), NewForwarder())

	req := validPushRequest(t)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected %d, got %d", http.StatusOK, rr.Code)
	}
	if rr.Body.Len() != 0 {
		t.Errorf("expected empty response body, got %q", rr.Body.String())
	}
	if rr.Header().Get("X-Internal") != "" {
		t.Error("backend headers leaked to client")
	}
}
