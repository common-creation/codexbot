package agentworker

import (
	"bufio"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/common-creation/codexbot/internal/appserver"
	"github.com/common-creation/codexbot/internal/domain"
	"github.com/google/uuid"
)

type Config struct{ AgentID, Token, ProfileDir, AgentHomeDir, CodexExecutable, CollaborationURL, CollaborationSocket string }

func (c Config) agentHome() string {
	if c.AgentHomeDir != "" {
		return c.AgentHomeDir
	}
	return "/home/agent"
}

type Worker struct {
	cfg                 Config
	client              *appserver.Client
	logger              *slog.Logger
	mux                 *http.ServeMux
	operationMu         sync.Mutex
	inputMu             sync.Mutex
	collaborationServer *http.Server
	mu                  sync.Mutex
	active              *activeRun
	ctx                 context.Context
	healthy             atomic.Bool
	failed              chan struct{}
}
type activeRun struct {
	RunID, ThreadID, TurnID string
	Permission              domain.PermissionMode
	Prompt                  string
	runContext              context.Context
	cancelRun               context.CancelFunc
	Sequence                int64
	Status, Error           string
}
type turnRequest struct {
	Attachments           []domain.Attachment   `json:"attachments"`
	Model                 string                `json:"model"`
	Effort                string                `json:"effort"`
	RunID                 string                `json:"runId"`
	ConversationID        string                `json:"conversationId"`
	ThreadID              string                `json:"threadId"`
	Prompt                string                `json:"prompt"`
	DeveloperInstructions string                `json:"developerInstructions"`
	Source                string                `json:"source"`
	Permission            domain.PermissionMode `json:"permission"`
}
type workerEvent struct {
	Sequence int64           `json:"sequence"`
	Type     string          `json:"type"`
	Payload  json.RawMessage `json:"payload"`
}

func Start(ctx context.Context, cfg Config, logger *slog.Logger) (*Worker, error) {
	if cfg.Token == "" {
		return nil, errors.New("worker token is required")
	}
	if cfg.ProfileDir == "" {
		cfg.ProfileDir = "/var/lib/codexbot/profile"
	}
	if cfg.CodexExecutable == "" {
		cfg.CodexExecutable = "codex"
	}
	if logger == nil {
		logger = slog.Default()
	}
	// The worker keeps its internal capability token in memory. Codex App
	// Server and every shell command it launches must not inherit control-plane
	// or Kasm credentials through the environment.
	for _, key := range []string{"CODEXBOT_WORKER_TOKEN", "KASM_PASSWORD", "KASMVNC_VIEWER_PASSWORD", "KASMVNC_OWNER_PASSWORD"} {
		_ = os.Unsetenv(key)
	}
	if err := os.MkdirAll(filepath.Join(cfg.ProfileDir, "outbox"), 0700); err != nil {
		return nil, err
	}
	recoverOutbox(filepath.Join(cfg.ProfileDir, "outbox"))
	title := "Codexbot Agent Worker"
	// Native auto-review notifications are experimental app-server events.
	client, err := appserver.Start(ctx, appserver.Config{Executable: cfg.CodexExecutable, Dir: cfg.agentHome(), Stderr: os.Stderr, ClientInfo: appserver.ClientInfo{Name: "codexbot", Title: &title, Version: "0.1.0"}, Capabilities: appserver.Capabilities{ExperimentalAPI: true}})
	if err != nil {
		return nil, err
	}
	w := &Worker{cfg: cfg, client: client, logger: logger, mux: http.NewServeMux(), ctx: ctx, failed: make(chan struct{})}
	w.healthy.Store(true)
	w.routes()
	if err := w.startCollaborationSocket(); err != nil {
		_ = client.Close()
		return nil, err
	}
	go w.consumeNotifications(ctx)
	go w.consumeApprovals(ctx)
	go w.monitorAppServer(ctx)
	return w, nil
}
func (w *Worker) Close() error {
	if w.collaborationServer != nil {
		_ = w.collaborationServer.Close()
	}
	w.mu.Lock()
	if w.active != nil && w.active.cancelRun != nil {
		w.active.cancelRun()
	}
	w.mu.Unlock()
	return w.client.Close()
}
func (w *Worker) Failed() <-chan struct{} { return w.failed }
func (w *Worker) Handler() http.Handler   { return w.auth(w.mux) }
func (w *Worker) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			next.ServeHTTP(rw, r)
			return
		}
		got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if subtle.ConstantTimeCompare([]byte(got), []byte(w.cfg.Token)) != 1 {
			http.Error(rw, "unauthorized", 401)
			return
		}
		next.ServeHTTP(rw, r)
	})
}
func (w *Worker) routes() {
	w.mux.HandleFunc("GET /healthz", func(rw http.ResponseWriter, _ *http.Request) {
		if !w.healthy.Load() {
			writeJSON(rw, 503, map[string]string{"status": "failed", "error": "codex app server is not running"})
			return
		}
		writeJSON(rw, 200, map[string]string{"status": "ok"})
	})
	w.mux.HandleFunc("POST /v1/turns", w.startTurn)
	w.mux.HandleFunc("POST /v1/interrupt", w.interrupt)
	w.mux.HandleFunc("POST /v1/steer", w.steer)
	w.mux.HandleFunc("GET /v1/runs/{runID}/events", w.events)
	w.mux.HandleFunc("POST /v1/auth/device", w.deviceLogin)
	w.mux.HandleFunc("POST /v1/auth/api-key", w.apiKeyLogin)
	w.mux.HandleFunc("POST /v1/auth/logout", w.logout)
	w.mux.HandleFunc("GET /v1/auth/status", w.authStatus)
	w.mux.HandleFunc("POST /v1/desktop/lease", w.desktopLease)
}
func (w *Worker) monitorAppServer(ctx context.Context) {
	select {
	case <-ctx.Done():
		return
	case <-w.client.Done():
		w.healthy.Store(false)
		w.mu.Lock()
		active := w.active
		shouldFail := active != nil && (active.Status == "running" || active.Status == "starting")
		w.mu.Unlock()
		if shouldFail {
			message := "codex app server stopped during an active turn"
			if err := w.client.Err(); err != nil {
				message = err.Error()
			}
			w.setTerminal("unknown", message)
		}
		close(w.failed)
	}
}
func writeJSON(rw http.ResponseWriter, status int, v any) {
	rw.Header().Set("Content-Type", "application/json")
	rw.WriteHeader(status)
	_ = json.NewEncoder(rw).Encode(v)
}
func decode(r *http.Request, v any) error {
	defer r.Body.Close()
	return json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(v)
}

