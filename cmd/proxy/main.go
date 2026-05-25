package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	proxy "github.com/strayer/doco-cd-webhook-proxy/internal/proxy"
)

const defaultGitHubMetaURL = "https://api.github.com/meta"

func main() {
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		os.Exit(healthcheck())
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	cfg, err := proxy.LoadConfig()
	if err != nil {
		slog.Error("failed to load config", "error", err)
		os.Exit(1)
	}

	metaURL := strings.TrimSpace(os.Getenv("GITHUB_META_URL"))
	if metaURL == "" {
		metaURL = defaultGitHubMetaURL
	}

	ln, err := net.Listen("tcp", cfg.ListenAddr)
	if err != nil {
		slog.Error("failed to listen", "addr", cfg.ListenAddr, "error", err)
		os.Exit(1)
	}

	if err := run(ctx, cfg, metaURL, ln); err != nil {
		slog.Error("fatal", "error", err)
		os.Exit(1)
	}
}

func healthcheck() int {
	addr := os.Getenv("LISTEN_ADDR")
	if addr == "" {
		addr = ":8080"
	}
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return 1
	}
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get("http://localhost:" + port + "/healthz")
	if err != nil {
		return 1
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return 1
	}
	return 0
}

func run(ctx context.Context, cfg *proxy.Config, metaURL string, ln net.Listener) error {
	checker, err := proxy.NewGitHubIPChecker(metaURL, cfg.GitHubMetaRefreshInterval)
	if err != nil {
		return fmt.Errorf("initializing IP checker: %w", err)
	}
	defer checker.Stop()

	forwarder := proxy.NewForwarder()
	handler := proxy.NewHandler(cfg, checker, forwarder)

	srv := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}

	errCh := make(chan error, 1)
	go func() {
		errCh <- srv.Serve(ln)
	}()

	slog.Info("server started", "addr", ln.Addr().String())

	select {
	case <-ctx.Done():
		slog.Info("shutting down server")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}
