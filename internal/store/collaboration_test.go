package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/common-creation/codexbot/internal/domain"
)

func collaborationAgents(t *testing.T, s *Store) {
	t.Helper()
	for _, id := range []string{"sender", "target", "other"} {
		if err := s.CreateAgent(context.Background(), domain.Agent{ID: id, Name: id, RolePrompt: id, RoleVersion: 1, Status: "stopped", ProviderAuthState: "disconnected", CreatedAt: time.Now(), UpdatedAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
	}
}

func collaborationEnqueue(t *testing.T, s *Store, key string) domain.CollaborationTask {
	t.Helper()
	task, created, err := s.EnqueueCollaborationTask(context.Background(), domain.CollaborationTask{SenderAgentID: "sender", TargetAgentID: "target", IdempotencyKey: key, Prompt: "do " + key})
	if err != nil || !created {
		t.Fatalf("enqueue created=%v err=%v", created, err)
	}
	return task
}

func collaborationRun(t *testing.T, s *Store, id string) {
	t.Helper()
	ctx := context.Background()
	if err := s.CreateConversation(ctx, domain.Conversation{ID: "conversation-" + id, AgentID: "target", Kind: "collaboration", RoleVersion: 1, CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateRun(ctx, domain.Run{ID: id, AgentID: "target", ConversationID: "conversation-" + id, Source: "collaboration", Prompt: "work", Status: "queued"}); err != nil {
		t.Fatal(err)
	}
}

func TestCollaborationDurableFIFOAndPagination(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "queue.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	collaborationAgents(t, s)
	first := collaborationEnqueue(t, s, "first")
	second := collaborationEnqueue(t, s, "second")
	third := collaborationEnqueue(t, s, "third")
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.RecoverCollaborationTasks(ctx); err != nil {
		t.Fatal(err)
	}
	queued, err := s.PendingCollaborationTasks(ctx, "target", 10)
	if err != nil || len(queued) != 3 || queued[0].ID != first.ID || queued[1].ID != second.ID || queued[2].ID != third.ID {
		t.Fatalf("durable FIFO=%+v err=%v", queued, err)
	}
	page, err := s.ListCollaborationTasks(ctx, CollaborationTaskFilter{AgentID: "sender", Limit: 2})
	if err != nil || len(page) != 2 || page[0].ID != third.ID || page[1].ID != second.ID {
		t.Fatalf("page=%+v err=%v", page, err)
	}
	page, err = s.ListCollaborationTasks(ctx, CollaborationTaskFilter{AgentID: "target", BeforeID: second.ID, Limit: 2})
	if err != nil || len(page) != 1 || page[0].ID != first.ID {
		t.Fatalf("page 2=%+v err=%v", page, err)
	}
	page, err = s.ListCollaborationTasks(ctx, CollaborationTaskFilter{AgentID: "other", Limit: 10})
	if err != nil || len(page) != 0 {
		t.Fatalf("unrelated visible=%+v err=%v", page, err)
	}
	if _, err = s.ListCollaborationTasks(ctx, CollaborationTaskFilter{}); !errors.Is(err, ErrInvalidCollaborationTask) {
		t.Fatalf("unscoped query err=%v", err)
	}
}

func TestCollaborationIdempotencyAndCapacity(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	collaborationAgents(t, s)
	first := collaborationEnqueue(t, s, "one")
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			task, created, err := s.EnqueueCollaborationTask(ctx, domain.CollaborationTask{SenderAgentID: "sender", TargetAgentID: "target", IdempotencyKey: "one", Prompt: "do one"})
			if err != nil || created || task.ID != first.ID {
				t.Errorf("retry task=%+v created=%v err=%v", task, created, err)
			}
		}()
	}
	wg.Wait()
	_, _, err := s.EnqueueCollaborationTask(ctx, domain.CollaborationTask{SenderAgentID: "sender", TargetAgentID: "target", IdempotencyKey: "one", Prompt: "different"})
	if !errors.Is(err, ErrCollaborationConflict) {
		t.Fatalf("conflict=%v", err)
	}
	_, _, err = s.EnqueueCollaborationTask(ctx, domain.CollaborationTask{SenderAgentID: "sender", TargetAgentID: "sender", IdempotencyKey: "self", Prompt: "loop"})
	if !errors.Is(err, ErrInvalidCollaborationTask) {
		t.Fatalf("self send=%v", err)
	}
	for i := 1; i < MaxPendingCollaborationTasks; i++ {
		collaborationEnqueue(t, s, fmt.Sprint(i))
	}
	_, _, err = s.EnqueueCollaborationTask(ctx, domain.CollaborationTask{SenderAgentID: "sender", TargetAgentID: "target", IdempotencyKey: "overflow", Prompt: "overflow"})
	if !errors.Is(err, ErrCollaborationQueueFull) {
		t.Fatalf("overflow=%v", err)
	}
	if _, created, err := s.EnqueueCollaborationTask(ctx, first); err != nil || created {
		t.Fatalf("retry full queue created=%v err=%v", created, err)
	}
	if changed, err := s.CancelCollaborationTask(ctx, first.ID); err != nil || !changed {
		t.Fatalf("cancel=%v err=%v", changed, err)
	}
	collaborationEnqueue(t, s, "after cancellation")
}

func TestCollaborationClaimCancelAndTerminalReconciliation(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	collaborationAgents(t, s)
	task := collaborationEnqueue(t, s, "task")
	queued := collaborationEnqueue(t, s, "queued")
	collaborationRun(t, s, "run")
	if claimed, err := s.ClaimCollaborationTask(ctx, task.ID, "run"); err != nil || !claimed {
		t.Fatalf("claim=%v err=%v", claimed, err)
	}
	if claimed, err := s.ClaimCollaborationTask(ctx, task.ID, "other-run"); err != nil || claimed {
		t.Fatalf("double claim=%v err=%v", claimed, err)
	}
	if cancelled, err := s.CancelCollaborationTask(ctx, task.ID); err != nil || cancelled {
		t.Fatalf("cancel dispatching=%v err=%v", cancelled, err)
	}
	if err := s.SetCollaborationTaskState(ctx, task.ID, "running", "run", ""); err != nil {
		t.Fatal(err)
	}
	if err := s.CancelQueuedCollaborationTasks(ctx, "target", "stopped"); err != nil {
		t.Fatal(err)
	}
	if got, err := s.CollaborationTask(ctx, queued.ID); err != nil || got.Status != "cancelled" || got.Error != "stopped" {
		t.Fatalf("cancelled queued=%+v err=%v", got, err)
	}
	if err := s.FinishRun(ctx, "run", "completed", ""); err != nil {
		t.Fatal(err)
	}
	if err := s.ReconcileCollaborationTasks(ctx); err != nil {
		t.Fatal(err)
	}
	if got, err := s.CollaborationTask(ctx, task.ID); err != nil || got.Status != "completed" || got.RunID != "run" {
		t.Fatalf("completed=%+v err=%v", got, err)
	}
	if err := s.SetCollaborationTaskState(ctx, task.ID, "unknown", "run", "late error"); !errors.Is(err, ErrCollaborationConflict) {
		t.Fatalf("terminal overwritten err=%v", err)
	}
}

func TestCollaborationRecoveryDoesNotRepeatAmbiguousDelivery(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	collaborationAgents(t, s)
	queued := collaborationEnqueue(t, s, "queued")
	claimed := collaborationEnqueue(t, s, "claimed")
	running := collaborationEnqueue(t, s, "running")
	collaborationRun(t, s, "run")
	if _, err := s.ClaimCollaborationTask(ctx, claimed.ID, "run"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ClaimCollaborationTask(ctx, running.ID, "run"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetCollaborationTaskState(ctx, running.ID, "running", "run", ""); err != nil {
		t.Fatal(err)
	}
	if err := s.RecoverCollaborationTasks(ctx); err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string]string{queued.ID: "queued", claimed.ID: "unknown", running.ID: "running"} {
		if got, err := s.CollaborationTask(ctx, id); err != nil || got.Status != want {
			t.Fatalf("recovery=%+v want=%s err=%v", got, want, err)
		}
	}
	if run, err := s.Run(ctx, "run"); err != nil || run.Status != "queued" {
		t.Fatalf("recovery changed live run=%+v err=%v", run, err)
	}
	if pending, err := s.PendingCollaborationTasks(ctx, "target", 100); err != nil || len(pending) != 1 || pending[0].ID != queued.ID {
		t.Fatalf("replay candidates=%+v err=%v", pending, err)
	}
}

func TestCollaborationEventsPage(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	collaborationAgents(t, s)
	collaborationRun(t, s, "run")
	for i := 1; i <= 105; i++ {
		if _, err := s.db.ExecContext(ctx, `INSERT INTO events(run_id,sequence,type,payload,created_at) VALUES(?,?,?,?,?)`, "run", i, "output", []byte(json.RawMessage(`{"text":"hello"}`)), ts(time.Now())); err != nil {
			t.Fatal(err)
		}
	}
	events, err := s.EventsAfterLimit(ctx, "run", 0, 10000)
	if err != nil || len(events) != 101 || events[0].Sequence != 1 || events[100].Sequence != 101 {
		t.Fatalf("first page len=%d err=%v", len(events), err)
	}
	events, err = s.EventsAfterLimit(ctx, "run", 101, 10)
	if err != nil || len(events) != 4 || events[0].Sequence != 102 || events[3].Sequence != 105 {
		t.Fatalf("second page=%+v err=%v", events, err)
	}
}

func TestCollaborationUnconfirmedSteerIsNotCompletedByTargetRun(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	collaborationAgents(t, s)
	task := collaborationEnqueue(t, s, "racing-steer")
	collaborationRun(t, s, "run")
	if _, err := s.ClaimCollaborationTask(ctx, task.ID, ""); err != nil {
		t.Fatal(err)
	}
	if err := s.SetCollaborationTaskState(ctx, task.ID, "dispatching", "run", ""); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishRun(ctx, "run", "completed", ""); err != nil {
		t.Fatal(err)
	}
	if err := s.ReconcileCollaborationTasks(ctx); err != nil {
		t.Fatal(err)
	}
	if got, err := s.CollaborationTask(ctx, task.ID); err != nil || got.Status != "dispatching" {
		t.Fatalf("unconfirmed delivery=%+v err=%v", got, err)
	}
	if err := s.RecoverCollaborationTasks(ctx); err != nil {
		t.Fatal(err)
	}
	if got, err := s.CollaborationTask(ctx, task.ID); err != nil || got.Status != "unknown" {
		t.Fatalf("recovered unconfirmed delivery=%+v err=%v", got, err)
	}
}

func TestCollaborationOutputCursorIsPersistedAndResetOnFallback(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	collaborationAgents(t, s)
	task := collaborationEnqueue(t, s, "cursor")
	if err := s.SetCollaborationTaskOutputCursor(ctx, task.ID, 42); !errors.Is(err, ErrCollaborationConflict) {
		t.Fatalf("queued cursor err=%v", err)
	}
	if _, err := s.ClaimCollaborationTask(ctx, task.ID, "run"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetCollaborationTaskOutputCursor(ctx, task.ID, -1); !errors.Is(err, ErrInvalidCollaborationTask) {
		t.Fatalf("negative cursor err=%v", err)
	}
	if err := s.SetCollaborationTaskOutputCursor(ctx, task.ID, 42); err != nil {
		t.Fatal(err)
	}
	if got, err := s.CollaborationTask(ctx, task.ID); err != nil || got.ResultAfterSequence != 42 {
		t.Fatalf("persisted cursor=%+v err=%v", got, err)
	}
	if err := s.SetCollaborationTaskState(ctx, task.ID, "queued", "", ""); err != nil {
		t.Fatal(err)
	}
	if got, err := s.CollaborationTask(ctx, task.ID); err != nil || got.ResultAfterSequence != 0 {
		t.Fatalf("fallback cursor=%+v err=%v", got, err)
	}
}

func TestCollaborationSteerFallbackSurvivesReopenAndIdempotentRetry(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "fallback.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	collaborationAgents(t, s)
	request := domain.CollaborationTask{SenderAgentID: "sender", TargetAgentID: "target", IdempotencyKey: "steer-fallback", Prompt: "follow up", Mode: "steer"}
	task, created, err := s.EnqueueCollaborationTask(ctx, request)
	if err != nil || !created || task.SteerFallback {
		t.Fatalf("enqueue=%+v created=%v err=%v", task, created, err)
	}
	if _, err := s.ClaimCollaborationTask(ctx, task.ID, "run"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetCollaborationTaskOutputCursor(ctx, task.ID, 42); err != nil {
		t.Fatal(err)
	}
	if err := s.SetCollaborationTaskState(ctx, task.ID, "queued", "", ""); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	got, created, err := s.EnqueueCollaborationTask(ctx, request)
	if err != nil || created || got.ID != task.ID || !got.SteerFallback || got.Mode != "steer" || got.Status != "queued" || got.ResultAfterSequence != 0 {
		t.Fatalf("reopened retry=%+v created=%v err=%v", got, created, err)
	}
	if _, err := s.ClaimCollaborationTask(ctx, task.ID, "next-run"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetCollaborationTaskState(ctx, task.ID, "running", "next-run", ""); err != nil {
		t.Fatal(err)
	}
	if got, err := s.CollaborationTask(ctx, task.ID); err != nil || !got.SteerFallback {
		t.Fatalf("fallback cleared by running=%+v err=%v", got, err)
	}
}

func TestCollaborationMigrationAddsDeliveryColumns(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "old-collaboration.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	collaborationAgents(t, s)
	task := collaborationEnqueue(t, s, "old-task")
	for _, column := range []string{"result_after_sequence", "steer_fallback", "delivery_confirmed"} {
		if _, err := s.db.Exec(`ALTER TABLE collaboration_tasks DROP COLUMN ` + column); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if got, err := s.CollaborationTask(ctx, task.ID); err != nil || got.SteerFallback || got.ResultAfterSequence != 0 {
		t.Fatalf("migration=%+v err=%v", got, err)
	}
}

func TestCollaborationUnknownDeliveryReconcilesOnlyItsOwnRun(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	collaborationAgents(t, s)
	owner := collaborationEnqueue(t, s, "own-run")
	steer := collaborationEnqueue(t, s, "steer-run")
	collaborationRun(t, s, "run")
	if _, err := s.db.ExecContext(ctx, `UPDATE runs SET source=? WHERE id='run'`, "collaboration:"+owner.ID); err != nil {
		t.Fatal(err)
	}
	for _, task := range []domain.CollaborationTask{owner, steer} {
		if _, err := s.ClaimCollaborationTask(ctx, task.ID, "run"); err != nil {
			t.Fatal(err)
		}
		if err := s.SetCollaborationTaskState(ctx, task.ID, "unknown", "run", "lost response"); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.ReconcileCollaborationTasks(ctx); err != nil {
		t.Fatal(err)
	}
	if got, err := s.CollaborationTask(ctx, owner.ID); err != nil || got.Status != "unknown" {
		t.Fatalf("nonterminal run reconciled=%+v err=%v", got, err)
	}
	if err := s.FinishRun(ctx, "run", "completed", ""); err != nil {
		t.Fatal(err)
	}
	if err := s.ReconcileCollaborationTasks(ctx); err != nil {
		t.Fatal(err)
	}
	if got, err := s.CollaborationTask(ctx, owner.ID); err != nil || got.Status != "completed" {
		t.Fatalf("own run outcome=%+v err=%v", got, err)
	}
	if got, err := s.CollaborationTask(ctx, steer.ID); err != nil || got.Status != "unknown" {
		t.Fatalf("unconfirmed steer outcome=%+v err=%v", got, err)
	}
}

func TestCollaborationDeliveryConfirmationSurvivesExecutionFailure(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	collaborationAgents(t, s)
	confirmed := collaborationEnqueue(t, s, "confirmed")
	unconfirmed := collaborationEnqueue(t, s, "unconfirmed")
	collaborationRun(t, s, "run")
	for _, task := range []domain.CollaborationTask{confirmed, unconfirmed} {
		if _, err := s.ClaimCollaborationTask(ctx, task.ID, "run"); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.SetCollaborationTaskState(ctx, confirmed.ID, "running", "run", ""); err != nil {
		t.Fatal(err)
	}
	if err := s.SetCollaborationTaskState(ctx, unconfirmed.ID, "unknown", "run", "lost response"); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishRun(ctx, "run", "failed", "execution error"); err != nil {
		t.Fatal(err)
	}
	if err := s.ReconcileCollaborationTasks(ctx); err != nil {
		t.Fatal(err)
	}
	if got, err := s.CollaborationTask(ctx, confirmed.ID); err != nil || got.Status != "failed" || !got.DeliveryConfirmed {
		t.Fatalf("confirmed execution failure=%+v err=%v", got, err)
	}
	if got, err := s.CollaborationTask(ctx, unconfirmed.ID); err != nil || got.Status != "unknown" || got.DeliveryConfirmed {
		t.Fatalf("unconfirmed delivery=%+v err=%v", got, err)
	}
}

func TestCollaborationOversizedEventKeepsCursorWithoutChangingStoredOutput(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	collaborationAgents(t, s)
	collaborationRun(t, s, "run")
	payload := []byte(`{"text":"` + strings.Repeat("x", 5<<20) + `"}`)
	if _, err := s.db.ExecContext(ctx, `INSERT INTO events(run_id,sequence,type,payload,created_at) VALUES(?,?,?,?,?)`, "run", 1, "output", payload, ts(time.Now())); err != nil {
		t.Fatal(err)
	}
	events, err := s.EventsAfterLimit(ctx, "run", 0, 10)
	if err != nil || len(events) != 1 || events[0].Sequence != 1 {
		t.Fatalf("events=%d err=%v", len(events), err)
	}
	var compact struct {
		Truncated     bool   `json:"truncated"`
		OriginalBytes int    `json:"originalBytes"`
		Preview       string `json:"preview"`
	}
	if err := json.Unmarshal(events[0].Payload, &compact); err != nil {
		t.Fatal(err)
	}
	if !compact.Truncated || compact.OriginalBytes != len(payload) || len(compact.Preview) != 16<<10 || len(events[0].Payload) > 256<<10 {
		t.Fatalf("compact output truncated=%v bytes=%d preview=%d", compact.Truncated, compact.OriginalBytes, len(compact.Preview))
	}
	var storedBytes int
	if err := s.db.QueryRowContext(ctx, `SELECT length(payload) FROM events WHERE run_id='run' AND sequence=1`).Scan(&storedBytes); err != nil {
		t.Fatal(err)
	}
	if storedBytes != len(payload) {
		t.Fatalf("stored output truncated: %d", storedBytes)
	}
}