func (w *Worker) startTurn(rw http.ResponseWriter, r *http.Request) {
	w.operationMu.Lock()
	defer w.operationMu.Unlock()
	var in turnRequest
	if domain.DecodeMessage(r.Body, &in) != nil || in.RunID == "" || (strings.TrimSpace(in.Prompt) == "" && len(in.Attachments) == 0) {
		http.Error(rw, "invalid turn request", 400)
		return
	}
	if err := domain.ValidateAttachments(in.Attachments); err != nil {
		http.Error(rw, err.Error(), 400)
		return
	}
	if _, err := uuid.Parse(in.RunID); err != nil {
		http.Error(rw, "invalid run id", 400)
		return
	}
	if in.Permission == "" {
		in.Permission = domain.PermissionAuto
	}
	if !in.Permission.Valid() {
		http.Error(rw, "invalid permission", 400)
		return
	}
	w.mu.Lock()
	if w.active != nil && (w.active.Status == "running" || w.active.Status == "starting") {
		w.mu.Unlock()
		http.Error(rw, "agent already has an active turn", 409)
		return
	}
	w.mu.Unlock()
	inputs, attachmentDir, err := materializeAttachments(w.cfg.agentHome(), in.Prompt, in.Attachments)
	if err != nil {
		http.Error(rw, "could not save attachments", 500)
		return
	}
	accepted := false
	defer func() {
		if !accepted && attachmentDir != "" {
			_ = os.RemoveAll(attachmentDir)
		}
	}()
	policy := "on-request"
	reviewer := "auto_review"
	sandbox := "workspace-write"
	sandboxPolicy := &appserver.SandboxPolicy{Type: "workspaceWrite", WritableRoots: []string{w.cfg.agentHome()}}
	if in.Permission == domain.PermissionFullAccess {
		policy = "never"
		reviewer = "user"
		sandbox = "danger-full-access"
		sandboxPolicy = &appserver.SandboxPolicy{Type: "dangerFullAccess"}
	}
	startingState, _ := json.Marshal(map[string]string{"status": "starting", "error": ""})
	if err := atomicWrite(w.statusPath(in.RunID), startingState, 0600); err != nil {
		http.Error(rw, "could not persist starting run", 500)
		return
	}
	startFailureStatus := "failed"
	defer func() {
		if !accepted {
			state, _ := json.Marshal(map[string]string{"status": startFailureStatus, "error": "turn startup did not complete"})
			_ = atomicWrite(w.statusPath(in.RunID), state, 0600)
		}
	}()
	threadID := in.ThreadID
	if threadID == "" {
		out, err := w.client.ThreadStart(r.Context(), appserver.ThreadStartParams{Model: in.Model, CWD: w.cfg.agentHome(), ApprovalPolicy: policy, ApprovalsReviewer: reviewer, Sandbox: sandbox, DeveloperInstructions: in.DeveloperInstructions})
		if err != nil {
			http.Error(rw, err.Error(), 502)
			return
		}
		threadID = out.Thread.ID
	} else {
		out, err := w.client.ThreadResume(r.Context(), appserver.ThreadResumeParams{Model: in.Model, ThreadID: threadID, CWD: w.cfg.agentHome(), ApprovalPolicy: policy, ApprovalsReviewer: reviewer, Sandbox: sandbox, DeveloperInstructions: in.DeveloperInstructions})
		if err != nil {
			http.Error(rw, err.Error(), 502)
			return
		}
		threadID = out.Thread.ID
	}
	w.mu.Lock()
	if w.active != nil && w.active.cancelRun != nil {
		w.active.cancelRun()
	}
	lifetime := w.ctx
	if lifetime == nil {
		lifetime = context.Background()
	}
	runContext, cancelRun := context.WithCancel(lifetime)
	w.active = &activeRun{RunID: in.RunID, ThreadID: threadID, Permission: in.Permission, Prompt: domain.AttachmentPrompt(in.Prompt, in.Attachments), Status: "starting", runContext: runContext, cancelRun: cancelRun}
	w.mu.Unlock()
	turn, err := w.client.TurnStart(r.Context(), appserver.TurnStartParams{Model: in.Model, Effort: in.Effort, ThreadID: threadID, Input: inputs, CWD: w.cfg.agentHome(), ApprovalPolicy: policy, ApprovalsReviewer: reviewer, SandboxPolicy: sandboxPolicy})
	if err != nil {
		var rpcErr *appserver.RPCError
		if !errors.As(err, &rpcErr) || (rpcErr.Code != -32600 && rpcErr.Code != -32602) {
			startFailureStatus = "unknown"
		}
		w.setTerminal(startFailureStatus, err.Error())
		http.Error(rw, err.Error(), 502)
		return
	}
	accepted = true
	w.mu.Lock()
	becameRunning := false
	if w.active != nil && w.active.Status == "starting" {
		w.active.TurnID = turn.Turn.ID
		w.active.Status = "running"
		becameRunning = true
	}
	w.mu.Unlock()
	if becameRunning {
		statusBytes, _ := json.Marshal(map[string]string{"status": "running", "error": ""})
		_ = atomicWrite(w.statusPath(in.RunID), statusBytes, 0600)
	}
	_ = w.appendEvent(in.RunID, "run.started", map[string]any{"threadId": threadID, "turnId": turn.Turn.ID})
	writeJSON(rw, 202, map[string]string{"threadId": threadID, "turnId": turn.Turn.ID})
}
func (w *Worker) interrupt(rw http.ResponseWriter, r *http.Request) {
	w.operationMu.Lock()
	defer w.operationMu.Unlock()
	var in struct {
		ThreadID string `json:"threadId"`
		TurnID   string `json:"turnId"`
	}
	if decode(r, &in) != nil {
		http.Error(rw, "invalid request", 400)
		return
	}
	w.mu.Lock()
	active := w.active
	matches := active != nil && (active.Status == "running" || active.Status == "starting") && active.ThreadID == in.ThreadID && active.TurnID == in.TurnID
	w.mu.Unlock()
	if !matches {
		http.Error(rw, "active turn changed", 409)
		return
	}
	if err := w.client.TurnInterrupt(r.Context(), appserver.TurnInterruptParams{ThreadID: in.ThreadID, TurnID: in.TurnID}); err != nil {
		http.Error(rw, err.Error(), 502)
		return
	}
	w.setTerminal("interrupted", "interrupted by control plane")
	rw.WriteHeader(204)
}

