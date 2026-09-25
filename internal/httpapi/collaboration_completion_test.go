package httpapi

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/common-creation/codexbot/internal/domain"
	"github.com/common-creation/codexbot/internal/store"
	"github.com/google/uuid"
)

func (h *collaborationHarness) activeSender() domain.Run {
	h.t.Helper()
	ctx := context.Background()
	a, err := h.s.store.Agent(ctx, "sender")
	if err != nil {
		h.t.Fatal(err)
	}
	c, err := h.s.store.EnsureAgentConversation(ctx, a)
	if err != nil {
		h.t.Fatal(err)
	}
	if err := h.s.store.SetConversationThread(ctx, c.ID, "sender-thread"); err != nil {
		h.t.Fatal(err)
	}
	run := domain.Run{ID: uuid.NewString(), AgentID: a.ID, ConversationID: c.ID, CodexTurnID: "sender-turn", Source: "manual", Prompt: "delegate and continue", Status: "running"}
	if err := h.s.store.CreateRun(ctx, run); err != nil {
		h.t.Fatal(err)
	}
	return run
}

func (h *collaborationHarness) sendNotify(mode string) domain.CollaborationTask {
	h.t.Helper()
	w := h.request("sender", "POST", "/tasks", map[string]string{"targetAgentId": "target", "prompt": "delegated work", "mode": mode, "completionMode": "notify", "idempotencyKey": "notify-task"})
	if w.Code != 202 {
		h.t.Fatalf("send notify %d %s", w.Code, w.Body.String())
	}
	var task domain.CollaborationTask
	if err := json.Unmarshal(w.Body.Bytes(), &task); err != nil {
		h.t.Fatal(err)
	}
	return task
}

func (h *collaborationHarness) completion(taskID string) domain.CollaborationTask {
	h.t.Helper()
	ctx := context.Background()
	if err := h.s.store.ReconcileCollaborationTasks(ctx); err != nil {
		h.t.Fatal(err)
	}
	if err := h.s.enqueueCollaborationCompletions(ctx, "sender"); err != nil {
		h.t.Fatal(err)
	}
	original, err := h.s.store.CollaborationTask(ctx, taskID)
	if err != nil {
		h.t.Fatal(err)
	}
	reply, err := h.s.store.CollaborationTask(ctx, original.CompletionTaskID)
	if err != nil {
		h.t.Fatalf("completion of %+v: %v", original, err)
	}
	return reply
}

func (h *collaborationHarness) finishTask(taskID, status string) {
	h.t.Helper()
	ctx := context.Background()
	task, err := h.s.store.CollaborationTask(ctx, taskID)
	if err != nil {
		h.t.Fatal(err)
	}
	if err := h.s.store.FinishRun(ctx, task.RunID, status, ""); err != nil {
		h.t.Fatal(err)
	}
}

func (h *collaborationHarness) dispatchSender() {
	h.t.Helper()
	if err := h.s.dispatchCollaboration(context.Background(), "sender"); err != nil {
		h.t.Fatal(err)
	}
}

func TestCollaborationCompletionSteersActiveSenderOnce(t *testing.T) {
	h := newCollaborationHarness(t)
	ctx := context.Background()
	sender := h.activeSender()
	task := h.sendNotify("queue")
	h.dispatch()
	if err := h.s.enqueueCollaborationCompletions(ctx, "sender"); err != nil {
		t.Fatal(err)
	}
	pending, _ := h.s.store.CollaborationTask(ctx, task.ID)
	if pending.CompletionTaskID != "" {
		t.Fatal("notified before task completion")
	}
	h.finishTask(task.ID, "completed")
	reply := h.completion(task.ID)
	if reply.NotificationForTaskID != task.ID || reply.TargetAgentID != "sender" || reply.CompletionMode != "poll" {
		t.Fatalf("reply %+v", reply)
	}
	for i := 0; i < 3; i++ {
		if next := h.completion(task.ID); next.ID != reply.ID {
			t.Fatal("completion was duplicated")
		}
		h.dispatchSender()
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.steers) != 1 || len(h.turns) != 1 {
		t.Fatalf("steers=%d turns=%d", len(h.steers), len(h.turns))
	}
	got := h.steers[0]
	if got.RunID != sender.ID || got.ExpectedTurnID != sender.CodexTurnID || got.ThreadID != "sender-thread" || got.MessageID != reply.ID || !strings.Contains(got.Prompt, `"taskId":"`+task.ID+`"`) || !strings.Contains(got.Prompt, `"status":"completed"`) || !strings.Contains(got.Prompt, "tasks_get") {
		t.Fatalf("notification steer %+v", got)
	}
}

