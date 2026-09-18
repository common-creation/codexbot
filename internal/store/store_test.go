package store

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/common-creation/codexbot/internal/domain"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestSharedAuthSchemaUpgradeAddsVerifiedAt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old-auth.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`
		CREATE TABLE shared_auth (
		 id INTEGER PRIMARY KEY CHECK (id = 1), state TEXT NOT NULL,
		 verified INTEGER NOT NULL DEFAULT 0, pending_agent_id TEXT NOT NULL DEFAULT '',
		 pending_login_id TEXT NOT NULL DEFAULT '', pending_since TEXT, updated_at TEXT NOT NULL
		);
		INSERT INTO shared_auth(id,state,verified,updated_at) VALUES(1,'connected',1,'2026-09-03T00:00:00Z');
	`)
	if err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	auth, err := s.SharedAuth(context.Background())
	if err != nil || auth.State != "connected" || !auth.Verified {
		t.Fatalf("upgraded auth=%+v err=%v", auth, err)
	}
	if err = s.SetSharedAuthState(context.Background(), "disconnected"); err != nil {
		t.Fatal(err)
	}
}

func TestSharedCodexHomeMigrationStartsFreshConversationsOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "migration.db")
	ctx := context.Background()
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err = s.CreateAgent(ctx, domain.Agent{ID: "a", Name: "A", RolePrompt: "x", RoleVersion: 1, Status: "stopped", ProviderAuthState: "disconnected", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err = s.CreateConversation(ctx, domain.Conversation{ID: "old", AgentID: "a", Kind: "manual", RoleVersion: 1, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.ExecContext(ctx, `DELETE FROM migrations WHERE name='shared-codex-home-v1'`); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	agent, err := s.Agent(ctx, "a")
	if err != nil || agent.RoleVersion != 2 {
		t.Fatalf("first migration agent=%+v err=%v", agent, err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close(); _ = os.Remove(path) })
	agent, err = s.Agent(ctx, "a")
	if err != nil || agent.RoleVersion != 2 {
		t.Fatalf("repeated migration agent=%+v err=%v", agent, err)
	}
}
func TestAgentRoleVersionAndSharedRunLifecycle(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	now := time.Now()
	a := domain.Agent{ID: "a", Name: "A", RolePrompt: "research", RoleVersion: 1, Status: "stopped", ProviderAuthState: "disconnected", CreatedAt: now, UpdatedAt: now}
	if err := s.CreateAgent(ctx, a); err != nil {
		t.Fatal(err)
	}
	updated, err := s.UpdateAgent(ctx, "a", "A2", "build", "", "", domain.PermissionAuto)
	if err != nil {
		t.Fatal(err)
	}
	if updated.RoleVersion != 2 {
		t.Fatalf("role version=%d", updated.RoleVersion)
	}
	updated, err = s.UpdateAgent(ctx, "a", "A3", "build", "", "", domain.PermissionAuto)
	if err != nil {
		t.Fatal(err)
	}
	if updated.RoleVersion != 2 {
		t.Fatalf("name-only update changed role version=%d", updated.RoleVersion)
	}
	c := domain.Conversation{ID: "c", AgentID: "a", Kind: "manual", RoleVersion: 2, CreatedAt: now}
	if err = s.CreateConversation(ctx, c); err != nil {
		t.Fatal(err)
	}
	r := domain.Run{ID: "r", AgentID: "a", ConversationID: "c", Source: "manual", Prompt: "hello", Status: "queued"}
	if err = s.CreateRun(ctx, r); err != nil {
		t.Fatal(err)
	}
	if err = s.SetRunStarted(ctx, "r", "turn"); err != nil {
		t.Fatal(err)
	}
	busy, err := s.AgentBusy(ctx, "a")
	if err != nil || !busy {
		t.Fatal("agent should be busy")
	}
	if _, err = s.AppendEvent(ctx, "r", "message", map[string]string{"text": "ok"}); err != nil {
		t.Fatal(err)
	}
	if err = s.FinishRun(ctx, "r", "completed", ""); err != nil {
		t.Fatal(err)
	}
	events, err := s.EventsAfter(ctx, "r", 0)
	if err != nil || len(events) != 1 || events[0].Sequence != 1 {
		t.Fatalf("events=%+v err=%v", events, err)
	}
}

func TestConversationRunsAreScopedAndArchiveDisablesSchedules(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	now := time.Now()
	agent := domain.Agent{ID: "a", Name: "A", RolePrompt: "x", RoleVersion: 1, Status: "running", ProviderAuthState: "connected", CreatedAt: now, UpdatedAt: now}
	if err := s.CreateAgent(ctx, agent); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"c1", "c2"} {
		if err := s.CreateConversation(ctx, domain.Conversation{ID: id, AgentID: "a", Kind: "manual", RoleVersion: 1, CreatedAt: now}); err != nil {
			t.Fatal(err)
		}
		if err := s.CreateRun(ctx, domain.Run{ID: "r-" + id, AgentID: "a", ConversationID: id, Source: "manual", Prompt: id, Status: "completed"}); err != nil {
			t.Fatal(err)
		}
	}
	runs, err := s.CompletedRunsForConversation(ctx, "c2", 20)
	if err != nil || len(runs) != 1 || runs[0].ConversationID != "c2" {
		t.Fatalf("conversation runs=%+v err=%v", runs, err)
	}
	due := now.Add(-time.Minute)
	if err = s.CreateSchedule(ctx, domain.Schedule{ID: "schedule", AgentID: "a", Name: "job", Prompt: "run", Kind: "cron", Expression: "* * * * *", Timezone: "UTC", Enabled: true, NextRunAt: &due, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.AcquireDesktopLease(ctx, "a", "human", time.Minute); err != nil {
		t.Fatal(err)
	}
	if err = s.SetDesktopContinuation(ctx, "a", "c1"); err != nil {
		t.Fatal(err)
	}
	if err = s.ArchiveAgent(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	dueSchedules, err := s.DueSchedules(ctx, time.Now())
	if err != nil || len(dueSchedules) != 0 {
		t.Fatalf("due schedules after archive=%+v err=%v", dueSchedules, err)
	}
	if _, err = s.Agent(ctx, "a"); err != ErrNotFound {
		t.Fatalf("archived agent error=%v", err)
	}
}
func TestDesktopLeaseFencesOldHolder(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	now := time.Now()
	_ = s.CreateAgent(ctx, domain.Agent{ID: "a", Name: "A", RolePrompt: "x", RoleVersion: 1, Status: "stopped", ProviderAuthState: "disconnected", CreatedAt: now, UpdatedAt: now})
	a, err := s.AcquireDesktopLease(ctx, "a", "agent", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	h, err := s.AcquireDesktopLease(ctx, "a", "human", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if h.Generation <= a.Generation || h.Holder != "human" {
		t.Fatalf("bad lease transition: %+v -> %+v", a, h)
	}
}

func TestProviderAuthStateIsSharedAcrossAgents(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	now := time.Now()
	for _, id := range []string{"a", "b"} {
		if err := s.CreateAgent(ctx, domain.Agent{ID: id, Name: id, RolePrompt: "x", RoleVersion: 1, Status: "stopped", ProviderAuthState: "disconnected", CreatedAt: now, UpdatedAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.SetSharedAuthPending(ctx, "a", "login-1", now); err != nil {
		t.Fatal(err)
	}
	auth, err := s.SharedAuth(ctx)
	if err != nil || auth.State != "pending" || auth.PendingAgentID != "a" || auth.PendingLoginID != "login-1" {
		t.Fatalf("pending auth=%+v err=%v", auth, err)
	}
	if err := s.SetSharedAuthState(ctx, "connected"); err != nil {
		t.Fatal(err)
	}
	auth, err = s.SharedAuth(ctx)
	if err != nil || auth.State != "connected" || !auth.Verified || auth.VerifiedAt == nil || auth.PendingAgentID != "" {
		t.Fatalf("connected auth=%+v err=%v", auth, err)
	}
	for _, id := range []string{"a", "b"} {
		agent, err := s.Agent(ctx, id)
		if err != nil || agent.ProviderAuthState != "connected" {
			t.Fatalf("agent %s auth=%q err=%v", id, agent.ProviderAuthState, err)
		}
	}
}

func TestClaimScheduleRunIsAtomicAndIdempotent(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	now := time.Now()
	agent := domain.Agent{ID: "a", Name: "A", RolePrompt: "x", RoleVersion: 1, Status: "stopped", ProviderAuthState: "disconnected", CreatedAt: now, UpdatedAt: now}
	if err := s.CreateAgent(ctx, agent); err != nil {
		t.Fatal(err)
	}
	due := now.Add(-time.Minute)
	next := now.Add(time.Hour)
	sc := domain.Schedule{ID: "s", AgentID: "a", Name: "job", Prompt: "run", Kind: "cron", Expression: "0 * * * *", Timezone: "UTC", Enabled: true, NextRunAt: &due, CreatedAt: now, UpdatedAt: now}
	if err := s.CreateSchedule(ctx, sc); err != nil {
		t.Fatal(err)
	}
	conversation := domain.Conversation{ID: "c", AgentID: "a", Kind: "scheduled", RoleVersion: 1, Title: "job", CreatedAt: now}
	run := domain.Run{ID: "r", AgentID: "a", ConversationID: "c", Source: "schedule:s", Prompt: "run", Status: "queued", ScheduledFor: &due}
	if err := s.ClaimScheduleRun(ctx, sc, conversation, run, &next, true); err != nil {
		t.Fatal(err)
	}
	stored, err := s.Run(ctx, "r")
	if err != nil || stored.Status != "queued" {
		t.Fatalf("run=%+v err=%v", stored, err)
	}
	advanced, err := s.Schedule(ctx, "s")
	if err != nil || advanced.NextRunAt == nil || !advanced.NextRunAt.Equal(next) {
		t.Fatalf("schedule=%+v err=%v", advanced, err)
	}
	if err = s.ClaimScheduleRun(ctx, sc, domain.Conversation{ID: "c2", AgentID: "a", Kind: "scheduled", RoleVersion: 1, CreatedAt: now}, domain.Run{ID: "r2", AgentID: "a", ConversationID: "c2", Source: "schedule:s", Prompt: "run", Status: "queued", ScheduledFor: &due}, &next, true); err != ErrNotFound {
		t.Fatalf("second claim error=%v", err)
	}
}

func TestHumanLeaseExpiryAndRenewal(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	now := time.Now()
	_ = s.CreateAgent(ctx, domain.Agent{ID: "a", Name: "A", RolePrompt: "x", RoleVersion: 1, Status: "stopped", ProviderAuthState: "disconnected", CreatedAt: now, UpdatedAt: now})
	if _, err := s.AcquireDesktopLease(ctx, "a", "human", -time.Second); err != nil {
		t.Fatal(err)
	}
	expired, err := s.ExpiredHumanDesktopLeases(ctx, time.Now())
	if err != nil || len(expired) != 1 {
		t.Fatalf("expired=%v err=%v", expired, err)
	}
	if _, err = s.AcquireDesktopLease(ctx, "a", "human", time.Minute); err != nil {
		t.Fatal(err)
	}
	renewed, err := s.RenewHumanDesktopLease(ctx, "a", 2*time.Minute)
	if err != nil || renewed.Holder != "human" {
		t.Fatalf("renewed=%+v err=%v", renewed, err)
	}
}

func TestDesktopContinuationOnlyExistsForInterruptedConversation(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	now := time.Now()
	_ = s.CreateAgent(ctx, domain.Agent{ID: "a", Name: "A", RolePrompt: "x", RoleVersion: 1, Status: "running", ProviderAuthState: "disconnected", CreatedAt: now, UpdatedAt: now})
	empty, err := s.TakeDesktopContinuation(ctx, "a")
	if err != nil || empty != "" {
		t.Fatalf("empty continuation=%q err=%v", empty, err)
	}
	conversation := domain.Conversation{ID: "c", AgentID: "a", Kind: "manual", RoleVersion: 1, CreatedAt: now}
	if err = s.CreateConversation(ctx, conversation); err != nil {
		t.Fatal(err)
	}
	if err = s.SetDesktopContinuation(ctx, "a", "c"); err != nil {
		t.Fatal(err)
	}
	got, err := s.TakeDesktopContinuation(ctx, "a")
	if err != nil || got != "c" {
		t.Fatalf("continuation=%q err=%v", got, err)
	}
	got, err = s.TakeDesktopContinuation(ctx, "a")
	if err != nil || got != "" {
		t.Fatalf("continuation was not consumed: %q err=%v", got, err)
	}
}

func TestAgentModelSettingsPersistAndInvalidateConversation(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	a := domain.Agent{ID: "settings", Name: "Agent", RolePrompt: "Role", RoleVersion: 1, Model: "model-a", Effort: "high", Status: "stopped", ProviderAuthState: "disconnected", CreatedAt: time.Now(), UpdatedAt: time.Now()}
	if err := s.CreateAgent(ctx, a); err != nil {
		t.Fatal(err)
	}
	agents, err := s.Agents(ctx)
	if err != nil || len(agents) != 1 || agents[0].Model != a.Model || agents[0].Effort != a.Effort {
		t.Fatalf("agents=%+v err=%v", agents, err)
	}
	updated, err := s.UpdateAgent(ctx, a.ID, "Renamed", a.RolePrompt, a.Model, a.Effort, domain.PermissionAuto)
	if err != nil || updated.Model != a.Model || updated.Effort != a.Effort || updated.RoleVersion != 1 {
		t.Fatalf("updated=%+v err=%v", updated, err)
	}
	updated, err = s.UpdateAgent(ctx, a.ID, "Renamed", a.RolePrompt, "model-b", "low", domain.PermissionAuto)
	if err != nil || updated.Model != "model-b" || updated.Effort != "low" || updated.RoleVersion != 2 {
		t.Fatalf("updated=%+v err=%v", updated, err)
	}
	updated, err = s.UpdateAgent(ctx, a.ID, "Renamed", a.RolePrompt, "", "", domain.PermissionAuto)
	if err != nil || updated.Model != "" || updated.Effort != "" || updated.RoleVersion != 3 {
		t.Fatalf("updated=%+v err=%v", updated, err)
	}
	if err := s.migrate(); err != nil {
		t.Fatal(err)
	}
	updated, err = s.Agent(ctx, a.ID)
	if err != nil || updated.RoleVersion != 3 {
		t.Fatalf("updated=%+v err=%v", updated, err)
	}
}

func TestAgentModelSettingsUpgradePreservesExistingAgent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old-agents.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE agents (id TEXT PRIMARY KEY, name TEXT NOT NULL, role_prompt TEXT NOT NULL, role_version INTEGER NOT NULL DEFAULT 1, status TEXT NOT NULL DEFAULT 'stopped', provider_auth_state TEXT NOT NULL DEFAULT 'disconnected', archived INTEGER NOT NULL DEFAULT 0, created_at TEXT NOT NULL, updated_at TEXT NOT NULL); INSERT INTO agents(id,name,role_prompt,created_at,updated_at) VALUES('old','Old Agent','Role','2026-09-03T00:00:00Z','2026-09-03T00:00:00Z');`)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	a, err := s.Agent(context.Background(), "old")
	if err != nil || a.Name != "Old Agent" || a.Model != "" || a.Effort != "" || a.Permission != domain.PermissionAuto || a.RoleVersion != 2 {
		t.Fatalf("agent=%+v err=%v", a, err)
	}
}