func (w *Worker) consumeNotifications(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case n, ok := <-w.client.Notifications():
			if !ok {
				return
			}
			w.inputMu.Lock()
			w.mu.Lock()
			active := w.active
			var run activeRun
			if active != nil {
				run = *active
			}
			w.mu.Unlock()
			if active == nil || !notificationBelongsToRun(n, run) {
				w.inputMu.Unlock()
				continue
			}
			_ = w.appendRawEvent(active.RunID, n.Method, n.Params)
			if n.Method == "turn/completed" {
				status, message := turnCompletion(n.Params)
				if status == "" {
					status = "completed"
				}
				if status == "inProgress" {
					status = "completed"
				}
				if status == "failed" && message == "" {
					w.mu.Lock()
					if w.active != nil {
						message = w.active.Error
					}
					w.mu.Unlock()
				}
				w.setTerminal(status, message)
			}
			if n.Method == "error" {
				message, willRetry := notificationError(n.Params)
				if message != "" && !willRetry {
					w.mu.Lock()
					if w.active != nil {
						w.active.Error = message
					}
					w.mu.Unlock()
				}
			}
			w.inputMu.Unlock()
		}
	}
}

// Review subagents and resumed threads can emit notifications on the same
// connection. Their completion or errors must not finish the working run.
func notificationBelongsToRun(n appserver.Notification, active activeRun) bool {
	var origin struct {
		ThreadID string `json:"threadId"`
		TurnID   string `json:"turnId"`
		Turn     struct {
			ID string `json:"id"`
		} `json:"turn"`
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
	}
	if len(n.Params) == 0 {
		return true
	}
	if err := json.Unmarshal(n.Params, &origin); err != nil {
		return false
	}
	if origin.ThreadID == "" {
		origin.ThreadID = origin.Thread.ID
	}
	if origin.TurnID == "" {
		origin.TurnID = origin.Turn.ID
	}
	return (origin.ThreadID == "" || origin.ThreadID == active.ThreadID) &&
		(origin.TurnID == "" || active.TurnID == "" || origin.TurnID == active.TurnID)
}

