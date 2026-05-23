package proxy

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestGitHubIPChecker_Check(t *testing.T) {
	meta := gitHubMeta{
		Hooks: []string{"192.30.252.0/22", "2620:0:860::/46"},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(meta)
	}))
	defer srv.Close()

	checker, err := NewGitHubIPChecker(srv.URL, 1*time.Hour)
	if err != nil {
		t.Fatalf("NewGitHubIPChecker: %v", err)
	}
	defer checker.Stop()

	tests := []struct {
		name string
		ip   string
		want bool
	}{
		{"IPv4 in range", "192.30.252.1", true},
		{"IPv4 out of range", "8.8.8.8", false},
		{"IPv6 in range", "2620:0:860::1", true},
		{"IPv6 out of range", "2001:db8::1", false},
		{"invalid IP", "not-an-ip", false},
		{"empty string", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := checker.Check(tt.ip); got != tt.want {
				t.Errorf("Check(%q) = %v, want %v", tt.ip, got, tt.want)
			}
		})
	}
}

func TestGitHubIPChecker_FailsOnInitialFetchError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	_, err := NewGitHubIPChecker(srv.URL, 1*time.Hour)
	if err == nil {
		t.Fatal("expected error for failing initial fetch")
	}
}

func TestGitHubIPChecker_FailsOnEmptyHooks(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(gitHubMeta{Hooks: []string{}})
	}))
	defer srv.Close()

	_, err := NewGitHubIPChecker(srv.URL, 1*time.Hour)
	if err == nil {
		t.Fatal("expected error for empty hooks CIDRs")
	}
}

func TestGitHubIPChecker_FailsOnInvalidCIDR(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(gitHubMeta{Hooks: []string{"not-a-cidr"}})
	}))
	defer srv.Close()

	_, err := NewGitHubIPChecker(srv.URL, 1*time.Hour)
	if err == nil {
		t.Fatal("expected error for invalid CIDR")
	}
}

func TestGitHubIPChecker_FailsOnUnreachableServer(t *testing.T) {
	_, err := NewGitHubIPChecker("http://127.0.0.1:1", 1*time.Hour)
	if err == nil {
		t.Fatal("expected error for unreachable server")
	}
}

func TestGitHubIPChecker_FailsOnInvalidJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("not json"))
	}))
	defer srv.Close()

	_, err := NewGitHubIPChecker(srv.URL, 1*time.Hour)
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestGitHubIPChecker_ETagCaching(t *testing.T) {
	var requestCount atomic.Int32
	etag := `"abc123"`
	meta := gitHubMeta{Hooks: []string{"10.0.0.0/8"}}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount.Add(1)
		if r.Header.Get("If-None-Match") == etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", etag)
		_ = json.NewEncoder(w).Encode(meta)
	}))
	defer srv.Close()

	checker, err := NewGitHubIPChecker(srv.URL, 50*time.Millisecond)
	if err != nil {
		t.Fatalf("NewGitHubIPChecker: %v", err)
	}
	defer checker.Stop()

	time.Sleep(150 * time.Millisecond)

	count := requestCount.Load()
	if count < 2 {
		t.Fatalf("expected at least 2 requests (initial + refresh), got %d", count)
	}

	if !checker.Check("10.0.0.1") {
		t.Error("Check should still work after 304 response")
	}
}

func TestGitHubIPChecker_RefreshKeepsLastKnownGood(t *testing.T) {
	var failRefresh atomic.Bool
	meta := gitHubMeta{Hooks: []string{"10.0.0.0/8"}}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if failRefresh.Load() {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_ = json.NewEncoder(w).Encode(meta)
	}))
	defer srv.Close()

	checker, err := NewGitHubIPChecker(srv.URL, 50*time.Millisecond)
	if err != nil {
		t.Fatalf("NewGitHubIPChecker: %v", err)
	}
	defer checker.Stop()

	failRefresh.Store(true)
	time.Sleep(150 * time.Millisecond)

	if !checker.Check("10.0.0.1") {
		t.Error("Check should use last-known-good ranges after refresh failure")
	}
}

func TestGitHubIPChecker_RefreshUpdatesRanges(t *testing.T) {
	var useNewRanges atomic.Bool

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if useNewRanges.Load() {
			_ = json.NewEncoder(w).Encode(gitHubMeta{Hooks: []string{"172.16.0.0/12"}})
			return
		}
		_ = json.NewEncoder(w).Encode(gitHubMeta{Hooks: []string{"10.0.0.0/8"}})
	}))
	defer srv.Close()

	checker, err := NewGitHubIPChecker(srv.URL, 50*time.Millisecond)
	if err != nil {
		t.Fatalf("NewGitHubIPChecker: %v", err)
	}
	defer checker.Stop()

	if !checker.Check("10.0.0.1") {
		t.Error("10.0.0.1 should be allowed initially")
	}

	useNewRanges.Store(true)
	time.Sleep(150 * time.Millisecond)

	if !checker.Check("172.16.0.1") {
		t.Error("172.16.0.1 should be allowed after refresh")
	}
	if checker.Check("10.0.0.1") {
		t.Error("10.0.0.1 should no longer be allowed after refresh")
	}
}

