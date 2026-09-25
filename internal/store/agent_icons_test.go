package store

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/common-creation/codexbot/internal/domain"
)

func TestAgentIconMigrationPersistenceAndRemoval(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old-agents.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE agents (
 id TEXT PRIMARY KEY, name TEXT NOT NULL, role_prompt TEXT NOT NULL, role_version INTEGER NOT NULL,
 status TEXT NOT NULL, provider_auth_state TEXT NOT NULL, archived INTEGER NOT NULL DEFAULT 0,
 created_at TEXT NOT NULL, updated_at TEXT NOT NULL, model TEXT NOT NULL DEFAULT '',
 effort TEXT NOT NULL DEFAULT '', permission TEXT NOT NULL DEFAULT 'auto'
 ); INSERT INTO agents(id,name,role_prompt,role_version,status,provider_auth_state,created_at,updated_at)
 VALUES('existing','Existing','Role',7,'stopped','disconnected','2026-09-01T00:00:00Z','2026-09-01T00:00:00Z');`)
	if err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	a, err := st.Agent(ctx, "existing")
	if err != nil || a.IconURL != "" || a.RoleVersion != 7 {
		t.Fatalf("migration agent=%+v err=%v", a, err)
	}
	icon := &AgentIconUpdate{Data: []byte("validated image")}
	a, err = st.UpdateAgentWithIcon(ctx, a.ID, a.Name, a.RolePrompt, a.Model, a.Effort, a.Permission, icon)
	if err != nil || a.IconURL == "" || a.RoleVersion != 7 {
		t.Fatalf("update agent=%+v err=%v", a, err)
	}
	wantURL := a.IconURL
	if err = st.Close(); err != nil {
		t.Fatal(err)
	}
	st, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	agents, err := st.Agents(ctx)
	if err != nil || len(agents) != 1 || agents[0].IconURL != wantURL || agents[0].RoleVersion != 7 {
		t.Fatalf("reopened agents=%+v err=%v", agents, err)
	}
	data, _, err := st.AgentIcon(ctx, a.ID)
	if err != nil || !bytes.Equal(data, icon.Data) {
		t.Fatalf("reopened icon=%q err=%v", data, err)
	}
	a, err = st.UpdateAgent(ctx, a.ID, "Renamed", a.RolePrompt, a.Model, a.Effort, a.Permission)
	if err != nil || a.IconURL != wantURL {
		t.Fatalf("omitted icon changed it: %+v err=%v", a, err)
	}
	a, err = st.UpdateAgentWithIcon(ctx, a.ID, a.Name, a.RolePrompt, a.Model, a.Effort, a.Permission, &AgentIconUpdate{})
	if err != nil || a.IconURL != "" || a.RoleVersion != 7 {
		t.Fatalf("removed icon=%+v err=%v", a, err)
	}
	if _, _, err = st.AgentIcon(ctx, a.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("removed icon err=%v", err)
	}
	if _, err = st.UpdateAgentWithIcon(ctx, a.ID, a.Name, a.RolePrompt, a.Model, a.Effort, a.Permission, icon); err != nil {
		t.Fatal(err)
	}
	if err = st.ArchiveAgent(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err = st.AgentIcon(ctx, a.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("archived icon err=%v", err)
	}
	var count int
	if err = st.db.QueryRow(`SELECT count(*) FROM agent_icons`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("archived icon retained: count=%d err=%v", count, err)
	}
}

func TestAgentIconWritesAreAtomicWithSettings(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	a := domain.Agent{ID: "existing", Name: "Original", RolePrompt: "Role", Permission: domain.PermissionAuto, RoleVersion: 1, Status: "stopped", ProviderAuthState: "disconnected", CreatedAt: time.Now(), UpdatedAt: time.Now()}
	original := &AgentIconUpdate{Data: []byte("original image")}
	if err := st.CreateAgentWithIcon(ctx, a, original); err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.Exec(`CREATE TRIGGER fail_icon_write BEFORE INSERT ON agent_icons BEGIN SELECT RAISE(ABORT, 'icon write failed'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpdateAgentWithIcon(ctx, a.ID, "Changed", "New role", "", "", a.Permission, &AgentIconUpdate{Data: []byte("new image")}); err == nil {
		t.Fatal("expected failing icon write")
	}
	stored, err := st.Agent(ctx, a.ID)
	if err != nil || stored.Name != a.Name || stored.RolePrompt != a.RolePrompt || stored.RoleVersion != a.RoleVersion {
		t.Fatalf("failed icon write changed settings: %+v err=%v", stored, err)
	}
	data, _, err := st.AgentIcon(ctx, a.ID)
	if err != nil || !bytes.Equal(data, original.Data) {
		t.Fatalf("failed icon write changed image: %q err=%v", data, err)
	}
	a.ID = "new"
	if err := st.CreateAgentWithIcon(ctx, a, original); err == nil {
		t.Fatal("expected create to fail")
	}
	if _, err := st.Agent(ctx, a.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("failed icon left agent behind: %v", err)
	}
}
