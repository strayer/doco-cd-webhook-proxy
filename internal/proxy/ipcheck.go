package proxy

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

type gitHubMeta struct {
	Hooks []string `json:"hooks"`
}

type GitHubIPChecker struct {
	metaURL  string
	interval time.Duration
	client   *http.Client
	etag     string

	mu    sync.RWMutex
	cidrs []*net.IPNet

	stopCh chan struct{}
	done   chan struct{}
}

func NewGitHubIPChecker(metaURL string, refreshInterval time.Duration) (*GitHubIPChecker, error) {
	c := &GitHubIPChecker{
		metaURL:  metaURL,
		interval: refreshInterval,
		client:   &http.Client{Timeout: 10 * time.Second},
		stopCh:   make(chan struct{}),
		done:     make(chan struct{}),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cidrs, etag, err := c.fetch(ctx, "")
	if err != nil {
		return nil, fmt.Errorf("initial GitHub meta fetch: %w", err)
	}
	if len(cidrs) == 0 {
		return nil, fmt.Errorf("GitHub meta returned no hooks CIDRs")
	}

	c.cidrs = cidrs
	c.etag = etag

	go c.refreshLoop()

	return c, nil
}

func (c *GitHubIPChecker) Stop() {
	close(c.stopCh)
	<-c.done
}

func (c *GitHubIPChecker) Check(ip string) bool {
	parsed := net.ParseIP(ip)
	if parsed == nil {
		return false
	}

	c.mu.RLock()
	defer c.mu.RUnlock()

	for _, cidr := range c.cidrs {
		if cidr.Contains(parsed) {
			return true
		}
	}
	return false
}

func (c *GitHubIPChecker) fetch(ctx context.Context, etag string) ([]*net.IPNet, string, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", c.metaURL, nil)
	if err != nil {
		return nil, "", err
	}
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusNotModified {
		return nil, etag, nil
	}

	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("unexpected status %d", resp.StatusCode)
	}

	var meta gitHubMeta
	if err := json.NewDecoder(resp.Body).Decode(&meta); err != nil {
		return nil, "", fmt.Errorf("decoding GitHub meta: %w", err)
	}

	if len(meta.Hooks) == 0 {
		return nil, "", fmt.Errorf("GitHub meta returned no hooks CIDRs")
	}

	cidrs := make([]*net.IPNet, 0, len(meta.Hooks))
	for _, cidrStr := range meta.Hooks {
		_, cidr, err := net.ParseCIDR(cidrStr)
		if err != nil {
			return nil, "", fmt.Errorf("invalid CIDR %q: %w", cidrStr, err)
		}
		cidrs = append(cidrs, cidr)
	}

	newETag := resp.Header.Get("ETag")
	return cidrs, newETag, nil
}

func (c *GitHubIPChecker) refreshLoop() {
	defer close(c.done)
	ticker := time.NewTicker(c.interval)
	defer ticker.Stop()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		select {
		case <-c.stopCh:
			cancel()
		case <-ctx.Done():
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			cidrs, newETag, err := c.fetch(ctx, c.etag)
			if err != nil {
				if ctx.Err() != nil {
					return
				}
				slog.Error("failed to refresh GitHub IP ranges, keeping last-known-good", "error", err)
				continue
			}
			c.etag = newETag
			if cidrs != nil {
				c.mu.Lock()
				c.cidrs = cidrs
				c.mu.Unlock()
				slog.Info("refreshed GitHub IP ranges", "cidr_count", len(cidrs))
			}
		}
	}
}

func ExtractClientIP(r *http.Request, trustedProxyCIDRs []*net.IPNet) string {
	remoteIP := stripPort(r.RemoteAddr)

	if len(trustedProxyCIDRs) == 0 {
		return remoteIP
	}

	if !ipInCIDRs(remoteIP, trustedProxyCIDRs) {
		return remoteIP
	}

	var parts []string
	for _, xff := range r.Header.Values("X-Forwarded-For") {
		for _, entry := range strings.Split(xff, ",") {
			if trimmed := strings.TrimSpace(entry); trimmed != "" {
				parts = append(parts, trimmed)
			}
		}
	}
	if len(parts) == 0 {
		return remoteIP
	}

	for i := len(parts) - 1; i >= 0; i-- {
		if !ipInCIDRs(parts[i], trustedProxyCIDRs) {
			return parts[i]
		}
	}

	return parts[0]
}

func stripPort(addr string) string {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	return host
}

func ipInCIDRs(ip string, cidrs []*net.IPNet) bool {
	parsed := net.ParseIP(ip)
	if parsed == nil {
		return false
	}
	for _, cidr := range cidrs {
		if cidr.Contains(parsed) {
			return true
		}
	}
	return false
}
