package proxy

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func clearConfigEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{
		"GITHUB_WEBHOOK_SECRET", "GITHUB_WEBHOOK_SECRET_FILE",
		"DOCO_CD_WEBHOOK_SECRET", "DOCO_CD_WEBHOOK_SECRET_FILE",
		"DOCO_CD_URL",
		"ALLOWED_REPOS",
		"LISTEN_ADDR",
		"TRUSTED_PROXY_CIDRS",
		"GITHUB_META_REFRESH_INTERVAL",
	} {
		t.Setenv(key, "")
		if err := os.Unsetenv(key); err != nil {
			t.Fatalf("failed to unset %s: %v", key, err)
		}
	}
}

func setRequiredEnv(t *testing.T) {
	t.Helper()
	t.Setenv("GITHUB_WEBHOOK_SECRET", "gh-secret")
	t.Setenv("DOCO_CD_WEBHOOK_SECRET", "cd-secret")
	t.Setenv("DOCO_CD_URL", "http://doco-cd:80")
	t.Setenv("ALLOWED_REPOS", "org/repo1")
}

func TestLoadRequiredVars(t *testing.T) {
	clearConfigEnv(t)

	required := []string{
		"GITHUB_WEBHOOK_SECRET",
		"DOCO_CD_WEBHOOK_SECRET",
		"DOCO_CD_URL",
		"ALLOWED_REPOS",
	}

	for _, envVar := range required {
		t.Run("missing_"+envVar, func(t *testing.T) {
			clearConfigEnv(t)
			setRequiredEnv(t)
			if err := os.Unsetenv(envVar); err != nil {
				t.Fatalf("failed to unset %s: %v", envVar, err)
			}

			_, err := LoadConfig()
			if err == nil {
				t.Fatalf("expected error for missing %s", envVar)
			}
			if !strings.Contains(err.Error(), envVar) {
				t.Fatalf("error should mention %s, got: %s", envVar, err.Error())
			}
		})

		t.Run("empty_"+envVar, func(t *testing.T) {
			clearConfigEnv(t)
			setRequiredEnv(t)
			t.Setenv(envVar, "")

			_, err := LoadConfig()
			if err == nil {
				t.Fatalf("expected error for empty %s", envVar)
			}
		})
	}
}

func TestLoadAllRequiredPresent(t *testing.T) {
	clearConfigEnv(t)
	setRequiredEnv(t)

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.GitHubWebhookSecret != "gh-secret" {
		t.Errorf("GitHubWebhookSecret = %q, want %q", cfg.GitHubWebhookSecret, "gh-secret")
	}
	if cfg.DocoCDWebhookSecret != "cd-secret" {
		t.Errorf("DocoCDWebhookSecret = %q, want %q", cfg.DocoCDWebhookSecret, "cd-secret")
	}
	if cfg.DocoCDURL != "http://doco-cd:80" {
		t.Errorf("DocoCDURL = %q, want %q", cfg.DocoCDURL, "http://doco-cd:80")
	}
}

func TestLoadDefaults(t *testing.T) {
	clearConfigEnv(t)
	setRequiredEnv(t)

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.ListenAddr != ":8080" {
		t.Errorf("ListenAddr = %q, want %q", cfg.ListenAddr, ":8080")
	}
	if cfg.GitHubMetaRefreshInterval != time.Hour {
		t.Errorf("GitHubMetaRefreshInterval = %v, want %v", cfg.GitHubMetaRefreshInterval, time.Hour)
	}
}

func TestLoadListenAddrOverride(t *testing.T) {
	clearConfigEnv(t)
	setRequiredEnv(t)
	t.Setenv("LISTEN_ADDR", ":9090")

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.ListenAddr != ":9090" {
		t.Errorf("ListenAddr = %q, want %q", cfg.ListenAddr, ":9090")
	}
}

func TestLoadGitHubMetaRefreshIntervalOverride(t *testing.T) {
	clearConfigEnv(t)
	setRequiredEnv(t)
	t.Setenv("GITHUB_META_REFRESH_INTERVAL", "30m")

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.GitHubMetaRefreshInterval != 30*time.Minute {
		t.Errorf("GitHubMetaRefreshInterval = %v, want %v", cfg.GitHubMetaRefreshInterval, 30*time.Minute)
	}
}

func TestLoadGitHubMetaRefreshIntervalInvalid(t *testing.T) {
	clearConfigEnv(t)
	setRequiredEnv(t)
	t.Setenv("GITHUB_META_REFRESH_INTERVAL", "not-a-duration")

	_, err := LoadConfig()
	if err == nil {
		t.Fatal("expected error for invalid duration")
	}
}

func TestLoadGitHubMetaRefreshIntervalNonPositive(t *testing.T) {
	for _, val := range []string{"0s", "-5m"} {
		t.Run(val, func(t *testing.T) {
			clearConfigEnv(t)
			setRequiredEnv(t)
			t.Setenv("GITHUB_META_REFRESH_INTERVAL", val)

			_, err := LoadConfig()
			if err == nil {
				t.Fatalf("expected error for non-positive interval %q", val)
			}
		})
	}
}

func TestLoadAllowedReposCanonicalization(t *testing.T) {
	clearConfigEnv(t)
	setRequiredEnv(t)
	t.Setenv("ALLOWED_REPOS", " Org/Repo1 , ORG/REPO2 , org/repo3 ")

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	want := []string{"org/repo1", "org/repo2", "org/repo3"}
	if len(cfg.AllowedRepos) != len(want) {
		t.Fatalf("AllowedRepos length = %d, want %d", len(cfg.AllowedRepos), len(want))
	}
	for i, repo := range cfg.AllowedRepos {
		if repo != want[i] {
			t.Errorf("AllowedRepos[%d] = %q, want %q", i, repo, want[i])
		}
	}
}

