package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/common-creation/codexbot/internal/runtimemanager"
)

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	shared, err := filepath.Abs(env("CODEXBOT_SHARED_HOME", "/data/shared-home"))
	if err != nil || shared == "/" {
		logger.Error("invalid shared home")
		os.Exit(1)
	}
	if err = os.MkdirAll(shared, 0770); err != nil {
		logger.Error("create shared home", "error", err)
		os.Exit(1)
	}
	token := os.Getenv("CODEXBOT_RUNTIME_TOKEN")
	if token == "" {
		logger.Error("CODEXBOT_RUNTIME_TOKEN is required")
		os.Exit(1)
	}
	mgr := runtimemanager.New(runtimemanager.ManagerConfig{AgentImage: env("CODEXBOT_AGENT_IMAGE", "codexbot-agent:local"), EgressImage: env("CODEXBOT_EGRESS_IMAGE", "codexbot-egress:local"), EgressNetwork: env("CODEXBOT_EGRESS_NETWORK", "codexbot-egress"), SharedHome: shared, ManagerContainer: os.Getenv("CODEXBOT_RUNTIME_MANAGER_CONTAINER"), RuntimeToken: token, ControlPlaneURL: env("CODEXBOT_CONTROL_PLANE_URL", "http://codexbot:8080")}, logger)
	srv := &http.Server{Addr: env("CODEXBOT_RUNTIME_LISTEN_ADDR", ":8081"), Handler: mgr.Handler(), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 90 * time.Second}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go func() {
		logger.Info("runtime manager listening", "address", srv.Addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("server failed", "error", err)
			stop()
		}
	}()
	<-ctx.Done()
	shutdown, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdown)
}
