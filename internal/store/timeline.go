package store

import (
	"bytes"
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

// AppendWorkerEvent preserves the source outbox sequence, including gaps. A
// retry at an already-persisted sequence returns that event without producing a
// second timeline entry. Both stores commit together before the relay advances.
func (s *Store) AppendWorkerEvent(ctx context.Context, runID string, sequence int64, typ string, payload any) (domain.Event, error) {
	if sequence < 1 {
		return domain.Event{}, fmt.Errorf("worker event sequence must be positive")
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return domain.Event{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Event{}, err
	}
	defer tx.Rollback()
	var existing domain.Event
	var created string
	err = tx.QueryRowContext(ctx, `SELECT run_id,sequence,type,payload,created_at FROM events WHERE run_id=? AND sequence=?`, runID, sequence).Scan(&existing.RunID, &existing.Sequence, &existing.Type, &existing.Payload, &created)
	if err == nil {
		if existing.Type != typ || !bytes.Equal(existing.Payload, b) {
			return domain.Event{}, fmt.Errorf("worker event sequence reused with different content")
		}
		existing.CreatedAt = parseTS(created)
		return existing, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return domain.Event{}, err
	}
	now := time.Now()
	if err = appendStoredEvent(ctx, tx, runID, sequence, typ, b, now); err != nil {
		return domain.Event{}, err
	}
	return domain.Event{RunID: runID, Sequence: sequence, Type: typ, Payload: b, CreatedAt: now}, tx.Commit()
}

func appendStoredEvent(ctx context.Context, tx *sql.Tx, runID string, sequence int64, typ string, payload []byte, created time.Time) error {
	if _, err := tx.ExecContext(ctx, `INSERT INTO events(run_id,sequence,type,payload,created_at) VALUES(?,?,?,?,?)`, runID, sequence, typ, payload, ts(created)); err != nil {
		return err
	}
	var agentID string
	if err := tx.QueryRowContext(ctx, `SELECT agent_id FROM runs WHERE id=?`, runID).Scan(&agentID); err != nil {
		return err
	}
	_, err := appendTimelineEvent(ctx, tx, agentID, runID, typ, payload, created, fmt.Sprintf("run:%s:%d", runID, sequence), sequence)
	return err
}

func (s *Store) migrateAgentTimeline() error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`CREATE TABLE IF NOT EXISTS agent_conversations (
 agent_id TEXT PRIMARY KEY REFERENCES agents(id) ON DELETE CASCADE,
 conversation_id TEXT NOT NULL UNIQUE REFERENCES conversations(id) ON DELETE CASCADE
);
CREATE TABLE IF NOT EXISTS agent_timeline (
 sequence INTEGER PRIMARY KEY AUTOINCREMENT,
 agent_id TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
 run_id TEXT NOT NULL DEFAULT '', type TEXT NOT NULL, payload BLOB NOT NULL,
 created_at TEXT NOT NULL, dedup_key TEXT, source_sequence INTEGER,
 UNIQUE(agent_id,dedup_key), UNIQUE(run_id,source_sequence)
);
CREATE INDEX IF NOT EXISTS agent_timeline_agent_sequence_idx ON agent_timeline(agent_id,sequence);
CREATE INDEX IF NOT EXISTS agent_timeline_run_idx ON agent_timeline(run_id);`); err != nil {
		return err
	}
	res, err := tx.Exec(`INSERT OR IGNORE INTO migrations(name,applied_at) VALUES('agent-timeline-v1',?)`, ts(time.Now()))
	if err != nil {
		return err
	}
	applied, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if applied == 1 {
		// Preserve historical chat conversations. Scheduled runs remain in their
		// own execution history. Timeline order uses the
		// earliest known input time and always places each input before its output.
		_, err = tx.Exec(`INSERT INTO agent_timeline(agent_id,run_id,type,payload,created_at,dedup_key,source_sequence)
SELECT agent_id,run_id,type,payload,created_at,dedup_key,source_sequence FROM (
 SELECT r.agent_id,r.id AS run_id,CASE WHEN r.status='skipped_overlap' THEN 'warning' ELSE 'message.user' END AS type,
 CAST(CASE WHEN r.status='skipped_overlap' THEN json_object('id','user-' || r.id,'message','Scheduled task skipped because the agent remained busy: ' || r.prompt,'source',r.source)
 ELSE json_object('id','user-' || r.id,'text',r.prompt,'source',r.source) END AS BLOB) AS payload,
 COALESCE((SELECT MIN(e.created_at) FROM events e WHERE e.run_id=r.id),r.started_at,r.scheduled_for,r.finished_at,c.created_at) AS created_at,
 'input:' || r.id AS dedup_key,NULL AS source_sequence,0 AS input_order
 FROM runs r JOIN conversations c ON c.id=r.conversation_id WHERE substr(r.source,1,9)<>'schedule:'
 UNION ALL
 SELECT r.agent_id,e.run_id,e.type,e.payload,e.created_at,
 'run:' || e.run_id || ':' || e.sequence,e.sequence,1
 FROM events e JOIN runs r ON r.id=e.run_id WHERE substr(r.source,1,9)<>'schedule:'
 UNION ALL
 SELECT r.agent_id,r.id,'run.status',
 CAST(json_object('id','run-status-' || r.id,'status',r.status,'error',r.error) AS BLOB),
 COALESCE(r.finished_at,(SELECT MAX(e.created_at) FROM events e WHERE e.run_id=r.id),r.started_at,r.scheduled_for,c.created_at),
 'run-terminal:' || r.id || ':' || r.status,NULL,2
 FROM runs r JOIN conversations c ON c.id=r.conversation_id WHERE r.status NOT IN ('queued','running') AND substr(r.source,1,9)<>'schedule:'
) ORDER BY created_at,run_id,input_order,source_sequence`)
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}

// EnsureAgentConversation selects the current context until an explicit New chat
// reset. Legacy contexts remain available through the agent timeline. Updating
// an agent's instructions does not discard the chosen thread and its context.
func (s *Store) EnsureAgentConversation(ctx context.Context, a domain.Agent) (domain.Conversation, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Conversation{}, err
	}
	defer tx.Rollback()
	var version int
	if err = tx.QueryRowContext(ctx, `SELECT role_version FROM agents WHERE id=? AND archived=0`, a.ID).Scan(&version); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			err = ErrNotFound
		}
		return domain.Conversation{}, err
	}
	var id string
	// A previously selected context may have a legacy scheduled kind and then
	// have been used for chat. Preserve every existing mapping until the user
	// explicitly requests New chat; only initial selection excludes schedules.
	err = tx.QueryRowContext(ctx, `SELECT conversation_id FROM agent_conversations WHERE agent_id=?`, a.ID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		err = tx.QueryRowContext(ctx, `SELECT c.id FROM conversations c WHERE c.agent_id=? AND c.kind IN ('manual','agent','collaboration') ORDER BY
 CASE WHEN EXISTS(SELECT 1 FROM runs r WHERE r.conversation_id=c.id AND r.agent_id=c.agent_id AND r.status IN ('queued','running') AND substr(r.source,1,9)<>'schedule:') THEN 0 WHEN c.kind='manual' THEN 1 ELSE 2 END,
 c.created_at DESC,c.rowid DESC LIMIT 1`, a.ID).Scan(&id)
		if errors.Is(err, sql.ErrNoRows) {
			id = uuid.NewString()
			_, err = tx.ExecContext(ctx, `INSERT INTO conversations(id,agent_id,kind,role_version,created_at) VALUES(?,?,'agent',?,?)`, id, a.ID, version, ts(time.Now()))
		}
		if err != nil {
			return domain.Conversation{}, err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO agent_conversations(agent_id,conversation_id) VALUES(?,?)`, a.ID, id); err != nil {
			return domain.Conversation{}, err
		}
	} else if err != nil {
		return domain.Conversation{}, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE conversations SET role_version=? WHERE id=? AND agent_id=?`, version, id, a.ID); err != nil {
		return domain.Conversation{}, err
	}
	var c domain.Conversation
	var created string
	err = tx.QueryRowContext(ctx, `SELECT id,agent_id,codex_thread_id,kind,role_version,title,created_at FROM conversations WHERE id=? AND agent_id=?`, id, a.ID).Scan(&c.ID, &c.AgentID, &c.CodexThreadID, &c.Kind, &c.RoleVersion, &c.Title, &created)
	if err != nil {
		return domain.Conversation{}, err
	}
	c.CreatedAt = parseTS(created)
	return c, tx.Commit()
}