func TestLoadAllowedReposEmptyAfterParsing(t *testing.T) {
	clearConfigEnv(t)
	setRequiredEnv(t)
	t.Setenv("ALLOWED_REPOS", " , , ")

	_, err := LoadConfig()
	if err == nil {
		t.Fatal("expected error for empty ALLOWED_REPOS after parsing")
	}
}

func TestLoadFileVariant(t *testing.T) {
	clearConfigEnv(t)

	dir := t.TempDir()

	ghSecretFile := filepath.Join(dir, "gh_secret")
	if err := os.WriteFile(ghSecretFile, []byte("  file-gh-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cdSecretFile := filepath.Join(dir, "cd_secret")
	if err := os.WriteFile(cdSecretFile, []byte("file-cd-secret  \n"), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Setenv("GITHUB_WEBHOOK_SECRET_FILE", ghSecretFile)
	t.Setenv("DOCO_CD_WEBHOOK_SECRET_FILE", cdSecretFile)
	t.Setenv("DOCO_CD_URL", "http://doco-cd:80")
	t.Setenv("ALLOWED_REPOS", "org/repo1")

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.GitHubWebhookSecret != "file-gh-secret" {
		t.Errorf("GitHubWebhookSecret = %q, want %q", cfg.GitHubWebhookSecret, "file-gh-secret")
	}
	if cfg.DocoCDWebhookSecret != "file-cd-secret" {
		t.Errorf("DocoCDWebhookSecret = %q, want %q", cfg.DocoCDWebhookSecret, "file-cd-secret")
	}
}

func TestLoadFileVariantTakesPrecedence(t *testing.T) {
	clearConfigEnv(t)

	dir := t.TempDir()
	ghSecretFile := filepath.Join(dir, "gh_secret")
	if err := os.WriteFile(ghSecretFile, []byte("from-file"), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Setenv("GITHUB_WEBHOOK_SECRET", "from-env")
	t.Setenv("GITHUB_WEBHOOK_SECRET_FILE", ghSecretFile)
	t.Setenv("DOCO_CD_WEBHOOK_SECRET", "cd-secret")
	t.Setenv("DOCO_CD_URL", "http://doco-cd:80")
	t.Setenv("ALLOWED_REPOS", "org/repo1")

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.GitHubWebhookSecret != "from-file" {
		t.Errorf("GitHubWebhookSecret = %q, want %q (file should take precedence)", cfg.GitHubWebhookSecret, "from-file")
	}
}

func TestLoadFileVariantMissingFile(t *testing.T) {
	clearConfigEnv(t)

	t.Setenv("GITHUB_WEBHOOK_SECRET_FILE", "/nonexistent/path")
	t.Setenv("DOCO_CD_WEBHOOK_SECRET", "cd-secret")
	t.Setenv("DOCO_CD_URL", "http://doco-cd:80")
	t.Setenv("ALLOWED_REPOS", "org/repo1")

	_, err := LoadConfig()
	if err == nil {
		t.Fatal("expected error for missing secret file")
	}
}

func TestLoadFileVariantEmptyContents(t *testing.T) {
	clearConfigEnv(t)

	dir := t.TempDir()
	ghSecretFile := filepath.Join(dir, "gh_secret")
	if err := os.WriteFile(ghSecretFile, []byte("  \n"), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Setenv("GITHUB_WEBHOOK_SECRET_FILE", ghSecretFile)
	t.Setenv("DOCO_CD_WEBHOOK_SECRET", "cd-secret")
	t.Setenv("DOCO_CD_URL", "http://doco-cd:80")
	t.Setenv("ALLOWED_REPOS", "org/repo1")

	_, err := LoadConfig()
	if err == nil {
		t.Fatal("expected error for empty secret file after trimming")
	}
}

func TestLoadTrustedProxyCIDRs(t *testing.T) {
	clearConfigEnv(t)
	setRequiredEnv(t)
	t.Setenv("TRUSTED_PROXY_CIDRS", "10.0.0.0/8, 172.16.0.0/12")

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(cfg.TrustedProxyCIDRs) != 2 {
		t.Fatalf("TrustedProxyCIDRs length = %d, want 2", len(cfg.TrustedProxyCIDRs))
	}

	testIP := net.ParseIP("10.1.2.3")
	if !cfg.TrustedProxyCIDRs[0].Contains(testIP) {
		t.Errorf("expected 10.0.0.0/8 to contain 10.1.2.3")
	}
}

func TestLoadTrustedProxyCIDRsInvalid(t *testing.T) {
	clearConfigEnv(t)
	setRequiredEnv(t)
	t.Setenv("TRUSTED_PROXY_CIDRS", "not-a-cidr")

	_, err := LoadConfig()
	if err == nil {
		t.Fatal("expected error for invalid CIDR")
	}
}

func TestLoadIdenticalSecretsWarning(t *testing.T) {
	clearConfigEnv(t)
	t.Setenv("GITHUB_WEBHOOK_SECRET", "same-secret")
	t.Setenv("DOCO_CD_WEBHOOK_SECRET", "same-secret")
	t.Setenv("DOCO_CD_URL", "http://doco-cd:80")
	t.Setenv("ALLOWED_REPOS", "org/repo1")

	// Should succeed but produce a warning (we can't easily capture slog output,
	// so just verify it doesn't error)
	_, err := LoadConfig()
	if err != nil {
		t.Fatalf("identical secrets should warn, not error: %v", err)
	}
}