func TestAgentPermissionUpgradeInvalidatesOldThreadOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old-permissions.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	a := domain.Agent{ID: "old", Name: "Old Agent", RolePrompt: "Role", RoleVersion: 4, Status: "stopped", ProviderAuthState: "disconnected", CreatedAt: time.Now(), UpdatedAt: time.Now()}
	if err := s.CreateAgent(ctx, a); err != nil {
		t.Fatal(err)
	}
	c := domain.Conversation{ID: "old-conversation", AgentID: a.ID, Kind: "manual", RoleVersion: 4, CodexThreadID: "old-thread-with-session-approvals", CreatedAt: time.Now()}
	if err := s.CreateConversation(ctx, c); err != nil {
		t.Fatal(err)
	}
	// Reproduce the preceding schema while retaining existing agents and threads.
	if _, err := s.db.Exec(`ALTER TABLE agents DROP COLUMN permission`); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		s, err = Open(path)
		if err != nil {
			t.Fatal(err)
		}
		stored, err := s.Agent(ctx, a.ID)
		if err != nil || stored.Permission != domain.PermissionAuto || stored.RoleVersion != 5 {
			t.Fatalf("open %d agent=%+v err=%v", i+1, stored, err)
		}
		conversation, err := s.Conversation(ctx, c.ID)
		if err != nil || conversation.RoleVersion != 4 || conversation.CodexThreadID != c.CodexThreadID || conversation.RoleVersion == stored.RoleVersion {
			t.Fatalf("open %d conversation=%+v err=%v", i+1, conversation, err)
		}
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestAgentPermissionMigrationRollsBackColumnWhenVersionUpdateFails(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	a := domain.Agent{ID: "old", Name: "Old", RolePrompt: "Role", RoleVersion: 7, Status: "stopped", ProviderAuthState: "disconnected", CreatedAt: time.Now(), UpdatedAt: time.Now()}
	if err := s.CreateAgent(ctx, a); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`ALTER TABLE agents DROP COLUMN permission; CREATE TRIGGER fail_role_version_update BEFORE UPDATE OF role_version ON agents BEGIN SELECT RAISE(ABORT, 'test version update failure'); END;`); err != nil {
		t.Fatal(err)
	}
	if err := s.migrateAgentSettings(); err == nil {
		t.Fatal("expected version update failure")
	}
	var version int
	if err := s.db.QueryRow(`SELECT role_version FROM agents WHERE id='old'`).Scan(&version); err != nil || version != 7 {
		t.Fatalf("version after rollback=%d err=%v", version, err)
	}
	var count int
	if err := s.db.QueryRow(`SELECT count(*) FROM pragma_table_info('agents') WHERE name='permission'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("permission columns after rollback=%d err=%v", count, err)
	}
	if _, err := s.db.Exec(`DROP TRIGGER fail_role_version_update`); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := s.migrateAgentSettings(); err != nil {
			t.Fatal(err)
		}
		stored, err := s.Agent(ctx, a.ID)
		if err != nil || stored.Permission != domain.PermissionAuto || stored.RoleVersion != 8 {
			t.Fatalf("retry %d agent=%+v err=%v", i+1, stored, err)
		}
	}
}

func TestAgentPermissionPersistsAndInvalidatesConversation(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	a := domain.Agent{ID: "permission", Name: "Agent", RolePrompt: "Role", RoleVersion: 1, Status: "stopped", ProviderAuthState: "disconnected", CreatedAt: time.Now(), UpdatedAt: time.Now()}
	if err := s.CreateAgent(ctx, a); err != nil {
		t.Fatal(err)
	}
	agents, err := s.Agents(ctx)
	if err != nil || len(agents) != 1 || agents[0].Permission != domain.PermissionAuto {
		t.Fatalf("default agents=%+v err=%v", agents, err)
	}
	updated, err := s.UpdateAgent(ctx, a.ID, a.Name, a.RolePrompt, "", "", domain.PermissionFullAccess)
	if err != nil || updated.Permission != domain.PermissionFullAccess || updated.RoleVersion != 2 {
		t.Fatalf("full access agent=%+v err=%v", updated, err)
	}
	updated, err = s.UpdateAgent(ctx, a.ID, "Renamed", a.RolePrompt, "", "", domain.PermissionFullAccess)
	if err != nil || updated.RoleVersion != 2 {
		t.Fatalf("same permission agent=%+v err=%v", updated, err)
	}
	if err := s.migrate(); err != nil {
		t.Fatal(err)
	}
	updated, err = s.Agent(ctx, a.ID)
	if err != nil || updated.Permission != domain.PermissionFullAccess || updated.RoleVersion != 2 {
		t.Fatalf("after migration agent=%+v err=%v", updated, err)
	}
	updated, err = s.UpdateAgent(ctx, a.ID, a.Name, a.RolePrompt, "", "", domain.PermissionAuto)
	if err != nil || updated.Permission != domain.PermissionAuto || updated.RoleVersion != 3 {
		t.Fatalf("auto agent=%+v err=%v", updated, err)
	}
	if _, err := s.UpdateAgent(ctx, a.ID, a.Name, a.RolePrompt, "", "", "invalid"); err == nil {
		t.Fatal("invalid permission stored")
	}
}
