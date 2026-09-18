package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/common-creation/codexbot/internal/domain"
)

func TestNewChatContextPersistsWithoutDeletingHistory(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "reset.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { s.Close() }()
	a := timelineAgent(t, s, "agent")
	old, err := s.EnsureAgentConversation(ctx, a)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.SetConversationThread(ctx, old.ID, "old-thread"); err != nil {
		t.Fatal(err)
	}
	run := domain.Run{ID: "prior", AgentID: a.ID, ConversationID: old.ID, Source: "manual", Prompt: "old context", Status: "running"}
	if err = s.CreateRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	if _, err = s.ResetAgentConversation(ctx, a.ID); !errors.Is(err, ErrConversationBusy) {
		t.Fatalf("active reset %v", err)
	}
	if err = s.FinishRun(ctx, run.ID, "completed", ""); err != nil {
		t.Fatal(err)
	}
	if _, err = s.AcquireDesktopLease(ctx, a.ID, "human", time.Minute); err != nil {
		t.Fatal(err)
	}
	if _, err = s.ResetAgentConversation(ctx, a.ID); !errors.Is(err, ErrConversationBusy) {
		t.Fatalf("human reset %v", err)
	}
	if _, err = s.AcquireDesktopLease(ctx, a.ID, "agent", time.Minute); err != nil {
		t.Fatal(err)
	}
	if err = s.SetDesktopContinuation(ctx, a.ID, old.ID); err != nil {
		t.Fatal(err)
	}
	fresh, err := s.ResetAgentConversation(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if fresh.ID == old.ID || fresh.CodexThreadID != "" {
		t.Fatalf("not fresh %+v", fresh)
	}
	if continuation, err := s.TakeDesktopContinuation(ctx, a.ID); err != nil || continuation != "" {
		t.Fatalf("old continuation %q %v", continuation, err)
	}
	if preserved, err := s.Conversation(ctx, old.ID); err != nil || preserved.CodexThreadID != "old-thread" {
		t.Fatalf("history removed %+v %v", preserved, err)
	}
	s.Close()
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	current, err := s.EnsureAgentConversation(ctx, a)
	if err != nil || current.ID != fresh.ID || current.CodexThreadID != "" {
		t.Fatalf("reset lost on restart %+v %v", current, err)
	}
	events, err := s.AgentTimelineAfter(ctx, a.ID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 3 || events[0].Type != "message.user" || events[2].Type != "conversation.reset" {
		t.Fatalf("timeline %+v", events)
	}
}