// AppendTimelineEvent records non-worker inputs such as queued messages and
// delegation state. A nonempty dedupKey makes retries return the original event.
func (s *Store) AppendTimelineEvent(ctx context.Context, agentID, runID, typ string, payload any, dedupKey string) (domain.Event, error) {
	b, err := json.Marshal(payload)
	if err != nil {
		return domain.Event{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Event{}, err
	}
	defer tx.Rollback()
	if runID != "" {
		var belongs bool
		if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM runs WHERE id=? AND agent_id=?)`, runID, agentID).Scan(&belongs); err != nil {
			return domain.Event{}, err
		}
		if !belongs {
			return domain.Event{}, ErrNotFound
		}
	}
	event, err := appendTimelineEvent(ctx, tx, agentID, runID, typ, b, time.Now(), dedupKey, nil)
	if err != nil {
		return domain.Event{}, err
	}
	return event, tx.Commit()
}

func appendTimelineEvent(ctx context.Context, tx *sql.Tx, agentID, runID, typ string, payload []byte, created time.Time, dedupKey string, sourceSequence any) (domain.Event, error) {
	if runID != "" {
		var scheduled bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM runs WHERE id=? AND substr(source,1,9)='schedule:')`, runID).Scan(&scheduled); err != nil {
			return domain.Event{}, err
		}
		if scheduled {
			return domain.Event{}, nil
		}
	}
	var key any
	if dedupKey != "" {
		key = dedupKey
	}
	res, err := tx.ExecContext(ctx, `INSERT INTO agent_timeline(agent_id,run_id,type,payload,created_at,dedup_key,source_sequence) VALUES(?,?,?,?,?,?,?) ON CONFLICT(agent_id,dedup_key) DO NOTHING`, agentID, runID, typ, payload, ts(created), key, sourceSequence)
	if err != nil {
		return domain.Event{}, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return domain.Event{}, err
	}
	if n == 0 {
		var event domain.Event
		var rawTime string
		err = tx.QueryRowContext(ctx, `SELECT run_id,sequence,type,payload,created_at FROM agent_timeline WHERE agent_id=? AND dedup_key=?`, agentID, dedupKey).Scan(&event.RunID, &event.Sequence, &event.Type, &event.Payload, &rawTime)
		event.CreatedAt = parseTS(rawTime)
		return event, err
	}
	seq, err := res.LastInsertId()
	return domain.Event{RunID: runID, Sequence: seq, Type: typ, Payload: payload, CreatedAt: created}, err
}

