package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	proxy "github.com/strayer/doco-cd-webhook-proxy/internal/proxy"
)

func metaServer(t *testing.T, cidrs []string) *httptest.Server {
	t.Helper()
	meta := map[string][]string{"hooks": cidrs}
	data, err := json.Marshal(meta)
	if err != nil {
		t.Fatal(err)
	}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(data)
	}))
}

func testCfg(docoCDURL string) *proxy.Config {
	return &proxy.Config{
		GitHubWebhookSecret:       "gh-secret",
		DocoCDWebhookSecret:       "cd-secret",
		DocoCDURL:                 docoCDURL,
		AllowedRepos:              []string{"org/repo"},
		ListenAddr:                "127.0.0.1:0",
		GitHubMetaRefreshInterval: time.Hour,
	}
}

func TestRun_GitHubMetaFetchFailure(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err = run(ctx, testCfg("http://127.0.0.1:1"), "http://127.0.0.1:1/meta", ln)
	if err == nil {
		t.Fatal("expected error for unreachable GitHub meta")
	}
	if !strings.Contains(err.Error(), "IP checker") {
		t.Errorf("expected error about IP checker, got: %v", err)
	}
}

func TestRun_GracefulShutdown(t *testing.T) {
	meta := metaServer(t, []string{"127.0.0.0/8"})
	defer meta.Close()

	docoCD := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer docoCD.Close()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()

	ctx, cancel := context.WithCancel(context.Background())

	errCh := make(chan error, 1)
	go func() {
		errCh <- run(ctx, testCfg(docoCD.URL), meta.URL, ln)
	}()

	waitForServer(t, addr)

	cancel()

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("expected clean shutdown, got: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("shutdown timed out")
	}
}

func TestRun_EndToEnd(t *testing.T) {
	meta := metaServer(t, []string{"127.0.0.0/8"})
	defer meta.Close()

	var receivedBody []byte
	var receivedHeaders http.Header
	docoCD := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedHeaders = r.Header.Clone()
		receivedBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer docoCD.Close()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- run(ctx, testCfg(docoCD.URL), meta.URL, ln)
	}()

	waitForServer(t, addr)

	payload := `{
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
	sig := proxy.ComputeSignature([]byte(payload), []byte("gh-secret"))

	req, err := http.NewRequest("POST", fmt.Sprintf("http://%s/webhook", addr), strings.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-GitHub-Event", "push")
	req.Header.Set("X-Hub-Signature-256", sig)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected %d, got %d", http.StatusOK, resp.StatusCode)
	}

	if receivedHeaders == nil {
		t.Fatal("doco-cd never received the forwarded request")
	}
	if receivedHeaders.Get("X-GitHub-Event") != "push" {
		t.Errorf("expected X-GitHub-Event push, got %q", receivedHeaders.Get("X-GitHub-Event"))
	}
	if err := proxy.VerifySignature(receivedBody, []byte("cd-secret"), receivedHeaders.Get("X-Hub-Signature-256")); err != nil {
		t.Errorf("outgoing signature verification failed: %v", err)
	}

	cancel()
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("expected clean shutdown, got: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("shutdown timed out")
	}
}

func TestRun_PingEvent(t *testing.T) {
	meta := metaServer(t, []string{"127.0.0.0/8"})
	defer meta.Close()

	docoCD := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("doco-cd should not receive ping events")
	}))
	defer docoCD.Close()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- run(ctx, testCfg(docoCD.URL), meta.URL, ln)
	}()

	waitForServer(t, addr)

	body := `{}`
	sig := proxy.ComputeSignature([]byte(body), []byte("gh-secret"))

	req, err := http.NewRequest("POST", fmt.Sprintf("http://%s/webhook", addr), strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-GitHub-Event", "ping")
	req.Header.Set("X-Hub-Signature-256", sig)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected %d, got %d", http.StatusOK, resp.StatusCode)
	}

	cancel()
	<-errCh
}

func TestRun_RejectsWrongMethod(t *testing.T) {
	meta := metaServer(t, []string{"127.0.0.0/8"})
	defer meta.Close()

	docoCD := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("doco-cd should not be called for wrong method")
	}))
	defer docoCD.Close()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- run(ctx, testCfg(docoCD.URL), meta.URL, ln)
	}()

	waitForServer(t, addr)

	resp, err := http.Get(fmt.Sprintf("http://%s/webhook", addr))
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("expected %d, got %d", http.StatusMethodNotAllowed, resp.StatusCode)
	}

	cancel()
	<-errCh
}

func waitForServer(t *testing.T, addr string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 100*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("server at %s did not become ready", addr)
}
