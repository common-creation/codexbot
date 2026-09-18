package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/common-creation/codexbot/internal/domain"
	"github.com/common-creation/codexbot/internal/runtimeclient"
	"github.com/google/uuid"
)

type collaborationHarness struct {
	s           *Server
	t           *testing.T
	mu          sync.Mutex
	turns       []runtimeclient.TurnRequest
	steers      []runtimeclient.SteerRequest
	steerStatus int
	startStatus int
	steerAfter  int64
	eventBatch  *runtimeclient.EventBatch
}

func newCollaborationHarness(t *testing.T) *collaborationHarness {
	t.Helper()
	h := &collaborationHarness{t: t, steerStatus: 200, startStatus: 202}
	runtime := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.mu.Lock()
		defer h.mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer runtime-secret" {
			t.Error("missing runtime auth")
		}
		switch {
		case strings.HasSuffix(r.URL.Path, "/turns"):
			var in runtimeclient.TurnRequest
			if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
				t.Error(err)
			}
			h.turns = append(h.turns, in)
			threadID := in.ThreadID
			if threadID == "" {
				threadID = "thread-" + in.RunID
			}
			writeJSON(w, h.startStatus, map[string]string{"threadId": threadID, "turnId": "turn-" + in.RunID})
		case strings.HasSuffix(r.URL.Path, "/steer"):
			var in runtimeclient.SteerRequest
			if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
				t.Error(err)
			}
			h.steers = append(h.steers, in)
			writeJSON(w, h.steerStatus, map[string]any{"threadId": in.ThreadID, "turnId": in.ExpectedTurnID, "afterSequence": h.steerAfter})
		case strings.HasSuffix(r.URL.Path, "/events"):
			if h.eventBatch != nil {
				writeJSON(w, 200, h.eventBatch)
			} else {
				writeJSON(w, 200, runtimeclient.EventBatch{Status: "running"})
			}
		default:
			w.WriteHeader(204)
		}
	}))
	st := permissionTestStore(t)
	h.s = New(st, runtimeclient.New(runtime.URL, "runtime-secret"), Config{}, nil)
	for _, id := range []string{"sender", "target", "stranger"} {
		if err := st.CreateAgent(context.Background(), domain.Agent{ID: id, Name: id + " name", RolePrompt: "Role for " + id, RoleVersion: 1, Permission: domain.PermissionAuto, Model: "test-model", Effort: "high", Status: "running", CreatedAt: time.Now(), UpdatedAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		h.s.relayMu.Lock()
		for _, cancel := range h.s.relays {
			cancel()
		}
		h.s.relayMu.Unlock()
		runtime.Close()
	})
	return h
}

func (h *collaborationHarness) request(sender, method, path string, body any) *httptest.ResponseRecorder {
	h.t.Helper()
	var b bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&b).Encode(body); err != nil {
			h.t.Fatal(err)
		}
	}
	r := httptest.NewRequest(method, "/internal/agents/"+sender+"/collaboration"+path, &b)
	r.Header.Set("Authorization", "Bearer runtime-secret")
	w := httptest.NewRecorder()
	h.s.Handler().ServeHTTP(w, r)
	return w
}

func (h *collaborationHarness) send(key, mode string) domain.CollaborationTask {
	h.t.Helper()
	w := h.request("sender", "POST", "/tasks", map[string]string{"targetAgentId": "target", "prompt": "Task " + key, "mode": mode, "idempotencyKey": key})
	if w.Code != 202 {
		h.t.Fatalf("send %d %s", w.Code, w.Body.String())
	}
	var task domain.CollaborationTask
	if err := json.Unmarshal(w.Body.Bytes(), &task); err != nil {
		h.t.Fatal(err)
	}
	return task
}

func (h *collaborationHarness) active() domain.Run {
	h.t.Helper()
	ctx := context.Background()
	c := domain.Conversation{ID: uuid.NewString(), AgentID: "target", Kind: "manual", RoleVersion: 1, CodexThreadID: "active-thread", CreatedAt: time.Now()}
	if err := h.s.store.CreateConversation(ctx, c); err != nil {
		h.t.Fatal(err)
	}
	run := domain.Run{ID: uuid.NewString(), AgentID: "target", ConversationID: c.ID, CodexTurnID: "active-turn", Source: "manual", Prompt: "existing user task", Status: "running"}
	if err := h.s.store.CreateRun(ctx, run); err != nil {
		h.t.Fatal(err)
	}
	return run
}