func appendRunInput(ctx context.Context, tx *sql.Tx, run domain.Run) error {
	input := map[string]any{"id": "user-" + run.ID, "text": run.Prompt, "source": run.Source}
	typ := "message.user"
	if run.Status == "skipped_overlap" {
		typ = "warning"
		input["message"] = "Scheduled task skipped because the agent remained busy: " + run.Prompt
		delete(input, "text")
	}
	if taskID, ok := strings.CutPrefix(run.Source, "collaboration:"); ok {
		input["taskId"] = taskID
		var sender string
		err := tx.QueryRowContext(ctx, `SELECT sender_agent_id FROM collaboration_tasks WHERE id=? AND target_agent_id=?`, taskID, run.AgentID).Scan(&sender)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if err == nil {
			input["senderAgentId"] = sender
		}
	}
	payload, err := json.Marshal(input)
	if err != nil {
		return err
	}
	_, err = appendTimelineEvent(ctx, tx, run.AgentID, run.ID, typ, payload, time.Now(), "input:"+run.ID, nil)
	return err
}

func appendRunTerminal(ctx context.Context, tx *sql.Tx, agentID, runID, status, message string, created time.Time) error {
	payload, err := json.Marshal(map[string]string{"id": "run-status-" + runID, "status": status, "error": message})
	if err != nil {
		return err
	}
	_, err = appendTimelineEvent(ctx, tx, agentID, runID, "run.status", payload, created, "run-terminal:"+runID+":"+status, nil)
	return err
}

// AgentTimeline returns ascending events. With after > 0 it returns the next
// page; otherwise it returns the latest page, optionally before a prior cursor.
func (s *Store) AgentTimeline(ctx context.Context, agentID string, after, before int64, limit int) ([]domain.Event, error) {
	return s.agentTimeline(ctx, agentID, after, before, limit, after > 0)
}

