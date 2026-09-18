package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/common-creation/codexbot/internal/domain"
	"github.com/google/uuid"
)

var (
	ErrCollaborationConflict    = errors.New("collaboration task conflicts with existing state or idempotency key")
	ErrCollaborationQueueFull   = errors.New("target collaboration queue is full")
	ErrInvalidCollaborationTask = errors.New("invalid collaboration task")
)

const MaxPendingCollaborationTasks = 100

func (s *Store) migrateCollaboration() error {
	_, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS collaboration_tasks (
 sequence INTEGER PRIMARY KEY AUTOINCREMENT,
 id TEXT NOT NULL UNIQUE,
 sender_agent_id TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
 sender_run_id TEXT NOT NULL DEFAULT '',
 target_agent_id TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
 idempotency_key TEXT NOT NULL, prompt TEXT NOT NULL,
 mode TEXT NOT NULL CHECK(mode IN ('queue','steer')),
 steer_fallback INTEGER NOT NULL DEFAULT 0,
 delivery_confirmed INTEGER NOT NULL DEFAULT 0,
 status TEXT NOT NULL CHECK(status IN ('queued','dispatching','running','completed','failed','interrupted','unknown','cancelled')),
 run_id TEXT NOT NULL DEFAULT '', result_after_sequence INTEGER NOT NULL DEFAULT 0, created_at TEXT NOT NULL, updated_at TEXT NOT NULL,
 error TEXT NOT NULL DEFAULT '',
 CHECK(sender_agent_id <> target_agent_id),
 UNIQUE(sender_agent_id,idempotency_key)
);
CREATE INDEX IF NOT EXISTS collaboration_target_queue_idx ON collaboration_tasks(target_agent_id,status,sequence);
CREATE INDEX IF NOT EXISTS collaboration_sender_idx ON collaboration_tasks(sender_agent_id,sequence);
CREATE INDEX IF NOT EXISTS collaboration_run_idx ON collaboration_tasks(run_id,status);`)
	if err != nil {
		return err
	}
	for _, column := range []string{"result_after_sequence", "steer_fallback", "delivery_confirmed"} {
		var exists int
		if err = s.db.QueryRow(`SELECT count(*) FROM pragma_table_info('collaboration_tasks') WHERE name=?`, column).Scan(&exists); err != nil {
			return err
		}
		if exists == 0 {
			if _, err = s.db.Exec(`ALTER TABLE collaboration_tasks ADD COLUMN ` + column + ` INTEGER NOT NULL DEFAULT 0`); err != nil {
				return err
			}
		}
	}
	return nil
}

const collaborationColumns = `id,sender_agent_id,sender_run_id,target_agent_id,idempotency_key,prompt,mode,steer_fallback,delivery_confirmed,status,run_id,result_after_sequence,created_at,updated_at,error`

func scanCollaborationTask(row interface{ Scan(...any) error }) (domain.CollaborationTask, error) {
	var task domain.CollaborationTask
	var created, updated string
	err := row.Scan(&task.ID, &task.SenderAgentID, &task.SenderRunID, &task.TargetAgentID, &task.IdempotencyKey, &task.Prompt, &task.Mode, &task.SteerFallback, &task.DeliveryConfirmed, &task.Status, &task.RunID, &task.ResultAfterSequence, &created, &updated, &task.Error)
	if errors.Is(err, sql.ErrNoRows) {
		return task, ErrNotFound
	}
	task.CreatedAt, task.UpdatedAt = parseTS(created), parseTS(updated)
	return task, err
}

// EnqueueCollaborationTask atomically deduplicates submissions and enforces a
// per-target pending limit. Retries return the original task, including after it
// has finished. SenderRunID is provenance, not part of the idempotency payload.
func (s *Store) EnqueueCollaborationTask(ctx context.Context, task domain.CollaborationTask) (domain.CollaborationTask, bool, error) {
	if task.Mode == "" {
		task.Mode = "queue"
	}
	if task.SenderAgentID == "" || task.TargetAgentID == "" || task.SenderAgentID == task.TargetAgentID || strings.TrimSpace(task.Prompt) == "" || len(task.Prompt) > 65536 || strings.TrimSpace(task.IdempotencyKey) == "" || len(task.IdempotencyKey) > 200 || (task.Mode != "queue" && task.Mode != "steer") {
		return domain.CollaborationTask{}, false, ErrInvalidCollaborationTask
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.CollaborationTask{}, false, err
	}
	defer tx.Rollback()
	existing, err := scanCollaborationTask(tx.QueryRowContext(ctx, `SELECT `+collaborationColumns+` FROM collaboration_tasks WHERE sender_agent_id=? AND idempotency_key=?`, task.SenderAgentID, task.IdempotencyKey))
	if err == nil {
		if existing.TargetAgentID != task.TargetAgentID || existing.Prompt != task.Prompt || existing.Mode != task.Mode {
			return domain.CollaborationTask{}, false, ErrCollaborationConflict
		}
		return existing, false, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return domain.CollaborationTask{}, false, err
	}
	var agents int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM agents WHERE id IN (?,?) AND archived=0`, task.SenderAgentID, task.TargetAgentID).Scan(&agents); err != nil {
		return domain.CollaborationTask{}, false, err
	}
	if agents != 2 {
		return domain.CollaborationTask{}, false, ErrNotFound
	}
	if task.SenderRunID != "" {
		var exists int
		if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM runs WHERE id=? AND agent_id=?`, task.SenderRunID, task.SenderAgentID).Scan(&exists); err != nil {
			return domain.CollaborationTask{}, false, err
		}
		if exists == 0 {
			return domain.CollaborationTask{}, false, ErrInvalidCollaborationTask
		}
	}
	var pending int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM collaboration_tasks WHERE target_agent_id=? AND status IN ('queued','dispatching','running')`, task.TargetAgentID).Scan(&pending); err != nil {
		return domain.CollaborationTask{}, false, err
	}
	if pending >= MaxPendingCollaborationTasks {
		return domain.CollaborationTask{}, false, ErrCollaborationQueueFull
	}
	if task.ID == "" {
		task.ID = uuid.NewString()
	}
	task.Status, task.RunID, task.Error = "queued", "", ""
	task.ResultAfterSequence = 0
	task.SteerFallback = false
	task.DeliveryConfirmed = false
	task.CreatedAt, task.UpdatedAt = time.Now().UTC(), time.Now().UTC()
	_, err = tx.ExecContext(ctx, `INSERT INTO collaboration_tasks (`+collaborationColumns+`) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, task.ID, task.SenderAgentID, task.SenderRunID, task.TargetAgentID, task.IdempotencyKey, task.Prompt, task.Mode, task.SteerFallback, task.DeliveryConfirmed, task.Status, task.RunID, task.ResultAfterSequence, ts(task.CreatedAt), ts(task.UpdatedAt), task.Error)
	if err != nil {
		return domain.CollaborationTask{}, false, err
	}
	if err = tx.Commit(); err != nil {
		return domain.CollaborationTask{}, false, err
	}
	return task, true, nil
}

func (s *Store) CollaborationTask(ctx context.Context, id string) (domain.CollaborationTask, error) {
	return scanCollaborationTask(s.db.QueryRowContext(ctx, `SELECT `+collaborationColumns+` FROM collaboration_tasks WHERE id=?`, id))
}

type CollaborationTaskFilter struct {
	// AgentID limits visibility to tasks sent or received by this agent.
	AgentID       string
	SenderAgentID string
	TargetAgentID string
	Status        string
	// BeforeID is the last task ID from a preceding newest-first page.
	BeforeID string
	Limit    int
}

func (s *Store) ListCollaborationTasks(ctx context.Context, filter CollaborationTaskFilter) ([]domain.CollaborationTask, error) {
	if filter.AgentID == "" {
		return nil, ErrInvalidCollaborationTask
	}
	query := `SELECT ` + collaborationColumns + ` FROM collaboration_tasks WHERE (sender_agent_id=? OR target_agent_id=?)`
	args := []any{filter.AgentID, filter.AgentID}
	for _, field := range []struct{ column, value string }{{"sender_agent_id", filter.SenderAgentID}, {"target_agent_id", filter.TargetAgentID}, {"status", filter.Status}} {
		if field.value != "" {
			query += ` AND ` + field.column + `=?`
			args = append(args, field.value)
		}
	}
	if filter.BeforeID != "" {
		query += ` AND sequence < (SELECT sequence FROM collaboration_tasks WHERE id=? AND (sender_agent_id=? OR target_agent_id=?))`
		args = append(args, filter.BeforeID, filter.AgentID, filter.AgentID)
	}
	query += ` ORDER BY sequence DESC LIMIT ?`
	args = append(args, collaborationLimit(filter.Limit))
	return s.queryCollaborationTasks(ctx, query, args...)
}

func collaborationLimit(limit int) int {
	if limit < 1 {
		return 50
	}
	if limit > 101 {
		return 101
	}
	return limit
}

func (s *Store) queryCollaborationTasks(ctx context.Context, query string, args ...any) ([]domain.CollaborationTask, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.CollaborationTask{}
	for rows.Next() {
		task, err := scanCollaborationTask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, task)
	}
	return out, rows.Err()
}

func (s *Store) PendingCollaborationTasks(ctx context.Context, target string, limit int) ([]domain.CollaborationTask, error) {
	return s.queryCollaborationTasks(ctx, `SELECT `+collaborationColumns+` FROM collaboration_tasks WHERE target_agent_id=? AND status='queued' ORDER BY sequence LIMIT ?`, target, collaborationLimit(limit))
}

func (s *Store) CollaborationTaskTargets(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT target_agent_id FROM collaboration_tasks WHERE status='queued' ORDER BY target_agent_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

func (s *Store) PendingCollaborationTaskCount(ctx context.Context, target string) (int, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM collaboration_tasks WHERE target_agent_id=? AND status IN ('queued','dispatching')`, target).Scan(&count)
	return count, err
}