func TestCollaborationCompletionResumesIdleConversation(t *testing.T) {
	h := newCollaborationHarness(t)
	ctx := context.Background()
	sender := h.activeSender()
	task := h.sendNotify("queue")
	h.dispatch()
	if err := h.s.store.FinishRun(ctx, sender.ID, "completed", ""); err != nil {
		t.Fatal(err)
	}
	h.finishTask(task.ID, "completed")
	reply := h.completion(task.ID)
	h.dispatchSender()
	h.mu.Lock()
	if len(h.turns) != 2 || len(h.steers) != 0 {
		t.Fatalf("steers=%d turns=%d", len(h.steers), len(h.turns))
	}
	continuation := h.turns[1]
	h.mu.Unlock()
	if continuation.ConversationID != sender.ConversationID || continuation.ThreadID != "sender-thread" || !strings.Contains(continuation.Prompt, task.ID) {
		t.Fatalf("wrong continuation %+v", continuation)
	}
	h.finishTask(reply.ID, "completed")
	if err := h.s.store.ReconcileCollaborationTasks(ctx); err != nil {
		t.Fatal(err)
	}
	if err := h.s.enqueueCollaborationCompletions(ctx, "sender"); err != nil {
		t.Fatal(err)
	}
	reply, _ = h.s.store.CollaborationTask(ctx, reply.ID)
	if reply.CompletionTaskID != "" {
		t.Fatal("notification created a recursive notification")
	}
	// Exact retries still return the receipt after the sender's turn has ended.
	w := h.request("sender", "POST", "/tasks", map[string]string{"targetAgentId": "target", "prompt": "delegated work", "mode": "queue", "completionMode": "notify", "idempotencyKey": "notify-task"})
	if w.Code != 200 || !strings.Contains(w.Body.String(), task.ID) {
		t.Fatalf("idle retry %d %s", w.Code, w.Body.String())
	}
}

func TestCollaborationCompletionDeliveryFailures(t *testing.T) {
	for _, status := range []int{409, 503} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			h := newCollaborationHarness(t)
			ctx := context.Background()
			sender := h.activeSender()
			task := h.sendNotify("queue")
			h.dispatch()
			h.finishTask(task.ID, "completed")
			reply := h.completion(task.ID)
			h.mu.Lock()
			h.steerStatus = status
			h.mu.Unlock()
			h.dispatchSender()
			h.dispatchSender()
			delivery, _ := h.s.store.CollaborationTask(ctx, reply.ID)
			if status == 503 && delivery.Status != "unknown" {
				t.Fatalf("ambiguous steer %+v", delivery)
			}
			if status == 409 && (delivery.Status != "queued" || !delivery.SteerFallback) {
				t.Fatalf("rejected steer %+v", delivery)
			}
			if err := h.s.store.FinishRun(ctx, sender.ID, "completed", ""); err != nil {
				t.Fatal(err)
			}
			h.dispatchSender()
			h.mu.Lock()
			defer h.mu.Unlock()
			if len(h.steers) != 1 || (status == 409 && len(h.turns) != 2) || (status == 503 && len(h.turns) != 1) {
				t.Fatalf("steers=%d turns=%d", len(h.steers), len(h.turns))
			}
		})
	}
}