func (h *collaborationHarness) dispatch() {
	h.t.Helper()
	if err := h.s.dispatchCollaboration(context.Background(), "target"); err != nil {
		h.t.Fatal(err)
	}
}

func TestCollaborationDiscoveryAuthAndVisibility(t *testing.T) {
	h := newCollaborationHarness(t)
	for _, header := range []string{"", "Bearer bad"} {
		r := httptest.NewRequest("GET", "/internal/agents/sender/collaboration/agents", nil)
		r.Header.Set("Authorization", header)
		w := httptest.NewRecorder()
		h.s.Handler().ServeHTTP(w, r)
		if w.Code != 401 {
			t.Fatalf("unauthenticated status %d", w.Code)
		}
	}
	w := h.request("sender", "GET", "/agents/target", nil)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	var detail collaborationAgentView
	if err := json.Unmarshal(w.Body.Bytes(), &detail); err != nil {
		t.Fatal(err)
	}
	if detail.RolePrompt != "Role for target" || !strings.Contains(detail.SystemInstructions, detail.RolePrompt) || detail.Busy {
		t.Fatalf("wrong detail %+v", detail)
	}
	task := h.send("discovery", "queue")
	h.active()
	w = h.request("sender", "GET", "/agents/target", nil)
	_ = json.Unmarshal(w.Body.Bytes(), &detail)
	if !detail.Busy || detail.ActiveRun == nil || detail.QueuedTaskCount != 1 {
		t.Fatalf("wrong status %+v", detail)
	}
	if strings.Contains(w.Body.String(), "existing user task") {
		t.Fatal("agent discovery exposes another run's prompt")
	}
	if w := h.request("stranger", "GET", "/tasks/"+task.ID, nil); w.Code != 404 {
		t.Fatalf("unrelated agent sees task: %d", w.Code)
	}
	if w := h.request("target", "GET", "/tasks/"+task.ID, nil); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if w := h.request("target", "POST", "/tasks/"+task.ID+"/cancel", nil); w.Code != 403 {
		t.Fatal("recipient cancelled sender's task")
	}
	if w := h.request("sender", "POST", "/tasks", map[string]string{"targetAgentId": "sender", "prompt": "loop", "idempotencyKey": "self"}); w.Code != 400 {
		t.Fatal("self delegation allowed")
	}
	if w := h.request("sender", "POST", "/tasks", map[string]string{"targetAgentId": "target", "prompt": "spoof", "idempotencyKey": "spoof", "senderAgentId": "stranger"}); w.Code != 400 {
		t.Fatal("identity field accepted")
	}
}

func TestCollaborationQueueFIFOIdempotencyAndSettings(t *testing.T) {
	h := newCollaborationHarness(t)
	ctx := context.Background()
	run := h.active()
	first := h.send("first", "queue")
	second := h.send("second", "queue")
	w := h.request("sender", "POST", "/tasks", map[string]string{"targetAgentId": "target", "prompt": "Task first", "mode": "queue", "idempotencyKey": "first"})
	if w.Code != 200 || !strings.Contains(w.Body.String(), first.ID) {
		t.Fatalf("retry %d %s", w.Code, w.Body.String())
	}
	h.dispatch()
	if len(h.turns) != 0 {
		t.Fatal("started task while busy")
	}
	if err := h.s.store.FinishRun(ctx, run.ID, "completed", ""); err != nil {
		t.Fatal(err)
	}
	h.dispatch()
	h.mu.Lock()
	turns := append([]runtimeclient.TurnRequest(nil), h.turns...)
	h.mu.Unlock()
	if len(turns) != 1 || !strings.Contains(turns[0].Prompt, first.ID) || turns[0].Model != "test-model" || turns[0].Effort != "high" || turns[0].Permission != domain.PermissionAuto || !strings.Contains(turns[0].DeveloperInstructions, "Role for target") {
		t.Fatalf("first turn %+v", turns)
	}
	if c, err := h.s.store.Conversation(ctx, turns[0].ConversationID); err != nil || c.ID != run.ConversationID || turns[0].ThreadID != "active-thread" {
		t.Fatalf("conversation %+v %v", c, err)
	}
	h.dispatch()
	if err := h.s.store.FinishRun(ctx, turns[0].RunID, "completed", ""); err != nil {
		t.Fatal(err)
	}
	h.s.cancelRelay(turns[0].RunID)
	if err := h.s.store.ReconcileCollaborationTasks(ctx); err != nil {
		t.Fatal(err)
	}
	stored, _ := h.s.store.CollaborationTask(ctx, first.ID)
	if stored.Status != "completed" {
		t.Fatalf("task %+v", stored)
	}
	h.dispatch()
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.turns) != 2 || !strings.Contains(h.turns[1].Prompt, second.ID) {
		t.Fatalf("FIFO %+v", h.turns)
	}
}

