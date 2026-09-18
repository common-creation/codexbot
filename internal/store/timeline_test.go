package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/common-creation/codexbot/internal/domain"
)

func timelineAgent(t *testing.T, s *Store, id string) domain.Agent {
	t.Helper()
	a := domain.Agent{ID: id, Name: id, RolePrompt: "inspect repository", RoleVersion: 1, Status: "running", ProviderAuthState: "connected", CreatedAt: time.Now(), UpdatedAt: time.Now()}
	if err := s.CreateAgent(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	return a
}

func TestEnsureAgentConversationKeepsThreadAcrossSettingsAndRestarts(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "timeline.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { s.Close() }()
	a := timelineAgent(t, s, "agent")
	c, err := s.EnsureAgentConversation(ctx, a)
	if err != nil || c.Kind != "agent" {
		t.Fatalf("conversation=%+v err=%v", c, err)
	}
	if err = s.SetConversationThread(ctx, c.ID, "retained-thread"); err != nil {
		t.Fatal(err)
	}
	a, err = s.UpdateAgent(ctx, a.ID, a.Name, "also inspect /workspace/project", "", "", domain.PermissionAuto)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.EnsureAgentConversation(ctx, a)
	if err != nil || got.ID != c.ID || got.CodexThreadID != "retained-thread" || got.RoleVersion != a.RoleVersion {
		t.Fatalf("conversation after restart=%+v err=%v", got, err)
	}
	if _, err = s.EnsureAgentConversation(ctx, domain.Agent{ID: "missing"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing agent err=%v", err)
	}
}

func TestEnsureAgentConversationAdoptsExistingContextAndSerializes(t *testing.T) {
	for _, tc := range []struct {
		name   string
		active bool
		manual bool
		want   string
	}{
		{"active wins", true, true, "active"},
		{"manual preferred", false, true, "manual"},
		{"latest any", false, false, "latest"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			s := testStore(t)
			a := timelineAgent(t, s, "agent")
			for i, id := range []string{"active", "manual", "latest"} {
				kind := "collaboration"
				if id == "manual" && tc.manual {
					kind = "manual"
				}
				if err := s.CreateConversation(ctx, domain.Conversation{ID: id, AgentID: a.ID, Kind: kind, RoleVersion: 1, CodexThreadID: "thread-" + id, CreatedAt: time.Now().Add(time.Duration(i) * time.Minute)}); err != nil {
					t.Fatal(err)
				}
			}
			if tc.active {
				if err := s.CreateRun(ctx, domain.Run{ID: "active-run", AgentID: a.ID, ConversationID: "active", Source: "collaboration:task", Prompt: "work", Status: "running"}); err != nil {
					t.Fatal(err)
				}
			}
			var wg sync.WaitGroup
			for i := 0; i < 8; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					c, err := s.EnsureAgentConversation(ctx, a)
					if err != nil || c.ID != tc.want || c.CodexThreadID != "thread-"+tc.want {
						t.Errorf("conversation=%+v err=%v", c, err)
					}
				}()
			}
			wg.Wait()
			var count int
			if err := s.db.QueryRow(`SELECT COUNT(*) FROM agent_conversations`).Scan(&count); err != nil || count != 1 {
				t.Fatalf("mapping count=%d err=%v", count, err)
			}
		})
	}
}

