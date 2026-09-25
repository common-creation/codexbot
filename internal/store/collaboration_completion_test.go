package store

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/common-creation/codexbot/internal/domain"
)

func completionSenderRun(t *testing.T, s *Store, id, agentID, source string) {
	t.Helper()
	ctx := context.Background()
	kind := "manual"
	if strings.HasPrefix(source, "schedule:") {
		kind = "scheduled"
	}
	if err := s.CreateConversation(ctx, domain.Conversation{ID: "conversation-" + id, AgentID: agentID, Kind: kind, RoleVersion: 1, CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateRun(ctx, domain.Run{ID: id, AgentID: agentID, ConversationID: "conversation-" + id, Source: source, Prompt: "delegate", Status: "completed"}); err != nil {
		t.Fatal(err)
	}
	if id == "sender-run" {
		if err := s.SetRunStarted(ctx, id, "sender-turn"); err != nil {
			t.Fatal(err)
		}
	}
}

func completionEnqueue(t *testing.T, s *Store, key string) domain.CollaborationTask {
	t.Helper()
	task, created, err := s.EnqueueCollaborationTask(context.Background(), domain.CollaborationTask{
		SenderAgentID: "sender", SenderRunID: "sender-run", TargetAgentID: "target",
		IdempotencyKey: key, Prompt: "do " + key, CompletionMode: "notify",
	})
	if err != nil || !created {
		t.Fatalf("enqueue notification task=%+v created=%v err=%v", task, created, err)
	}
	return task
}

func completionSetStatus(t *testing.T, s *Store, taskID, status string) {
	t.Helper()
	if _, err := s.db.Exec(`UPDATE collaboration_tasks SET status=? WHERE id=?`, status, taskID); err != nil {
		t.Fatal(err)
	}
}

func TestCollaborationCompletionModeValidationAndIdempotency(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	collaborationAgents(t, s)
	completionSenderRun(t, s, "sender-run", "sender", "manual")
	completionSenderRun(t, s, "scheduled-run", "sender", "schedule:job")
	completionSenderRun(t, s, "other-run", "other", "manual")
	poll := collaborationEnqueue(t, s, "poll")
	if poll.CompletionMode != "poll" || poll.CompletionTaskID != "" || poll.NotificationForTaskID != "" {
		t.Fatalf("default task=%+v", poll)
	}
	for name, change := range map[string]func(*domain.CollaborationTask){
		"invalid mode":        func(task *domain.CollaborationTask) { task.CompletionMode = "await" },
		"missing run":         func(task *domain.CollaborationTask) { task.SenderRunID = "" },
		"unknown run":         func(task *domain.CollaborationTask) { task.SenderRunID = "missing" },
		"other run":           func(task *domain.CollaborationTask) { task.SenderRunID = "other-run" },
		"scheduled run":       func(task *domain.CollaborationTask) { task.SenderRunID = "scheduled-run" },
		"forged completion":   func(task *domain.CollaborationTask) { task.CompletionTaskID = "forged" },
		"forged notification": func(task *domain.CollaborationTask) { task.NotificationForTaskID = poll.ID },
	} {
		t.Run(name, func(t *testing.T) {
			task := domain.CollaborationTask{SenderAgentID: "sender", SenderRunID: "sender-run", TargetAgentID: "target", IdempotencyKey: name, Prompt: "work", CompletionMode: "notify"}
			change(&task)
			if _, _, err := s.EnqueueCollaborationTask(ctx, task); !errors.Is(err, ErrInvalidCollaborationTask) {
				t.Fatalf("invalid request err=%v", err)
			}
		})
	}
	notify := completionEnqueue(t, s, "notify")
	retry := notify
	retry.SenderRunID = ""
	if got, created, err := s.EnqueueCollaborationTask(ctx, retry); err != nil || created || got.ID != notify.ID || got.SenderRunID != "sender-run" {
		t.Fatalf("idle retry=%+v created=%v err=%v", got, created, err)
	}
	for _, task := range []domain.CollaborationTask{poll, notify} {
		if task.CompletionMode == "poll" {
			task.CompletionMode, task.SenderRunID = "notify", "sender-run"
		} else {
			task.CompletionMode = "poll"
		}
		if _, _, err := s.EnqueueCollaborationTask(ctx, task); !errors.Is(err, ErrCollaborationConflict) {
			t.Fatalf("completion mode changed err=%v", err)
		}
	}
	if err := s.FinishRun(ctx, "sender-run", "completed", ""); err != nil {
		t.Fatal(err)
	}
	if got, created, err := s.EnqueueCollaborationTask(ctx, retry); err != nil || created || got.ID != notify.ID {
		t.Fatalf("completed sender retry=%+v created=%v err=%v", got, created, err)
	}
	retry.IdempotencyKey = "new-after-completion"
	retry.SenderRunID = "sender-run"
	if _, _, err := s.EnqueueCollaborationTask(ctx, retry); !errors.Is(err, ErrInvalidCollaborationTask) {
		t.Fatalf("new notification from finished sender err=%v", err)
	}
}

func TestCollaborationCompletionDurableAndConcurrent(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "completions.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	collaborationAgents(t, s)
	completionSenderRun(t, s, "sender-run", "sender", "manual")
	completionSenderRun(t, s, "target-run", "target", "collaboration")
	original := completionEnqueue(t, s, "notify")
	if claimed, err := s.ClaimCollaborationTask(ctx, original.ID, "target-run"); err != nil || !claimed {
		t.Fatalf("claim=%v err=%v", claimed, err)
	}
	if err := s.SetCollaborationTaskState(ctx, original.ID, "completed", "target-run", ""); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	pending, err := s.PendingCollaborationCompletions(ctx, "sender", 10)
	if err != nil || len(pending) != 1 || pending[0].ID != original.ID {
		t.Fatalf("reopened pending=%+v err=%v", pending, err)
	}
	type result struct {
		task    domain.CollaborationTask
		created bool
		err     error
	}
	results := make(chan result, 8)
	var wg sync.WaitGroup
	for i := 0; i < cap(results); i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			task, created, err := s.EnqueueCollaborationCompletion(ctx, original.ID, "Task complete: fetch result.")
			results <- result{task, created, err}
		}()
	}
	wg.Wait()
	close(results)
	var notification domain.CollaborationTask
	createdCount := 0
	for result := range results {
		if result.err != nil {
			t.Fatal(result.err)
		}
		if result.created {
			createdCount++
		}
		if notification.ID != "" && notification.ID != result.task.ID {
			t.Fatalf("duplicate notification: %+v vs %+v", notification, result.task)
		}
		notification = result.task
	}
	if createdCount != 1 || notification.SenderAgentID != "target" || notification.TargetAgentID != "sender" || notification.SenderRunID != "target-run" || notification.Mode != "steer" || notification.CompletionMode != "poll" || notification.NotificationForTaskID != original.ID || notification.Status != "queued" || notification.CompletionTaskID != "" {
		t.Fatalf("notification=%+v created count=%d", notification, createdCount)
	}
	forged := notification
	forged.NotificationForTaskID = ""
	if _, _, err := s.EnqueueCollaborationTask(ctx, forged); !errors.Is(err, ErrCollaborationConflict) {
		t.Fatalf("public replay of internal notification err=%v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	linked, err := s.CollaborationTask(ctx, original.ID)
	if err != nil || linked.CompletionTaskID != notification.ID {
		t.Fatalf("durable link=%+v err=%v", linked, err)
	}
	if got, created, err := s.EnqueueCollaborationCompletion(ctx, original.ID, "Retry with fresh rendering"); err != nil || created || got.ID != notification.ID || got.Prompt != notification.Prompt {
		t.Fatalf("durable retry=%+v created=%v err=%v", got, created, err)
	}
	completionSetStatus(t, s, notification.ID, "completed")
	if pending, err := s.PendingCollaborationCompletions(ctx, "sender", 10); err != nil || len(pending) != 0 {
		t.Fatalf("recursive or duplicate pending=%+v err=%v", pending, err)
	}
	if _, _, err := s.EnqueueCollaborationCompletion(ctx, notification.ID, "recursive"); !errors.Is(err, ErrCollaborationConflict) {
		t.Fatalf("recursive callback err=%v", err)
	}
	if _, err := s.db.Exec(`INSERT INTO collaboration_tasks (`+collaborationColumns+`) SELECT 'forged',sender_agent_id,sender_run_id,target_agent_id,'forged',prompt,mode,completion_mode,completion_task_id,notification_for_task_id,steer_fallback,delivery_confirmed,status,run_id,result_after_sequence,created_at,updated_at,error FROM collaboration_tasks WHERE id=?`, notification.ID); err == nil {
		t.Fatal("unique notification constraint accepted duplicate")
	}
}

func TestCollaborationCompletionTerminalStatusesAndLimits(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	collaborationAgents(t, s)
	completionSenderRun(t, s, "sender-run", "sender", "manual")
	var expected []string
	for _, status := range []string{"queued", "dispatching", "running", "completed", "failed", "interrupted", "unknown", "cancelled"} {
		task := completionEnqueue(t, s, status)
		completionSetStatus(t, s, task.ID, status)
		switch status {
		case "queued", "dispatching", "running":
			if _, _, err := s.EnqueueCollaborationCompletion(ctx, task.ID, "premature"); !errors.Is(err, ErrCollaborationConflict) {
				t.Fatalf("nonterminal %s err=%v", status, err)
			}
		default:
			expected = append(expected, task.ID)
		}
	}
	poll := collaborationEnqueue(t, s, "completed-poll")
	completionSetStatus(t, s, poll.ID, "completed")
	pending, err := s.PendingCollaborationCompletions(ctx, "sender", 2)
	if err != nil || len(pending) != 2 || pending[0].ID != expected[0] || pending[1].ID != expected[1] {
		t.Fatalf("limited FIFO pending=%+v err=%v", pending, err)
	}
	pending, err = s.PendingCollaborationCompletions(ctx, "sender", 10)
	if err != nil || len(pending) != len(expected) {
		t.Fatalf("terminal pending=%+v err=%v", pending, err)
	}
	if other, err := s.PendingCollaborationCompletions(ctx, "other", 10); err != nil || len(other) != 0 {
		t.Fatalf("cross-sender pending=%+v err=%v", other, err)
	}
	targets, err := s.CollaborationTaskTargets(ctx)
	if err != nil || len(targets) != 2 || targets[0] != "sender" || targets[1] != "target" {
		t.Fatalf("dispatch targets omit completion requester=%v err=%v", targets, err)
	}
	for i, task := range pending {
		if task.ID != expected[i] {
			t.Fatalf("completion FIFO=%+v", pending)
		}
		if notification, created, err := s.EnqueueCollaborationCompletion(ctx, task.ID, "Terminal notification"); err != nil || !created || notification.SenderRunID != "" {
			t.Fatalf("%s notification=%+v created=%v err=%v", task.Status, notification, created, err)
		}
	}
	for _, prompt := range []string{"", "   ", strings.Repeat("x", 65537)} {
		if _, _, err := s.EnqueueCollaborationCompletion(ctx, expected[0], prompt); !errors.Is(err, ErrInvalidCollaborationTask) {
			t.Fatalf("invalid prompt bytes=%d err=%v", len(prompt), err)
		}
	}
	if _, _, err := s.EnqueueCollaborationCompletion(ctx, "missing", "result"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing original err=%v", err)
	}
}

func TestCollaborationCompletionQueueCapacityDefersAtomically(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	collaborationAgents(t, s)
	completionSenderRun(t, s, "sender-run", "sender", "manual")
	original := completionEnqueue(t, s, "notify")
	completionSetStatus(t, s, original.ID, "completed")
	var first domain.CollaborationTask
	for i := 0; i < MaxPendingCollaborationTasks; i++ {
		task, _, err := s.EnqueueCollaborationTask(ctx, domain.CollaborationTask{SenderAgentID: "other", TargetAgentID: "sender", IdempotencyKey: fmt.Sprint(i), Prompt: "waiting"})
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			first = task
		}
	}
	if _, created, err := s.EnqueueCollaborationCompletion(ctx, original.ID, "finished"); created || !errors.Is(err, ErrCollaborationQueueFull) {
		t.Fatalf("full queue created=%v err=%v", created, err)
	}
	if pending, err := s.PendingCollaborationCompletions(ctx, "sender", 10); err != nil || len(pending) != 1 {
		t.Fatalf("deferred completion=%+v err=%v", pending, err)
	}
	if deferred, err := s.CollaborationTask(ctx, original.ID); err != nil || deferred.CompletionTaskID != "" {
		t.Fatalf("deferred completion linked=%+v err=%v", deferred, err)
	}
	if _, err := s.CancelCollaborationTask(ctx, first.ID); err != nil {
		t.Fatal(err)
	}
	if notification, created, err := s.EnqueueCollaborationCompletion(ctx, original.ID, "finished"); err != nil || !created || notification.Status != "queued" {
		t.Fatalf("queue released=%+v created=%v err=%v", notification, created, err)
	}
}

func TestCollaborationCompletionArchivedRequesterCancelledButWorkerStillNotifies(t *testing.T) {
	for _, archived := range []string{"sender", "target"} {
		t.Run(archived, func(t *testing.T) {
			ctx := context.Background()
			s := testStore(t)
			collaborationAgents(t, s)
			completionSenderRun(t, s, "sender-run", "sender", "manual")
			original := completionEnqueue(t, s, "notify")
			completionSetStatus(t, s, original.ID, "completed")
			if err := s.ArchiveAgent(ctx, archived); err != nil {
				t.Fatal(err)
			}
			notification, created, err := s.EnqueueCollaborationCompletion(ctx, original.ID, "finished")
			wantStatus := "queued"
			if archived == "sender" {
				wantStatus = "cancelled"
			}
			if err != nil || !created || notification.Status != wantStatus || (archived == "sender" && !strings.Contains(notification.Error, "archived")) {
				t.Fatalf("archived receipt=%+v created=%v err=%v", notification, created, err)
			}
			if pending, err := s.PendingCollaborationCompletions(ctx, "sender", 10); err != nil || len(pending) != 0 {
				t.Fatalf("archived blocking pending=%+v err=%v", pending, err)
			}
		})
	}
}

func TestCollaborationCompletionSuppressionIncludesUnfinishedWork(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	collaborationAgents(t, s)
	completionSenderRun(t, s, "sender-run", "sender", "manual")
	var originals []domain.CollaborationTask
	for _, status := range []string{"queued", "running", "completed"} {
		task := completionEnqueue(t, s, status)
		completionSetStatus(t, s, task.ID, status)
		originals = append(originals, task)
	}
	already := completionEnqueue(t, s, "already-enqueued")
	completionSetStatus(t, s, already.ID, "completed")
	existing, _, err := s.EnqueueCollaborationCompletion(ctx, already.ID, "finished")
	if err != nil {
		t.Fatal(err)
	}
	incoming, _, err := s.EnqueueCollaborationTask(ctx, domain.CollaborationTask{SenderAgentID: "other", TargetAgentID: "sender", IdempotencyKey: "incoming", Prompt: "unrelated work"})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := s.SuppressCollaborationCompletions(ctx, "sender", "sender interrupted"); err != nil {
			t.Fatal(err)
		}
	}
	for _, original := range originals {
		stored, err := s.CollaborationTask(ctx, original.ID)
		if err != nil || stored.CompletionTaskID == "" || stored.CompletionMode != "notify" {
			t.Fatalf("suppressed original=%+v err=%v", stored, err)
		}
		notification, err := s.CollaborationTask(ctx, stored.CompletionTaskID)
		if err != nil || notification.Status != "cancelled" || notification.Error != "sender interrupted" || notification.NotificationForTaskID != original.ID {
			t.Fatalf("suppressed receipt=%+v err=%v", notification, err)
		}
		completionSetStatus(t, s, original.ID, "completed")
		if retry, created, err := s.EnqueueCollaborationCompletion(ctx, original.ID, "late completion"); err != nil || created || retry.ID != notification.ID {
			t.Fatalf("late completion=%+v created=%v err=%v", retry, created, err)
		}
	}
	if got, err := s.CollaborationTask(ctx, existing.ID); err != nil || got.Status != "cancelled" || got.Error != "sender interrupted" {
		t.Fatalf("queued callback=%+v err=%v", got, err)
	}
	if got, err := s.CollaborationTask(ctx, incoming.ID); err != nil || got.Status != "queued" {
		t.Fatalf("unrelated incoming task changed=%+v err=%v", got, err)
	}
	if pending, err := s.PendingCollaborationCompletions(ctx, "sender", 10); err != nil || len(pending) != 0 {
		t.Fatalf("suppressed pending=%+v err=%v", pending, err)
	}
}

