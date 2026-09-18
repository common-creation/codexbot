package httpapi

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/common-creation/codexbot/internal/auth"
	"github.com/common-creation/codexbot/internal/domain"
	"github.com/common-creation/codexbot/internal/modelcatalog"
	"github.com/common-creation/codexbot/internal/runtimeclient"
	"github.com/common-creation/codexbot/internal/store"
	"github.com/google/uuid"
	"github.com/robfig/cron/v3"
)

const (
	sessionCookie        = "codexbot_session"
	sharedAuthPendingTTL = 15 * time.Minute
	sharedAuthVerifyTTL  = 30 * time.Second
)

type Server struct {
	models               []modelcatalog.Model
	store                *store.Store
	runtime              *runtimeclient.Client
	bootstrapToken       string
	secureCookies        bool
	logger               *slog.Logger
	mux                  *http.ServeMux
	relayMu              sync.Mutex
	relays               map[string]context.CancelFunc
	agentOps             sync.Map
	authMu               sync.Mutex
	collaborationWake    chan struct{}
	collaborationWorkers sync.Map
}

type Config struct {
	Models         []modelcatalog.Model
	BootstrapToken string
	SecureCookies  bool
}

func New(st *store.Store, rt *runtimeclient.Client, cfg Config, logger *slog.Logger) *Server {
	if logger == nil {
		logger = slog.Default()
	}
	if len(cfg.Models) == 0 {
		cfg.Models = modelcatalog.Fallback()
	}
	s := &Server{models: cfg.Models, store: st, runtime: rt, bootstrapToken: cfg.BootstrapToken, secureCookies: cfg.SecureCookies, logger: logger, mux: http.NewServeMux(), relays: map[string]context.CancelFunc{}}
	s.collaborationWake = make(chan struct{}, 1)
	s.routes()
	return s
}
func (s *Server) Handler() http.Handler { return s.securityHeaders(s.mux) }

func (s *Server) lockAgent(id string) func() {
	value, _ := s.agentOps.LoadOrStore(id, &sync.Mutex{})
	mu := value.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

func (s *Server) routes() {
	s.collaborationRoutes()
	s.mux.HandleFunc("GET /healthz", s.health)
	s.mux.HandleFunc("GET /api/setup/status", s.setupStatus)
	s.mux.HandleFunc("POST /api/setup", s.setup)
	s.mux.HandleFunc("POST /api/session", s.login)
	s.mux.Handle("DELETE /api/session", s.requireAuth(http.HandlerFunc(s.logout)))
	s.mux.Handle("GET /api/session", s.requireAuth(http.HandlerFunc(s.session)))
	s.mux.Handle("GET /api/models", s.requireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, s.models) })))
	s.mux.Handle("GET /api/agents", s.requireAuth(http.HandlerFunc(s.listAgents)))
	s.mux.Handle("GET /api/sidebar", s.requireAuth(http.HandlerFunc(s.getSidebar)))
	s.mux.Handle("PUT /api/sidebar", s.requireAuth(http.HandlerFunc(s.updateSidebar)))
	s.mux.Handle("POST /api/agents", s.requireAuth(http.HandlerFunc(s.createAgent)))
	s.mux.Handle("GET /api/agents/{agentID}", s.requireAuth(http.HandlerFunc(s.getAgent)))
	s.mux.Handle("PATCH /api/agents/{agentID}", s.requireAuth(http.HandlerFunc(s.updateAgent)))
	s.mux.Handle("DELETE /api/agents/{agentID}", s.requireAuth(http.HandlerFunc(s.archiveAgent)))
	s.mux.Handle("POST /api/agents/{agentID}/start", s.requireAuth(http.HandlerFunc(s.startAgent)))
	s.mux.Handle("POST /api/agents/{agentID}/stop", s.requireAuth(http.HandlerFunc(s.stopAgent)))
	s.mux.Handle("POST /api/agents/{agentID}/messages", s.requireAuth(http.HandlerFunc(s.message)))
	s.mux.Handle("POST /api/agents/{agentID}/conversations", s.requireAuth(http.HandlerFunc(s.newConversation)))
	s.mux.Handle("GET /api/agents/{agentID}/timeline", s.requireAuth(http.HandlerFunc(s.agentTimeline)))
	s.mux.Handle("GET /api/agents/{agentID}/history", s.requireAuth(http.HandlerFunc(s.agentHistory)))
	s.mux.Handle("POST /api/agents/{agentID}/interrupt", s.requireAuth(http.HandlerFunc(s.interrupt)))
	s.mux.Handle("POST /api/agents/{agentID}/auth/device", s.requireAuth(http.HandlerFunc(s.deviceLogin)))
	s.mux.Handle("POST /api/agents/{agentID}/auth/api-key", s.requireAuth(http.HandlerFunc(s.apiKeyLogin)))
	s.mux.Handle("POST /api/agents/{agentID}/auth/logout", s.requireAuth(http.HandlerFunc(s.providerLogout)))
	s.mux.Handle("GET /api/agents/{agentID}/auth", s.requireAuth(http.HandlerFunc(s.providerStatus)))
	s.mux.Handle("GET /api/runs/{runID}", s.requireAuth(http.HandlerFunc(s.getRun)))
	s.mux.Handle("GET /api/runs/{runID}/events", s.requireAuth(http.HandlerFunc(s.events)))
	s.mux.Handle("GET /api/schedules", s.requireAuth(http.HandlerFunc(s.listSchedules)))
	s.mux.Handle("GET /api/schedules/{scheduleID}", s.requireAuth(http.HandlerFunc(s.getSchedule)))
	s.mux.Handle("GET /api/schedules/{scheduleID}/runs", s.requireAuth(http.HandlerFunc(s.scheduleRuns)))
	s.mux.Handle("GET /api/schedules/{scheduleID}/runs/{runID}/events", s.requireAuth(http.HandlerFunc(s.scheduleRunEvents)))
	s.mux.Handle("POST /api/schedules", s.requireAuth(http.HandlerFunc(s.createSchedule)))
	s.mux.Handle("PATCH /api/schedules/{scheduleID}", s.requireAuth(http.HandlerFunc(s.updateSchedule)))
	s.mux.Handle("DELETE /api/schedules/{scheduleID}", s.requireAuth(http.HandlerFunc(s.deleteSchedule)))
	s.mux.Handle("POST /api/agents/{agentID}/desktop/takeover", s.requireAuth(http.HandlerFunc(s.takeover)))
	s.mux.Handle("POST /api/agents/{agentID}/desktop/release", s.requireAuth(http.HandlerFunc(s.releaseDesktop)))
	s.mux.Handle("POST /api/agents/{agentID}/desktop/heartbeat", s.requireAuth(http.HandlerFunc(s.desktopHeartbeat)))
	s.mux.Handle("/api/agents/{agentID}/desktop/{rest...}", s.requireAuth(http.HandlerFunc(s.desktopProxy)))
}

