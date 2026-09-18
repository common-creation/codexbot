package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/common-creation/codexbot/internal/domain"
	_ "modernc.org/sqlite"
)

var ErrNotFound = errors.New("not found")

type Store struct{ db *sql.DB }

func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err = db.Exec(`PRAGMA journal_mode=WAL; PRAGMA foreign_keys=ON; PRAGMA busy_timeout=5000;`); err != nil {
		db.Close()
		return nil, err
	}
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate() error {
	const schema = `
CREATE TABLE IF NOT EXISTS admins (
 id INTEGER PRIMARY KEY CHECK (id = 1), username TEXT NOT NULL UNIQUE, password_hash TEXT NOT NULL, created_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS sessions (
 token_hash TEXT PRIMARY KEY, admin_id INTEGER NOT NULL REFERENCES admins(id) ON DELETE CASCADE,
 csrf_token TEXT NOT NULL, expires_at TEXT NOT NULL, created_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS agents (
 id TEXT PRIMARY KEY, name TEXT NOT NULL, role_prompt TEXT NOT NULL, role_version INTEGER NOT NULL DEFAULT 1,
 status TEXT NOT NULL DEFAULT 'stopped', provider_auth_state TEXT NOT NULL DEFAULT 'disconnected',
 archived INTEGER NOT NULL DEFAULT 0, created_at TEXT NOT NULL, updated_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS sidebar_layout (
 id INTEGER PRIMARY KEY CHECK (id = 1), revision INTEGER NOT NULL DEFAULT 0, layout TEXT NOT NULL
);
INSERT OR IGNORE INTO sidebar_layout(id,revision,layout) VALUES(1,0,'{"sections":[],"unsectionedAgentIds":[]}');
CREATE TABLE IF NOT EXISTS shared_auth (
 id INTEGER PRIMARY KEY CHECK (id = 1), state TEXT NOT NULL,
 verified INTEGER NOT NULL DEFAULT 0, pending_agent_id TEXT NOT NULL DEFAULT '',
 pending_login_id TEXT NOT NULL DEFAULT '', pending_since TEXT, verified_at TEXT,
 updated_at TEXT NOT NULL
);
INSERT OR IGNORE INTO shared_auth(id,state,verified,updated_at) VALUES(1,'disconnected',0,'1970-01-01T00:00:00Z');
CREATE TABLE IF NOT EXISTS migrations (
 name TEXT PRIMARY KEY, applied_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS conversations (
 id TEXT PRIMARY KEY, agent_id TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
 codex_thread_id TEXT NOT NULL DEFAULT '', kind TEXT NOT NULL, role_version INTEGER NOT NULL,
 title TEXT NOT NULL DEFAULT '', created_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS runs (
 id TEXT PRIMARY KEY, agent_id TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
 conversation_id TEXT NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
 codex_turn_id TEXT NOT NULL DEFAULT '', source TEXT NOT NULL, prompt TEXT NOT NULL,
 status TEXT NOT NULL, scheduled_for TEXT, started_at TEXT, finished_at TEXT, error TEXT NOT NULL DEFAULT '',
 UNIQUE(source, scheduled_for)
);
CREATE INDEX IF NOT EXISTS runs_agent_status_idx ON runs(agent_id, status);
CREATE UNIQUE INDEX IF NOT EXISTS runs_one_active_per_agent_idx ON runs(agent_id) WHERE status IN ('queued','running');
CREATE TABLE IF NOT EXISTS events (
 run_id TEXT NOT NULL REFERENCES runs(id) ON DELETE CASCADE, sequence INTEGER NOT NULL,
 type TEXT NOT NULL, payload BLOB NOT NULL, created_at TEXT NOT NULL, PRIMARY KEY(run_id, sequence)
);
CREATE TABLE IF NOT EXISTS schedules (
 id TEXT PRIMARY KEY, agent_id TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
 name TEXT NOT NULL, prompt TEXT NOT NULL, kind TEXT NOT NULL, expression TEXT NOT NULL,
 timezone TEXT NOT NULL, enabled INTEGER NOT NULL, next_run_at TEXT, last_run_at TEXT,
 created_at TEXT NOT NULL, updated_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS schedules_due_idx ON schedules(enabled, next_run_at);
CREATE TABLE IF NOT EXISTS desktop_leases (
 agent_id TEXT PRIMARY KEY REFERENCES agents(id) ON DELETE CASCADE,
 holder TEXT NOT NULL, generation INTEGER NOT NULL, expires_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS desktop_continuations (
 agent_id TEXT PRIMARY KEY REFERENCES agents(id) ON DELETE CASCADE,
 conversation_id TEXT NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
 created_at TEXT NOT NULL
);`
	if _, err := s.db.Exec(schema); err != nil {
		return err
	}
	if err := s.migrateSharedAuthVerifiedAt(); err != nil {
		return err
	}
	if err := s.migrateAgentSettings(); err != nil {
		return err
	}
	if err := s.migrateCollaboration(); err != nil {
		return err
	}
	if err := s.migrateAgentTimeline(); err != nil {
		return err
	}
	return s.migrateSharedCodexHome()
}

