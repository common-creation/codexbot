package main

import (
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/common-creation/codexbot/internal/httpapi"
	"github.com/common-creation/codexbot/internal/modelcatalog"
	"github.com/common-creation/codexbot/internal/runtimeclient"
	"github.com/common-creation/codexbot/internal/store"
)

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	slog.SetDefault(logger)
	dataDir := env("CODEXBOT_DATA_DIR", "./data")
	if err := os.MkdirAll(dataDir, 0700); err != nil {
		logger.Error("create data directory", "error", err)
		os.Exit(1)
	}
	st, err := store.Open(filepath.Join(dataDir, "codexbot.db"))
	if err != nil {
		logger.Error("open database", "error", err)
		os.Exit(1)
	}
	defer st.Close()
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	models, err := modelcatalog.Fetch(ctx, nil, modelcatalog.URL)
	if err != nil {
		logger.Warn("fetch model catalog; using built-in models", "error", err)
		models = modelcatalog.Fallback()
	} else {
		logger.Info("loaded model catalog", "models", len(models))
	}
	rt := runtimeclient.New(env("CODEXBOT_RUNTIME_MANAGER_URL", "http://runtime-manager:8081"), os.Getenv("CODEXBOT_RUNTIME_TOKEN"))
	api := httpapi.New(st, rt, httpapi.Config{Models: models, BootstrapToken: os.Getenv("CODEXBOT_BOOTSTRAP_TOKEN"), SecureCookies: env("CODEXBOT_SECURE_COOKIES", "true") != "false"}, logger)
	api.StartBackground(ctx)
	handler := withStatic(api.Handler(), env("CODEXBOT_STATIC_DIR", "web/dist"))
	srv := &http.Server{Addr: env("CODEXBOT_LISTEN_ADDR", ":8080"), Handler: handler, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 90 * time.Second}
	go func() {
		logger.Info("codexbot listening", "address", srv.Addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("server failed", "error", err)
			stop()
		}
	}()
	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)
}

func withStatic(api http.Handler, dir string) http.Handler {
	static := http.FileServer(http.Dir(dir))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") || strings.HasPrefix(r.URL.Path, "/internal/") || r.URL.Path == "/healthz" {
			api.ServeHTTP(w, r)
			return
		}
		path := filepath.Join(dir, filepath.Clean(r.URL.Path))
		if info, err := os.Stat(path); err == nil && !info.IsDir() {
			static.ServeHTTP(w, r)
			return
		}
		if _, err := fs.Stat(os.DirFS(dir), "index.html"); err != nil {
			http.NotFound(w, r)
			return
		}
		r2 := r.Clone(r.Context())
		r2.URL.Path = "/"
		static.ServeHTTP(w, r2)
	})
}
