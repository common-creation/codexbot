package store

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/common-creation/codexbot/internal/domain"
)

func TestScheduleRunsPagesAllStatesAndScopesCursor(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	a := timelineAgent(t, s, "agent")
	c := domain.Conversation{ID: "scheduled", AgentID: a.ID, Kind: "scheduled", RoleVersion: 1, CreatedAt: time.Now()}
	if err := s.CreateConversation(ctx, c); err != nil {
		t.Fatal(err)
	}
	states := []string{"queued", "running", "completed", "failed", "interrupted", "unknown", "skipped_overlap"}
	for i, state := range states {
		// Each agent may have only one active execution. A second agent permits
		// both active states in the fixture without bypassing that constraint.
		agentID := a.ID
		conversationID := c.ID
		if state == "running" {
			other := timelineAgent(t, s, "running-agent")
			cc := domain.Conversation{ID: "running-context", AgentID: other.ID, Kind: "scheduled", RoleVersion: 1, CreatedAt: time.Now()}
			if err := s.CreateConversation(ctx, cc); err != nil {
				t.Fatal(err)
			}
			agentID, conversationID = other.ID, cc.ID
		}
		due := time.Date(2026, 9, 14, i, 0, 0, 0, time.UTC)
		r := domain.Run{ID: fmt.Sprintf("run-%d", i), AgentID: agentID, ConversationID: conversationID, Source: "schedule:job_%", Prompt: state, Status: state, ScheduledFor: &due}
		if err := s.CreateRun(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	for _, source := range []string{"manual", "schedule:job_a", "schedule:job_%extra", "schedule:other"} {
		if err := s.CreateRun(ctx, domain.Run{ID: source, AgentID: a.ID, ConversationID: c.ID, Source: source, Prompt: "other", Status: "completed"}); err != nil {
			t.Fatal(err)
		}
	}
	var cursor string
	var got []domain.Run
	for {
		page, err := s.ScheduleRuns(ctx, "job_%", cursor, 2)
		if err != nil {
			t.Fatal(err)
		}
		if len(page) == 0 {
			break
		}
		got = append(got, page...)
		cursor = page[len(page)-1].ID
	}
	if len(got) != len(states) {
		t.Fatalf("runs=%+v", got)
	}
	for i, r := range got {
		want := len(states) - i - 1
		if r.ID != fmt.Sprintf("run-%d", want) || r.Status != states[want] {
			t.Fatalf("run %d=%+v", i, r)
		}
	}
	for _, cursor := range []string{"manual", "schedule:other", "missing"} {
		if _, err := s.ScheduleRuns(ctx, "job_%", cursor, 2); !errors.Is(err, ErrNotFound) {
			t.Fatalf("cursor %q err=%v", cursor, err)
		}
	}
	if empty, err := s.ScheduleRuns(ctx, "missing", "", 0); err != nil || empty == nil || len(empty) != 0 {
		t.Fatalf("empty=%+v err=%v", empty, err)
	}
}

func TestScheduleRunsLegacyNullTimesHaveStablePagesAndLimit(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	a := timelineAgent(t, s, "agent")
	c, err := s.EnsureAgentConversation(ctx, a)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 105; i++ {
		if err := s.CreateRun(ctx, domain.Run{ID: fmt.Sprintf("run-%03d", i), AgentID: a.ID, ConversationID: c.ID, Source: "schedule:job", Status: "completed"}); err != nil {
			t.Fatal(err)
		}
	}
	first, err := s.ScheduleRuns(ctx, "job", "", 1000)
	if err != nil || len(first) != 101 || first[0].ID != "run-104" || first[100].ID != "run-004" {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	last, err := s.ScheduleRuns(ctx, "job", first[100].ID, 100)
	if err != nil || len(last) != 4 || last[0].ID != "run-003" || last[3].ID != "run-000" {
		t.Fatalf("last=%+v err=%v", last, err)
	}
}

func TestScheduledRunEventsRemainAvailableWithoutTimelineProjection(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	a := timelineAgent(t, s, "agent")
	c, err := s.EnsureAgentConversation(ctx, a)
	if err != nil {
		t.Fatal(err)
	}
	for _, source := range []string{"schedule:job", "schedule:job_%", "scheduled", "schedule", "Schedule:job", "manual", "collaboration:task"} {
		r := domain.Run{ID: source, AgentID: a.ID, ConversationID: c.ID, Source: source, Status: "completed"}
		if err := s.CreateRun(ctx, r); err != nil {
			t.Fatal(err)
		}
		if _, err := s.AppendWorkerEvent(ctx, r.ID, 1, "message.assistant.completed", map[string]string{"text": "output"}); err != nil {
			t.Fatal(err)
		}
		if err := s.FinishRun(ctx, r.ID, "completed", ""); err != nil {
			t.Fatal(err)
		}
		if events, err := s.EventsAfter(ctx, r.ID, 0); err != nil || len(events) != 1 {
			t.Fatalf("per-run events=%+v err=%v", events, err)
		}
	}
	var projected int
	if err := s.db.QueryRow(`SELECT count(*) FROM agent_timeline WHERE run_id IN ('schedule:job','schedule:job_%')`).Scan(&projected); err != nil || projected != 0 {
		t.Fatalf("scheduled projections=%d err=%v", projected, err)
	}
	// Simulate projections left by the old release. Keep the rows for history
	// recovery, but exclude them from initial, incremental, and max cursors.
	if _, err := s.db.Exec(`INSERT INTO agent_timeline(agent_id,run_id,type,payload,created_at) VALUES(?,'schedule:job','message.user','{}',?)`, a.ID, ts(time.Now())); err != nil {
		t.Fatal(err)
	}
	all, err := s.AgentTimeline(ctx, a.ID, 0, 0, 100)
	if err != nil || len(all) != 15 {
		t.Fatalf("timeline=%+v err=%v", all, err)
	}
	for _, event := range all {
		if event.RunID == "schedule:job" || event.RunID == "schedule:job_%" {
			t.Fatalf("scheduled event leaked: %+v", event)
		}
	}
	max, err := s.MaxAgentTimelineSequence(ctx, a.ID)
	if err != nil || max != all[len(all)-1].Sequence {
		t.Fatalf("max=%d err=%v", max, err)
	}
	incremental, err := s.AgentTimelineAfter(ctx, a.ID, max, 100)
	if err != nil || len(incremental) != 0 {
		t.Fatalf("incremental=%+v err=%v", incremental, err)
	}
}

func TestAgentConversationInitialSelectionIgnoresSchedulesButPreservesExistingMapping(t *testing.T) {
	for _, mapping := range []string{"none", "scheduled", "manual"} {
		t.Run(mapping, func(t *testing.T) {
			ctx := context.Background()
			s := testStore(t)
			a := timelineAgent(t, s, "agent")
			for i, kind := range []string{"manual", "scheduled"} {
				if err := s.CreateConversation(ctx, domain.Conversation{ID: kind, AgentID: a.ID, Kind: kind, CodexThreadID: "thread-" + kind, RoleVersion: 1, CreatedAt: time.Now().Add(time.Duration(i) * time.Hour)}); err != nil {
					t.Fatal(err)
				}
			}
			if mapping != "none" {
				if _, err := s.db.Exec(`INSERT INTO agent_conversations(agent_id,conversation_id) VALUES(?,?)`, a.ID, mapping); err != nil {
					t.Fatal(err)
				}
			}
			want := "manual"
			if mapping == "scheduled" {
				want = "scheduled"
				// This legacy conversation was selected by the prior release and
				// subsequently used for both user and delegated chat messages.
				for _, source := range []string{"manual", "collaboration:task"} {
					if err := s.CreateRun(ctx, domain.Run{ID: "chat-" + source, AgentID: a.ID, ConversationID: "scheduled", Source: source, Prompt: "retain this context", Status: "completed"}); err != nil {
						t.Fatal(err)
					}
				}
			}
			if err := s.CreateRun(ctx, domain.Run{ID: "active-schedule", AgentID: a.ID, ConversationID: "scheduled", Source: "schedule:job", Status: "running"}); err != nil {
				t.Fatal(err)
			}
			got, err := s.EnsureAgentConversation(ctx, a)
			if err != nil || got.ID != want || got.CodexThreadID != "thread-"+want {
				t.Fatalf("conversation=%+v err=%v", got, err)
			}
			// A schedule hosted in a manual context by the prior version must not
			// cause its context to be discarded on the next request.
			if _, err := s.db.Exec(`UPDATE runs SET conversation_id='manual' WHERE id='active-schedule'`); err != nil {
				t.Fatal(err)
			}
			got, err = s.EnsureAgentConversation(ctx, a)
			if err != nil || got.ID != want || got.CodexThreadID != "thread-"+want {
				t.Fatalf("retained conversation=%+v err=%v", got, err)
			}
			if mapping == "scheduled" {
				events, err := s.AgentTimeline(ctx, a.ID, 0, 0, 100)
				if err != nil || len(events) != 2 {
					t.Fatalf("legacy chat events=%+v err=%v", events, err)
				}
				for _, event := range events {
					if event.RunID == "active-schedule" {
						t.Fatalf("schedule leaked into chat: %+v", event)
					}
				}
			}
		})
	}
}

func TestRepeatedScheduleClaimsNeverReuseOrModifyChatContext(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	a := timelineAgent(t, s, "agent")
	chat, err := s.EnsureAgentConversation(ctx, a)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetConversationThread(ctx, chat.ID, "keep-chat-thread"); err != nil {
		t.Fatal(err)
	}
	due := time.Now().Add(-time.Hour)
	sc := domain.Schedule{ID: "job", AgentID: a.ID, Name: "Job", Prompt: "inspect", Kind: "cron", Expression: "0 * * * *", Timezone: "UTC", Enabled: true, NextRunAt: &due, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	if err := s.CreateSchedule(ctx, sc); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		c := domain.Conversation{ID: fmt.Sprintf("schedule-context-%d", i), AgentID: a.ID, Kind: "scheduled", RoleVersion: 1, CreatedAt: time.Now()}
		r := domain.Run{ID: fmt.Sprintf("scheduled-%d", i), AgentID: a.ID, ConversationID: c.ID, Source: "schedule:job", Prompt: sc.Prompt, Status: "queued", ScheduledFor: &due}
		next := due.Add(time.Hour)
		if err := s.ClaimScheduleRun(ctx, sc, c, r, &next, true); err != nil {
			t.Fatal(err)
		}
		if err := s.SetConversationThread(ctx, c.ID, fmt.Sprintf("schedule-thread-%d", i)); err != nil {
			t.Fatal(err)
		}
		if _, err := s.AppendWorkerEvent(ctx, r.ID, 1, "message.assistant.completed", map[string]string{"text": "result"}); err != nil {
			t.Fatal(err)
		}
		if err := s.FinishRun(ctx, r.ID, "completed", ""); err != nil {
			t.Fatal(err)
		}
		got, err := s.EnsureAgentConversation(ctx, a)
		if err != nil || got.ID != chat.ID || got.CodexThreadID != "keep-chat-thread" {
			t.Fatalf("chat=%+v err=%v", got, err)
		}
		due = next
	}
	for _, c := range []domain.Conversation{chat, {ID: "schedule-context-0", AgentID: a.ID, Kind: "scheduled", RoleVersion: 1, CreatedAt: time.Now()}} {
		r := domain.Run{ID: "invalid-reuse", AgentID: a.ID, ConversationID: c.ID, Source: "schedule:job", Status: "queued", ScheduledFor: &due}
		if err := s.ClaimScheduleRun(ctx, sc, c, r, nil, false); err == nil {
			t.Fatal("accepted shared or reused schedule conversation")
		}
	}
	if events, err := s.AgentTimeline(ctx, a.ID, 0, 0, 100); err != nil || len(events) != 0 {
		t.Fatalf("chat timeline=%+v err=%v", events, err)
	}
	if runs, err := s.ScheduleRuns(ctx, sc.ID, "", 100); err != nil || len(runs) != 2 || runs[0].ConversationID == runs[1].ConversationID {
		t.Fatalf("scheduled runs=%+v err=%v", runs, err)
	}
}

func TestScheduledDelegationReceiptsDoNotLeakIntoSenderChat(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	sender := timelineAgent(t, s, "sender")
	target := timelineAgent(t, s, "target")
	c, err := s.EnsureAgentConversation(ctx, sender)
	if err != nil {
		t.Fatal(err)
	}
	r := domain.Run{ID: "scheduled", AgentID: sender.ID, ConversationID: c.ID, Source: "schedule:job", Status: "running"}
	if err := s.CreateRun(ctx, r); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.EnqueueCollaborationTask(ctx, domain.CollaborationTask{SenderAgentID: sender.ID, SenderRunID: r.ID, TargetAgentID: target.ID, IdempotencyKey: "request", Prompt: "inspect"}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := s.SyncCollaborationTimeline(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if events, err := s.AgentTimeline(ctx, sender.ID, 0, 0, 100); err != nil || len(events) != 0 {
		t.Fatalf("sender timeline=%+v err=%v", events, err)
	}
	if events, err := s.AgentTimeline(ctx, target.ID, 0, 0, 100); err != nil || len(events) != 2 {
		t.Fatalf("target timeline=%+v err=%v", events, err)
	}
}