func (s *Store) migrateAgentSettings() error {
	rows, err := s.db.Query(`PRAGMA table_info(agents)`)
	if err != nil {
		return err
	}
	columns := map[string]bool{}
	for rows.Next() {
		var position, notNull, primaryKey int
		var name, columnType string
		var defaultValue sql.NullString
		if err = rows.Scan(&position, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			rows.Close()
			return err
		}
		columns[name] = true
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err = rows.Close(); err != nil {
		return err
	}
	for _, name := range []string{"model", "effort"} {
		if !columns[name] {
			if _, err = s.db.Exec(`ALTER TABLE agents ADD COLUMN ` + name + ` TEXT NOT NULL DEFAULT ''`); err != nil {
				return err
			}
		}
	}
	if !columns["permission"] {
		tx, err := s.db.Begin()
		if err != nil {
			return err
		}
		defer tx.Rollback()
		if _, err = tx.Exec(`ALTER TABLE agents ADD COLUMN permission TEXT NOT NULL DEFAULT 'auto' CHECK(permission IN ('auto','full-access'))`); err != nil {
			return err
		}
		// Older threads may contain approvals granted for their whole session.
		// Start fresh contexts when switching existing agents to Auto so those
		// cached grants cannot bypass the new permission review.
		if _, err = tx.Exec(`UPDATE agents SET role_version=role_version+1,updated_at=?`, ts(time.Now())); err != nil {
			return err
		}
		return tx.Commit()
	}
	return nil
}

func (s *Store) migrateSharedAuthVerifiedAt() error {
	rows, err := s.db.Query(`PRAGMA table_info(shared_auth)`)
	if err != nil {
		return err
	}
	found := false
	for rows.Next() {
		var position, notNull, primaryKey int
		var name, columnType string
		var defaultValue sql.NullString
		if err = rows.Scan(&position, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			rows.Close()
			return err
		}
		if name == "verified_at" {
			found = true
		}
	}
	if err = rows.Close(); err != nil {
		return err
	}
	if found {
		return nil
	}
	_, err = s.db.Exec(`ALTER TABLE shared_auth ADD COLUMN verified_at TEXT`)
	return err
}

func (s *Store) migrateSharedCodexHome() error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.Exec(`INSERT OR IGNORE INTO migrations(name,applied_at) VALUES('shared-codex-home-v1',?)`, ts(time.Now()))
	if err != nil {
		return err
	}
	applied, _ := res.RowsAffected()
	if applied == 1 {
		var conversations int
		if err = tx.QueryRow(`SELECT count(*) FROM conversations`).Scan(&conversations); err != nil {
			return err
		}
		if conversations > 0 {
			if _, err = tx.Exec(`UPDATE agents SET role_version=role_version+1,updated_at=? WHERE archived=0`, ts(time.Now())); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

func ts(t time.Time) string      { return t.UTC().Format(time.RFC3339Nano) }
func parseTS(v string) time.Time { t, _ := time.Parse(time.RFC3339Nano, v); return t }
func nullTS(v sql.NullString) *time.Time {
	if !v.Valid {
		return nil
	}
	t := parseTS(v.String)
	return &t
}

func (s *Store) AdminCount(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM admins`).Scan(&n)
	return n, err
}
func (s *Store) CreateAdmin(ctx context.Context, username, passwordHash string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO admins(id,username,password_hash,created_at) VALUES(1,?,?,?)`, username, passwordHash, ts(time.Now()))
	return err
}
func (s *Store) AdminPasswordHash(ctx context.Context, username string) (string, error) {
	var h string
	err := s.db.QueryRowContext(ctx, `SELECT password_hash FROM admins WHERE username=?`, username).Scan(&h)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return h, err
}
func (s *Store) CreateSession(ctx context.Context, tokenHash, csrf string, expires time.Time) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO sessions(token_hash,admin_id,csrf_token,expires_at,created_at) VALUES(?,1,?,?,?)`, tokenHash, csrf, ts(expires), ts(time.Now()))
	return err
}
func (s *Store) Session(ctx context.Context, tokenHash string) (string, string, time.Time, error) {
	var username, csrf, exp string
	err := s.db.QueryRowContext(ctx, `SELECT admins.username,sessions.csrf_token,sessions.expires_at FROM sessions JOIN admins ON admins.id=sessions.admin_id WHERE sessions.token_hash=?`, tokenHash).Scan(&username, &csrf, &exp)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", time.Time{}, ErrNotFound
	}
	return username, csrf, parseTS(exp), err
}
func (s *Store) DeleteSession(ctx context.Context, tokenHash string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE token_hash=?`, tokenHash)
	return err
}