func TestCollaborationCompletionTerminalOutcomes(t *testing.T) {
	for _, status := range []string{"completed", "failed", "interrupted", "unknown", "cancelled"} {
		t.Run(status, func(t *testing.T) {
			h := newCollaborationHarness(t)
			ctx := context.Background()
			h.activeSender()
			task := h.sendNotify("queue")
			if status == "cancelled" {
				if w := h.request("sender", "POST", "/tasks/"+task.ID+"/cancel", nil); w.Code != 200 {
					t.Fatal(w.Body.String())
				}
			} else {
				h.dispatch()
				h.finishTask(task.ID, status)
			}
			reply := h.completion(task.ID)
			h.dispatchSender()
			if !strings.Contains(reply.Prompt, `"status":"`+status+`"`) {
				t.Fatal(reply.Prompt)
			}
			w := h.request("stranger", "GET", "/tasks/"+reply.ID, nil)
			if w.Code != 404 {
				t.Fatalf("completion exposed to stranger: %d", w.Code)
			}
			stored, _ := h.s.store.CollaborationTask(ctx, reply.ID)
			if !stored.DeliveryConfirmed {
				t.Fatal("completion was not delivered")
			}
		})
	}
}

func TestCollaborationCompletionResetAndStop(t *testing.T) {
	for _, action := range []string{"reset", "stop-before-completion", "stop-after-completion", "interrupt"} {
		t.Run(action, func(t *testing.T) {
			h := newCollaborationHarness(t)
			ctx := context.Background()
			sender := h.activeSender()
			task := h.sendNotify("queue")
			h.dispatch()
			if action == "stop-after-completion" {
				h.finishTask(task.ID, "completed")
				h.completion(task.ID)
			}
			switch action {
			case "reset":
				if err := h.s.store.FinishRun(ctx, sender.ID, "completed", ""); err != nil {
					t.Fatal(err)
				}
				if _, err := h.s.store.ResetAgentConversation(ctx, "sender"); err != nil {
					t.Fatal(err)
				}
			case "interrupt":
				r := httptest.NewRequest("POST", "/api/agents/sender/interrupt", nil)
				r.SetPathValue("agentID", "sender")
				w := httptest.NewRecorder()
				h.s.interrupt(w, r)
				if w.Code != 204 {
					t.Fatalf("interrupt: %d %s", w.Code, w.Body.String())
				}
			default:
				if err := h.s.stopAgentRuntime(ctx, "sender"); err != nil {
					t.Fatal(err)
				}
			}
			h.finishTask(task.ID, "completed")
			reply := h.completion(task.ID)
			h.dispatchSender()
			reply, _ = h.s.store.CollaborationTask(ctx, reply.ID)
			if reply.Status != "cancelled" || reply.Error == "" {
				t.Fatalf("obsolete completion %+v", reply)
			}
			h.mu.Lock()
			defer h.mu.Unlock()
			if len(h.turns) != 1 || len(h.steers) != 0 {
				t.Fatal("obsolete completion restarted sender")
			}
		})
	}
}