func TestCollaborationCompletionMigrationDefaultsLegacyTasksToPoll(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "legacy.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	collaborationAgents(t, s)
	original := collaborationEnqueue(t, s, "legacy")
	if _, err := s.db.Exec(`DROP INDEX collaboration_notification_idx; DROP INDEX collaboration_completion_pending_idx`); err != nil {
		t.Fatal(err)
	}
	for _, column := range []string{"completion_mode", "completion_task_id", "notification_for_task_id"} {
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
	got, err := s.CollaborationTask(ctx, original.ID)
	if err != nil || got.CompletionMode != "poll" || got.CompletionTaskID != "" || got.NotificationForTaskID != "" || got.Prompt != original.Prompt {
		t.Fatalf("migrated task=%+v err=%v", got, err)
	}
	if pending, err := s.PendingCollaborationCompletions(ctx, "sender", 10); err != nil || len(pending) != 0 {
		t.Fatalf("legacy notification=%+v err=%v", pending, err)
	}
}

func TestCollaborationCompletionRejectsActiveScheduledSender(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	collaborationAgents(t, s)
	completionSenderRun(t, s, "sender-run", "sender", "schedule:job")
	if _, _, err := s.EnqueueCollaborationTask(ctx, domain.CollaborationTask{SenderAgentID: "sender", SenderRunID: "sender-run", TargetAgentID: "target", IdempotencyKey: "scheduled", Prompt: "delegate", CompletionMode: "notify"}); !errors.Is(err, ErrInvalidCollaborationTask) {
		t.Fatalf("scheduled sender err=%v", err)
	}
}

func TestCollaborationCompletionReconciliationDoesNotExposeRequesterErrors(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	collaborationAgents(t, s)
	completionSenderRun(t, s, "sender-run", "sender", "manual")
	original := completionEnqueue(t, s, "work")
	completionSetStatus(t, s, original.ID, "completed")
	notification, _, err := s.EnqueueCollaborationCompletion(ctx, original.ID, "work completed")
	if err != nil {
		t.Fatal(err)
	}
	if claimed, err := s.ClaimCollaborationTask(ctx, notification.ID, "sender-run"); err != nil || !claimed {
		t.Fatalf("claim=%v err=%v", claimed, err)
	}
	if err := s.SetCollaborationTaskState(ctx, notification.ID, "running", "sender-run", ""); err != nil {
		t.Fatal(err)
	}
	const privateError = "requester host error: private access token"
	if err := s.FinishRun(ctx, "sender-run", "failed", privateError); err != nil {
		t.Fatal(err)
	}
	if err := s.ReconcileCollaborationTasks(ctx); err != nil {
		t.Fatal(err)
	}
	got, err := s.CollaborationTask(ctx, notification.ID)
	if err != nil || got.Status != "failed" || got.Error != "requester continuation did not complete successfully" || strings.Contains(got.Error, privateError) {
		t.Fatalf("notification leaked requester error=%+v err=%v", got, err)
	}
	if run, err := s.Run(ctx, "sender-run"); err != nil || run.Error != privateError {
		t.Fatalf("requester run lost diagnostic=%+v err=%v", run, err)
	}
}