func TestAgentTimelineMirrorsChatRunsWithPaginationAndIdempotency(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	a := timelineAgent(t, s, "agent")
	b := timelineAgent(t, s, "other")
	c, err := s.EnsureAgentConversation(ctx, a)
	if err != nil {
		t.Fatal(err)
	}
	for i, source := range []string{"manual", "collaboration:task", "manual"} {
		run := domain.Run{ID: fmt.Sprintf("run-%d", i), AgentID: a.ID, ConversationID: c.ID, Source: source, Prompt: "work " + source, Status: "completed"}
		if err = s.CreateRun(ctx, run); err != nil {
			t.Fatal(err)
		}
		event, err := s.AppendEvent(ctx, run.ID, "message.assistant.completed", map[string]string{"id": "reply-" + run.ID, "text": "done"})
		if err != nil || event.Sequence != 1 {
			t.Fatalf("run event=%+v err=%v", event, err)
		}
	}
	all, err := s.AgentTimeline(ctx, a.ID, 0, 0, 100)
	if err != nil || len(all) != 6 {
		t.Fatalf("timeline=%+v err=%v", all, err)
	}
	for i, event := range all {
		if event.Sequence != int64(i+1) {
			t.Fatalf("sequence=%d at index=%d", event.Sequence, i)
		}
		if i%2 == 0 && event.Type != "message.user" {
			t.Fatalf("missing input before output: %+v", event)
		}
	}
	latest, err := s.AgentTimeline(ctx, a.ID, 0, 0, 2)
	if err != nil || len(latest) != 2 || latest[0].Sequence != 5 {
		t.Fatalf("latest=%+v err=%v", latest, err)
	}
	previous, err := s.AgentTimeline(ctx, a.ID, 0, latest[0].Sequence, 2)
	if err != nil || len(previous) != 2 || previous[0].Sequence != 3 {
		t.Fatalf("previous=%+v err=%v", previous, err)
	}
	next, err := s.AgentTimeline(ctx, a.ID, 2, 0, 2)
	if err != nil || len(next) != 2 || next[0].Sequence != 3 {
		t.Fatalf("next=%+v err=%v", next, err)
	}
	first, err := s.AgentTimelineAfter(ctx, a.ID, 0, 2)
	if err != nil || len(first) != 2 || first[0].Sequence != 1 || first[1].Sequence != 2 {
		t.Fatalf("incremental from zero=%+v err=%v", first, err)
	}
	if _, err = s.AgentTimeline(ctx, a.ID, 2, 3, 2); err == nil {
		t.Fatal("accepted both cursors")
	}
	event, err := s.AppendTimelineEvent(ctx, a.ID, "", "collaboration.received", map[string]string{"taskId": "queued-task"}, "receipt")
	if err != nil {
		t.Fatal(err)
	}
	duplicate, err := s.AppendTimelineEvent(ctx, a.ID, "", "collaboration.received", map[string]string{"taskId": "queued-task"}, "receipt")
	if err != nil || event.Sequence != duplicate.Sequence {
		t.Fatalf("duplicate=%+v err=%v", duplicate, err)
	}
	if _, err = s.AppendTimelineEvent(ctx, b.ID, "run-0", "message.user", map[string]string{"text": "wrong owner"}, "wrong"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-agent run err=%v", err)
	}
	other, err := s.AgentTimeline(ctx, b.ID, 0, 0, 100)
	if err != nil || len(other) != 0 {
		t.Fatalf("other timeline=%+v err=%v", other, err)
	}
	max, err := s.MaxAgentTimelineSequence(ctx, a.ID)
	if err != nil || max != event.Sequence {
		t.Fatalf("max=%d err=%v", max, err)
	}
}

func TestAgentTimelineWritesRollbackTogether(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	a := timelineAgent(t, s, "agent")
	c, err := s.EnsureAgentConversation(ctx, a)
	if err != nil {
		t.Fatal(err)
	}
	run := domain.Run{ID: "run", AgentID: a.ID, ConversationID: c.ID, Source: "manual", Prompt: "task", Status: "queued"}
	if err = s.CreateRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec(`CREATE TRIGGER reject_timeline BEFORE INSERT ON agent_timeline BEGIN SELECT RAISE(ABORT,'test timeline failure'); END;`); err != nil {
		t.Fatal(err)
	}
	if _, err = s.AppendEvent(ctx, run.ID, "message.assistant.completed", map[string]string{"text": "cannot persist"}); err == nil {
		t.Fatal("expected append failure")
	}
	if events, err := s.EventsAfter(ctx, run.ID, 0); err != nil || len(events) != 0 {
		t.Fatalf("event write escaped rollback: %+v %v", events, err)
	}
	if err = s.FinishRun(ctx, run.ID, "interrupted", "stop"); err == nil {
		t.Fatal("expected terminal persistence failure")
	}
	if stored, err := s.Run(ctx, run.ID); err != nil || stored.Status != "queued" || stored.FinishedAt != nil {
		t.Fatalf("terminal state escaped rollback: %+v %v", stored, err)
	}
	if _, err = s.db.Exec(`DROP TRIGGER reject_timeline`); err != nil {
		t.Fatal(err)
	}
	if err = s.FinishRun(ctx, run.ID, "completed", ""); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec(`CREATE TRIGGER reject_timeline BEFORE INSERT ON agent_timeline BEGIN SELECT RAISE(ABORT,'test timeline failure'); END;`); err != nil {
		t.Fatal(err)
	}
	run.ID = "rejected-run"
	if err = s.CreateRun(ctx, run); err == nil {
		t.Fatal("expected input persistence failure")
	}
	if _, err = s.Run(ctx, run.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("run write escaped rollback: %v", err)
	}
}