func (s *Server) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		// KasmVNC serves its own client assets through the authenticated desktop
		// proxy and must remain embeddable by the same-origin WebUI. Its upstream
		// security headers are preserved instead of applying the API policy.
		if !strings.Contains(r.URL.Path, "/desktop/") {
			w.Header().Set("X-Frame-Options", "SAMEORIGIN")
			w.Header().Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data: blob:; style-src 'self' 'unsafe-inline'; connect-src 'self' ws: wss:; frame-src 'self'; object-src 'none'; base-uri 'self'")
		}
		next.ServeHTTP(w, r)
	})
}
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
func decode(r *http.Request, v any) error {
	defer r.Body.Close()
	return json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(v)
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	if err := s.store.Health(r.Context()); err != nil {
		writeError(w, 503, err.Error())
		return
	}
	writeJSON(w, 200, map[string]string{"status": "ok"})
}
func (s *Server) setupStatus(w http.ResponseWriter, r *http.Request) {
	n, err := s.store.AdminCount(r.Context())
	if err != nil {
		writeError(w, 500, "database error")
		return
	}
	writeJSON(w, 200, map[string]bool{"required": n == 0})
}
func (s *Server) setup(w http.ResponseWriter, r *http.Request) {
	n, _ := s.store.AdminCount(r.Context())
	if n != 0 {
		writeError(w, 409, "setup already completed")
		return
	}
	if s.bootstrapToken != "" && subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Bootstrap-Token")), []byte(s.bootstrapToken)) != 1 {
		writeError(w, 403, "invalid bootstrap token")
		return
	}
	var in struct{ Username, Password string }
	if err := decode(r, &in); err != nil {
		writeError(w, 400, "invalid JSON")
		return
	}
	in.Username = strings.TrimSpace(in.Username)
	if len(in.Username) < 3 || len(in.Username) > 64 {
		writeError(w, 400, "username must be 3-64 characters")
		return
	}
	h, err := auth.HashPassword(in.Password)
	if err != nil {
		writeError(w, 400, err.Error())
		return
	}
	if err = s.store.CreateAdmin(r.Context(), in.Username, h); err != nil {
		writeError(w, 500, "could not create administrator")
		return
	}
	writeJSON(w, 201, map[string]string{"status": "created"})
}
func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	var in struct{ Username, Password string }
	if decode(r, &in) != nil {
		writeError(w, 400, "invalid JSON")
		return
	}
	h, err := s.store.AdminPasswordHash(r.Context(), in.Username)
	if err != nil || !auth.VerifyPassword(h, in.Password) {
		writeError(w, 401, "invalid credentials")
		return
	}
	token, _ := auth.RandomToken(32)
	csrf, _ := auth.RandomToken(24)
	exp := time.Now().Add(24 * time.Hour)
	if err = s.store.CreateSession(r.Context(), auth.TokenHash(token), csrf, exp); err != nil {
		writeError(w, 500, "could not create session")
		return
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: token, Path: "/", HttpOnly: true, Secure: s.secureCookies, SameSite: http.SameSiteStrictMode, Expires: exp, MaxAge: 86400})
	writeJSON(w, 200, map[string]any{"username": in.Username, "csrfToken": csrf, "expiresAt": exp})
}
func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		_ = s.store.DeleteSession(r.Context(), auth.TokenHash(c.Value))
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", HttpOnly: true, Secure: s.secureCookies, SameSite: http.SameSiteStrictMode, MaxAge: -1})
	w.WriteHeader(204)
}
func (s *Server) session(w http.ResponseWriter, r *http.Request) {
	csrf, _ := r.Context().Value(csrfContext).(string)
	username, _ := r.Context().Value(usernameContext).(string)
	writeJSON(w, 200, map[string]any{"authenticated": true, "username": username, "csrfToken": csrf})
}

type contextKey string

const csrfContext contextKey = "csrf"
const usernameContext contextKey = "username"

func (s *Server) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(sessionCookie)
		if err != nil {
			writeError(w, 401, "authentication required")
			return
		}
		username, csrf, exp, err := s.store.Session(r.Context(), auth.TokenHash(c.Value))
		if err != nil || time.Now().After(exp) {
			writeError(w, 401, "session expired")
			return
		}
		if r.Method != "GET" && r.Method != "HEAD" && r.Method != "OPTIONS" {
			if strings.Contains(r.URL.Path, "/desktop/") {
				origin := r.Header.Get("Origin")
				expected := "http://" + r.Host
				if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
					expected = "https://" + r.Host
				}
				if origin != expected {
					writeError(w, 403, "invalid desktop origin")
					return
				}
			} else if subtle.ConstantTimeCompare([]byte(r.Header.Get("X-CSRF-Token")), []byte(csrf)) != 1 {
				writeError(w, 403, "invalid CSRF token")
				return
			}
		}
		ctx := context.WithValue(r.Context(), csrfContext, csrf)
		ctx = context.WithValue(ctx, usernameContext, username)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (s *Server) listAgents(w http.ResponseWriter, r *http.Request) {
	v, err := s.store.Agents(r.Context())
	if err != nil {
		writeError(w, 500, "database error")
		return
	}
	if v == nil {
		v = []domain.Agent{}
	}
	writeJSON(w, 200, v)
}
func (s *Server) validModelSettings(model, effort string) bool {
	if len(model) > 200 {
		return false
	}
	for _, candidate := range s.models {
		if candidate.ID == model {
			for _, supported := range candidate.Efforts {
				if effort == supported {
					return true
				}
			}
		}
	}
	switch effort {
	case "", "none", "minimal", "low", "medium", "high", "xhigh", "max", "ultra":
		return true
	default:
		return false
	}
}

func agentPermission(raw json.RawMessage, fallback domain.PermissionMode) (domain.PermissionMode, bool) {
	if len(raw) == 0 {
		return fallback, true
	}
	var permission domain.PermissionMode
	if json.Unmarshal(raw, &permission) != nil || !permission.Valid() {
		return "", false
	}
	return permission, true
}

