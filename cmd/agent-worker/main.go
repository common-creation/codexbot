package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/common-creation/codexbot/internal/agentworker"
)

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	worker, err := agentworker.Start(ctx, agentworker.Config{AgentID: os.Getenv("CODEXBOT_AGENT_ID"), CollaborationURL: os.Getenv("CODEXBOT_COLLABORATION_URL"), CollaborationSocket: env("CODEXBOT_COLLABORATION_SOCKET", "/run/codexbot/collaboration.sock"), Token: os.Getenv("CODEXBOT_WORKER_TOKEN"), ProfileDir: env("CODEXBOT_PROFILE_DIR", "/var/lib/codexbot/profile"), CodexExecutable: env("CODEXBOT_CODEX_PATH", "codex")}, logger)
	if err != nil {
		logger.Error("start worker", "error", err)
		os.Exit(1)
	}
	defer worker.Close()
	srv := &http.Server{Addr: env("AGENT_WORKER_LISTEN_ADDR", ":8082"), Handler: worker.Handler(), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second}
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("worker server failed", "error", err)
			stop()
		}
	}()
	failed := false
	select {
	case <-ctx.Done():
	case <-worker.Failed():
		failed = true
		stop()
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdown)
	if failed {
		os.Exit(1)
	}
}