func TestAgentTimelineMigrationBackfillsChatConversationsOnce(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "legacy.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	a := timelineAgent(t, s, "agent")
	for i, kind := range []string{"manual", "scheduled", "collaboration"} {
		c := domain.Conversation{ID: kind, AgentID: a.ID, Kind: kind, RoleVersion: 1, CreatedAt: time.Now().Add(time.Duration(i) * time.Minute)}
		if err = s.CreateConversation(ctx, c); err != nil {
			t.Fatal(err)
		}
		run := domain.Run{ID: "run-" + kind, AgentID: a.ID, ConversationID: c.ID, Source: map[string]string{"manual": "manual", "scheduled": "schedule:job", "collaboration": "collaboration:task"}[kind], Prompt: "input-" + kind, Status: "completed"}
		if err = s.CreateRun(ctx, run); err != nil {
			t.Fatal(err)
		}
		if _, err = s.AppendEvent(ctx, run.ID, "message.assistant.completed", map[string]string{"text": "output-" + kind}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = s.db.Exec(`DROP TABLE agent_timeline; DROP TABLE agent_conversations; DELETE FROM migrations WHERE name='agent-timeline-v1'`); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	for reopen := 0; reopen < 2; reopen++ {
		s, err = Open(path)
		if err != nil {
			t.Fatal(err)
		}
		events, err := s.AgentTimeline(ctx, a.ID, 0, 0, 100)
		if err != nil || len(events) != 6 {
			t.Fatalf("backfilled events=%+v err=%v", events, err)
		}
		for i, event := range events {
			if i%3 == 0 {
				var input map[string]any
				if err = json.Unmarshal(event.Payload, &input); err != nil {
					t.Fatal(err)
				}
				if event.Type != "message.user" || input["id"] != "user-"+event.RunID {
					t.Fatalf("bad restored input: %+v", event)
				}
			} else if i%3 == 1 && (event.Type != "message.assistant.completed" || event.RunID != events[i-1].RunID) {
				t.Fatalf("bad restored output: %+v", event)
			} else if i%3 == 2 && (event.Type != "run.status" || event.RunID != events[i-1].RunID) {
				t.Fatalf("bad restored terminal status: %+v", event)
			}
		}
		if err = s.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestFinishRunProjectsTerminalWithoutChangingWorkerCursor(t *testing.T) {
	for _, status := range []string{"interrupted", "unknown"} {
		t.Run(status, func(t *testing.T) {
			ctx := context.Background()
			s := testStore(t)
			a := timelineAgent(t, s, "agent")
			c, err := s.EnsureAgentConversation(ctx, a)
			if err != nil {
				t.Fatal(err)
			}
			run := domain.Run{ID: "run", AgentID: a.ID, ConversationID: c.ID, Source: "manual", Prompt: "work", Status: "running"}
			if err = s.CreateRun(ctx, run); err != nil {
				t.Fatal(err)
			}
			if _, err = s.AppendEvent(ctx, run.ID, "turn/started", map[string]string{"id": "turn"}); err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 2; i++ {
				if err = s.FinishRun(ctx, run.ID, status, "stopped or connection lost"); err != nil {
					t.Fatal(err)
				}
			}
			seq, err := s.MaxEventSequence(ctx, run.ID)
			if err != nil || seq != 1 {
				t.Fatalf("worker cursor=%d err=%v", seq, err)
			}
			events, err := s.AgentTimeline(ctx, a.ID, 0, 0, 100)
			if err != nil || len(events) != 3 || events[2].Type != "run.status" {
				t.Fatalf("terminal events=%+v err=%v", events, err)
			}
			var payload map[string]string
			if err = json.Unmarshal(events[2].Payload, &payload); err != nil {
				t.Fatal(err)
			}
			if payload["status"] != status || payload["error"] != "stopped or connection lost" {
				t.Fatalf("terminal payload=%+v", payload)
			}
		})
	}
}

func TestScheduleRunKeepsAgentConversationAndTimelineIsolated(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	a := timelineAgent(t, s, "agent")
	c, err := s.EnsureAgentConversation(ctx, a)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	due := now.Add(-time.Minute)
	sc := domain.Schedule{ID: "job", AgentID: a.ID, Name: "Job", Prompt: "inspect", Kind: "once", Expression: "", Timezone: "UTC", Enabled: true, NextRunAt: &due, CreatedAt: now, UpdatedAt: now}
	if err = s.CreateSchedule(ctx, sc); err != nil {
		t.Fatal(err)
	}
	scheduled := domain.Conversation{ID: "scheduled", AgentID: a.ID, Kind: "scheduled", RoleVersion: 1, CreatedAt: now}
	run := domain.Run{ID: "scheduled-run", AgentID: a.ID, ConversationID: scheduled.ID, Source: "schedule:job", Prompt: sc.Prompt, Status: "queued", ScheduledFor: &due}
	if err = s.ClaimScheduleRun(ctx, sc, scheduled, run, nil, false); err != nil {
		t.Fatal(err)
	}
	got, err := s.Conversation(ctx, c.ID)
	if err != nil || got.Kind != "agent" {
		t.Fatalf("canonical conversation=%+v err=%v", got, err)
	}
	events, err := s.AgentTimeline(ctx, a.ID, 0, 0, 100)
	if err != nil || len(events) != 0 {
		t.Fatalf("schedule input=%+v err=%v", events, err)
	}
}

func TestCollaborationTimelineProjectsQueueAndResultOnBothAgents(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	sender := timelineAgent(t, s, "sender")
	target := timelineAgent(t, s, "target")
	task, _, err := s.EnqueueCollaborationTask(ctx, domain.CollaborationTask{SenderAgentID: sender.ID, TargetAgentID: target.ID, IdempotencyKey: "request", Prompt: "inspect /work/project"})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err = s.SyncCollaborationTimeline(ctx); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{sender.ID, target.ID} {
		events, err := s.AgentTimeline(ctx, id, 0, 0, 100)
		if err != nil || len(events) != 2 {
			t.Fatalf("%s receipt events=%+v err=%v", id, events, err)
		}
	}
	c, err := s.EnsureAgentConversation(ctx, target)
	if err != nil {
		t.Fatal(err)
	}
	run := domain.Run{ID: "delegated-run", AgentID: target.ID, ConversationID: c.ID, Source: "collaboration:" + task.ID, Prompt: task.Prompt, Status: "queued"}
	if err = s.CreateRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	if claimed, err := s.ClaimCollaborationTask(ctx, task.ID, run.ID); err != nil || !claimed {
		t.Fatalf("claim=%v err=%v", claimed, err)
	}
	if err = s.SetCollaborationTaskState(ctx, task.ID, "running", run.ID, ""); err != nil {
		t.Fatal(err)
	}
	if err = s.SyncCollaborationTimeline(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = s.AppendEvent(ctx, run.ID, "message.assistant.completed", map[string]string{"text": "found files"}); err != nil {
		t.Fatal(err)
	}
	if err = s.FinishRun(ctx, run.ID, "completed", ""); err != nil {
		t.Fatal(err)
	}
	if err = s.ReconcileCollaborationTasks(ctx); err != nil {
		t.Fatal(err)
	}
	if err = s.SyncCollaborationTimeline(ctx); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{sender.ID, target.ID} {
		events, err := s.AgentTimeline(ctx, id, 0, 0, 100)
		if err != nil {
			t.Fatal(err)
		}
		var state map[string]any
		if err = json.Unmarshal(events[len(events)-1].Payload, &state); err != nil {
			t.Fatal(err)
		}
		if state["status"] != "completed" || state["runId"] != run.ID {
			t.Fatalf("%s state=%+v", id, state)
		}
		var outputs, inputs int
		for _, event := range events {
			if event.Type == "message.assistant.completed" {
				outputs++
			}
			if event.Type == "message.user" {
				inputs++
				var input map[string]any
				if err = json.Unmarshal(event.Payload, &input); err != nil {
					t.Fatal(err)
				}
				if input["senderAgentId"] != sender.ID || input["taskId"] != task.ID {
					t.Fatalf("delegated input=%+v", input)
				}
			}
		}
		if (id == target.ID && (inputs != 1 || outputs != 1)) || (id == sender.ID && (inputs != 0 || outputs != 0)) {
			t.Fatalf("%s inputs=%d outputs=%d", id, inputs, outputs)
		}
	}
}

func TestSkippedScheduleStaysOutsideChatIncludingMigration(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	a := timelineAgent(t, s, "agent")
	c := domain.Conversation{ID: "scheduled", AgentID: a.ID, Kind: "scheduled", RoleVersion: 1, CreatedAt: time.Now()}
	var err error
	now := time.Now()
	due := now.Add(-20 * time.Minute)
	sc := domain.Schedule{ID: "job", AgentID: a.ID, Name: "Job", Prompt: "inspect /work/project", Kind: "once", Timezone: "UTC", Enabled: true, NextRunAt: &due, CreatedAt: now, UpdatedAt: now}
	if err = s.CreateSchedule(ctx, sc); err != nil {
		t.Fatal(err)
	}
	run := domain.Run{ID: "skipped-run", AgentID: a.ID, ConversationID: c.ID, Source: "schedule:job", Prompt: sc.Prompt, Status: "skipped_overlap", Error: "agent busy beyond misfire grace", ScheduledFor: &due}
	if err = s.ClaimScheduleRun(ctx, sc, c, run, nil, false); err != nil {
		t.Fatal(err)
	}
	assertTimeline := func() {
		t.Helper()
		events, err := s.AgentTimeline(ctx, a.ID, 0, 0, 100)
		if err != nil || len(events) != 0 {
			t.Fatalf("skipped schedule timeline=%+v err=%v", events, err)
		}
		seq, err := s.MaxEventSequence(ctx, run.ID)
		if err != nil || seq != 0 {
			t.Fatalf("worker cursor=%d err=%v", seq, err)
		}
	}
	assertTimeline()
	if _, err = s.db.Exec(`DROP TABLE agent_timeline; DELETE FROM migrations WHERE name='agent-timeline-v1'`); err != nil {
		t.Fatal(err)
	}
	if err = s.migrateAgentTimeline(); err != nil {
		t.Fatal(err)
	}
	assertTimeline()
}

func TestAppendWorkerEventPreservesGapAndRetriesAtomically(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	a := timelineAgent(t, s, "agent")
	c, err := s.EnsureAgentConversation(ctx, a)
	if err != nil {
		t.Fatal(err)
	}
	run := domain.Run{ID: "run", AgentID: a.ID, ConversationID: c.ID, Source: "manual", Prompt: "work", Status: "running"}
	if err = s.CreateRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	for _, seq := range []int64{1, 3, 3} {
		event, err := s.AppendWorkerEvent(ctx, run.ID, seq, "message.assistant.delta", map[string]string{"id": "answer", "delta": fmt.Sprint(seq)})
		if err != nil || event.Sequence != seq {
			t.Fatalf("worker event=%+v err=%v", event, err)
		}
	}
	assertEvents := func() {
		t.Helper()
		events, err := s.EventsAfter(ctx, run.ID, 0)
		if err != nil || len(events) != 2 || events[0].Sequence != 1 || events[1].Sequence != 3 {
			t.Fatalf("worker events=%+v err=%v", events, err)
		}
		timeline, err := s.AgentTimeline(ctx, a.ID, 0, 0, 100)
		if err != nil || len(timeline) != 3 {
			t.Fatalf("worker timeline=%+v err=%v", timeline, err)
		}
		max, err := s.MaxEventSequence(ctx, run.ID)
		if err != nil || max != 3 {
			t.Fatalf("worker cursor=%d err=%v", max, err)
		}
	}
	assertEvents()
	if _, err = s.AppendWorkerEvent(ctx, run.ID, 3, "message.assistant.delta", map[string]string{"delta": "different"}); err == nil {
		t.Fatal("accepted conflicting source event")
	}
	if _, err = s.db.Exec(`CREATE TRIGGER reject_worker_timeline BEFORE INSERT ON agent_timeline BEGIN SELECT RAISE(ABORT,'test timeline failure'); END;`); err != nil {
		t.Fatal(err)
	}
	if _, err = s.AppendWorkerEvent(ctx, run.ID, 5, "message.assistant.completed", map[string]string{"text": "done"}); err == nil {
		t.Fatal("expected persistence failure")
	}
	assertEvents()
	if _, err = s.db.Exec(`DROP TRIGGER reject_worker_timeline`); err != nil {
		t.Fatal(err)
	}
	event, err := s.AppendWorkerEvent(ctx, run.ID, 5, "message.assistant.completed", map[string]string{"text": "done"})
	if err != nil || event.Sequence != 5 {
		t.Fatalf("retried event=%+v err=%v", event, err)
	}
	if _, err = s.AppendWorkerEvent(ctx, run.ID, 0, "message.assistant.delta", nil); err == nil {
		t.Fatal("accepted zero source cursor")
	}
}