func (s *Server) createAgent(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name, RolePrompt, Model, Effort string
		Permission                      json.RawMessage
	}
	if decode(r, &in) != nil {
		writeError(w, 400, "invalid JSON")
		return
	}
	in.Name = strings.TrimSpace(in.Name)
	in.RolePrompt = strings.TrimSpace(in.RolePrompt)
	if len(in.Name) < 1 || len(in.Name) > 80 || len(in.RolePrompt) < 1 || len(in.RolePrompt) > 16384 {
		writeError(w, 400, "name or rolePrompt is outside allowed length")
		return
	}
	in.Model = strings.TrimSpace(in.Model)
	in.Effort = strings.TrimSpace(in.Effort)
	if !s.validModelSettings(in.Model, in.Effort) {
		writeError(w, 400, "invalid model or effort")
		return
	}
	permission, valid := agentPermission(in.Permission, domain.PermissionAuto)
	if !valid {
		writeError(w, 400, "permission must be auto or full-access")
		return
	}
	now := time.Now()
	s.authMu.Lock()
	sharedAuth, err := s.store.SharedAuth(r.Context())
	if err != nil {
		s.authMu.Unlock()
		writeError(w, 500, "could not load shared authentication state")
		return
	}
	providerState := sharedAuth.State
	if providerState != "connected" && providerState != "pending" {
		providerState = "disconnected"
	}
	a := domain.Agent{ID: uuid.NewString(), Name: in.Name, RolePrompt: in.RolePrompt, Model: in.Model, Effort: in.Effort, Permission: permission, RoleVersion: 1, Status: "stopped", ProviderAuthState: providerState, CreatedAt: now, UpdatedAt: now}
	err = s.store.CreateAgent(r.Context(), a)
	s.authMu.Unlock()
	if err != nil {
		writeError(w, 500, "could not create agent")
		return
	}
	if err := s.ensureAgent(r.Context(), a); err != nil {
		s.logger.Error("start newly created agent", "agent", a.ID, "error", err)
		_ = s.store.SetAgentStatus(r.Context(), a.ID, "error")
		a.Status = "error"
	} else if running, loadErr := s.store.Agent(r.Context(), a.ID); loadErr == nil {
		a = running
	}
	writeJSON(w, 201, a)
}
func (s *Server) getAgent(w http.ResponseWriter, r *http.Request) {
	a, err := s.store.Agent(r.Context(), r.PathValue("agentID"))
	if err != nil {
		writeError(w, 404, "agent not found")
		return
	}
	writeJSON(w, 200, a)
}
func (s *Server) updateAgent(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name, RolePrompt string
		Model, Effort    *string
		Permission       json.RawMessage
	}
	if decode(r, &in) != nil {
		writeError(w, 400, "invalid JSON")
		return
	}
	in.Name = strings.TrimSpace(in.Name)
	in.RolePrompt = strings.TrimSpace(in.RolePrompt)
	if len(in.Name) < 1 || len(in.Name) > 80 || len(in.RolePrompt) < 1 || len(in.RolePrompt) > 16384 {
		writeError(w, 400, "name or rolePrompt is outside allowed length")
		return
	}
	id := r.PathValue("agentID")
	unlock := s.lockAgent(id)
	defer unlock()
	current, err := s.store.Agent(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, 404, "agent not found")
		return
	}
	if err != nil {
		writeError(w, 500, "database error")
		return
	}
	model, effort := current.Model, current.Effort
	if in.Model != nil {
		model = strings.TrimSpace(*in.Model)
	}
	if in.Effort != nil {
		effort = strings.TrimSpace(*in.Effort)
	}
	if !s.validModelSettings(model, effort) {
		writeError(w, 400, "invalid model or effort")
		return
	}
	permission, valid := agentPermission(in.Permission, current.Permission)
	if !valid {
		writeError(w, 400, "permission must be auto or full-access")
		return
	}
	if in.RolePrompt != current.RolePrompt || model != current.Model || effort != current.Effort || permission != current.Permission {
		busy, busyErr := s.store.AgentBusy(r.Context(), id)
		if busyErr != nil {
			writeError(w, 500, "could not inspect active run")
			return
		}
		if human, humanErr := s.store.HumanControlsDesktop(r.Context(), id, time.Now()); humanErr != nil {
			writeError(w, 500, "could not inspect desktop control")
			return
		} else if human {
			busy = true
		}
		if busy {
			writeError(w, 409, "finish the active run or release desktop control before changing agent settings")
			return
		}
	}
	a, err := s.store.UpdateAgent(r.Context(), id, in.Name, in.RolePrompt, model, effort, permission)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, 404, "agent not found")
		} else {
			writeError(w, 500, "could not update agent")
		}
		return
	}
	writeJSON(w, 200, a)
}
func (s *Server) archiveAgent(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("agentID")
	s.authMu.Lock()
	defer s.authMu.Unlock()
	unlock := s.lockAgent(id)
	defer unlock()
	if auth, err := s.store.SharedAuth(r.Context()); err == nil && auth.State == "pending" && auth.PendingAgentID == id && sharedAuthPending(auth, time.Now()) {
		writeError(w, 409, "finish the shared device login before deleting this agent")
		return
	}
	if _, err := s.store.Agent(r.Context(), id); err != nil {
		writeError(w, 404, "agent not found")
		return
	}
	if err := s.stopAgentRuntime(r.Context(), id); err != nil {
		writeError(w, 502, err.Error())
		return
	}
	if err := s.store.ArchiveAgent(r.Context(), id); err != nil {
		writeError(w, 500, "could not archive agent")
		return
	}
	w.WriteHeader(204)
}
func (s *Server) ensureAgent(ctx context.Context, a domain.Agent) error {
	_ = s.store.SetAgentStatus(ctx, a.ID, "starting")
	if err := s.runtime.StartAgent(ctx, runtimeclient.AgentConfig{ID: a.ID, Name: a.Name, RolePrompt: a.RolePrompt, RoleVersion: a.RoleVersion}); err != nil {
		_ = s.store.SetAgentStatus(ctx, a.ID, "error")
		return err
	}
	return s.store.SetAgentStatus(ctx, a.ID, "running")
}
func (s *Server) startAgent(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("agentID")
	unlock := s.lockAgent(id)
	defer unlock()
	a, err := s.store.Agent(r.Context(), id)
	if err != nil {
		writeError(w, 404, "agent not found")
		return
	}
	if err = s.ensureAgent(r.Context(), a); err != nil {
		writeError(w, 502, err.Error())
		return
	}
	a, _ = s.store.Agent(r.Context(), a.ID)
	writeJSON(w, 200, a)
}
func (s *Server) stopAgent(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("agentID")
	unlock := s.lockAgent(id)
	defer unlock()
	if _, err := s.store.Agent(r.Context(), id); err != nil {
		writeError(w, 404, "agent not found")
		return
	}
	if err := s.stopAgentRuntime(r.Context(), id); err != nil {
		writeError(w, 502, err.Error())
		return
	}
	w.WriteHeader(204)
}
func (s *Server) stopAgentRuntime(ctx context.Context, id string) error {
	if err := s.store.CancelQueuedCollaborationTasks(ctx, id, "agent stopped by user"); err != nil {
		return err
	}
	var active *domain.Run
	if run, err := s.store.ActiveRun(ctx, id); err == nil {
		active = &run
		if c, e := s.store.Conversation(ctx, run.ConversationID); e == nil && run.CodexTurnID != "" {
			_ = s.runtime.Interrupt(ctx, id, c.CodexThreadID, run.CodexTurnID)
		}
	}
	if err := s.runtime.StopAgent(ctx, id); err != nil {
		return err
	}
	if active != nil {
		_ = s.store.FinishRun(ctx, active.ID, "interrupted", "agent stopped by user")
		s.cancelRelay(active.ID)
	}
	// Queue acceptance stays responsive during the remote stop operation. Also
	// cancel instructions accepted during that operation before releasing its lock.
	if err := s.store.CancelQueuedCollaborationTasks(ctx, id, "agent stopped by user"); err != nil {
		return err
	}
	return s.store.SetAgentStatus(ctx, id, "stopped")
}

