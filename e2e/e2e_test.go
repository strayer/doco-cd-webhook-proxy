//go:build e2e

package e2e

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	proxy "github.com/strayer/doco-cd-webhook-proxy/internal/proxy"
)

var binaryPath string

func TestMain(m *testing.M) {
	tmp, err := os.CreateTemp("", "doco-cd-webhook-proxy-*")
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to create temp file: %v\n", err)
		os.Exit(1)
	}
	tmp.Close()
	binaryPath = tmp.Name()

	cmd := exec.Command("go", "build", "-o", binaryPath, "../cmd/proxy")
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "failed to build binary: %v\n", err)
		os.Exit(1)
	}

	code := m.Run()
	os.Remove(binaryPath)
	os.Exit(code)
}

type docoCDCapture struct {
	mu       sync.Mutex
	body     []byte
	headers  http.Header
	method   string
	path     string
	called   bool
	statusFn func() int
}

func (c *docoCDCapture) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c.mu.Lock()
		defer c.mu.Unlock()
		c.called = true
		c.method = r.Method
		c.path = r.URL.Path
		c.headers = r.Header.Clone()
		c.body, _ = io.ReadAll(r.Body)
		status := http.StatusOK
		if c.statusFn != nil {
			status = c.statusFn()
		}
		w.WriteHeader(status)
	})
}

func (c *docoCDCapture) wasCalled() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.called
}

func (c *docoCDCapture) getBody() []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.body
}

func (c *docoCDCapture) getHeaders() http.Header {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.headers
}

func (c *docoCDCapture) getPath() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.path
}

func githubMetaServer(cidrs []string) *httptest.Server {
	meta := map[string][]string{"hooks": cidrs}
	data, _ := json.Marshal(meta)
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write(data)
	}))
}

type proxyProcess struct {
	cmd  *exec.Cmd
	addr string
}

func minimalEnv() []string {
	return []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + os.Getenv("HOME"),
	}
}

func startProxy(t *testing.T, env map[string]string) *proxyProcess {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()

	cmd := exec.Command(binaryPath)
	cmd.Env = append(minimalEnv(), mapToEnv(env)...)
	cmd.Env = append(cmd.Env, "LISTEN_ADDR="+addr)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		t.Fatalf("failed to start proxy: %v", err)
	}

	waitForReady(t, addr)

	t.Cleanup(func() {
		cmd.Process.Signal(syscall.SIGTERM)
		cmd.Wait()
	})

	return &proxyProcess{cmd: cmd, addr: addr}
}

func mapToEnv(m map[string]string) []string {
	env := make([]string, 0, len(m))
	for k, v := range m {
		env = append(env, k+"="+v)
	}
	return env
}

func waitForReady(t *testing.T, addr string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 100*time.Millisecond)
		if err == nil {
			conn.Close()
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("proxy at %s did not become ready", addr)
}

func baseEnv(metaURL, docoCDURL string) map[string]string {
	return map[string]string{
		"GITHUB_WEBHOOK_SECRET":        "test-gh-secret",
		"WEBHOOK_SECRET":               "test-cd-secret",
		"DOCO_CD_URL":                  docoCDURL,
		"ALLOWED_REPOS":                "myorg/myrepo,myorg/other-repo",
		"GITHUB_META_URL":              metaURL,
		"GITHUB_META_REFRESH_INTERVAL": "1h",
	}
}

const pushPayload = `{
	"ref": "refs/heads/main",
	"before": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	"after": "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
	"repository": {
		"name": "myrepo",
		"full_name": "myorg/myrepo",
		"clone_url": "https://github.com/myorg/myrepo.git"
	},
	"pusher": {
		"name": "deployer",
		"email": "deployer@example.com"
	}
}`

