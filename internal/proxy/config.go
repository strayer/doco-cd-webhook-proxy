package proxy

import (
	"fmt"
	"log/slog"
	"net"
	"os"
	"strings"
	"time"
)

type Config struct {
	GitHubWebhookSecret       string
	DocoCDWebhookSecret       string
	DocoCDURL                 string
	AllowedRepos              []string
	ListenAddr                string
	TrustedProxyCIDRs         []*net.IPNet
	GitHubMetaRefreshInterval time.Duration
}

func LoadConfig() (*Config, error) {
	ghSecret, err := loadSecret("GITHUB_WEBHOOK_SECRET")
	if err != nil {
		return nil, err
	}

	cdSecret, err := loadSecret("WEBHOOK_SECRET")
	if err != nil {
		return nil, err
	}

	docoCDURL := os.Getenv("DOCO_CD_URL")
	if docoCDURL == "" {
		return nil, fmt.Errorf("required environment variable DOCO_CD_URL is not set")
	}

	allowedReposRaw := os.Getenv("ALLOWED_REPOS")
	if allowedReposRaw == "" {
		return nil, fmt.Errorf("required environment variable ALLOWED_REPOS is not set")
	}

	allowedRepos, err := parseAllowedRepos(allowedReposRaw)
	if err != nil {
		return nil, fmt.Errorf("invalid ALLOWED_REPOS: %w", err)
	}
	if len(allowedRepos) == 0 {
		return nil, fmt.Errorf("ALLOWED_REPOS is empty after parsing")
	}

	listenAddr := os.Getenv("LISTEN_ADDR")
	if listenAddr == "" {
		listenAddr = ":8080"
	}

	refreshInterval := time.Hour
	if v := os.Getenv("GITHUB_META_REFRESH_INTERVAL"); v != "" {
		refreshInterval, err = time.ParseDuration(v)
		if err != nil {
			return nil, fmt.Errorf("invalid GITHUB_META_REFRESH_INTERVAL: %w", err)
		}
		if refreshInterval <= 0 {
			return nil, fmt.Errorf("GITHUB_META_REFRESH_INTERVAL must be positive, got %s", refreshInterval)
		}
	}

	var trustedCIDRs []*net.IPNet
	if v := os.Getenv("TRUSTED_PROXY_CIDRS"); v != "" {
		trustedCIDRs, err = parseCIDRs(v)
		if err != nil {
			return nil, fmt.Errorf("invalid TRUSTED_PROXY_CIDRS: %w", err)
		}
	}

	if ghSecret == cdSecret {
		slog.Warn("GITHUB_WEBHOOK_SECRET and WEBHOOK_SECRET are identical — this is not recommended")
	}

	cfg := &Config{
		GitHubWebhookSecret:       ghSecret,
		DocoCDWebhookSecret:       cdSecret,
		DocoCDURL:                 docoCDURL,
		AllowedRepos:              allowedRepos,
		ListenAddr:                listenAddr,
		TrustedProxyCIDRs:         trustedCIDRs,
		GitHubMetaRefreshInterval: refreshInterval,
	}

	slog.Info("configuration loaded",
		"listen_addr", cfg.ListenAddr,
		"doco_cd_url", cfg.DocoCDURL,
		"allowed_repos", cfg.AllowedRepos,
		"trusted_proxy_cidrs", len(cfg.TrustedProxyCIDRs),
		"github_meta_refresh_interval", cfg.GitHubMetaRefreshInterval,
	)

	return cfg, nil
}

func loadSecret(envVar string) (string, error) {
	fileVar := envVar + "_FILE"

	if filePath := os.Getenv(fileVar); filePath != "" {
		data, err := os.ReadFile(filePath)
		if err != nil {
			return "", fmt.Errorf("reading %s from %q: %w", fileVar, filePath, err)
		}
		val := strings.TrimSpace(string(data))
		if val == "" {
			return "", fmt.Errorf("%s file %q is empty after trimming whitespace", fileVar, filePath)
		}
		return val, nil
	}

	val := os.Getenv(envVar)
	if val == "" {
		return "", fmt.Errorf("required environment variable %s is not set", envVar)
	}
	return val, nil
}

func parseAllowedRepos(raw string) ([]string, error) {
	parts := strings.Split(raw, ",")
	var repos []string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		p = strings.ToLower(p)
		if p == "" {
			continue
		}
		if !strings.Contains(p, "/") || strings.Count(p, "/") != 1 {
			return nil, fmt.Errorf("invalid repo format %q: expected owner/repo", p)
		}
		owner, repo, _ := strings.Cut(p, "/")
		if owner == "" || repo == "" {
			return nil, fmt.Errorf("invalid repo format %q: owner and repo must be non-empty", p)
		}
		repos = append(repos, p)
	}
	return repos, nil
}

func parseCIDRs(raw string) ([]*net.IPNet, error) {
	parts := strings.Split(raw, ",")
	var cidrs []*net.IPNet
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		_, cidr, err := net.ParseCIDR(p)
		if err != nil {
			return nil, fmt.Errorf("invalid CIDR %q: %w", p, err)
		}
		cidrs = append(cidrs, cidr)
	}
	return cidrs, nil
}