func platformInstructions(role string) string {
	return "You are an isolated Codexbot agent working in a shared /home/agent directory. Other agents may modify shared files concurrently. Prefer browser DOM automation and use raw desktop control only when needed. Keep application profiles under the configured private profile volume. Permission requests are handled automatically by the worker according to the agent Permission setting. Do not ask the user for permission approval. If a request is denied, follow the reason and try an appropriate alternative, or report the limitation. You may still ask for information needed to complete the task.\n\n" + collaborationInstructions + "\n\nAgent role:\n" + role
}

// New chat rotates the agent's active context while keeping its timeline.
func (s *Server) newConversation(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("agentID")
	unlock := s.lockAgent(id)
	defer unlock()
	c, err := s.store.ResetAgentConversation(r.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrConversationBusy) {
			writeError(w, 409, err.Error())
		} else if errors.Is(err, store.ErrNotFound) {
			writeError(w, 404, "agent not found")
		} else {
			writeError(w, 500, "could not start new chat")
		}
		return
	}
	writeJSON(w, 201, c)
}
func (s *Server) message(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Attachments    []domain.Attachment `json:"attachments"`
		Prompt         string
		ConversationID string
		MessageID      string `json:"messageId"`
	}
	if domain.DecodeMessage(r.Body, &in) != nil || (strings.TrimSpace(in.Prompt) == "" && len(in.Attachments) == 0) {
		writeError(w, 400, "valid prompt or attachments are required")
		return
	}
	if err := domain.ValidateAttachments(in.Attachments); err != nil {
		writeError(w, 400, err.Error())
		return
	}
	displayPrompt := domain.AttachmentPrompt(in.Prompt, in.Attachments)
	id := r.PathValue("agentID")
	unlock := s.lockAgent(id)
	defer unlock()
	a, err := s.store.Agent(r.Context(), id)
	if err != nil {
		writeError(w, 404, "agent not found")
		return
	}
	if human, err := s.store.HumanControlsDesktop(r.Context(), a.ID, time.Now()); err != nil {
		writeError(w, 500, "could not inspect desktop control")
		return
	} else if human {
		writeError(w, 409, "release desktop control before sending instructions")
		return
	}
	if in.ConversationID != "" {
		previous, err := s.store.Conversation(r.Context(), in.ConversationID)
		if err != nil || previous.AgentID != a.ID {
			writeError(w, 404, "conversation not found")
			return
		}
	}
	c, err := s.store.EnsureAgentConversation(r.Context(), a)
	if err != nil {
		writeError(w, 500, "could not load agent conversation")
		return
	}
	if in.ConversationID != "" && in.ConversationID != c.ID {
		writeError(w, 409, "a new chat has started; refresh the timeline before sending this message")
		return
	}
	if active, activeErr := s.store.ActiveRun(r.Context(), a.ID); activeErr == nil {
		if strings.HasPrefix(active.Source, "schedule:") {
			writeError(w, 409, "scheduled work is running; send this chat message after it finishes")
			return
		}
		activeConversation, err := s.store.Conversation(r.Context(), active.ConversationID)
		if err != nil {
			writeError(w, 500, "could not read active conversation")
			return
		}
		messageID := in.MessageID
		if messageID == "" {
			messageID = uuid.NewString()
		}
		if _, err := uuid.Parse(messageID); err != nil {
			writeError(w, 400, "invalid messageId")
			return
		}
		out, err := s.runtime.SteerTurn(r.Context(), a.ID, runtimeclient.SteerRequest{MessageID: messageID, RunID: active.ID, ThreadID: activeConversation.CodexThreadID, ExpectedTurnID: active.CodexTurnID, Prompt: in.Prompt, Attachments: in.Attachments})
		if err != nil {
			status := 502
			var runtimeErr *runtimeclient.HTTPError
			if errors.As(err, &runtimeErr) && runtimeErr.StatusCode == 409 {
				status = 409
			}
			writeError(w, status, err.Error())
			return
		}
		go s.relayRun(a.ID, active.ID)
		writeJSON(w, 202, map[string]any{"runId": active.ID, "conversationId": c.ID, "threadId": out.ThreadID, "turnId": out.TurnID, "messageId": messageID, "steered": true})
		return
	} else if !errors.Is(activeErr, store.ErrNotFound) {
		writeError(w, 500, "could not inspect active run")
		return
	}
	if c.Title == "" {
		c.Title = truncate(displayPrompt, 80)
		if err = s.store.SetConversationTitle(r.Context(), c.ID, c.Title); err != nil {
			writeError(w, 500, "could not update conversation")
			return
		}
	}
	if err = s.ensureAgent(r.Context(), a); err != nil {
		writeError(w, 502, err.Error())
		return
	}
	run := domain.Run{ID: uuid.NewString(), AgentID: a.ID, ConversationID: c.ID, Source: "manual", Prompt: displayPrompt, Status: "queued"}
	if err = s.store.CreateRun(r.Context(), run); err != nil {
		writeError(w, 409, "agent already has an active run")
		return
	}
	out, err := s.runtime.StartTurn(r.Context(), a.ID, runtimeclient.TurnRequest{Attachments: in.Attachments, Model: a.Model, Effort: a.Effort, Permission: a.Permission, RunID: run.ID, ConversationID: c.ID, ThreadID: c.CodexThreadID, Prompt: in.Prompt, DeveloperInstructions: platformInstructions(a.RolePrompt), Source: "manual"})
	if err != nil {
		_ = s.store.FinishRun(r.Context(), run.ID, "failed", err.Error())
		writeError(w, 502, err.Error())
		return
	}
	if c.CodexThreadID == "" {
		_ = s.store.SetConversationThread(r.Context(), c.ID, out.ThreadID)
	}
	_ = s.store.SetRunStarted(r.Context(), run.ID, out.TurnID)
	go s.relayRun(a.ID, run.ID)
	writeJSON(w, 202, map[string]any{"runId": run.ID, "conversationId": c.ID, "threadId": out.ThreadID, "turnId": out.TurnID})
}
func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	r := []rune(s)
	if len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}
