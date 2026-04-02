package main

import (
	"context"
	"fmt"
	"log"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/hashicorp/go-cleanhttp"
	"github.com/hekmon/httplog/v3"
	autoslog "github.com/iguanesolutions/auto-slog/v2"
)

const (
	stopTimeout = 3 * time.Minute
)

var (
	logger           *slog.Logger
	modifiedRequests atomic.Int64
)

func main() {
	cfg, err := LoadConfig()
	if err != nil {
		log.Fatalf("load config: %s\n", err)
	}

	// Init
	logger = autoslog.NewLogger(slog.HandlerOptions{
		AddSource: true,
		Level:     parseLogLevel(cfg.LogLevel),
	})
	// Warn if COMPLETE log level is enabled
	if cfg.LogLevel == COMPLETE_LEVEL {
		logger.Warn("COMPLETE log level enabled - full request/response bodies will be logged, including potentially sensitive data",
			slog.String("log_level", cfg.LogLevel),
		)
	}
	backendURL, err := url.Parse(cfg.Target)
	if err != nil {
		logger.Error("failed to parse backend URL", slog.Any("error", err))
		os.Exit(1)
	}

	// Define HTTP handlers and middleware
	httplogger := httplog.New(logger, &httplog.Config{
		RequestDumpLogLevel:  COMPLETE,
		ResponseDumpLogLevel: COMPLETE,
		// BodyMaxRead:          128 * 1024,
	})
	// Create pooled HTTP client for forwarding requests
	httpClient := cleanhttp.DefaultPooledClient()
	// Handler for /v1/embeddings endpoint (rewrites model name and adds dimensions)
	http.HandleFunc("POST /v1/embeddings", httplogger.LogFunc(
		embeddings(httpClient, backendURL, cfg.ServedModelName, cfg.Dimensions),
	))
	// Health check endpoint (not logged)
	http.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"healthy"}`))
	})

	// Prepare HTTP server and clean stop
	server := &http.Server{Addr: fmt.Sprintf("%s:%d", cfg.Listen, cfg.Port)}
	signalStopCtx, signalStopCtxCancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer signalStopCtxCancel()
	go cleanStop(signalStopCtx, server)

	logger.Debug("skipping systemd integration")

	// Start server
	logger.Info("starting reverse proxy server",
		slog.String("listen", cfg.Listen),
		slog.Int("port", cfg.Port),
		slog.String("target", backendURL.String()),
	)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		logger.Error("failed to start HTTP server", "err", err)
		os.Exit(1)
	}
}

func cleanStop(signalStopCtx context.Context, server *http.Server) {
	<-signalStopCtx.Done()
	logger.Info("shutting down HTTP server...",
		slog.Duration("grace_period", stopTimeout),
	)
	ctx, cancel := context.WithTimeout(context.Background(), stopTimeout)
	defer cancel()
	if err := server.Shutdown(ctx); err != nil {
		logger.Error("failed to shutdown HTTP server properly", "err", err)
	}
}