func TestCollaborationSteerPassesQueueAndReturnsBoundedOutput(t *testing.T) {
	h := newCollaborationHarness(t)
	ctx := context.Background()
	run := h.active()
	for i := 0; i < 4; i++ {
		if _, err := h.s.store.AppendEvent(ctx, run.ID, "item/agentMessage/delta", map[string]string{"delta": fmt.Sprint(i)}); err != nil {
			t.Fatal(err)
		}
	}
	h.steerAfter = 3
	queued := h.send("independent", "queue")
	steered := h.send("urgent", "steer")
	h.dispatch()
	h.mu.Lock()
	steers := append([]runtimeclient.SteerRequest(nil), h.steers...)
	h.mu.Unlock()
	if len(steers) != 1 || steers[0].ExpectedTurnID != run.CodexTurnID || steers[0].ThreadID != "active-thread" || steers[0].MessageID != steered.ID {
		t.Fatalf("steers %+v", steers)
	}
	q, _ := h.s.store.CollaborationTask(ctx, queued.ID)
	if q.Status != "queued" {
		t.Fatalf("queue %+v", q)
	}
	w := h.request("sender", "GET", "/tasks/"+steered.ID+"?limit=1", nil)
	var result struct {
		Task   domain.CollaborationTask `json:"task"`
		Events []domain.Event           `json:"events"`
		Next   int64                    `json:"nextSequence"`
		Scope  string                   `json:"outputScope"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Task.Status != "running" || len(result.Events) != 1 || result.Events[0].Sequence != 4 || result.Next != 4 || result.Scope != "shared_run" {
		t.Fatalf("result %s", w.Body.String())
	}
	if err := h.s.store.FinishRun(ctx, run.ID, "completed", ""); err != nil {
		t.Fatal(err)
	}
	w = h.request("sender", "GET", "/tasks/"+steered.ID+"?afterSequence=4", nil)
	if !strings.Contains(w.Body.String(), `"status":"completed"`) {
		t.Fatal(w.Body.String())
	}
}

func TestCollaborationSteerRejectionFallsBackWithoutLosingOutput(t *testing.T) {
	h := newCollaborationHarness(t)
	ctx := context.Background()
	run := h.active()
	h.steerStatus = 409
	_, _ = h.s.store.AppendEvent(ctx, run.ID, "old", nil)
	task := h.send("race", "steer")
	h.dispatch()
	stored, _ := h.s.store.CollaborationTask(ctx, task.ID)
	if stored.Status != "queued" || stored.RunID != "" || stored.ResultAfterSequence != 0 {
		t.Fatalf("fallback %+v", stored)
	}
	_ = h.s.store.FinishRun(ctx, run.ID, "completed", "")
	h.dispatch()
	stored, _ = h.s.store.CollaborationTask(ctx, task.ID)
	if stored.Status != "running" || stored.RunID == run.ID {
		t.Fatalf("fallback run %+v", stored)
	}
	_, _ = h.s.store.AppendEvent(ctx, stored.RunID, "answer", map[string]string{"text": "fresh answer"})
	w := h.request("sender", "GET", "/tasks/"+task.ID, nil)
	if !strings.Contains(w.Body.String(), "fresh answer") || !strings.Contains(w.Body.String(), `"outputScope":"task"`) {
		t.Fatal(w.Body.String())
	}
}

func TestCollaborationUnknownSteerIsNotRetriedOrExposed(t *testing.T) {
	h := newCollaborationHarness(t)
	ctx := context.Background()
	run := h.active()
	h.steerStatus = 503
	task := h.send("uncertain", "steer")
	h.dispatch()
	h.dispatch()
	_, _ = h.s.store.AppendEvent(ctx, run.ID, "old-private", map[string]string{"text": "unrelated output"})
	_ = h.s.store.FinishRun(ctx, run.ID, "completed", "")
	w := h.request("sender", "GET", "/tasks/"+task.ID, nil)
	if !strings.Contains(w.Body.String(), `"status":"unknown"`) || strings.Contains(w.Body.String(), "unrelated output") {
		t.Fatal(w.Body.String())
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.steers) != 1 {
		t.Fatalf("ambiguous delivery retried %d", len(h.steers))
	}
}

func TestCollaborationRejectedSteerDoesNotTargetLaterTurn(t *testing.T) {
	h := newCollaborationHarness(t)
	ctx := context.Background()
	first := h.active()
	h.steerStatus = 409
	task := h.send("rejected", "steer")
	h.dispatch()
	_ = h.s.store.FinishRun(ctx, first.ID, "completed", "")
	later := h.active()
	h.dispatch()
	stored, _ := h.s.store.CollaborationTask(ctx, task.ID)
	if stored.Status != "queued" || !stored.SteerFallback {
		t.Fatalf("task %+v", stored)
	}
	h.mu.Lock()
	count := len(h.steers)
	h.mu.Unlock()
	if count != 1 {
		t.Fatal("rejected message was steered into later turn")
	}
	_ = h.s.store.FinishRun(ctx, later.ID, "completed", "")
	h.dispatch()
	stored, _ = h.s.store.CollaborationTask(ctx, task.ID)
	if stored.Status != "running" {
		t.Fatalf("fallback did not dispatch %+v", stored)
	}
}

func TestCollaborationDefiniteStartRejectionReleasesRun(t *testing.T) {
	h := newCollaborationHarness(t)
	h.startStatus = 400
	task := h.send("invalid-runtime", "queue")
	h.dispatch()
	stored, _ := h.s.store.CollaborationTask(context.Background(), task.ID)
	busy, _ := h.s.store.AgentBusy(context.Background(), "target")
	if stored.Status != "failed" || busy {
		t.Fatalf("rejection %+v busy=%v", stored, busy)
	}
}

func TestCollaborationHumanLeaseAndStop(t *testing.T) {
	h := newCollaborationHarness(t)
	ctx := context.Background()
	task := h.send("wait-human", "queue")
	if _, err := h.s.store.AcquireDesktopLease(ctx, "target", "human", time.Minute); err != nil {
		t.Fatal(err)
	}
	h.dispatch()
	stored, _ := h.s.store.CollaborationTask(ctx, task.ID)
	if stored.Status != "queued" {
		t.Fatalf("human lease ignored %+v", stored)
	}
	if err := h.s.stopAgentRuntime(ctx, "target"); err != nil {
		t.Fatal(err)
	}
	stored, _ = h.s.store.CollaborationTask(ctx, task.ID)
	if stored.Status != "cancelled" {
		t.Fatalf("stop left queued task %+v", stored)
	}
}

func TestCollaborationCancellationAndResultPagination(t *testing.T) {
	h := newCollaborationHarness(t)
	ctx := context.Background()
	task := h.send("cancel", "queue")
	w := h.request("sender", "POST", "/tasks/"+task.ID+"/cancel", nil)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	h.dispatch()
	task = h.send("output", "queue")
	h.dispatch()
	stored, _ := h.s.store.CollaborationTask(ctx, task.ID)
	for i := 0; i < 3; i++ {
		_, _ = h.s.store.AppendEvent(ctx, stored.RunID, "answer", map[string]int{"part": i})
	}
	w = h.request("sender", "GET", "/tasks/"+task.ID+"?limit=2", nil)
	var result struct {
		Events []domain.Event `json:"events"`
		Next   int64          `json:"nextSequence"`
		More   bool           `json:"hasMore"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &result)
	if len(result.Events) != 2 || result.Next != 2 || !result.More {
		t.Fatal(w.Body.String())
	}
	w = h.request("sender", "GET", "/tasks/"+task.ID+"?limit=2&afterSequence=2", nil)
	_ = json.Unmarshal(w.Body.Bytes(), &result)
	if len(result.Events) != 1 || result.Next != 3 || result.More {
		t.Fatal(w.Body.String())
	}
	if w := h.request("sender", "POST", "/tasks/"+task.ID+"/cancel", nil); w.Code != 409 {
		t.Fatal("running task cancelled")
	}
}

