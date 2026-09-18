package runtimemanager

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

type Manager struct {
	cfg    ManagerConfig
	run    commandRunner
	logger *slog.Logger
	mux    *http.ServeMux
	mu     sync.Mutex
	leases map[string]string
}

func New(cfg ManagerConfig, logger *slog.Logger) *Manager {
	if logger == nil {
		logger = slog.Default()
	}
	m := &Manager{cfg: cfg, run: dockerCLI{binary: "docker"}, logger: logger, mux: http.NewServeMux(), leases: map[string]string{}}
	m.routes()
	return m
}
func NewWithRunner(cfg ManagerConfig, logger *slog.Logger, r commandRunner) *Manager {
	m := New(cfg, logger)
	m.run = r
	return m
}
func (m *Manager) Handler() http.Handler { return m.auth(m.mux) }
func derivedSecret(seed, scope string) string {
	h := sha256.Sum256([]byte(seed + "\x00" + scope))
	return base64.RawURLEncoding.EncodeToString(h[:])
}
func validID(id string) bool { _, err := uuid.Parse(id); return err == nil }
func (m *Manager) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			next.ServeHTTP(w, r)
			return
		}
		got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if id, ok := collaborationSender(r.URL.Path); ok {
			if m.cfg.RuntimeToken == "" || subtle.ConstantTimeCompare([]byte(got), []byte(derivedSecret(m.cfg.RuntimeToken, "worker:"+id))) != 1 {
				http.Error(w, "unauthorized", 401)
				return
			}
			next.ServeHTTP(w, r)
			return
		}
		if m.cfg.RuntimeToken == "" || subtle.ConstantTimeCompare([]byte(got), []byte(m.cfg.RuntimeToken)) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}