func sendWebhook(t *testing.T, addr, event, body, secret string) *http.Response {
	t.Helper()
	sig := proxy.ComputeSignature([]byte(body), []byte(secret))
	req, err := http.NewRequest("POST", "http://"+addr+"/webhook", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-GitHub-Event", event)
	req.Header.Set("X-Hub-Signature-256", sig)

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	return resp
}

func TestE2E_PushEventForwarded(t *testing.T) {
	meta := githubMetaServer([]string{"127.0.0.0/8"})
	defer meta.Close()

	capture := &docoCDCapture{}
	docoCD := httptest.NewServer(capture.handler())
	defer docoCD.Close()

	p := startProxy(t, baseEnv(meta.URL, docoCD.URL))

	resp := sendWebhook(t, p.addr, "push", pushPayload, "test-gh-secret")
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	if !capture.wasCalled() {
		t.Fatal("doco-cd was never called")
	}

	if capture.getPath() != "/v1/webhook" {
		t.Errorf("expected path /v1/webhook, got %q", capture.getPath())
	}

	headers := capture.getHeaders()
	if headers.Get("X-GitHub-Event") != "push" {
		t.Errorf("expected X-GitHub-Event push, got %q", headers.Get("X-GitHub-Event"))
	}
	if headers.Get("Content-Type") != "application/json" {
		t.Errorf("expected Content-Type application/json, got %q", headers.Get("Content-Type"))
	}

	body := capture.getBody()
	if err := proxy.VerifySignature(body, []byte("test-cd-secret"), headers.Get("X-Hub-Signature-256")); err != nil {
		t.Errorf("outgoing signature verification failed: %v", err)
	}

	event, err := proxy.ParsePayload(body)
	if err != nil {
		t.Fatalf("forwarded body is not valid payload: %v", err)
	}
	if event.Repository.FullName != "myorg/myrepo" {
		t.Errorf("expected repo myorg/myrepo, got %q", event.Repository.FullName)
	}
	if event.Ref != "refs/heads/main" {
		t.Errorf("expected ref refs/heads/main, got %q", event.Ref)
	}
}

func TestE2E_PingEventNotForwarded(t *testing.T) {
	meta := githubMetaServer([]string{"127.0.0.0/8"})
	defer meta.Close()

	capture := &docoCDCapture{}
	docoCD := httptest.NewServer(capture.handler())
	defer docoCD.Close()

	p := startProxy(t, baseEnv(meta.URL, docoCD.URL))

	body := `{}`
	resp := sendWebhook(t, p.addr, "ping", body, "test-gh-secret")
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}

	time.Sleep(50 * time.Millisecond)
	if capture.wasCalled() {
		t.Error("doco-cd should not be called for ping events")
	}
}

func TestE2E_InvalidSignatureRejected(t *testing.T) {
	meta := githubMetaServer([]string{"127.0.0.0/8"})
	defer meta.Close()

	capture := &docoCDCapture{}
	docoCD := httptest.NewServer(capture.handler())
	defer docoCD.Close()

	p := startProxy(t, baseEnv(meta.URL, docoCD.URL))

	resp := sendWebhook(t, p.addr, "push", pushPayload, "wrong-secret")
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("expected 403, got %d", resp.StatusCode)
	}
	if capture.wasCalled() {
		t.Error("doco-cd should not be called for invalid signatures")
	}
}

func TestE2E_DisallowedRepoRejected(t *testing.T) {
	meta := githubMetaServer([]string{"127.0.0.0/8"})
	defer meta.Close()

	capture := &docoCDCapture{}
	docoCD := httptest.NewServer(capture.handler())
	defer docoCD.Close()

	p := startProxy(t, baseEnv(meta.URL, docoCD.URL))

	body := `{
		"ref": "refs/heads/main",
		"before": "aaaa",
		"after": "bbbb",
		"repository": {
			"name": "evil",
			"full_name": "hacker/evil",
			"clone_url": "https://github.com/hacker/evil.git"
		},
		"pusher": {
			"name": "user",
			"email": "user@example.com"
		}
	}`
	resp := sendWebhook(t, p.addr, "push", body, "test-gh-secret")
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("expected 403, got %d", resp.StatusCode)
	}
	if capture.wasCalled() {
		t.Error("doco-cd should not be called for disallowed repos")
	}
}

func TestE2E_WrongMethodRejected(t *testing.T) {
	meta := githubMetaServer([]string{"127.0.0.0/8"})
	defer meta.Close()

	capture := &docoCDCapture{}
	docoCD := httptest.NewServer(capture.handler())
	defer docoCD.Close()

	p := startProxy(t, baseEnv(meta.URL, docoCD.URL))

	resp, err := http.Get("http://" + p.addr + "/webhook")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", resp.StatusCode)
	}
	if capture.wasCalled() {
		t.Error("doco-cd should not be called for wrong HTTP method")
	}
}

func TestE2E_UnregisteredPathReturns404(t *testing.T) {
	meta := githubMetaServer([]string{"127.0.0.0/8"})
	defer meta.Close()

	capture := &docoCDCapture{}
	docoCD := httptest.NewServer(capture.handler())
	defer docoCD.Close()

	p := startProxy(t, baseEnv(meta.URL, docoCD.URL))

	resp, err := http.Get("http://" + p.addr + "/nonexistent")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404, got %d", resp.StatusCode)
	}
}

func TestE2E_BackendErrorPropagated(t *testing.T) {
	meta := githubMetaServer([]string{"127.0.0.0/8"})
	defer meta.Close()

	capture := &docoCDCapture{statusFn: func() int { return http.StatusServiceUnavailable }}
	docoCD := httptest.NewServer(capture.handler())
	defer docoCD.Close()

	p := startProxy(t, baseEnv(meta.URL, docoCD.URL))

	resp := sendWebhook(t, p.addr, "push", pushPayload, "test-gh-secret")
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d", resp.StatusCode)
	}
}

func TestE2E_GracefulShutdown(t *testing.T) {
	meta := githubMetaServer([]string{"127.0.0.0/8"})
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
	ln.Close()

	env := baseEnv(meta.URL, docoCD.URL)
	cmd := exec.Command(binaryPath)
	cmd.Env = append(minimalEnv(), mapToEnv(env)...)
	cmd.Env = append(cmd.Env, "LISTEN_ADDR="+addr)

	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}

	waitForReady(t, addr)

	cmd.Process.Signal(syscall.SIGTERM)

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("expected clean exit, got: %v", err)
		}
	case <-time.After(10 * time.Second):
		cmd.Process.Kill()
		t.Fatal("process did not exit within timeout after SIGTERM")
	}
}