func (s *Server) relayRun(agentID, runID string) {
	defer s.wakeCollaboration()
	ctx, cancel := context.WithTimeout(context.Background(), 7*24*time.Hour)
	s.relayMu.Lock()
	if _, exists := s.relays[runID]; exists {
		s.relayMu.Unlock()
		cancel()
		return
	}
	s.relays[runID] = cancel
	s.relayMu.Unlock()
	defer func() { cancel(); s.relayMu.Lock(); delete(s.relays, runID); s.relayMu.Unlock() }()
	after, _ := s.store.MaxEventSequence(ctx, runID)
	for {
		batch, err := s.runtime.Events(ctx, agentID, runID, after)
		if err != nil {
			s.logger.Warn("event relay failed", "agent", agentID, "run", runID, "error", err)
			select {
			case <-ctx.Done():
				if errors.Is(ctx.Err(), context.DeadlineExceeded) {
					_ = s.store.FinishRun(context.Background(), runID, "unknown", ctx.Err().Error())
				}
				return
			case <-time.After(time.Second):
				continue
			}
		}
		persistFailed := false
		for _, e := range batch.Events {
			var payload any = e.Payload
			if len(e.Payload) > 0 {
				var decoded any
				if json.Unmarshal(e.Payload, &decoded) == nil {
					payload = decoded
				}
			}
			ev, err := s.store.AppendWorkerEvent(ctx, runID, e.Sequence, e.Type, payload)
			if err != nil {
				s.logger.Warn("persist run event failed", "run", runID, "sequence", e.Sequence, "error", err)
				persistFailed = true
				break
			}
			if ev.Sequence > after {
				after = ev.Sequence
			}
		}
		if !persistFailed && batch.Status != "" && batch.Status != "running" && batch.Status != "starting" {
			status := batch.Status
			if status != "completed" && status != "interrupted" && status != "failed" {
				status = "unknown"
			}
			_ = s.store.FinishRun(context.Background(), runID, status, batch.Error)
			return
		}
		select {
		case <-ctx.Done():
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				_ = s.store.FinishRun(context.Background(), runID, "unknown", ctx.Err().Error())
			}
			return
		case <-time.After(350 * time.Millisecond):
		}
	}
}
func (s *Server) cancelRelay(runID string) {
	s.relayMu.Lock()
	cancel := s.relays[runID]
	s.relayMu.Unlock()
	if cancel != nil {
		cancel()
	}
}
func (s *Server) getRun(w http.ResponseWriter, r *http.Request) {
	run, err := s.store.Run(r.Context(), r.PathValue("runID"))
	if err != nil {
		writeError(w, 404, "run not found")
		return
	}
	writeJSON(w, 200, run)
}
func (s *Server) agentHistory(w http.ResponseWriter, r *http.Request) {
	s.agentTimeline(w, r)
}
func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	runID := r.PathValue("runID")
	after, _ := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, 500, "streaming unsupported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		events, err := s.store.EventsAfter(r.Context(), runID, after)
		if err != nil {
			return
		}
		for _, e := range events {
			b, _ := json.Marshal(e)
			fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", e.Sequence, e.Type, b)
			after = e.Sequence
		}
		flusher.Flush()
		run, err := s.store.Run(r.Context(), runID)
		if err != nil {
			return
		}
		if run.Status != "queued" && run.Status != "running" && len(events) == 0 {
			payload, _ := json.Marshal(map[string]string{"status": run.Status, "error": run.Error})
			fmt.Fprintf(w, "event: run.status\ndata: %s\n\n", payload)
			flusher.Flush()
			return
		}
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
		}
	}
}
func (s *Server) interrupt(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("agentID")
	unlock := s.lockAgent(id)
	defer unlock()
	run, err := s.store.ActiveRun(r.Context(), id)
	if err != nil {
		writeError(w, 404, "no active run")
		return
	}
	c, err := s.store.Conversation(r.Context(), run.ConversationID)
	if err != nil {
		writeError(w, 500, "conversation not found")
		return
	}
	if run.CodexTurnID != "" {
		if err = s.runtime.Interrupt(r.Context(), run.AgentID, c.CodexThreadID, run.CodexTurnID); err != nil {
			writeError(w, 502, err.Error())
			return
		}
	}
	_ = s.store.FinishRun(r.Context(), run.ID, "interrupted", "interrupted by user")
	s.cancelRelay(run.ID)
	w.WriteHeader(204)
}