func TestCollaborationConfirmedSteerFailureStillReturnsOutput(t *testing.T) {
	h := newCollaborationHarness(t)
	ctx := context.Background()
	run := h.active()
	task := h.send("confirmed-failure", "steer")
	h.dispatch()
	_, _ = h.s.store.AppendEvent(ctx, run.ID, "error", map[string]string{"message": "useful failure details"})
	_ = h.s.store.FinishRun(ctx, run.ID, "failed", "host failed")
	w := h.request("sender", "GET", "/tasks/"+task.ID, nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "useful failure details") || !strings.Contains(w.Body.String(), `"deliveryConfirmed":true`) || !strings.Contains(w.Body.String(), `"status":"failed"`) {
		t.Fatal(w.Body.String())
	}
}

func TestCollaborationByteBoundedPagesAndOversizedEvent(t *testing.T) {
	h := newCollaborationHarness(t)
	ctx := context.Background()
	task := h.send("large-output", "queue")
	h.dispatch()
	stored, _ := h.s.store.CollaborationTask(ctx, task.ID)
	_, _ = h.s.store.AppendEvent(ctx, stored.RunID, "large-tool-output", map[string]string{"text": strings.Repeat("x", 5<<20)})
	for i := 0; i < 12; i++ {
		_, _ = h.s.store.AppendEvent(ctx, stored.RunID, "normal-output", map[string]string{"text": strings.Repeat("a", 240<<10)})
	}
	w := h.request("sender", "GET", "/tasks/"+task.ID+"?limit=100", nil)
	var page struct {
		Events []domain.Event `json:"events"`
		Next   int64          `json:"nextSequence"`
		More   bool           `json:"hasMore"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if w.Code != 200 || w.Body.Len() > 3<<20 || !page.More || page.Next < 1 || !strings.Contains(string(page.Events[0].Payload), `"truncated":true`) {
		t.Fatalf("status=%d bytes=%d next=%d more=%v", w.Code, w.Body.Len(), page.Next, page.More)
	}
	w = h.request("sender", "GET", fmt.Sprintf("/tasks/%s?limit=100&afterSequence=%d", task.ID, page.Next), nil)
	if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if page.More || page.Next != 13 {
		t.Fatalf("did not consume remaining output %+v", page)
	}
	for i := 0; i < 40; i++ {
		_, _, err := h.s.store.EnqueueCollaborationTask(ctx, domain.CollaborationTask{SenderAgentID: "sender", TargetAgentID: "target", Prompt: strings.Repeat("b", 64<<10), IdempotencyKey: fmt.Sprint("big-task-", i)})
		if err != nil {
			t.Fatal(err)
		}
	}
	w = h.request("sender", "GET", "/tasks?limit=100", nil)
	var tasks struct {
		Tasks []domain.CollaborationTask `json:"tasks"`
		Next  string                     `json:"nextCursor"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &tasks); err != nil {
		t.Fatal(err)
	}
	if w.Body.Len() > 3<<20 || tasks.Next == "" || len(tasks.Tasks) >= 40 {
		t.Fatalf("task page bytes=%d count=%d next=%s", w.Body.Len(), len(tasks.Tasks), tasks.Next)
	}
}