func TestGitHubIPChecker_RefreshKeepsLastKnownGoodOnEmptyHooks(t *testing.T) {
	var returnEmpty atomic.Bool

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if returnEmpty.Load() {
			_ = json.NewEncoder(w).Encode(gitHubMeta{Hooks: []string{}})
			return
		}
		_ = json.NewEncoder(w).Encode(gitHubMeta{Hooks: []string{"10.0.0.0/8"}})
	}))
	defer srv.Close()

	checker, err := NewGitHubIPChecker(srv.URL, 50*time.Millisecond)
	if err != nil {
		t.Fatalf("NewGitHubIPChecker: %v", err)
	}
	defer checker.Stop()

	returnEmpty.Store(true)
	time.Sleep(150 * time.Millisecond)

	if !checker.Check("10.0.0.1") {
		t.Error("Check should use last-known-good ranges when refresh returns empty hooks")
	}
}

func TestExtractClientIP(t *testing.T) {
	tests := []struct {
		name              string
		remoteAddr        string
		xForwardedFor     string
		trustedProxyCIDRs []*net.IPNet
		want              string
	}{
		{
			name:       "direct connection uses RemoteAddr",
			remoteAddr: "203.0.113.1:12345",
			want:       "203.0.113.1",
		},
		{
			name:       "RemoteAddr without port",
			remoteAddr: "203.0.113.1",
			want:       "203.0.113.1",
		},
		{
			name:              "no trusted proxies ignores X-Forwarded-For",
			remoteAddr:        "203.0.113.1:12345",
			xForwardedFor:     "10.0.0.1",
			trustedProxyCIDRs: nil,
			want:              "203.0.113.1",
		},
		{
			name:              "trusted proxy uses last untrusted X-Forwarded-For",
			remoteAddr:        "10.0.0.1:12345",
			xForwardedFor:     "203.0.113.50, 10.0.0.2",
			trustedProxyCIDRs: mustParseCIDRs("10.0.0.0/8"),
			want:              "203.0.113.50",
		},
		{
			name:              "multiple trusted proxies in chain",
			remoteAddr:        "10.0.0.1:12345",
			xForwardedFor:     "198.51.100.1, 10.0.0.3, 10.0.0.2",
			trustedProxyCIDRs: mustParseCIDRs("10.0.0.0/8"),
			want:              "198.51.100.1",
		},
		{
			name:              "all X-Forwarded-For entries are trusted falls back to leftmost",
			remoteAddr:        "10.0.0.1:12345",
			xForwardedFor:     "10.0.0.5, 10.0.0.4",
			trustedProxyCIDRs: mustParseCIDRs("10.0.0.0/8"),
			want:              "10.0.0.5",
		},
		{
			name:              "RemoteAddr not in trusted CIDRs ignores X-Forwarded-For",
			remoteAddr:        "203.0.113.1:12345",
			xForwardedFor:     "198.51.100.1",
			trustedProxyCIDRs: mustParseCIDRs("10.0.0.0/8"),
			want:              "203.0.113.1",
		},
		{
			name:              "empty X-Forwarded-For with trusted proxy uses RemoteAddr",
			remoteAddr:        "10.0.0.1:12345",
			xForwardedFor:     "",
			trustedProxyCIDRs: mustParseCIDRs("10.0.0.0/8"),
			want:              "10.0.0.1",
		},
		{
			name:       "IPv6 RemoteAddr",
			remoteAddr: "[2001:db8::1]:12345",
			want:       "2001:db8::1",
		},
		{
			name:              "IPv6 trusted proxy with X-Forwarded-For",
			remoteAddr:        "[fd00::1]:12345",
			xForwardedFor:     "203.0.113.1",
			trustedProxyCIDRs: mustParseCIDRs("fd00::/8"),
			want:              "203.0.113.1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "/", nil)
			r.RemoteAddr = tt.remoteAddr
			if tt.xForwardedFor != "" {
				r.Header.Set("X-Forwarded-For", tt.xForwardedFor)
			}

			got := ExtractClientIP(r, tt.trustedProxyCIDRs)
			if got != tt.want {
				t.Errorf("ExtractClientIP() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestExtractClientIP_MultipleXFFHeaders(t *testing.T) {
	trustedCIDRs := mustParseCIDRs("10.0.0.0/8")

	t.Run("attacker spoofs XFF header and proxy appends separate header", func(t *testing.T) {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = "10.0.0.1:12345"
		// Attacker sends: X-Forwarded-For: 192.30.252.1 (spoofed GitHub IP)
		// Proxy appends:  X-Forwarded-For: 203.0.113.50 (real client IP)
		r.Header.Add("X-Forwarded-For", "192.30.252.1")
		r.Header.Add("X-Forwarded-For", "203.0.113.50")

		got := ExtractClientIP(r, trustedCIDRs)
		if got != "203.0.113.50" {
			t.Errorf("ExtractClientIP() = %q, want %q (must use real client IP, not spoofed)", got, "203.0.113.50")
		}
	})

	t.Run("multiple XFF headers with trusted proxies in chain", func(t *testing.T) {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = "10.0.0.1:12345"
		r.Header.Add("X-Forwarded-For", "198.51.100.1")
		r.Header.Add("X-Forwarded-For", "10.0.0.3, 10.0.0.2")

		got := ExtractClientIP(r, trustedCIDRs)
		if got != "198.51.100.1" {
			t.Errorf("ExtractClientIP() = %q, want %q", got, "198.51.100.1")
		}
	})
}

func mustParseCIDRs(cidrs ...string) []*net.IPNet {
	var result []*net.IPNet
	for _, c := range cidrs {
		_, cidr, err := net.ParseCIDR(c)
		if err != nil {
			panic(err)
		}
		result = append(result, cidr)
	}
	return result
}