func (s *Store) ClaimCollaborationTask(ctx context.Context, id, runID string) (bool, error) {
	result, err := s.db.ExecContext(ctx, `UPDATE collaboration_tasks SET status='dispatching',run_id=?,updated_at=?,error='' WHERE id=? AND status='queued'`, runID, ts(time.Now()), id)
	return collaborationChanged(result, err)
}

func (s *Store) SetCollaborationTaskOutputCursor(ctx context.Context, id string, after int64) error {
	if after < 0 {
		return ErrInvalidCollaborationTask
	}
	result, err := s.db.ExecContext(ctx, `UPDATE collaboration_tasks SET result_after_sequence=?,updated_at=? WHERE id=? AND status='dispatching'`, after, ts(time.Now()), id)
	changed, err := collaborationChanged(result, err)
	if err != nil {
		return err
	}
	if !changed {
		return ErrCollaborationConflict
	}
	return nil
}

// SetCollaborationTaskState only updates nonterminal deliveries. A return to
// queued is allowed after a definite non-delivery; ambiguous failures must use
// unknown to prevent the scheduler from repeating possible side effects.
func (s *Store) SetCollaborationTaskState(ctx context.Context, id, status, runID, message string) error {
	allowed := ""
	switch status {
	case "queued", "dispatching", "running":
		allowed = `status='dispatching'`
	case "completed", "failed", "interrupted", "unknown":
		allowed = `status IN ('dispatching','running')`
	default:
		return ErrInvalidCollaborationTask
	}
	result, err := s.db.ExecContext(ctx, `UPDATE collaboration_tasks SET status=?,run_id=?,error=?,updated_at=?,result_after_sequence=CASE WHEN ?='queued' THEN 0 ELSE result_after_sequence END,steer_fallback=CASE WHEN ?='queued' AND mode='steer' THEN 1 ELSE steer_fallback END,delivery_confirmed=CASE WHEN ?='running' THEN 1 WHEN ?='queued' THEN 0 ELSE delivery_confirmed END WHERE id=? AND `+allowed, status, runID, message, ts(time.Now()), status, status, status, status, id)
	changed, err := collaborationChanged(result, err)
	if err != nil {
		return err
	}
	if !changed {
		return ErrCollaborationConflict
	}
	return nil
}