func (s *Store) CreateAgent(ctx context.Context, a domain.Agent) error {
	if a.Permission == "" {
		a.Permission = domain.PermissionAuto
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO agents(id,name,role_prompt,role_version,status,provider_auth_state,created_at,updated_at,model,effort,permission) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, a.ID, a.Name, a.RolePrompt, a.RoleVersion, a.Status, a.ProviderAuthState, ts(a.CreatedAt), ts(a.UpdatedAt), a.Model, a.Effort, a.Permission)
	return err
}
func scanAgent(row interface{ Scan(...any) error }) (domain.Agent, error) {
	var a domain.Agent
	var c, u string
	err := row.Scan(&a.ID, &a.Name, &a.RolePrompt, &a.RoleVersion, &a.Status, &a.ProviderAuthState, &c, &u, &a.Model, &a.Effort, &a.Permission)
	a.CreatedAt = parseTS(c)
	a.UpdatedAt = parseTS(u)
	return a, err
}
func (s *Store) Agent(ctx context.Context, id string) (domain.Agent, error) {
	a, err := scanAgent(s.db.QueryRowContext(ctx, `SELECT id,name,role_prompt,role_version,status,provider_auth_state,created_at,updated_at,model,effort,permission FROM agents WHERE id=? AND archived=0`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return a, ErrNotFound
	}
	return a, err
}
func (s *Store) Agents(ctx context.Context) ([]domain.Agent, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,name,role_prompt,role_version,status,provider_auth_state,created_at,updated_at,model,effort,permission FROM agents WHERE archived=0 ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Agent
	for rows.Next() {
		a, e := scanAgent(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, a)
	}
	return out, rows.Err()
}
func (s *Store) UpdateAgent(ctx context.Context, id, name, role, model, effort string, permission domain.PermissionMode) (domain.Agent, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE agents SET name=?,role_prompt=?,role_version=role_version+CASE WHEN role_prompt<>? OR model<>? OR effort<>? OR permission<>? THEN 1 ELSE 0 END,model=?,effort=?,permission=?,updated_at=? WHERE id=? AND archived=0`, name, role, role, model, effort, permission, model, effort, permission, ts(time.Now()), id)
	if err != nil {
		return domain.Agent{}, err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return domain.Agent{}, ErrNotFound
	}
	return s.Agent(ctx, id)
}
func (s *Store) SetAgentStatus(ctx context.Context, id, status string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE agents SET status=?,updated_at=? WHERE id=?`, status, ts(time.Now()), id)
	return err
}
func (s *Store) SharedAuth(ctx context.Context) (domain.SharedAuth, error) {
	var auth domain.SharedAuth
	var verified int
	var pendingSince, verifiedAt sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT state,verified,pending_agent_id,pending_login_id,pending_since,verified_at FROM shared_auth WHERE id=1`).Scan(&auth.State, &verified, &auth.PendingAgentID, &auth.PendingLoginID, &pendingSince, &verifiedAt)
	auth.Verified = verified != 0
	auth.PendingSince = nullTS(pendingSince)
	auth.VerifiedAt = nullTS(verifiedAt)
	return auth, err
}
func (s *Store) SetSharedAuthState(ctx context.Context, state string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := ts(time.Now())
	if _, err = tx.ExecContext(ctx, `UPDATE shared_auth SET state=?,verified=1,pending_agent_id='',pending_login_id='',pending_since=NULL,verified_at=?,updated_at=? WHERE id=1`, state, now, now); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE agents SET provider_auth_state=?,updated_at=? WHERE archived=0`, state, now); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Store) SetSharedAuthPending(ctx context.Context, agentID, loginID string, since time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := ts(time.Now())
	if _, err = tx.ExecContext(ctx, `UPDATE shared_auth SET state='pending',verified=1,pending_agent_id=?,pending_login_id=?,pending_since=?,verified_at=?,updated_at=? WHERE id=1`, agentID, loginID, ts(since), now, now); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE agents SET provider_auth_state='pending',updated_at=? WHERE archived=0`, now); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Store) ArchiveAgent(ctx context.Context, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `UPDATE agents SET archived=1,status='archived',updated_at=? WHERE id=? AND archived=0`, ts(time.Now()), id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	if _, err = tx.ExecContext(ctx, `UPDATE schedules SET enabled=0,next_run_at=NULL,updated_at=? WHERE agent_id=?`, ts(time.Now()), id); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM desktop_continuations WHERE agent_id=?`, id); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM desktop_leases WHERE agent_id=?`, id); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) CreateConversation(ctx context.Context, c domain.Conversation) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO conversations(id,agent_id,codex_thread_id,kind,role_version,title,created_at) VALUES(?,?,?,?,?,?,?)`, c.ID, c.AgentID, c.CodexThreadID, c.Kind, c.RoleVersion, c.Title, ts(c.CreatedAt))
	return err
}
func (s *Store) SetConversationThread(ctx context.Context, id, thread string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE conversations SET codex_thread_id=? WHERE id=?`, thread, id)
	return err
}
func (s *Store) SetConversationTitle(ctx context.Context, id, title string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE conversations SET title=? WHERE id=?`, title, id)
	return err
}
func (s *Store) Conversation(ctx context.Context, id string) (domain.Conversation, error) {
	var c domain.Conversation
	var created string
	err := s.db.QueryRowContext(ctx, `SELECT id,agent_id,codex_thread_id,kind,role_version,title,created_at FROM conversations WHERE id=?`, id).Scan(&c.ID, &c.AgentID, &c.CodexThreadID, &c.Kind, &c.RoleVersion, &c.Title, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return c, ErrNotFound
	}
	c.CreatedAt = parseTS(created)
	return c, err
}
func (s *Store) LatestManualConversation(ctx context.Context, agentID string) (domain.Conversation, error) {
	var c domain.Conversation
	var created string
	err := s.db.QueryRowContext(ctx, `SELECT id,agent_id,codex_thread_id,kind,role_version,title,created_at FROM conversations WHERE agent_id=? AND kind='manual' ORDER BY created_at DESC LIMIT 1`, agentID).Scan(&c.ID, &c.AgentID, &c.CodexThreadID, &c.Kind, &c.RoleVersion, &c.Title, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return c, ErrNotFound
	}
	c.CreatedAt = parseTS(created)
	return c, err
}

