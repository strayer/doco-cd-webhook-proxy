package proxy

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func validTestEvent() GitHubPushEvent {
	return GitHubPushEvent{
		Ref:    "refs/heads/main",
		Before: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		After:  "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		Repository: Repository{
			Name:     "my-repo",
			FullName: "org/my-repo",
			CloneURL: "https://github.com/org/my-repo.git",
		},
		Pusher: Pusher{
			Name:  "octocat",
			Email: "octocat@github.com",
		},
	}
}

func TestNewForwarder(t *testing.T) {
	f := NewForwarder()

	if f.client == nil {
		t.Fatal("expected non-nil http client")
	}

	if f.client.Timeout.Seconds() != 15 {
		t.Errorf("expected 15s timeout, got %v", f.client.Timeout)
	}
}

func TestForwarderNoRedirects(t *testing.T) {
	redirectTarget := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer redirectTarget.Close()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, redirectTarget.URL, http.StatusFound)
	}))
	defer server.Close()

	f := NewForwarder()
	_, err := f.Forward(validTestEvent(), server.URL, []byte("secret"))
	if err == nil {
		t.Fatal("expected error when server redirects")
	}
}

func TestForwarderSuccess(t *testing.T) {
	secret := []byte("test-secret")
	event := validTestEvent()

	var receivedBody []byte
	var receivedHeaders http.Header
	var receivedMethod string
	var receivedPath string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedMethod = r.Method
		receivedPath = r.URL.Path
		receivedHeaders = r.Header
		var err error
		receivedBody, err = io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("failed to read body: %v", err)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	f := NewForwarder()
	statusCode, err := f.Forward(event, server.URL, secret)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if statusCode != http.StatusOK {
		t.Errorf("expected status 200, got %d", statusCode)
	}

	if receivedMethod != http.MethodPost {
		t.Errorf("expected POST, got %s", receivedMethod)
	}

	if receivedPath != "/v1/webhook" {
		t.Errorf("expected /v1/webhook, got %s", receivedPath)
	}

	if receivedHeaders.Get("Content-Type") != "application/json" {
		t.Errorf("expected application/json content-type, got %s", receivedHeaders.Get("Content-Type"))
	}

	if receivedHeaders.Get("X-GitHub-Event") != "push" {
		t.Errorf("expected push event header, got %s", receivedHeaders.Get("X-GitHub-Event"))
	}

	sigHeader := receivedHeaders.Get("X-Hub-Signature-256")
	if err := VerifySignature(receivedBody, secret, sigHeader); err != nil {
		t.Errorf("signature verification failed: %v", err)
	}

	var receivedEvent GitHubPushEvent
	if err := json.Unmarshal(receivedBody, &receivedEvent); err != nil {
		t.Fatalf("failed to unmarshal received body: %v", err)
	}

	if receivedEvent.Repository.FullName != event.Repository.FullName {
		t.Errorf("expected repo %s, got %s", event.Repository.FullName, receivedEvent.Repository.FullName)
	}
}

func TestForwarderReturnsBackendStatusCode(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
	}{
		{"ok", http.StatusOK},
		{"accepted", http.StatusAccepted},
		{"bad request", http.StatusBadRequest},
		{"internal server error", http.StatusInternalServerError},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.statusCode)
			}))
			defer server.Close()

			f := NewForwarder()
			code, err := f.Forward(validTestEvent(), server.URL, []byte("secret"))
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if code != tt.statusCode {
				t.Errorf("expected %d, got %d", tt.statusCode, code)
			}
		})
	}
}

func TestForwarderUnreachableBackend(t *testing.T) {
	f := NewForwarder()
	_, err := f.Forward(validTestEvent(), "http://127.0.0.1:1", []byte("secret"))
	if err == nil {
		t.Fatal("expected error for unreachable backend")
	}
}

func TestForwarderURLPath(t *testing.T) {
	tests := []struct {
		name     string
		baseURL  string
		wantPath string
	}{
		{"no trailing slash", "URL_PLACEHOLDER", "/v1/webhook"},
		{"with trailing slash", "URL_PLACEHOLDER/", "/v1/webhook"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var receivedPath string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				receivedPath = r.URL.Path
				w.WriteHeader(http.StatusOK)
			}))
			defer server.Close()

			baseURL := server.URL
			if tt.name == "with trailing slash" {
				baseURL += "/"
			}

			f := NewForwarder()
			_, err := f.Forward(validTestEvent(), baseURL, []byte("secret"))
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if receivedPath != tt.wantPath {
				t.Errorf("expected path %s, got %s", tt.wantPath, receivedPath)
			}
		})
	}
}

func TestForwarderSignatureOverMarshaledBytes(t *testing.T) {
	secret := []byte("signing-secret")
	event := validTestEvent()

	var receivedBody []byte
	var receivedSig string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var err error
		receivedBody, err = io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("failed to read body: %v", err)
		}
		receivedSig = r.Header.Get("X-Hub-Signature-256")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	f := NewForwarder()
	_, err := f.Forward(event, server.URL, secret)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	expectedSig := ComputeSignature(receivedBody, secret)
	if receivedSig != expectedSig {
		t.Errorf("signature mismatch: got %s, want %s", receivedSig, expectedSig)
	}
}
