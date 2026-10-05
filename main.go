// Command gitlab-mcp is an MCP (Model Context Protocol) server that lets an AI
// agent read a GitLab installation and operate its pipelines.
//
// Everything except pipelines is read-only. Secret-bearing endpoints — CI/CD
// variables, pipeline variables, secure files, access and deploy tokens,
// webhooks, integrations, runner tokens — are permanently blocked and cannot be
// enabled by configuration.
//
// The HTTP transport can require agent authentication (static bearer tokens
// and/or OIDC). When auth is disabled, run it behind an internal or OIDC-gated
// ingress; never expose an unauthenticated transport publicly.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/truepace-io-oss/gitlab-mcp-server/internal/auth"
	"github.com/truepace-io-oss/gitlab-mcp-server/internal/config"
	"github.com/truepace-io-oss/gitlab-mcp-server/internal/instances"
	"github.com/truepace-io-oss/gitlab-mcp-server/internal/mcpserver"
	"github.com/truepace-io-oss/gitlab-mcp-server/internal/metrics"
)

// version is set via -ldflags "-X main.version=...".
var version = "dev"

func main() {
	configPath := flag.String("config", os.Getenv("GMCP_CONFIG"), "path to the server config YAML (env: GMCP_CONFIG)")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Println(version)
		return
	}

	if err := run(*configPath); err != nil {
		slog.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run(configPath string) error {
	cfg, err := config.Load(configPath)
	if err != nil {
		return err
	}

	logger := newLogger(cfg.LogLevel)
	slog.SetDefault(logger)
	mcpserver.SetVersion(version)
	metrics.SetBuildInfo(version)

	for _, w := range cfg.Warnings() {
		logger.Warn("config", "warning", w)
	}

	reg, err := instances.Build(cfg)
	if err != nil {
		return err
	}
	for _, in := range cfg.Instances {
		logger.Info("gitlab instance", "config", in.String())
	}
	logger.Info("instance registry built",
		"instances", cfg.InstanceNames(), "default", cfg.DefaultInstance, "readOnly", cfg.ReadOnly)

	srv := mcpserver.New(reg, cfg)

	// Agent-facing authentication, independent of the GitLab token.
	authn, err := auth.Build(context.Background(), cfg.Auth)
	if err != nil {
		return err
	}
	logger.Info("agent authentication", "mode", authn.Description)

	// One MCP server value, reused for every session.
	mcpSrv := srv.MCPServer()
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return mcpSrv }, nil)

	mux := http.NewServeMux()
	// Only /mcp is authenticated; health probes stay open, and the
	// protected-resource metadata endpoint is public by spec.
	mux.Handle("/mcp", authn.Middleware(handler))
	if authn.MetadataHandler != nil {
		mux.Handle(authn.MetadataPath, authn.MetadataHandler)
	}
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, r *http.Request) {
		// Ready if the default instance is reachable. A degraded secondary
		// instance must not fail readiness, so only the default is probed.
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		defer cancel()
		if _, err := reg.Default().Ping(ctx); err != nil {
			http.Error(w, "default GitLab instance unreachable: "+err.Error(), http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ready"))
	})

	httpSrv := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Metrics on a separate, unauthenticated port, plus a periodic instance
	// reachability and token-expiry probe.
	var metricsSrv *http.Server
	if addr := cfg.MetricsAddr; addr != "" && addr != "off" {
		mmux := http.NewServeMux()
		mmux.Handle("/metrics", promhttp.Handler())
		metricsSrv = &http.Server{Addr: addr, Handler: mmux, ReadHeaderTimeout: 10 * time.Second}
		go func() {
			logger.Info("metrics listening", "addr", addr, "endpoint", "/metrics")
			if err := metricsSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				logger.Error("metrics server", "err", err)
			}
		}()
		go probeInstances(ctx, reg, logger)
	}

	errCh := make(chan error, 1)
	go func() {
		logger.Info("listening", "addr", cfg.ListenAddr, "endpoint", "/mcp")
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case <-ctx.Done():
		logger.Info("shutdown signal received")
	case err := <-errCh:
		return err
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if metricsSrv != nil {
		_ = metricsSrv.Shutdown(shutdownCtx)
	}
	return httpSrv.Shutdown(shutdownCtx)
}

// probeInstances periodically pings every instance and reads its token metadata,
// so gmcp_instance_up and gmcp_token_expires_in_seconds reflect reality
// independently of tool traffic.
func probeInstances(ctx context.Context, reg *instances.Registry, logger *slog.Logger) {
	probe := func() {
		for _, in := range reg.All() {
			pctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			_, err := in.Ping(pctx)
			metrics.SetInstanceUp(in.Name, err == nil)
			if err == nil {
				if tok, terr := in.TokenInfo(pctx); terr == nil {
					if w := tok.Warning(); w != "" {
						logger.Warn("gitlab token", "instance", in.Name, "warning", w)
					}
				}
			}
			cancel()
		}
	}
	probe()
	t := time.NewTicker(60 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			probe()
		}
	}
}

func newLogger(level string) *slog.Logger {
	var lvl slog.Level
	switch level {
	case "debug":
		lvl = slog.LevelDebug
	case "warn":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}
	return slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: lvl}))
}