func (s *Store) CreateRun(ctx context.Context, r domain.Run) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var sf any
	if r.ScheduledFor != nil {
		sf = ts(*r.ScheduledFor)
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO runs(id,agent_id,conversation_id,codex_turn_id,source,prompt,status,scheduled_for) VALUES(?,?,?,?,?,?,?,?)`, r.ID, r.AgentID, r.ConversationID, r.CodexTurnID, r.Source, r.Prompt, r.Status, sf); err != nil {
		return err
	}
	if err = appendRunInput(ctx, tx, r); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Store) SetRunStarted(ctx context.Context, id, turnID string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE runs SET codex_turn_id=?,status='running',started_at=? WHERE id=?`, turnID, ts(time.Now()), id)
	return err
}
func (s *Store) FinishRun(ctx context.Context, id, status, msg string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := time.Now()
	if _, err = tx.ExecContext(ctx, `UPDATE runs SET status=?,error=?,finished_at=? WHERE id=?`, status, msg, ts(now), id); err != nil {
		return err
	}
	var agentID string
	if err = tx.QueryRowContext(ctx, `SELECT agent_id FROM runs WHERE id=?`, id).Scan(&agentID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	if err = appendRunTerminal(ctx, tx, agentID, id, status, msg, now); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Store) AgentBusy(ctx context.Context, agentID string) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM runs WHERE agent_id=? AND status IN ('queued','running')`, agentID).Scan(&n)
	return n > 0, err
}
func (s *Store) ActiveRun(ctx context.Context, agentID string) (domain.Run, error) {
	var r domain.Run
	var sf, st, ft sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT id,agent_id,conversation_id,codex_turn_id,source,prompt,status,scheduled_for,started_at,finished_at,error FROM runs WHERE agent_id=? AND status IN ('queued','running') ORDER BY COALESCE(started_at,scheduled_for) LIMIT 1`, agentID).Scan(&r.ID, &r.AgentID, &r.ConversationID, &r.CodexTurnID, &r.Source, &r.Prompt, &r.Status, &sf, &st, &ft, &r.Error)
	if errors.Is(err, sql.ErrNoRows) {
		return r, ErrNotFound
	}
	r.ScheduledFor = nullTS(sf)
	r.StartedAt = nullTS(st)
	r.FinishedAt = nullTS(ft)
	return r, err
}
func (s *Store) ActiveRuns(ctx context.Context) ([]domain.Run, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,agent_id,conversation_id,codex_turn_id,source,prompt,status,scheduled_for,started_at,finished_at,error FROM runs WHERE status IN ('queued','running') ORDER BY COALESCE(started_at,scheduled_for)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Run
	for rows.Next() {
		var r domain.Run
		var sf, st, ft sql.NullString
		if err := rows.Scan(&r.ID, &r.AgentID, &r.ConversationID, &r.CodexTurnID, &r.Source, &r.Prompt, &r.Status, &sf, &st, &ft, &r.Error); err != nil {
			return nil, err
		}
		r.ScheduledFor = nullTS(sf)
		r.StartedAt = nullTS(st)
		r.FinishedAt = nullTS(ft)
		out = append(out, r)
	}
	return out, rows.Err()
}
func (s *Store) Run(ctx context.Context, id string) (domain.Run, error) {
	var r domain.Run
	var sf, st, ft sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT id,agent_id,conversation_id,codex_turn_id,source,prompt,status,scheduled_for,started_at,finished_at,error FROM runs WHERE id=?`, id).Scan(&r.ID, &r.AgentID, &r.ConversationID, &r.CodexTurnID, &r.Source, &r.Prompt, &r.Status, &sf, &st, &ft, &r.Error)
	if errors.Is(err, sql.ErrNoRows) {
		return r, ErrNotFound
	}
	r.ScheduledFor = nullTS(sf)
	r.StartedAt = nullTS(st)
	r.FinishedAt = nullTS(ft)
	return r, err
}
func (s *Store) CompletedRunsForAgent(ctx context.Context, agentID string, limit int) ([]domain.Run, error) {
	if limit < 1 || limit > 100 {
		limit = 20
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id,agent_id,conversation_id,codex_turn_id,source,prompt,status,scheduled_for,started_at,finished_at,error FROM (SELECT * FROM runs WHERE agent_id=? AND status NOT IN ('queued','running') ORDER BY COALESCE(started_at,scheduled_for) DESC LIMIT ?) ORDER BY COALESCE(started_at,scheduled_for)`, agentID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Run
	for rows.Next() {
		var r domain.Run
		var sf, st, ft sql.NullString
		if err := rows.Scan(&r.ID, &r.AgentID, &r.ConversationID, &r.CodexTurnID, &r.Source, &r.Prompt, &r.Status, &sf, &st, &ft, &r.Error); err != nil {
			return nil, err
		}
		r.ScheduledFor = nullTS(sf)
		r.StartedAt = nullTS(st)
		r.FinishedAt = nullTS(ft)
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) CompletedRunsForConversation(ctx context.Context, conversationID string, limit int) ([]domain.Run, error) {
	if limit < 1 || limit > 100 {
		limit = 20
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id,agent_id,conversation_id,codex_turn_id,source,prompt,status,scheduled_for,started_at,finished_at,error FROM (SELECT * FROM runs WHERE conversation_id=? AND status NOT IN ('queued','running') ORDER BY COALESCE(started_at,scheduled_for) DESC LIMIT ?) ORDER BY COALESCE(started_at,scheduled_for)`, conversationID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Run
	for rows.Next() {
		var r domain.Run
		var sf, st, ft sql.NullString
		if err := rows.Scan(&r.ID, &r.AgentID, &r.ConversationID, &r.CodexTurnID, &r.Source, &r.Prompt, &r.Status, &sf, &st, &ft, &r.Error); err != nil {
			return nil, err
		}
		r.ScheduledFor = nullTS(sf)
		r.StartedAt = nullTS(st)
		r.FinishedAt = nullTS(ft)
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) AppendEvent(ctx context.Context, runID, typ string, payload any) (domain.Event, error) {
	b, err := json.Marshal(payload)
	if err != nil {
		return domain.Event{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Event{}, err
	}
	defer tx.Rollback()
	var seq int64
	if err = tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(sequence),0)+1 FROM events WHERE run_id=?`, runID).Scan(&seq); err != nil {
		return domain.Event{}, err
	}
	now := time.Now()
	if err = appendStoredEvent(ctx, tx, runID, seq, typ, b, now); err != nil {
		return domain.Event{}, err
	}
	if err = tx.Commit(); err != nil {
		return domain.Event{}, err
	}
	return domain.Event{RunID: runID, Sequence: seq, Type: typ, Payload: b, CreatedAt: now}, nil
}
func (s *Store) EventsAfter(ctx context.Context, runID string, after int64) ([]domain.Event, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT run_id,sequence,type,payload,created_at FROM events WHERE run_id=? AND sequence>? ORDER BY sequence`, runID, after)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Event
	for rows.Next() {
		var e domain.Event
		var created string
		if err := rows.Scan(&e.RunID, &e.Sequence, &e.Type, &e.Payload, &created); err != nil {
			return nil, err
		}
		e.CreatedAt = parseTS(created)
		out = append(out, e)
	}
	return out, rows.Err()
}
func (s *Store) MaxEventSequence(ctx context.Context, runID string) (int64, error) {
	var sequence int64
	err := s.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(sequence),0) FROM events WHERE run_id=?`, runID).Scan(&sequence)
	return sequence, err
}