func collaborationChanged(result sql.Result, err error) (bool, error) {
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	return n > 0, err
}

func (s *Store) CancelCollaborationTask(ctx context.Context, id string) (bool, error) {
	result, err := s.db.ExecContext(ctx, `UPDATE collaboration_tasks SET status='cancelled',updated_at=? WHERE id=? AND status='queued'`, ts(time.Now()), id)
	return collaborationChanged(result, err)
}

func (s *Store) CancelQueuedCollaborationTasks(ctx context.Context, targetID, reason string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE collaboration_tasks SET status='cancelled',error=?,updated_at=? WHERE target_agent_id=? AND status='queued'`, reason, ts(time.Now()), targetID)
	return err
}

func (s *Store) ReconcileCollaborationTasks(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `UPDATE collaboration_tasks SET
 status=(SELECT status FROM runs WHERE runs.id=collaboration_tasks.run_id),
 error=(SELECT error FROM runs WHERE runs.id=collaboration_tasks.run_id), updated_at=?,
 delivery_confirmed=CASE WHEN (SELECT status FROM runs WHERE runs.id=collaboration_tasks.run_id) IN ('completed','failed','interrupted') THEN 1 ELSE delivery_confirmed END
 WHERE (status='running' OR (status='unknown' AND EXISTS (
 SELECT 1 FROM runs WHERE runs.id=collaboration_tasks.run_id AND runs.source='collaboration:' || collaboration_tasks.id
 ))) AND run_id IN (SELECT id FROM runs WHERE status IN ('completed','failed','interrupted','unknown'))`, ts(time.Now()))
	return err
}

// RecoverCollaborationTasks runs once when the dispatcher starts. A process may
// die after remote acceptance but before persisting it, so an in-flight delivery
// is never replayed automatically. Runs themselves are left untouched.
func (s *Store) RecoverCollaborationTasks(ctx context.Context) error {
	if err := s.ReconcileCollaborationTasks(ctx); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `UPDATE collaboration_tasks SET status='unknown',error='delivery outcome unknown after server restart',updated_at=? WHERE status='dispatching'`, ts(time.Now()))
	return err
}

func (s *Store) EventsAfterLimit(ctx context.Context, runID string, after int64, limit int) ([]domain.Event, error) {
	if after < 0 {
		return nil, fmt.Errorf("event cursor must not be negative")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT run_id,sequence,type,payload,created_at FROM events WHERE run_id=? AND sequence>? ORDER BY sequence LIMIT ?`, runID, after, collaborationLimit(limit))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.Event{}
	for rows.Next() {
		var event domain.Event
		var created string
		if err := rows.Scan(&event.RunID, &event.Sequence, &event.Type, &event.Payload, &created); err != nil {
			return nil, err
		}
		if len(event.Payload) > 256<<10 {
			event.Payload, err = json.Marshal(struct {
				Truncated     bool   `json:"truncated"`
				OriginalBytes int    `json:"originalBytes"`
				Preview       string `json:"preview"`
			}{Truncated: true, OriginalBytes: len(event.Payload), Preview: string(event.Payload[:16<<10])})
			if err != nil {
				return nil, err
			}
		}
		event.CreatedAt = parseTS(created)
		out = append(out, event)
	}
	return out, rows.Err()
}