func TestCollaborationCompletionHumanAndSchedulePostpone(t *testing.T) {
	for _, busy := range []string{"human", "schedule"} {
		t.Run(busy, func(t *testing.T) {
			h := newCollaborationHarness(t)
			ctx := context.Background()
			sender := h.activeSender()
			task := h.sendNotify("queue")
			h.dispatch()
			h.finishTask(task.ID, "completed")
			h.completion(task.ID)
			if err := h.s.store.FinishRun(ctx, sender.ID, "completed", ""); err != nil {
				t.Fatal(err)
			}
			if busy == "human" {
				if _, err := h.s.store.AcquireDesktopLease(ctx, "sender", "human", time.Minute); err != nil {
					t.Fatal(err)
				}
			} else {
				c := domain.Conversation{ID: uuid.NewString(), AgentID: "sender", Kind: "scheduled", RoleVersion: 1, CreatedAt: time.Now()}
				if err := h.s.store.CreateConversation(ctx, c); err != nil {
					t.Fatal(err)
				}
				if err := h.s.store.CreateRun(ctx, domain.Run{ID: "sender-schedule", AgentID: "sender", ConversationID: c.ID, Source: "schedule:test", Status: "running", CodexTurnID: "scheduled-turn"}); err != nil {
					t.Fatal(err)
				}
			}
			h.dispatchSender()
			h.mu.Lock()
			if len(h.turns) != 1 || len(h.steers) != 0 {
				t.Fatal("completion entered occupied context")
			}
			h.mu.Unlock()
			if busy == "human" {
				if _, err := h.s.store.AcquireDesktopLease(ctx, "sender", "agent", time.Minute); err != nil {
					t.Fatal(err)
				}
			} else if err := h.s.store.FinishRun(ctx, "sender-schedule", "completed", ""); err != nil {
				t.Fatal(err)
			}
			h.dispatchSender()
			h.mu.Lock()
			defer h.mu.Unlock()
			if len(h.turns) != 2 || h.turns[1].ConversationID != sender.ConversationID {
				t.Fatal("completion did not resume original context")
			}
		})
	}
}

func TestCollaborationCompletionValidationAndPollCompatibility(t *testing.T) {
	h := newCollaborationHarness(t)
	ctx := context.Background()
	for _, body := range []map[string]string{
		{"completionMode": "invalid"},
		{"completionMode": "notify"}, // No active sender run.
		{"notificationForTaskId": "forged"},
		{"completionTaskId": "forged"},
	} {
		body["targetAgentId"], body["prompt"], body["idempotencyKey"] = "target", "task", "invalid"
		if w := h.request("sender", "POST", "/tasks", body); w.Code != 400 {
			t.Fatalf("validation %v: %d %s", body, w.Code, w.Body.String())
		}
	}
	task := h.send("legacy-poll", "queue")
	h.dispatch()
	h.finishTask(task.ID, "completed")
	if err := h.s.store.ReconcileCollaborationTasks(ctx); err != nil {
		t.Fatal(err)
	}
	if err := h.s.enqueueCollaborationCompletions(ctx, "sender"); err != nil {
		t.Fatal(err)
	}
	task, _ = h.s.store.CollaborationTask(ctx, task.ID)
	if task.CompletionMode != "poll" || task.CompletionTaskID != "" {
		t.Fatalf("legacy behavior changed %+v", task)
	}
	h.activeSender()
	task = h.sendNotify("steer")
	w := h.request("sender", "POST", "/tasks", map[string]string{"targetAgentId": "target", "prompt": "delegated work", "mode": "steer", "completionMode": "poll", "idempotencyKey": "notify-task"})
	if w.Code != 409 {
		t.Fatalf("changed completion mode accepted: %d %s", w.Code, w.Body.String())
	}
	if task.CompletionMode != "notify" {
		t.Fatal(task)
	}
	list, err := h.s.store.ListCollaborationTasks(ctx, store.CollaborationTaskFilter{AgentID: "sender"})
	if err != nil || len(list) != 2 {
		t.Fatalf("unexpected tasks: %+v %v", list, err)
	}
}