func notificationError(raw json.RawMessage) (string, bool) {
	var payload struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
		WillRetry bool `json:"willRetry"`
	}
	_ = json.Unmarshal(raw, &payload)
	return payload.Error.Message, payload.WillRetry
}

func turnCompletion(raw json.RawMessage) (string, string) {
	var payload struct {
		Turn struct {
			Status string `json:"status"`
			Error  *struct {
				Message string `json:"message"`
			} `json:"error"`
		} `json:"turn"`
	}
	_ = json.Unmarshal(raw, &payload)
	message := ""
	if payload.Turn.Error != nil {
		message = payload.Turn.Error.Message
	}
	return payload.Turn.Status, message
}
func (w *Worker) eventPath(runID string) string {
	return filepath.Join(w.cfg.ProfileDir, "outbox", runID+".jsonl")
}
func (w *Worker) statusPath(runID string) string {
	return filepath.Join(w.cfg.ProfileDir, "outbox", runID+".status.json")
}
func (w *Worker) appendRawEvent(runID, typ string, payload json.RawMessage) error {
	var v any = map[string]any{}
	if len(payload) > 0 {
		_ = json.Unmarshal(payload, &v)
	}
	return w.appendEvent(runID, typ, v)
}
func (w *Worker) appendEvent(runID, typ string, payload any) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.active == nil || w.active.RunID != runID {
		return nil
	}
	w.active.Sequence++
	e := workerEvent{Sequence: w.active.Sequence, Type: typ}
	b, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	e.Payload = b
	line, err := json.Marshal(e)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(w.eventPath(runID), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err = f.Write(append(line, '\n')); err != nil {
		return err
	}
	return f.Sync()
}
func (w *Worker) setTerminal(status, msg string) {
	w.mu.Lock()
	if w.active == nil {
		w.mu.Unlock()
		return
	}
	w.active.Status = status
	if w.active.cancelRun != nil {
		w.active.cancelRun()
	}
	w.active.Error = msg
	runID := w.active.RunID
	state := map[string]string{"status": status, "error": msg}
	b, _ := json.Marshal(state)
	w.mu.Unlock()
	_ = atomicWrite(w.statusPath(runID), b, 0600)
}
func (w *Worker) events(rw http.ResponseWriter, r *http.Request) {
	runID := r.PathValue("runID")
	if _, err := uuid.Parse(runID); err != nil {
		http.Error(rw, "invalid run id", 400)
		return
	}
	after, _ := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
	// Completion can arrive before a steer RPC responds and persists its input.
	// Keep polling nonblocking, but do not finalize the relay until that operation
	// has finished. Acquire before scanning so a just-finished steer cannot put
	// its final events beyond an already-read terminal batch.
	operationComplete := w.operationMu.TryLock()
	if operationComplete {
		defer w.operationMu.Unlock()
	}
	status := "unknown"
	errMsg := "run not found in worker outbox"
	w.mu.Lock()
	if w.active != nil && w.active.RunID == runID {
		status, errMsg = w.active.Status, w.active.Error
	}
	w.mu.Unlock()
	if b, err := os.ReadFile(w.statusPath(runID)); err == nil {
		var st struct {
			Status string `json:"status"`
			Error  string `json:"error"`
		}
		if json.Unmarshal(b, &st) == nil {
			status = st.Status
			errMsg = st.Error
		}
	}
	if !operationComplete && status != "starting" && status != "running" {
		status, errMsg = "running", ""
	}
	// Snapshot status first: observing a new terminal status after reading the
	// file could drop the completion event appended between those two reads.
	events := []workerEvent{}
	if f, err := os.Open(w.eventPath(runID)); err == nil {
		scanner := bufio.NewScanner(f)
		buf := make([]byte, 64*1024)
		scanner.Buffer(buf, 16<<20)
		for scanner.Scan() {
			var e workerEvent
			if json.Unmarshal(scanner.Bytes(), &e) == nil && e.Sequence > after {
				events = append(events, e)
			}
		}
		f.Close()
	}
	writeJSON(rw, 200, map[string]any{"events": events, "status": status, "error": errMsg})
}