func (m *Manager) routes() {
	m.mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"status":"ok"}`)
	})
	m.mux.HandleFunc("POST /v1/agents/{agentID}/start", m.start)
	m.mux.HandleFunc("POST /v1/agents/{agentID}/stop", m.stop)
	m.mux.HandleFunc("POST /v1/agents/{agentID}/turns", m.workerProxy)
	m.mux.HandleFunc("POST /v1/agents/{agentID}/interrupt", m.workerProxy)
	m.mux.HandleFunc("POST /v1/agents/{agentID}/steer", m.workerProxy)
	m.mux.HandleFunc("/v1/agents/{agentID}/collaboration/{rest...}", m.collaborationProxy)
	m.mux.HandleFunc("GET /v1/agents/{agentID}/runs/{runID}/events", m.workerProxy)
	m.mux.HandleFunc("POST /v1/agents/{agentID}/auth/{action}", m.workerProxy)
	m.mux.HandleFunc("GET /v1/agents/{agentID}/auth/status", m.workerProxy)
	m.mux.HandleFunc("POST /v1/agents/{agentID}/desktop/lease", m.desktopLease)
	m.mux.HandleFunc("/v1/agents/{agentID}/desktop/{rest...}", m.desktop)
}
func decodeLimited(r *http.Request, v any) error {
	defer r.Body.Close()
	return json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(v)
}
func (m *Manager) start(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("agentID")
	if !validID(id) {
		http.Error(w, "invalid agent id", 400)
		return
	}
	m.mu.Lock()
	err := ensureAgentContainer(r.Context(), m.run, m.cfg, id)
	if _, ok := m.leases[id]; !ok {
		m.leases[id] = "agent"
	}
	m.mu.Unlock()
	if err != nil {
		m.logger.Error("start agent", "agent", id, "error", err)
		http.Error(w, err.Error(), 502)
		return
	}
	if err = m.waitWorker(r.Context(), id); err != nil {
		http.Error(w, err.Error(), 502)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	io.WriteString(w, `{"status":"running"}`)
}
func (m *Manager) stop(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("agentID")
	if !validID(id) {
		http.Error(w, "invalid agent id", 400)
		return
	}
	m.mu.Lock()
	err := stopAgentContainer(r.Context(), m.run, id)
	m.mu.Unlock()
	if err != nil {
		http.Error(w, err.Error(), 502)
		return
	}
	w.WriteHeader(204)
}
func (m *Manager) agentURL(ctx context.Context, id string, port int) (*url.URL, error) {
	ip, err := inspectIP(ctx, m.run, id)
	if err != nil {
		return nil, err
	}
	return url.Parse(fmt.Sprintf("http://%s:%d", ip, port))
}
func (m *Manager) waitWorker(ctx context.Context, id string) error {
	readyCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	healthClient := &http.Client{Timeout: 2 * time.Second}
	for readyCtx.Err() == nil {
		u, err := m.agentURL(readyCtx, id, 8082)
		if err == nil {
			req, _ := http.NewRequestWithContext(readyCtx, "GET", u.String()+"/healthz", nil)
			resp, e := healthClient.Do(req)
			if e == nil {
				resp.Body.Close()
				if resp.StatusCode == 200 {
					return nil
				}
			}
		}
		select {
		case <-readyCtx.Done():
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return errors.New("agent worker did not become ready")
		case <-time.After(500 * time.Millisecond):
		}
	}
	return errors.New("agent worker did not become ready")
}
func (m *Manager) workerProxy(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("agentID")
	if !validID(id) {
		http.Error(w, "invalid agent id", 400)
		return
	}
	target, err := m.agentURL(r.Context(), id, 8082)
	if err != nil {
		http.Error(w, err.Error(), 502)
		return
	}
	workerPath, ok := workerProxyPath(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	proxy := httputil.NewSingleHostReverseProxy(target)
	baseDirector := proxy.Director
	proxy.Director = func(req *http.Request) {
		baseDirector(req)
		req.URL.Path = workerPath
		req.Host = target.Host
		req.Header.Set("Authorization", "Bearer "+derivedSecret(m.cfg.RuntimeToken, "worker:"+id))
	}
	proxy.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, e error) { http.Error(w, e.Error(), 502) }
	proxy.ServeHTTP(w, r)
}

func workerProxyPath(r *http.Request) (string, bool) {
	switch {
	case strings.HasSuffix(r.URL.Path, "/turns"):
		return "/v1/turns", true
	case strings.HasSuffix(r.URL.Path, "/steer"):
		return "/v1/steer", true
	case strings.HasSuffix(r.URL.Path, "/interrupt"):
		return "/v1/interrupt", true
	case strings.Contains(r.URL.Path, "/runs/"):
		return "/v1/runs/" + url.PathEscape(r.PathValue("runID")) + "/events", true
	case strings.HasSuffix(r.URL.Path, "/auth/status"):
		return "/v1/auth/status", true
	case strings.Contains(r.URL.Path, "/auth/"):
		return "/v1/auth/" + url.PathEscape(r.PathValue("action")), true
	default:
		return "", false
	}
}
func (m *Manager) desktopLease(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("agentID")
	if !validID(id) {
		http.Error(w, "invalid agent id", 400)
		return
	}
	var in struct {
		Holder     string `json:"holder"`
		Generation int64  `json:"generation"`
	}
	if decodeLimited(r, &in) != nil || (in.Holder != "agent" && in.Holder != "human") || in.Generation < 1 {
		http.Error(w, "invalid lease", 400)
		return
	}
	target, err := m.agentURL(r.Context(), id, 8082)
	if err != nil {
		http.Error(w, err.Error(), 502)
		return
	}
	b, _ := json.Marshal(in)
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, target.String()+"/v1/desktop/lease", strings.NewReader(string(b)))
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+derivedSecret(m.cfg.RuntimeToken, "worker:"+id))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		http.Error(w, err.Error(), 502)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		http.Error(w, string(body), resp.StatusCode)
		return
	}
	m.mu.Lock()
	m.leases[id] = in.Holder
	m.mu.Unlock()
	w.WriteHeader(204)
}
func (m *Manager) desktop(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("agentID")
	if !validID(id) {
		http.Error(w, "invalid agent id", 400)
		return
	}
	ip, err := inspectIP(r.Context(), m.run, id)
	if err != nil {
		http.Error(w, err.Error(), 502)
		return
	}
	target, _ := url.Parse("https://" + ip + ":6901")
	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.Transport = &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: true}} // internal self-signed KasmVNC endpoint
	baseDirector := proxy.Director
	proxy.Director = func(req *http.Request) {
		baseDirector(req)
		rest := r.PathValue("rest")
		if rest == "" {
			rest = "/"
		} else {
			rest = "/" + strings.TrimLeft(filepath.ToSlash(rest), "/")
		}
		req.URL.Path = rest
		req.Host = target.Host
		m.mu.Lock()
		holder := m.leases[id]
		m.mu.Unlock()
		user := "codexbot"
		if holder == "human" {
			user = "owner"
		}
		req.SetBasicAuth(user, derivedSecret(m.cfg.RuntimeToken, "kasm:"+id)[:24])
	}
	proxy.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, e error) { http.Error(w, "desktop unavailable", 502) }
	proxy.ServeHTTP(w, r)
}
