package httpapi

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/common-creation/codexbot/internal/domain"
	"github.com/common-creation/codexbot/internal/runtimeclient"
)

type timelineResponse struct {
	Events         []domain.Event `json:"events"`
	ConversationID string         `json:"conversationId"`
	ActiveRunID    string         `json:"activeRunId"`
	RuntimeBusy    bool           `json:"runtimeBusy"`
	LastSequence   int64          `json:"lastSequence"`
	BeforeSequence int64          `json:"beforeSequence"`
	HasMore        bool           `json:"hasMore"`
}

func (h *collaborationHarness) timeline(agentID, query string) timelineResponse {
	h.t.Helper()
	r := httptest.NewRequest("GET", "/api/agents/"+agentID+"/timeline"+query, nil)
	r.SetPathValue("agentID", agentID)
	w := httptest.NewRecorder()
	h.s.agentTimeline(w, r)
	if w.Code != 200 {
		h.t.Fatalf("timeline %d %s", w.Code, w.Body.String())
	}
	var result timelineResponse
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		h.t.Fatal(err)
	}
	return result
}

func (h *collaborationHarness) userMessage(body any) *httptest.ResponseRecorder {
	h.t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		h.t.Fatal(err)
	}
	r := httptest.NewRequest("POST", "/api/agents/target/messages", bytes.NewReader(raw))
	r.SetPathValue("agentID", "target")
	w := httptest.NewRecorder()
	h.s.message(w, r)
	return w
}