func TestE2E_SecretFromFile(t *testing.T) {
	meta := githubMetaServer([]string{"127.0.0.0/8"})
	defer meta.Close()

	capture := &docoCDCapture{}
	docoCD := httptest.NewServer(capture.handler())
	defer docoCD.Close()

	ghSecretFile, err := os.CreateTemp("", "gh-secret-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(ghSecretFile.Name())
	ghSecretFile.WriteString("file-gh-secret\n")
	ghSecretFile.Close()

	cdSecretFile, err := os.CreateTemp("", "cd-secret-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(cdSecretFile.Name())
	cdSecretFile.WriteString("  file-cd-secret  \n")
	cdSecretFile.Close()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()

	env := map[string]string{
		"GITHUB_WEBHOOK_SECRET_FILE":   ghSecretFile.Name(),
		"WEBHOOK_SECRET_FILE":          cdSecretFile.Name(),
		"DOCO_CD_URL":                  docoCD.URL,
		"ALLOWED_REPOS":                "myorg/myrepo",
		"GITHUB_META_URL":              meta.URL,
		"GITHUB_META_REFRESH_INTERVAL": "1h",
		"LISTEN_ADDR":                  addr,
	}

	cmd := exec.Command(binaryPath)
	cmd.Env = append(minimalEnv(), mapToEnv(env)...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		cmd.Process.Signal(syscall.SIGTERM)
		cmd.Wait()
	}()

	waitForReady(t, addr)

	resp := sendWebhook(t, addr, "push", pushPayload, "file-gh-secret")
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if !capture.wasCalled() {
		t.Fatal("doco-cd was never called")
	}

	body := capture.getBody()
	headers := capture.getHeaders()
	if err := proxy.VerifySignature(body, []byte("file-cd-secret"), headers.Get("X-Hub-Signature-256")); err != nil {
		t.Errorf("outgoing signature with file-based secret failed: %v", err)
	}
}

func TestE2E_MissingConfigExitsNonZero(t *testing.T) {
	cmd := exec.Command(binaryPath)
	cmd.Env = minimalEnv()

	err := cmd.Run()
	if err == nil {
		t.Fatal("expected non-zero exit with missing config")
	}
	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("expected ExitError, got %T: %v", err, err)
	}
	if exitErr.ExitCode() == 0 {
		t.Error("expected non-zero exit code")
	}
}

func TestE2E_ResponseBodyNotLeaked(t *testing.T) {
	meta := githubMetaServer([]string{"127.0.0.0/8"})
	defer meta.Close()

	docoCD := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Internal-Secret", "should-not-leak")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("internal backend data"))
	}))
	defer docoCD.Close()

	p := startProxy(t, baseEnv(meta.URL, docoCD.URL))

	resp := sendWebhook(t, p.addr, "push", pushPayload, "test-gh-secret")
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if len(respBody) != 0 {
		t.Errorf("expected empty response body, got %q", string(respBody))
	}
	if resp.Header.Get("X-Internal-Secret") != "" {
		t.Error("backend internal headers leaked to client")
	}
}

func TestE2E_MetaURLWhitespaceTrimmed(t *testing.T) {
	meta := githubMetaServer([]string{"127.0.0.0/8"})
	defer meta.Close()

	capture := &docoCDCapture{}
	docoCD := httptest.NewServer(capture.handler())
	defer docoCD.Close()

	env := baseEnv(meta.URL, docoCD.URL)
	env["GITHUB_META_URL"] = "  " + meta.URL + "\n"

	p := startProxy(t, env)

	resp := sendWebhook(t, p.addr, "push", pushPayload, "test-gh-secret")
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d — meta URL trimming may have failed", resp.StatusCode)
	}
	if !capture.wasCalled() {
		t.Error("request should have been forwarded after meta URL trimming")
	}
}

func TestE2E_MultipleAllowedRepos(t *testing.T) {
	meta := githubMetaServer([]string{"127.0.0.0/8"})
	defer meta.Close()

	capture := &docoCDCapture{}
	docoCD := httptest.NewServer(capture.handler())
	defer docoCD.Close()

	p := startProxy(t, baseEnv(meta.URL, docoCD.URL))

	body := `{
		"ref": "refs/heads/develop",
		"before": "cccc",
		"after": "dddd",
		"repository": {
			"name": "other-repo",
			"full_name": "myorg/other-repo",
			"clone_url": "https://github.com/myorg/other-repo.git"
		},
		"pusher": {
			"name": "dev",
			"email": "dev@example.com"
		}
	}`
	resp := sendWebhook(t, p.addr, "push", body, "test-gh-secret")
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
	if !capture.wasCalled() {
		t.Error("second allowed repo should be forwarded")
	}
}
