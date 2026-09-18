package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/common-creation/codexbot/internal/domain"
)

func (h *collaborationHarness) scheduleRequest(scheduleID, runID, path, query string) *httptest.ResponseRecorder {
	h.t.Helper()
	r := httptest.NewRequest("GET", path+query, nil)
	r.SetPathValue("scheduleID", scheduleID)
	r.SetPathValue("runID", runID)
	w := httptest.NewRecorder()
	switch path {
	case "/detail":
		h.s.getSchedule(w, r)
	case "/runs":
		h.s.scheduleRuns(w, r)
	case "/events":
		h.s.scheduleRunEvents(w, r)
	}
	return w
}

func scheduleFixture(t *testing.T, h *collaborationHarness, id string) domain.Schedule {
	t.Helper()
	sc := domain.Schedule{ID: id, AgentID: "target", Name: "Scheduled audit", Prompt: "original schedule input", Kind: "cron", Expression: "0 9 * * *", Timezone: "Asia/Tokyo", Enabled: true, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	if err := h.s.store.CreateSchedule(context.Background(), sc); err != nil {
		t.Fatal(err)
	}
	return sc
}

func scheduleRunFixture(t *testing.T, h *collaborationHarness, sc domain.Schedule, id, status string, scheduled time.Time) domain.Run {
	t.Helper()
	ctx := context.Background()
	c := domain.Conversation{ID: "context-" + id, AgentID: sc.AgentID, Kind: "scheduled", RoleVersion: 1, CreatedAt: scheduled}
	if err := h.s.store.CreateConversation(ctx, c); err != nil {
		t.Fatal(err)
	}
	run := domain.Run{ID: id, AgentID: sc.AgentID, ConversationID: c.ID, Source: "schedule:" + sc.ID, Prompt: "input for " + id, Status: status, ScheduledFor: &scheduled}
	if err := h.s.store.CreateRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	if status != "running" && status != "queued" {
		if err := h.s.store.FinishRun(ctx, id, status, "reason for "+status); err != nil {
			t.Fatal(err)
		}
	}
	return run
}

func TestScheduleDetailsHistoryAndOutputRemainOutsideChat(t *testing.T) {
	h := newCollaborationHarness(t)
	ctx := context.Background()
	sc := scheduleFixture(t, h, "schedule")
	other := scheduleFixture(t, h, "schedule-other")
	now := time.Now()
	statuses := []string{"completed", "failed", "interrupted", "unknown", "skipped_overlap", "running"}
	for i, status := range statuses {
		scheduleRunFixture(t, h, sc, fmt.Sprint("run-", i), status, now.Add(time.Duration(i)*time.Minute))
	}
	foreign := scheduleRunFixture(t, h, other, "other-run", "completed", now.Add(time.Hour))
	_, err := h.s.store.AppendWorkerEvent(ctx, "run-0", 1, "item/agentMessage/delta", map[string]string{"itemId": "answer", "delta": "schedule answer"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = h.s.store.AppendWorkerEvent(ctx, "run-0", 3, "item/completed", map[string]any{"item": map[string]string{"id": "answer", "type": "agentMessage", "text": "schedule final answer"}})
	if err != nil {
		t.Fatal(err)
	}
	detail := h.scheduleRequest(sc.ID, "", "/detail", "")
	if detail.Code != 200 || !strings.Contains(detail.Body.String(), sc.Prompt) {
		t.Fatal(detail.Body.String())
	}
	first := h.scheduleRequest(sc.ID, "", "/runs", "?limit=2")
	var page struct {
		Runs []domain.Run `json:"runs"`
		Next string       `json:"nextCursor"`
	}
	if err := json.Unmarshal(first.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if first.Code != 200 || len(page.Runs) != 2 || page.Runs[0].Status != "running" || page.Runs[1].Status != "skipped_overlap" || page.Next != "run-4" {
		t.Fatalf("history %s", first.Body.String())
	}
	rest := h.scheduleRequest(sc.ID, "", "/runs", "?limit=100&before="+page.Next)
	if err := json.Unmarshal(rest.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Runs) != 4 || page.Next != "" || page.Runs[len(page.Runs)-1].ID != "run-0" {
		t.Fatal(rest.Body.String())
	}
	output := h.scheduleRequest(sc.ID, "run-0", "/events", "?limit=1&after=0")
	var result struct {
		Run    domain.Run     `json:"run"`
		Events []domain.Event `json:"events"`
		Next   int64          `json:"nextSequence"`
		More   bool           `json:"hasMore"`
	}
	if err := json.Unmarshal(output.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if output.Code != 200 || result.Run.Prompt != "input for run-0" || len(result.Events) != 1 || result.Next != 1 || !result.More {
		t.Fatal(output.Body.String())
	}
	output = h.scheduleRequest(sc.ID, "run-0", "/events", "?limit=1&after=1")
	if err := json.Unmarshal(output.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Next != 3 || result.More || !strings.Contains(output.Body.String(), "schedule final answer") {
		t.Fatal(output.Body.String())
	}
	if w := h.scheduleRequest(sc.ID, foreign.ID, "/events", ""); w.Code != 404 {
		t.Fatalf("cross-schedule output exposed %d", w.Code)
	}
	if w := h.scheduleRequest(sc.ID, "", "/runs", "?before="+foreign.ID); w.Code != 400 {
		t.Fatalf("cross-schedule cursor %d", w.Code)
	}
	chat := h.timeline("target", "")
	if len(chat.Events) != 0 || chat.ActiveRunID != "" || !chat.RuntimeBusy {
		t.Fatalf("schedule mixed into chat %+v", chat)
	}
	for _, path := range []string{"/api/schedules/schedule", "/api/schedules/schedule/runs", "/api/schedules/schedule/runs/run-0/events"} {
		w := httptest.NewRecorder()
		h.s.Handler().ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != 401 {
			t.Fatalf("unauthenticated %s: %d", path, w.Code)
		}
	}
}

func TestScheduleOutputBoundsAndMissingHistory(t *testing.T) {
	h := newCollaborationHarness(t)
	sc := scheduleFixture(t, h, "schedule")
	if w := h.scheduleRequest("missing", "", "/detail", ""); w.Code != 404 {
		t.Fatal(w.Code)
	}
	w := h.scheduleRequest(sc.ID, "", "/runs", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"runs":[]`) {
		t.Fatal(w.Body.String())
	}
	run := scheduleRunFixture(t, h, sc, "large-output", "completed", time.Now())
	_, err := h.s.store.AppendWorkerEvent(context.Background(), run.ID, 1, "item/completed", map[string]string{"text": strings.Repeat("x", 5<<20)})
	if err != nil {
		t.Fatal(err)
	}
	w = h.scheduleRequest(sc.ID, run.ID, "/events", "")
	if w.Code != 200 || w.Body.Len() > 256<<10 || !strings.Contains(w.Body.String(), `"truncated":true`) || !strings.Contains(w.Body.String(), `"nextSequence":1`) {
		t.Fatalf("bounded output status=%d bytes=%d", w.Code, w.Body.Len())
	}
	for _, query := range []string{"?limit=0", "?limit=101", "?after=-1"} {
		if w := h.scheduleRequest(sc.ID, run.ID, "/events", query); w.Code != 400 {
			t.Fatalf("invalid query %s status=%d", query, w.Code)
		}
	}
}