func TestCollaborationCompletionDoesNotExposeRequesterOutput(t *testing.T) {
	h := newCollaborationHarness(t)
	ctx := context.Background()
	sender := h.activeSender()
	task := h.sendNotify("queue")
	h.dispatch()
	h.finishTask(task.ID, "completed")
	reply := h.completion(task.ID)
	if w := h.request("target", "POST", "/tasks/"+reply.ID+"/cancel", nil); w.Code != 409 {
		t.Fatalf("worker can cancel harness callback: %d %s", w.Code, w.Body.String())
	}
	h.dispatchSender()
	if _, err := h.s.store.AppendEvent(ctx, sender.ID, "answer", map[string]string{"text": "private-requester-output"}); err != nil {
		t.Fatal(err)
	}
	if err := h.s.store.FinishRun(ctx, sender.ID, "failed", "private-requester-error"); err != nil {
		t.Fatal(err)
	}
	w := h.request("target", "GET", "/tasks/"+reply.ID, nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"outputScope":"notification"`) || strings.Contains(w.Body.String(), "private-requester") || strings.Contains(w.Body.String(), `"run":`) {
		t.Fatalf("requester output exposed: %d %s", w.Code, w.Body.String())
	}
	w = h.request("target", "GET", "/tasks?direction=outgoing", nil)
	if w.Code != 200 || strings.Contains(w.Body.String(), "private-requester") {
		t.Fatalf("requester error exposed via task list: %d %s", w.Code, w.Body.String())
	}
}

func TestCollaborationCompletionLoopProgressesWithoutRequesterPolling(t *testing.T) {
	h := newCollaborationHarness(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h.activeSender()
	task := h.sendNotify("queue")
	h.dispatch()
	h.finishTask(task.ID, "completed")
	// A busy operation for the requester must not hold the global dispatcher.
	unlock := h.s.lockAgent("sender")
	defer func() {
		if unlock != nil {
			unlock()
		}
	}()
	w := h.request("stranger", "POST", "/tasks", map[string]string{"targetAgentId": "target", "prompt": "independent work", "idempotencyKey": "independent"})
	if w.Code != 202 {
		t.Fatal(w.Body.String())
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		h.s.collaborationLoop(ctx)
	}()
	defer func() { cancel(); <-done }()
	until := time.Now().Add(3 * time.Second)
	for {
		h.mu.Lock()
		progressed := len(h.turns) == 2
		h.mu.Unlock()
		if progressed {
			break
		}
		if time.Now().After(until) {
			t.Fatal("a locked callback recipient blocked another target")
		}
		time.Sleep(10 * time.Millisecond)
	}
	unlock()
	unlock = nil
	until = time.Now().Add(3 * time.Second)
	for {
		h.mu.Lock()
		notified := len(h.steers) == 1 && strings.Contains(h.steers[0].Prompt, task.ID)
		h.mu.Unlock()
		if notified {
			break
		}
		if time.Now().After(until) {
			t.Fatal("completion was not pushed without requester polling")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestCollaborationSteeredTaskCompletionKeepsResultBoundary(t *testing.T) {
	for _, confirmed := range []bool{true, false} {
		t.Run(strconv.FormatBool(confirmed), func(t *testing.T) {
			h := newCollaborationHarness(t)
			ctx := context.Background()
			h.activeSender()
			host := h.active()
			if !confirmed {
				h.steerStatus = 503
			}
			task := h.sendNotify("steer")
			h.dispatch()
			if _, err := h.s.store.AppendEvent(ctx, host.ID, "answer", map[string]string{"text": "shared-host-result"}); err != nil {
				t.Fatal(err)
			}
			if err := h.s.store.FinishRun(ctx, host.ID, "completed", ""); err != nil {
				t.Fatal(err)
			}
			reply := h.completion(task.ID)
			h.mu.Lock()
			h.steerStatus = 200
			h.mu.Unlock()
			h.dispatchSender()
			status := "completed"
			if !confirmed {
				status = "unknown"
			}
			if !strings.Contains(reply.Prompt, `"status":"`+status+`"`) {
				t.Fatal(reply.Prompt)
			}
			w := h.request("sender", "GET", "/tasks/"+task.ID, nil)
			if w.Code != 200 || strings.Contains(w.Body.String(), "shared-host-result") != confirmed {
				t.Fatalf("wrong result visibility: %d %s", w.Code, w.Body.String())
			}
		})
	}
}