func TestCollaborationAgentDiscoveryPagination(t *testing.T) {
	h := newCollaborationHarness(t)
	w := h.request("sender", "GET", "/agents?limit=2", nil)
	var page struct {
		Agents []collaborationAgentView `json:"agents"`
		Next   string                   `json:"nextCursor"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Agents) != 2 || page.Next == "" {
		t.Fatal(w.Body.String())
	}
	w = h.request("sender", "GET", "/agents?limit=2&after="+page.Next, nil)
	if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Agents) != 1 || page.Next != "" {
		t.Fatal(w.Body.String())
	}
}

func TestCollaborationBackgroundDispatchesAcceptedTask(t *testing.T) {
	h := newCollaborationHarness(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h.s.StartBackground(ctx)
	task := h.send("background", "queue")
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		stored, err := h.s.store.CollaborationTask(ctx, task.ID)
		if err != nil {
			t.Fatal(err)
		}
		if stored.Status == "running" {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("background dispatcher did not start accepted task")
}

func TestCollaborationAcceptsWhileTargetOperationIsBlocked(t *testing.T) {
	h := newCollaborationHarness(t)
	unlock := h.s.lockAgent("target")
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		done <- h.request("sender", "POST", "/tasks", map[string]string{"targetAgentId": "target", "prompt": "enqueue during startup", "idempotencyKey": "during-startup"})
	}()
	select {
	case response := <-done:
		unlock()
		if response.Code != 202 {
			t.Fatal(response.Body.String())
		}
	case <-time.After(time.Second):
		unlock()
		<-done
		t.Fatal("queue acceptance waited for target operation")
	}
}