// AgentTimelineAfter requests incremental delivery, including from cursor zero.
// This differs from AgentTimeline's initial latest-page semantics when empty.
func (s *Store) AgentTimelineAfter(ctx context.Context, agentID string, after int64, limit int) ([]domain.Event, error) {
	return s.agentTimeline(ctx, agentID, after, 0, limit, true)
}

func (s *Store) agentTimeline(ctx context.Context, agentID string, after, before int64, limit int, incremental bool) ([]domain.Event, error) {
	if after < 0 || before < 0 || (after > 0 && before > 0) {
		return nil, fmt.Errorf("invalid timeline cursor")
	}
	if limit < 1 {
		limit = 500
	}
	if limit > 2001 {
		limit = 2001
	}
	query := `SELECT run_id,sequence,type,payload,created_at FROM agent_timeline WHERE agent_id=? AND NOT EXISTS (SELECT 1 FROM runs r WHERE r.id=agent_timeline.run_id AND substr(r.source,1,9)='schedule:')`
	args := []any{agentID}
	if incremental {
		query += ` AND sequence>?`
		args = append(args, after)
	}
	if before > 0 {
		query += ` AND sequence<?`
		args = append(args, before)
	}
	if incremental {
		query += ` ORDER BY sequence ASC LIMIT ?`
	} else {
		query = `SELECT * FROM (` + query + ` ORDER BY sequence DESC LIMIT ?) ORDER BY sequence ASC`
	}
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	events := []domain.Event{}
	for rows.Next() {
		var event domain.Event
		var created string
		if err = rows.Scan(&event.RunID, &event.Sequence, &event.Type, &event.Payload, &created); err != nil {
			return nil, err
		}
		event.CreatedAt = parseTS(created)
		events = append(events, event)
	}
	return events, rows.Err()
}

func (s *Store) MaxAgentTimelineSequence(ctx context.Context, agentID string) (int64, error) {
	var sequence int64
	err := s.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(sequence),0) FROM agent_timeline WHERE agent_id=? AND NOT EXISTS (SELECT 1 FROM runs r WHERE r.id=agent_timeline.run_id AND substr(r.source,1,9)='schedule:')`, agentID).Scan(&sequence)
	return sequence, err
}

// SyncCollaborationTimeline durably projects task receipts and state to both
// participants. It also repairs a process exit between a task mutation and its
// projection. Execution events themselves remain on the executing agent.
func (s *Store) SyncCollaborationTimeline(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, projection := range []struct{ agent, run, typ, key, created string }{
		{"sender_agent_id", "sender_run_id", "collaboration.sent", "'task:' || t.id || ':receipt'", "created_at"},
		{"target_agent_id", "run_id", "collaboration.received", "'task:' || t.id || ':receipt'", "created_at"},
		{"sender_agent_id", "sender_run_id", "collaboration.status", "'task:' || t.id || ':status:' || t.updated_at", "updated_at"},
		{"target_agent_id", "run_id", "collaboration.status", "'task:' || t.id || ':status:' || t.updated_at", "updated_at"},
	} {
		query := `INSERT INTO agent_timeline(agent_id,run_id,type,payload,created_at,dedup_key)
SELECT t.` + projection.agent + `,t.` + projection.run + `,?,
CAST(json_object('id','task-' || t.id,'taskId',t.id,'senderAgentId',t.sender_agent_id,'targetAgentId',t.target_agent_id,'prompt',t.prompt,'status',t.status,'error',t.error,'runId',t.run_id,'mode',t.mode) AS BLOB),
t.` + projection.created + `,` + projection.key + `
FROM collaboration_tasks t WHERE NOT EXISTS(SELECT 1 FROM runs r WHERE r.id=t.` + projection.run + ` AND substr(r.source,1,9)='schedule:') AND NOT EXISTS(SELECT 1 FROM agent_timeline e WHERE e.agent_id=t.` + projection.agent + ` AND e.dedup_key=` + projection.key + `)
ORDER BY t.sequence`
		if _, err = tx.ExecContext(ctx, query, projection.typ); err != nil {
			return err
		}
	}
	return tx.Commit()
}
