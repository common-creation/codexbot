package agentworker

import (
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// CollaborationHandler exposes only the agent-scoped collaboration capability.
// It is served on a private Unix socket, never on the worker's TCP listener.
func (w *Worker) CollaborationHandler() http.Handler {
	mux := http.NewServeMux()
	for _, pattern := range []string{"GET /agents", "GET /agents/{id}", "GET /tasks", "POST /tasks", "GET /tasks/{id}", "POST /tasks/{id}/cancel"} {
		mux.HandleFunc(pattern, w.collaborationProxy)
	}
	return mux
}

func (w *Worker) startCollaborationSocket() error {
	if w.cfg.CollaborationURL == "" {
		return nil
	}
	if w.cfg.CollaborationSocket == "" {
		w.cfg.CollaborationSocket = "/run/codexbot/collaboration.sock"
	}
	path := w.cfg.CollaborationSocket
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return errors.New("collaboration socket path is not a socket")
		}
		if err := os.Remove(path); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	listener, err := net.Listen("unix", path)
	if err != nil {
		return err
	}
	if err := os.Chmod(path, 0600); err != nil {
		_ = listener.Close()
		return err
	}
	w.collaborationServer = &http.Server{Handler: w.CollaborationHandler(), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second}
	go func() {
		if err := w.collaborationServer.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			w.logger.Error("collaboration socket failed", "error", err)
		}
	}()
	return nil
}

func (w *Worker) collaborationProxy(rw http.ResponseWriter, r *http.Request) {
	if w.cfg.CollaborationURL == "" || w.cfg.AgentID == "" {
		http.Error(rw, "collaboration unavailable", 503)
		return
	}
	target := strings.TrimRight(w.cfg.CollaborationURL, "/") + "/v1/agents/" + url.PathEscape(w.cfg.AgentID) + "/collaboration" + r.URL.EscapedPath()
	if r.URL.RawQuery != "" {
		target += "?" + r.URL.RawQuery
	}
	req, err := http.NewRequestWithContext(r.Context(), r.Method, target, http.MaxBytesReader(rw, r.Body, 1<<20))
	if err != nil {
		http.Error(rw, "invalid collaboration request", 400)
		return
	}
	req.Header.Set("Authorization", "Bearer "+w.cfg.Token)
	req.Header.Set("Content-Type", "application/json")
	// The agent's environment has HTTP_PROXY for Internet access. This internal
	// capability must reach the manager directly, without disclosing its token.
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		http.Error(rw, "collaboration service unavailable", 502)
		return
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, (8<<20)+1))
	if err != nil {
		http.Error(rw, "could not read collaboration response", 502)
		return
	}
	if len(body) > 8<<20 {
		http.Error(rw, "collaboration response exceeds 8 MiB limit", 502)
		return
	}
	rw.Header().Set("Content-Type", "application/json")
	rw.WriteHeader(resp.StatusCode)
	_, _ = rw.Write(body)
}