func (s *Server) deviceLogin(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("agentID")
	s.authMu.Lock()
	defer s.authMu.Unlock()
	unlock := s.lockAgent(id)
	defer unlock()
	if pending, err := s.sharedAuthPending(r.Context(), time.Now()); err != nil {
		writeError(w, 500, "could not load shared authentication state")
		return
	} else if pending {
		writeError(w, 409, "a shared device login is already pending")
		return
	}
	a, err := s.store.Agent(r.Context(), id)
	if err != nil {
		writeError(w, 404, "agent not found")
		return
	}
	if err = s.ensureAgent(r.Context(), a); err != nil {
		writeError(w, 502, err.Error())
		return
	}
	pendingSince := time.Now()
	if err = s.store.SetSharedAuthPending(r.Context(), id, "", pendingSince); err != nil {
		writeError(w, 500, "could not reserve shared login state")
		return
	}
	out, err := s.runtime.DeviceLogin(r.Context(), id)
	if err != nil {
		_ = s.store.SetSharedAuthState(r.Context(), "disconnected")
		writeError(w, 502, err.Error())
		return
	}
	if err = s.store.SetSharedAuthPending(r.Context(), id, out.LoginID, pendingSince); err != nil {
		writeError(w, 500, "could not save shared login state")
		return
	}
	writeJSON(w, 200, out)
}
func (s *Server) apiKeyLogin(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("agentID")
	var in struct {
		APIKey string `json:"apiKey"`
	}
	if decode(r, &in) != nil || !strings.HasPrefix(in.APIKey, "sk-") {
		writeError(w, 400, "invalid API key")
		return
	}
	s.authMu.Lock()
	defer s.authMu.Unlock()
	unlock := s.lockAgent(id)
	defer unlock()
	if pending, err := s.sharedAuthPending(r.Context(), time.Now()); err != nil {
		writeError(w, 500, "could not load shared authentication state")
		return
	} else if pending {
		writeError(w, 409, "a shared device login is pending")
		return
	}
	a, err := s.store.Agent(r.Context(), id)
	if err != nil {
		writeError(w, 404, "agent not found")
		return
	}
	if err = s.ensureAgent(r.Context(), a); err == nil {
		err = s.runtime.APIKeyLogin(r.Context(), id, in.APIKey)
	}
	in.APIKey = ""
	if err != nil {
		writeError(w, 502, err.Error())
		return
	}
	if err = s.store.SetSharedAuthState(r.Context(), "connected"); err != nil {
		writeError(w, 500, "could not save shared authentication state")
		return
	}
	w.WriteHeader(204)
}
func (s *Server) providerLogout(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("agentID")
	s.authMu.Lock()
	defer s.authMu.Unlock()
	unlock := s.lockAgent(id)
	defer unlock()
	if pending, err := s.sharedAuthPending(r.Context(), time.Now()); err != nil {
		writeError(w, 500, "could not load shared authentication state")
		return
	} else if pending {
		writeError(w, 409, "a shared device login is pending")
		return
	}
	if _, err := s.store.Agent(r.Context(), id); err != nil {
		writeError(w, 404, "agent not found")
		return
	}
	if err := s.runtime.Logout(r.Context(), id); err != nil {
		writeError(w, 502, err.Error())
		return
	}
	if err := s.store.SetSharedAuthState(r.Context(), "disconnected"); err != nil {
		writeError(w, 500, "could not save shared authentication state")
		return
	}
	w.WriteHeader(204)
}
func (s *Server) providerStatus(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("agentID")
	s.authMu.Lock()
	defer s.authMu.Unlock()
	if _, err := s.store.Agent(r.Context(), id); err != nil {
		writeError(w, 404, "agent not found")
		return
	}
	auth, err := s.store.SharedAuth(r.Context())
	if err != nil {
		writeError(w, 500, "could not load shared authentication state")
		return
	}
	if auth.State == "pending" && !sharedAuthPending(auth, time.Now()) {
		if err = s.store.SetSharedAuthState(r.Context(), "disconnected"); err != nil {
			writeError(w, 500, "could not expire shared login state")
			return
		}
		writeJSON(w, 200, map[string]string{"state": "disconnected"})
		return
	}
	if auth.Verified && auth.State != "pending" && auth.VerifiedAt != nil && time.Since(*auth.VerifiedAt) < sharedAuthVerifyTTL {
		writeJSON(w, 200, map[string]string{"state": auth.State})
		return
	}
	targetID := id
	if auth.State == "pending" {
		targetID = auth.PendingAgentID
	}
	unlock := s.lockAgent(targetID)
	defer unlock()
	a, err := s.store.Agent(r.Context(), targetID)
	if err != nil {
		writeError(w, 409, "the agent handling the shared login is unavailable")
		return
	}
	if err = s.ensureAgent(r.Context(), a); err != nil {
		writeError(w, 502, err.Error())
		return
	}
	status, err := s.runtime.AuthStatus(r.Context(), targetID)
	if err != nil {
		writeError(w, 502, err.Error())
		return
	}
	if status.State == "connected" {
		if err = s.store.SetSharedAuthState(r.Context(), "connected"); err != nil {
			writeError(w, 500, "could not save shared authentication state")
			return
		}
		writeJSON(w, 200, status)
		return
	}
	if auth.State == "pending" {
		writeJSON(w, 200, map[string]string{"state": "pending"})
		return
	}
	if err = s.store.SetSharedAuthState(r.Context(), "disconnected"); err != nil {
		writeError(w, 500, "could not save shared authentication state")
		return
	}
	writeJSON(w, 200, map[string]string{"state": "disconnected"})
}

func sharedAuthPending(auth domain.SharedAuth, now time.Time) bool {
	return auth.State == "pending" && auth.PendingSince != nil && now.Sub(*auth.PendingSince) < sharedAuthPendingTTL
}

func (s *Server) sharedAuthPending(ctx context.Context, now time.Time) (bool, error) {
	auth, err := s.store.SharedAuth(ctx)
	if err != nil {
		return false, err
	}
	if auth.State != "pending" {
		return false, nil
	}
	if sharedAuthPending(auth, now) {
		return true, nil
	}
	return false, s.store.SetSharedAuthState(ctx, "disconnected")
}

func (s *Server) listSchedules(w http.ResponseWriter, r *http.Request) {
	v, err := s.store.Schedules(r.Context(), r.URL.Query().Get("agentId"))
	if err != nil {
		writeError(w, 500, "database error")
		return
	}
	if v == nil {
		v = []domain.Schedule{}
	}
	writeJSON(w, 200, v)
}
func scheduleNext(kind, expr, tz string, from time.Time) (*time.Time, error) {
	loc, err := time.LoadLocation(tz)
	if err != nil {
		return nil, errors.New("unknown timezone")
	}
	switch kind {
	case "once":
		t, err := time.Parse(time.RFC3339, expr)
		if err != nil {
			return nil, errors.New("once expression must be RFC3339")
		}
		if !t.After(from) {
			return nil, errors.New("scheduled time must be in the future")
		}
		return &t, nil
	case "cron":
		spec, err := cron.ParseStandard(expr)
		if err != nil {
			return nil, fmt.Errorf("invalid cron expression: %w", err)
		}
		n := spec.Next(from.In(loc))
		return &n, nil
	default:
		return nil, errors.New("kind must be once or cron")
	}
}
func (s *Server) createSchedule(w http.ResponseWriter, r *http.Request) {
	var in struct {
		AgentID, Name, Prompt, Kind, Expression, Timezone string
		Enabled                                           *bool
	}
	if decode(r, &in) != nil {
		writeError(w, 400, "invalid JSON")
		return
	}
	if in.Timezone == "" {
		in.Timezone = "Asia/Tokyo"
	}
	unlock := s.lockAgent(in.AgentID)
	defer unlock()
	if _, err := s.store.Agent(r.Context(), in.AgentID); err != nil {
		writeError(w, 404, "agent not found")
		return
	}
	next, err := scheduleNext(in.Kind, in.Expression, in.Timezone, time.Now())
	if err != nil {
		writeError(w, 400, err.Error())
		return
	}
	enabled := true
	if in.Enabled != nil {
		enabled = *in.Enabled
	}
	now := time.Now()
	sc := domain.Schedule{ID: uuid.NewString(), AgentID: in.AgentID, Name: strings.TrimSpace(in.Name), Prompt: strings.TrimSpace(in.Prompt), Kind: in.Kind, Expression: in.Expression, Timezone: in.Timezone, Enabled: enabled, NextRunAt: next, CreatedAt: now, UpdatedAt: now}
	if sc.Name == "" || sc.Prompt == "" {
		writeError(w, 400, "name and prompt are required")
		return
	}
	if err = s.store.CreateSchedule(r.Context(), sc); err != nil {
		writeError(w, 500, "could not create schedule")
		return
	}
	writeJSON(w, 201, sc)
}
func (s *Server) deleteSchedule(w http.ResponseWriter, r *http.Request) {
	if err := s.store.DeleteSchedule(r.Context(), r.PathValue("scheduleID")); err != nil {
		writeError(w, 500, "could not delete schedule")
		return
	}
	w.WriteHeader(204)
}
func (s *Server) updateSchedule(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Enabled *bool `json:"enabled"`
	}
	if decode(r, &in) != nil || in.Enabled == nil {
		writeError(w, 400, "enabled is required")
		return
	}
	sc, err := s.store.Schedule(r.Context(), r.PathValue("scheduleID"))
	if err != nil {
		writeError(w, 404, "schedule not found")
		return
	}
	unlock := s.lockAgent(sc.AgentID)
	defer unlock()
	if _, err = s.store.Agent(r.Context(), sc.AgentID); err != nil {
		writeError(w, 404, "agent not found")
		return
	}
	sc, err = s.store.Schedule(r.Context(), sc.ID)
	if err != nil {
		writeError(w, 404, "schedule not found")
		return
	}
	var next *time.Time
	if *in.Enabled {
		next, err = scheduleNext(sc.Kind, sc.Expression, sc.Timezone, time.Now())
		if err != nil {
			writeError(w, 400, err.Error())
			return
		}
	}
	if err = s.store.SetScheduleEnabled(r.Context(), sc.ID, *in.Enabled, next); err != nil {
		writeError(w, 500, "could not update schedule")
		return
	}
	sc, _ = s.store.Schedule(r.Context(), sc.ID)
	writeJSON(w, 200, sc)
}