func TestDelegationAndManualInputUseOneConversationAndTimeline(t *testing.T) {
	h := newCollaborationHarness(t)
	ctx := context.Background()
	prior := h.active()
	if err := h.s.store.FinishRun(ctx, prior.ID, "completed", ""); err != nil {
		t.Fatal(err)
	}
	task := h.send("inspect-project", "queue")
	queued := h.timeline("target", "")
	queuedJSON, _ := json.Marshal(queued)
	if !strings.Contains(string(queuedJSON), "collaboration.received") || !strings.Contains(string(queuedJSON), task.Prompt) {
		t.Fatalf("queued input not visible %s", queuedJSON)
	}
	h.dispatch()
	h.mu.Lock()
	delegated := h.turns[0]
	h.mu.Unlock()
	if delegated.ConversationID != prior.ConversationID || delegated.ThreadID != "active-thread" {
		t.Fatalf("delegation discarded context %+v", delegated)
	}
	active := h.timeline("target", "")
	if active.ActiveRunID != delegated.RunID || active.ConversationID != prior.ConversationID {
		t.Fatalf("wrong target timeline %+v", active)
	}
	w := h.userMessage(map[string]any{"prompt": "Also inspect /home/agent/project/src", "conversationId": prior.ConversationID})
	if w.Code != 202 || !strings.Contains(w.Body.String(), `"steered":true`) {
		t.Fatalf("supplemental input %d %s", w.Code, w.Body.String())
	}
	h.mu.Lock()
	steer := h.steers[len(h.steers)-1]
	h.mu.Unlock()
	if steer.ThreadID != "active-thread" || steer.RunID != delegated.RunID || steer.ExpectedTurnID != "turn-"+delegated.RunID {
		t.Fatalf("supplement entered another context %+v", steer)
	}
	// The worker wire tests verify these exact input/output events; this test
	// checks their projection, cursor and conversation association through the API.
	_, err := h.s.store.AppendEvent(ctx, delegated.RunID, "message.user", map[string]string{"id": "steer-" + steer.MessageID, "text": steer.Prompt, "source": "steer"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = h.s.store.AppendEvent(ctx, delegated.RunID, "item/completed", map[string]any{"item": map[string]string{"id": "answer", "type": "agentMessage", "text": "Inspected project/src and found the entrypoint."}})
	if err != nil {
		t.Fatal(err)
	}
	if err = h.s.store.FinishRun(ctx, delegated.RunID, "completed", ""); err != nil {
		t.Fatal(err)
	}
	h.s.cancelRelay(delegated.RunID)
	updates := h.timeline("target", fmt.Sprintf("?after=%d", active.LastSequence))
	updatesJSON, _ := json.Marshal(updates)
	if updates.ActiveRunID != "" || !strings.Contains(string(updatesJSON), "project/src") || !strings.Contains(string(updatesJSON), "found the entrypoint") {
		t.Fatalf("missing input/output %s", updatesJSON)
	}
	sender := h.timeline("sender", "")
	senderJSON, _ := json.Marshal(sender)
	if !strings.Contains(string(senderJSON), "collaboration.sent") || !strings.Contains(string(senderJSON), `"status":"completed"`) || strings.Contains(string(senderJSON), "found the entrypoint") {
		t.Fatalf("sender mixes target outputs or loses state %s", senderJSON)
	}
	w = h.userMessage(map[string]string{"prompt": "Now explain those findings", "conversationId": prior.ConversationID})
	if w.Code != 202 {
		t.Fatalf("follow up %d %s", w.Code, w.Body.String())
	}
	h.mu.Lock()
	followup := h.turns[len(h.turns)-1]
	h.mu.Unlock()
	if followup.ConversationID != prior.ConversationID || followup.ThreadID != "active-thread" {
		t.Fatalf("manual followup lost delegated context %+v", followup)
	}
}

func TestManualAttachmentOnlySupplementUsesActiveTurn(t *testing.T) {
	h := newCollaborationHarness(t)
	active := h.active()
	file := domain.Attachment{Name: "directories.txt", Data: base64.StdEncoding.EncodeToString([]byte("/home/agent/project"))}
	w := h.userMessage(map[string]any{"attachments": []domain.Attachment{file}})
	if w.Code != 202 {
		t.Fatalf("status %d %s", w.Code, w.Body.String())
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.turns) != 0 || len(h.steers) != 1 || h.steers[0].RunID != active.ID || len(h.steers[0].Attachments) != 1 || h.steers[0].Attachments[0] != file {
		t.Fatalf("attachment steer %+v", h.steers)
	}
}

func TestScheduleIsIsolatedAndDoesNotResetOrSteerChat(t *testing.T) {
	h := newCollaborationHarness(t)
	ctx := context.Background()
	prior := h.active()
	_ = h.s.store.FinishRun(ctx, prior.ID, "completed", "")
	before := h.timeline("target", "")
	due := time.Now().Add(-2 * time.Minute)
	schedule := domain.Schedule{ID: "scheduled-task", AgentID: "target", Name: "Periodic check", Kind: "cron", Expression: "* * * * *", Timezone: "UTC", Prompt: "Scheduled-only instruction", Enabled: true, NextRunAt: &due, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	if err := h.s.store.CreateSchedule(ctx, schedule); err != nil {
		t.Fatal(err)
	}
	h.s.runDueSchedule(ctx, schedule)
	h.mu.Lock()
	if len(h.turns) != 1 {
		h.mu.Unlock()
		t.Fatal("schedule did not start")
	}
	first := h.turns[0]
	h.mu.Unlock()
	if first.ThreadID != "" || first.ConversationID == prior.ConversationID {
		t.Fatalf("schedule reused chat context %+v", first)
	}
	_, err := h.s.store.AppendWorkerEvent(ctx, first.RunID, 1, "item/completed", map[string]any{"item": map[string]string{"id": "schedule-answer", "type": "agentMessage", "text": "Scheduled-only output"}})
	if err != nil {
		t.Fatal(err)
	}
	page := h.timeline("target", "")
	encoded, _ := json.Marshal(page)
	if strings.Contains(string(encoded), "Scheduled-only") || page.ActiveRunID != "" || !page.RuntimeBusy || page.ConversationID != before.ConversationID {
		t.Fatalf("schedule leaked into chat %s", encoded)
	}
	w := h.userMessage(map[string]string{"prompt": "manual supplement", "conversationId": prior.ConversationID})
	if w.Code != 409 {
		t.Fatalf("manual message steered schedule: %d", w.Code)
	}
	task := h.send("wait-for-schedule", "steer")
	h.dispatch()
	h.mu.Lock()
	steerCount := len(h.steers)
	h.mu.Unlock()
	pending, _ := h.s.store.CollaborationTask(ctx, task.ID)
	if steerCount != 0 || pending.Status != "queued" {
		t.Fatalf("delegation steered schedule %+v", pending)
	}
	_ = h.s.store.FinishRun(ctx, first.RunID, "completed", "")
	h.s.cancelRelay(first.RunID)
	h.dispatch()
	h.mu.Lock()
	chat := h.turns[len(h.turns)-1]
	h.mu.Unlock()
	if chat.ConversationID != prior.ConversationID || chat.ThreadID != "active-thread" {
		t.Fatalf("schedule reset delegated chat %+v", chat)
	}
	_ = h.s.store.FinishRun(ctx, chat.RunID, "completed", "")
	h.s.cancelRelay(chat.RunID)
	schedule, err = h.s.store.Schedule(ctx, schedule.ID)
	if err != nil {
		t.Fatal(err)
	}
	h.s.runDueSchedule(ctx, schedule)
	h.mu.Lock()
	second := h.turns[len(h.turns)-1]
	h.mu.Unlock()
	if second.Source != "scheduled" || second.ThreadID != "" || second.ConversationID == first.ConversationID || second.ConversationID == prior.ConversationID {
		t.Fatalf("schedule not fresh %+v", second)
	}
	current := h.timeline("target", "")
	if current.ConversationID != prior.ConversationID {
		t.Fatal("second schedule reset chat context")
	}
	runs, err := h.s.store.ScheduleRuns(ctx, schedule.ID, "", 20)
	if err != nil || len(runs) != 2 || runs[0].ID != second.RunID || runs[1].ID != first.RunID {
		t.Fatalf("schedule history %+v %v", runs, err)
	}
}

func TestAgentTimelinePaginationAndCrossAgentBoundaries(t *testing.T) {
	h := newCollaborationHarness(t)
	ctx := context.Background()
	initial := h.timeline("target", "")
	if len(initial.Events) != 0 || initial.LastSequence != 0 {
		t.Fatalf("unexpected initial %+v", initial)
	}
	for i := 0; i < 7; i++ {
		_, _ = h.s.store.AppendTimelineEvent(ctx, "target", "", "message.user", map[string]string{"id": fmt.Sprint(i), "text": fmt.Sprint("visible", i)}, "")
		_, _ = h.s.store.AppendTimelineEvent(ctx, "stranger", "", "message.user", map[string]string{"text": "private"}, "")
	}
	first := h.timeline("target", "?after=0&limit=2")
	if len(first.Events) != 2 || !first.HasMore || !strings.Contains(string(first.Events[0].Payload), "visible0") {
		t.Fatalf("after zero skipped early inputs %+v", first)
	}
	second := h.timeline("target", fmt.Sprintf("?after=%d&limit=2", first.LastSequence))
	if len(second.Events) != 2 || second.Events[0].Sequence <= first.LastSequence || !strings.Contains(string(second.Events[0].Payload), "visible2") {
		t.Fatalf("incremental %+v", second)
	}
	latest := h.timeline("target", "?limit=2")
	if !latest.HasMore || !strings.Contains(string(latest.Events[0].Payload), "visible5") {
		t.Fatalf("latest %+v", latest)
	}
	older := h.timeline("target", fmt.Sprintf("?before=%d&limit=2", latest.BeforeSequence))
	if !older.HasMore || !strings.Contains(string(older.Events[0].Payload), "visible3") {
		t.Fatalf("older %+v", older)
	}
	all := h.timeline("target", "?after=0&limit=100")
	encoded, _ := json.Marshal(all)
	if len(all.Events) != 7 || strings.Contains(string(encoded), "private") {
		t.Fatalf("cross-agent timeline %s", encoded)
	}
	r := httptest.NewRequest("GET", "/api/agents/target/timeline", nil)
	w := httptest.NewRecorder()
	h.s.Handler().ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatalf("unauthenticated timeline status %d", w.Code)
	}
}

func TestNewChatResetsFutureDelegationContextAndKeepsTimeline(t *testing.T) {
	h := newCollaborationHarness(t)
	ctx := context.Background()
	prior := h.active()
	reset := func() *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/api/agents/target/conversations", nil)
		r.SetPathValue("agentID", "target")
		w := httptest.NewRecorder()
		h.s.newConversation(w, r)
		return w
	}
	if w := reset(); w.Code != 409 {
		t.Fatalf("reset during active run: %d", w.Code)
	}
	_ = h.s.store.FinishRun(ctx, prior.ID, "completed", "")
	task := h.send("after-new-chat", "queue")
	w := reset()
	var c domain.Conversation
	if err := json.Unmarshal(w.Body.Bytes(), &c); err != nil {
		t.Fatal(err)
	}
	if w.Code != 201 || c.ID == prior.ConversationID || c.CodexThreadID != "" {
		t.Fatalf("context not reset %d %+v", w.Code, c)
	}
	page := h.timeline("target", "")
	encoded, _ := json.Marshal(page)
	if page.ConversationID != c.ID || !strings.Contains(string(encoded), "existing user task") || !strings.Contains(string(encoded), "conversation.reset") {
		t.Fatalf("history missing after reset %s", encoded)
	}
	stale := h.userMessage(map[string]string{"prompt": "stale-tab message", "conversationId": prior.ConversationID})
	if stale.Code != 409 {
		t.Fatalf("stale context silently redirected: %d", stale.Code)
	}
	h.dispatch()
	h.mu.Lock()
	turn := h.turns[0]
	h.mu.Unlock()
	if turn.ConversationID != c.ID || turn.ThreadID != "" || !strings.Contains(turn.Prompt, task.ID) {
		t.Fatalf("delegation reused old context %+v", turn)
	}
	_ = h.s.store.FinishRun(ctx, turn.RunID, "completed", "")
	h.s.cancelRelay(turn.RunID)
	w = h.userMessage(map[string]string{"prompt": "continue in new context", "conversationId": c.ID})
	if w.Code != 202 {
		t.Fatalf("new context send %d %s", w.Code, w.Body.String())
	}
	h.mu.Lock()
	followup := h.turns[len(h.turns)-1]
	h.mu.Unlock()
	if followup.ConversationID != c.ID || followup.ThreadID != "thread-"+turn.RunID {
		t.Fatalf("new context not shared %+v", followup)
	}
}

func TestTimelineRelayRetriesPersistenceBeforeAcknowledgingCompletion(t *testing.T) {
	h := newCollaborationHarness(t)
	ctx := context.Background()
	run := h.active()
	first := runtimeclient.WorkerEvent{Sequence: 1, Type: "item/agentMessage/delta", Payload: json.RawMessage(`{"itemId":"answer","delta":"first"}`)}
	invalid := runtimeclient.WorkerEvent{Sequence: 0, Type: "invalid-sequence", Payload: json.RawMessage(`{}`)}
	h.eventBatch = &runtimeclient.EventBatch{Events: []runtimeclient.WorkerEvent{first, invalid}, Status: "completed"}
	done := make(chan struct{})
	go func() { h.s.relayRun("target", run.ID); close(done) }()
	deadline := time.Now().Add(2 * time.Second)
	for {
		sequence, err := h.s.store.MaxEventSequence(ctx, run.ID)
		if err != nil {
			t.Fatal(err)
		}
		if sequence == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("first event not persisted")
		}
		time.Sleep(5 * time.Millisecond)
	}
	stored, err := h.s.store.Run(ctx, run.ID)
	if err != nil || stored.Status != "running" {
		t.Fatalf("terminal acknowledged after persistence failure %+v %v", stored, err)
	}
	third := runtimeclient.WorkerEvent{Sequence: 3, Type: "item/completed", Payload: json.RawMessage(`{"item":{"id":"answer","type":"agentMessage","text":"first and final"}}`)}
	h.mu.Lock()
	h.eventBatch = &runtimeclient.EventBatch{Events: []runtimeclient.WorkerEvent{first, third}, Status: "completed"}
	h.mu.Unlock()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("relay did not recover")
	}
	events, err := h.s.store.EventsAfter(ctx, run.ID, 0)
	if err != nil || len(events) != 2 || events[0].Sequence != 1 || events[1].Sequence != 3 {
		t.Fatalf("source sequence lost %+v %v", events, err)
	}
	page := h.timeline("target", "")
	count := 0
	for _, event := range page.Events {
		if event.Type == "item/completed" {
			count++
		}
	}
	if count != 1 || page.ActiveRunID != "" {
		t.Fatalf("timeline duplicate or unfinished %+v", page)
	}
}