func (s *Store) CreateSchedule(ctx context.Context, sc domain.Schedule) error {
	var n any
	if sc.NextRunAt != nil {
		n = ts(*sc.NextRunAt)
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO schedules(id,agent_id,name,prompt,kind,expression,timezone,enabled,next_run_at,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, sc.ID, sc.AgentID, sc.Name, sc.Prompt, sc.Kind, sc.Expression, sc.Timezone, sc.Enabled, n, ts(sc.CreatedAt), ts(sc.UpdatedAt))
	return err
}
func scanSchedule(row interface{ Scan(...any) error }) (domain.Schedule, error) {
	var sc domain.Schedule
	var enabled int
	var next, last sql.NullString
	var c, u string
	err := row.Scan(&sc.ID, &sc.AgentID, &sc.Name, &sc.Prompt, &sc.Kind, &sc.Expression, &sc.Timezone, &enabled, &next, &last, &c, &u)
	sc.Enabled = enabled != 0
	sc.NextRunAt = nullTS(next)
	sc.LastRunAt = nullTS(last)
	sc.CreatedAt = parseTS(c)
	sc.UpdatedAt = parseTS(u)
	return sc, err
}
func (s *Store) Schedules(ctx context.Context, agentID string) ([]domain.Schedule, error) {
	q := `SELECT id,agent_id,name,prompt,kind,expression,timezone,enabled,next_run_at,last_run_at,created_at,updated_at FROM schedules`
	args := []any{}
	if agentID != "" {
		q += ` WHERE agent_id=?`
		args = append(args, agentID)
	}
	q += ` ORDER BY created_at`
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Schedule
	for rows.Next() {
		sc, e := scanSchedule(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, sc)
	}
	return out, rows.Err()
}
func (s *Store) Schedule(ctx context.Context, id string) (domain.Schedule, error) {
	sc, err := scanSchedule(s.db.QueryRowContext(ctx, `SELECT id,agent_id,name,prompt,kind,expression,timezone,enabled,next_run_at,last_run_at,created_at,updated_at FROM schedules WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return sc, ErrNotFound
	}
	return sc, err
}
func (s *Store) SetScheduleEnabled(ctx context.Context, id string, enabled bool, next *time.Time) error {
	var value any
	if next != nil {
		value = ts(*next)
	}
	res, err := s.db.ExecContext(ctx, `UPDATE schedules SET enabled=?,next_run_at=?,updated_at=? WHERE id=?`, enabled, value, ts(time.Now()), id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}
func (s *Store) DueSchedules(ctx context.Context, now time.Time) ([]domain.Schedule, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,agent_id,name,prompt,kind,expression,timezone,enabled,next_run_at,last_run_at,created_at,updated_at FROM schedules WHERE enabled=1 AND next_run_at<=? ORDER BY next_run_at`, ts(now))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Schedule
	for rows.Next() {
		sc, e := scanSchedule(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, sc)
	}
	return out, rows.Err()
}
func (s *Store) AdvanceSchedule(ctx context.Context, id string, last time.Time, next *time.Time, enabled bool) error {
	var n any
	if next != nil {
		n = ts(*next)
	}
	_, err := s.db.ExecContext(ctx, `UPDATE schedules SET last_run_at=?,next_run_at=?,enabled=?,updated_at=? WHERE id=?`, ts(last), n, enabled, ts(time.Now()), id)
	return err
}
func (s *Store) ClaimScheduleRun(ctx context.Context, sc domain.Schedule, c domain.Conversation, r domain.Run, next *time.Time, enabled bool) error {
	if r.ScheduledFor == nil {
		return errors.New("scheduled run must have scheduledFor")
	}
	if c.Kind != "scheduled" || c.CodexThreadID != "" || r.Source != "schedule:"+sc.ID {
		return errors.New("scheduled run must use a fresh scheduled conversation and matching source")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var nextValue any
	if next != nil {
		nextValue = ts(*next)
	}
	res, err := tx.ExecContext(ctx, `UPDATE schedules SET last_run_at=?,next_run_at=?,enabled=?,updated_at=? WHERE id=? AND enabled=1 AND next_run_at=?`, ts(*r.ScheduledFor), nextValue, enabled, ts(time.Now()), sc.ID, ts(*r.ScheduledFor))
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		return ErrNotFound
	}
	if c.ID != r.ConversationID || c.AgentID != r.AgentID || sc.AgentID != r.AgentID {
		return errors.New("schedule run conversation does not belong to agent")
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO conversations(id,agent_id,codex_thread_id,kind,role_version,title,created_at) VALUES(?,?,?,?,?,?,?)`, c.ID, c.AgentID, c.CodexThreadID, c.Kind, c.RoleVersion, c.Title, ts(c.CreatedAt)); err != nil {
		return err
	}
	var owner string
	if err = tx.QueryRowContext(ctx, `SELECT agent_id FROM conversations WHERE id=?`, c.ID).Scan(&owner); err != nil {
		return err
	}
	if owner != r.AgentID {
		return errors.New("schedule run conversation does not belong to agent")
	}
	var finished any
	if r.Status != "queued" && r.Status != "running" {
		finished = ts(time.Now())
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO runs(id,agent_id,conversation_id,codex_turn_id,source,prompt,status,scheduled_for,finished_at,error) VALUES(?,?,?,?,?,?,?,?,?,?)`, r.ID, r.AgentID, r.ConversationID, r.CodexTurnID, r.Source, r.Prompt, r.Status, ts(*r.ScheduledFor), finished, r.Error); err != nil {
		return err
	}
	if err = appendRunInput(ctx, tx, r); err != nil {
		return err
	}
	if r.Status != "queued" && r.Status != "running" {
		if err = appendRunTerminal(ctx, tx, r.AgentID, r.ID, r.Status, r.Error, time.Now()); err != nil {
			return err
		}
	}
	return tx.Commit()
}
func (s *Store) DeleteSchedule(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM schedules WHERE id=?`, id)
	return err
}

func (s *Store) AcquireDesktopLease(ctx context.Context, agentID, holder string, ttl time.Duration) (domain.DesktopLease, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.DesktopLease{}, err
	}
	defer tx.Rollback()
	var gen int64
	_ = tx.QueryRowContext(ctx, `SELECT generation FROM desktop_leases WHERE agent_id=?`, agentID).Scan(&gen)
	gen++
	exp := time.Now().Add(ttl)
	_, err = tx.ExecContext(ctx, `INSERT INTO desktop_leases(agent_id,holder,generation,expires_at) VALUES(?,?,?,?) ON CONFLICT(agent_id) DO UPDATE SET holder=excluded.holder,generation=excluded.generation,expires_at=excluded.expires_at`, agentID, holder, gen, ts(exp))
	if err != nil {
		return domain.DesktopLease{}, err
	}
	if err = tx.Commit(); err != nil {
		return domain.DesktopLease{}, err
	}
	return domain.DesktopLease{AgentID: agentID, Holder: holder, Generation: gen, ExpiresAt: exp}, nil
}
func (s *Store) HumanControlsDesktop(ctx context.Context, agentID string, now time.Time) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM desktop_leases WHERE agent_id=? AND holder='human' AND expires_at>?`, agentID, ts(now)).Scan(&n)
	return n > 0, err
}
func (s *Store) ExpiredHumanDesktopLeases(ctx context.Context, now time.Time) ([]domain.DesktopLease, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT agent_id,holder,generation,expires_at FROM desktop_leases WHERE holder='human' AND expires_at<=?`, ts(now))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.DesktopLease
	for rows.Next() {
		var lease domain.DesktopLease
		var expires string
		if err := rows.Scan(&lease.AgentID, &lease.Holder, &lease.Generation, &expires); err != nil {
			return nil, err
		}
		lease.ExpiresAt = parseTS(expires)
		out = append(out, lease)
	}
	return out, rows.Err()
}
func (s *Store) RenewHumanDesktopLease(ctx context.Context, agentID string, ttl time.Duration) (domain.DesktopLease, error) {
	expires := time.Now().Add(ttl)
	res, err := s.db.ExecContext(ctx, `UPDATE desktop_leases SET expires_at=? WHERE agent_id=? AND holder='human' AND expires_at>?`, ts(expires), agentID, ts(time.Now()))
	if err != nil {
		return domain.DesktopLease{}, err
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		return domain.DesktopLease{}, ErrNotFound
	}
	var lease domain.DesktopLease
	var raw string
	err = s.db.QueryRowContext(ctx, `SELECT agent_id,holder,generation,expires_at FROM desktop_leases WHERE agent_id=?`, agentID).Scan(&lease.AgentID, &lease.Holder, &lease.Generation, &raw)
	lease.ExpiresAt = parseTS(raw)
	return lease, err
}

func (s *Store) SetDesktopContinuation(ctx context.Context, agentID, conversationID string) error {
	if conversationID == "" {
		_, err := s.db.ExecContext(ctx, `DELETE FROM desktop_continuations WHERE agent_id=?`, agentID)
		return err
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO desktop_continuations(agent_id,conversation_id,created_at) VALUES(?,?,?) ON CONFLICT(agent_id) DO UPDATE SET conversation_id=excluded.conversation_id,created_at=excluded.created_at`, agentID, conversationID, ts(time.Now()))
	return err
}

func (s *Store) TakeDesktopContinuation(ctx context.Context, agentID string) (string, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	var conversationID string
	err = tx.QueryRowContext(ctx, `SELECT conversation_id FROM desktop_continuations WHERE agent_id=?`, agentID).Scan(&conversationID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", tx.Commit()
	}
	if err != nil {
		return "", err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM desktop_continuations WHERE agent_id=?`, agentID); err != nil {
		return "", err
	}
	return conversationID, tx.Commit()
}

func (s *Store) Health(ctx context.Context) error {
	if err := s.db.PingContext(ctx); err != nil {
		return fmt.Errorf("sqlite: %w", err)
	}
	return nil
}