func (s *Server) takeover(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("agentID")
	unlock := s.lockAgent(id)
	defer unlock()
	a, err := s.store.Agent(r.Context(), id)
	if err != nil {
		writeError(w, 404, "agent not found")
		return
	}
	if err = s.ensureAgent(r.Context(), a); err != nil {
		writeError(w, 502, err.Error())
		return
	}
	if run, activeErr := s.store.ActiveRun(r.Context(), id); activeErr == nil {
		continuationID := run.ConversationID
		if strings.HasPrefix(run.Source, "schedule:") {
			continuationID = ""
		}
		if err = s.store.SetDesktopContinuation(r.Context(), id, continuationID); err != nil {
			writeError(w, 500, "could not preserve interrupted conversation")
			return
		}
		c, _ := s.store.Conversation(r.Context(), run.ConversationID)
		if run.CodexTurnID != "" {
			_ = s.runtime.Interrupt(r.Context(), id, c.CodexThreadID, run.CodexTurnID)
		}
		_ = s.store.FinishRun(r.Context(), run.ID, "interrupted", "desktop taken over by user")
		s.cancelRelay(run.ID)
	} else if errors.Is(activeErr, store.ErrNotFound) {
		if err = s.store.SetDesktopContinuation(r.Context(), id, ""); err != nil {
			writeError(w, 500, "could not clear desktop continuation")
			return
		}
	} else {
		writeError(w, 500, "could not inspect active run")
		return
	}
	lease, err := s.store.AcquireDesktopLease(r.Context(), id, "human", 15*time.Minute)
	if err == nil {
		err = s.runtime.SetDesktopLease(r.Context(), id, "human", lease.Generation)
	}
	if err != nil {
		writeError(w, 502, err.Error())
		return
	}
	writeJSON(w, 200, lease)
}
func (s *Server) releaseDesktop(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("agentID")
	unlock := s.lockAgent(id)
	defer unlock()
	a, err := s.store.Agent(r.Context(), id)
	if err != nil {
		writeError(w, 404, "agent not found")
		return
	}
	if err = s.ensureAgent(r.Context(), a); err != nil {
		writeError(w, 502, err.Error())
		return
	}
	lease, err := s.store.AcquireDesktopLease(r.Context(), id, "agent", 30*time.Minute)
	if err == nil {
		err = s.runtime.SetDesktopLease(r.Context(), id, "agent", lease.Generation)
	}
	if err != nil {
		writeError(w, 502, err.Error())
		return
	}
	conversationID, takeErr := s.store.TakeDesktopContinuation(r.Context(), id)
	continueRunID, continueErr := "", takeErr
	if continueErr == nil && conversationID != "" {
		continueRunID, continueErr = s.startContinuation(r.Context(), a, conversationID)
	}
	response := map[string]any{"agentId": lease.AgentID, "holder": lease.Holder, "generation": lease.Generation, "expiresAt": lease.ExpiresAt, "continueRunId": continueRunID}
	if continueErr != nil {
		response["continueError"] = continueErr.Error()
	}
	writeJSON(w, 200, response)
}
func (s *Server) startContinuation(ctx context.Context, a domain.Agent, conversationID string) (string, error) {
	if conversationID == "" {
		return "", nil
	}
	busy, err := s.store.AgentBusy(ctx, a.ID)
	if err != nil {
		return "", err
	}
	if busy {
		return "", errors.New("agent already has an active run")
	}
	prompt := "Continue from the current desktop state. Inspect the latest browser and desktop screenshots before acting, and preserve the user's manual changes."
	c, err := s.store.Conversation(ctx, conversationID)
	if err != nil {
		return "", err
	}
	if c.AgentID != a.ID {
		return "", errors.New("conversation not found")
	}
	previous := c
	c, err = s.store.EnsureAgentConversation(ctx, a)
	if err != nil {
		return "", err
	}
	if previous.Kind == "scheduled" && previous.ID != c.ID {
		return "", nil
	}
	run := domain.Run{ID: uuid.NewString(), AgentID: a.ID, ConversationID: c.ID, Source: "manual", Prompt: prompt, Status: "queued"}
	if err = s.store.CreateRun(ctx, run); err != nil {
		return "", err
	}
	out, err := s.runtime.StartTurn(ctx, a.ID, runtimeclient.TurnRequest{Model: a.Model, Effort: a.Effort, Permission: a.Permission, RunID: run.ID, ConversationID: c.ID, ThreadID: c.CodexThreadID, Prompt: prompt, DeveloperInstructions: platformInstructions(a.RolePrompt), Source: "manual"})
	if err != nil {
		_ = s.store.FinishRun(ctx, run.ID, "failed", err.Error())
		return "", err
	}
	if c.CodexThreadID == "" {
		_ = s.store.SetConversationThread(ctx, c.ID, out.ThreadID)
	}
	_ = s.store.SetRunStarted(ctx, run.ID, out.TurnID)
	go s.relayRun(a.ID, run.ID)
	return run.ID, nil
}
func (s *Server) desktopHeartbeat(w http.ResponseWriter, r *http.Request) {
	lease, err := s.store.RenewHumanDesktopLease(r.Context(), r.PathValue("agentID"), 15*time.Minute)
	if err != nil {
		writeError(w, 409, "human desktop lease is not active")
		return
	}
	writeJSON(w, 200, lease)
}
func (s *Server) desktopProxy(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("agentID")
	rest := r.PathValue("rest")
	unlock := s.lockAgent(id)
	a, err := s.store.Agent(r.Context(), id)
	if err != nil {
		unlock()
		writeError(w, 404, "agent not found")
		return
	}
	// Loading the desktop root is also the reconciliation point after a
	// runtime-manager restart: it reattaches the manager to the agent's private
	// network even when the persisted Agent status is already running.
	if a.Status != "running" || rest == "" || rest == "index.html" {
		if err = s.ensureAgent(r.Context(), a); err != nil {
			unlock()
			writeError(w, 502, err.Error())
			return
		}
	}
	unlock()
	target := s.runtime.ProxyURL(id, "desktop", rest)
	proxy := httputil.NewSingleHostReverseProxy(target)
	orig := proxy.Director
	proxy.Director = func(req *http.Request) {
		orig(req)
		req.URL.Path = target.Path
		req.Host = target.Host
		req.Header.Set("Authorization", "Bearer "+s.runtime.Token())
	}
	proxy.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, err error) { writeError(w, 502, "desktop unavailable") }
	proxy.ServeHTTP(w, r)
}