func (w *Worker) deviceLogin(rw http.ResponseWriter, r *http.Request) {
	out, err := w.client.AccountLoginDeviceCode(r.Context())
	if err != nil {
		http.Error(rw, err.Error(), 502)
		return
	}
	writeJSON(rw, 200, map[string]string{"loginId": out.LoginID, "verificationUrl": out.VerificationURL, "userCode": out.UserCode})
}
func (w *Worker) apiKeyLogin(rw http.ResponseWriter, r *http.Request) {
	var in struct {
		APIKey string `json:"apiKey"`
	}
	if decode(r, &in) != nil || in.APIKey == "" {
		http.Error(rw, "invalid API key", 400)
		return
	}
	err := w.client.AccountLoginAPIKey(r.Context(), in.APIKey)
	in.APIKey = ""
	if err != nil {
		http.Error(rw, err.Error(), 502)
		return
	}
	rw.WriteHeader(204)
}
func (w *Worker) logout(rw http.ResponseWriter, r *http.Request) {
	if err := w.client.AccountLogout(r.Context()); err != nil {
		http.Error(rw, err.Error(), 502)
		return
	}
	rw.WriteHeader(204)
}
func (w *Worker) authStatus(rw http.ResponseWriter, r *http.Request) {
	out, err := w.client.AccountRead(r.Context(), false)
	if err != nil {
		http.Error(rw, err.Error(), 502)
		return
	}
	state := "disconnected"
	method := ""
	label := ""
	if out.Account != nil {
		state = "connected"
		method = out.Account.Type
		if out.Account.Email != nil {
			label = *out.Account.Email
		}
	}
	writeJSON(rw, 200, map[string]string{"state": state, "method": method, "accountLabel": label})
}
func (w *Worker) desktopLease(rw http.ResponseWriter, r *http.Request) {
	var in struct {
		Holder     string `json:"holder"`
		Generation int64  `json:"generation"`
	}
	if decode(r, &in) != nil || (in.Holder != "agent" && in.Holder != "human") || in.Generation < 1 {
		http.Error(rw, "invalid lease", 400)
		return
	}
	b, _ := json.Marshal(in)
	if err := atomicWrite(filepath.Join(w.cfg.ProfileDir, "desktop-lease.json"), b, 0600); err != nil {
		http.Error(rw, err.Error(), 500)
		return
	}
	rw.WriteHeader(204)
}
func atomicWrite(path string, b []byte, mode os.FileMode) error {
	tmp := fmt.Sprintf("%s.tmp-%d", path, time.Now().UnixNano())
	if err := os.WriteFile(tmp, b, mode); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
func recoverOutbox(dir string) {
	entries, _ := filepath.Glob(filepath.Join(dir, "*.status.json"))
	for _, path := range entries {
		b, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var st struct {
			Status string `json:"status"`
			Error  string `json:"error"`
		}
		if json.Unmarshal(b, &st) != nil || (st.Status != "running" && st.Status != "starting") {
			continue
		}
		recovered, _ := json.Marshal(map[string]string{"status": "unknown", "error": "agent worker restarted during an active turn"})
		_ = atomicWrite(path, recovered, 0600)
	}
}