func (s *Server) StartBackground(ctx context.Context) {
	if err := s.store.RecoverCollaborationTasks(ctx); err != nil {
		s.logger.Error("recover collaboration deliveries", "error", err)
	}
	if runs, err := s.store.ActiveRuns(ctx); err == nil {
		for _, run := range runs {
			go s.relayRun(run.AgentID, run.ID)
		}
	} else {
		s.logger.Error("resume active runs", "error", err)
	}
	go s.scheduler(ctx)
	go s.collaborationLoop(ctx)
}
func (s *Server) scheduler(ctx context.Context) {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		s.expireDesktopLeases(ctx)
		s.runDue(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
func (s *Server) runDue(ctx context.Context) {
	due, err := s.store.DueSchedules(ctx, time.Now())
	if err != nil {
		s.logger.Error("load due schedules", "error", err)
		return
	}
	for _, sc := range due {
		s.runDueSchedule(ctx, sc)
	}
}

func (s *Server) runDueSchedule(ctx context.Context, sc domain.Schedule) {
	unlock := s.lockAgent(sc.AgentID)
	defer unlock()
	scheduled := *sc.NextRunAt
	busy, _ := s.store.AgentBusy(ctx, sc.AgentID)
	if human, _ := s.store.HumanControlsDesktop(ctx, sc.AgentID, time.Now()); human {
		busy = true
	}
	if busy && time.Since(scheduled) < 15*time.Minute {
		return
	}
	next, enabled := (*time.Time)(nil), false
	if sc.Kind == "cron" {
		next, _ = scheduleNext(sc.Kind, sc.Expression, sc.Timezone, scheduled)
		enabled = true
	}
	a, err := s.store.Agent(ctx, sc.AgentID)
	if err != nil {
		return
	}
	c := domain.Conversation{ID: uuid.NewString(), AgentID: a.ID, Kind: "scheduled", RoleVersion: a.RoleVersion, Title: sc.Name, CreatedAt: time.Now()}
	status := "queued"
	if busy {
		status = "skipped_overlap"
	}
	run := domain.Run{ID: uuid.NewString(), AgentID: a.ID, ConversationID: c.ID, Source: "schedule:" + sc.ID, Prompt: sc.Prompt, Status: status, ScheduledFor: &scheduled}
	if busy {
		run.Error = "agent busy beyond misfire grace"
	}
	if err = s.store.ClaimScheduleRun(ctx, sc, c, run, next, enabled); err != nil {
		return
	}
	if busy {
		return
	}
	if err = s.ensureAgent(ctx, a); err != nil {
		_ = s.store.FinishRun(ctx, run.ID, "failed", err.Error())
		return
	}
	out, err := s.runtime.StartTurn(ctx, a.ID, runtimeclient.TurnRequest{Model: a.Model, Effort: a.Effort, Permission: a.Permission, RunID: run.ID, ConversationID: c.ID, Prompt: sc.Prompt, DeveloperInstructions: platformInstructions(a.RolePrompt), Source: "scheduled"})
	if err != nil {
		_ = s.store.FinishRun(ctx, run.ID, "failed", err.Error())
		return
	}
	_ = s.store.SetConversationThread(ctx, c.ID, out.ThreadID)
	_ = s.store.SetRunStarted(ctx, run.ID, out.TurnID)
	go s.relayRun(a.ID, run.ID)
}
func (s *Server) expireDesktopLeases(ctx context.Context) {
	leases, err := s.store.ExpiredHumanDesktopLeases(ctx, time.Now())
	if err != nil {
		s.logger.Error("load expired desktop leases", "error", err)
		return
	}
	for _, expired := range leases {
		s.expireDesktopLease(ctx, expired)
	}
}

func (s *Server) expireDesktopLease(ctx context.Context, expired domain.DesktopLease) {
	unlock := s.lockAgent(expired.AgentID)
	defer unlock()
	a, err := s.store.Agent(ctx, expired.AgentID)
	if err != nil {
		return
	}
	lease, err := s.store.AcquireDesktopLease(ctx, a.ID, "agent", 30*time.Minute)
	if err != nil {
		return
	}
	if err = s.runtime.SetDesktopLease(ctx, a.ID, "agent", lease.Generation); err != nil {
		_, _ = s.store.AcquireDesktopLease(ctx, a.ID, "human", time.Minute)
		s.logger.Warn("release expired desktop lease", "agent", a.ID, "error", err)
		return
	}
	conversationID, takeErr := s.store.TakeDesktopContinuation(ctx, a.ID)
	if takeErr != nil {
		s.logger.Warn("load expired desktop continuation", "agent", a.ID, "error", takeErr)
		return
	}
	if _, err = s.startContinuation(ctx, a, conversationID); err != nil {
		s.logger.Warn("continue after expired desktop lease", "agent", a.ID, "error", err)
	}
}
