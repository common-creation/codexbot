package store

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/common-creation/codexbot/internal/domain"
)

func createSidebarAgents(t *testing.T, s *Store, ids ...string) {
	t.Helper()
	for i, id := range ids {
		now := time.Date(2026, 9, 13, 0, 0, i, 0, time.UTC)
		if err := s.CreateAgent(context.Background(), domain.Agent{ID: id, Name: id, RolePrompt: "role", Model: "model", Effort: "high", Permission: domain.PermissionFullAccess, RoleVersion: 3, Status: "stopped", ProviderAuthState: "disconnected", CreatedAt: now, UpdatedAt: now}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestSidebarPersistsOrderingAndNormalizesAgentLifecycle(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "sidebar.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	empty, err := s.Sidebar(ctx)
	if err != nil || empty.Revision != 0 || empty.Sections == nil || empty.UnsectionedAgentIDs == nil {
		t.Fatalf("empty=%+v err=%v", empty, err)
	}
	createSidebarAgents(t, s, "a", "b", "c")
	initial, err := s.Sidebar(ctx)
	if err != nil || !reflect.DeepEqual(initial.UnsectionedAgentIDs, []string{"a", "b", "c"}) {
		t.Fatalf("initial=%+v err=%v", initial, err)
	}
	agentBefore, err := s.Agent(ctx, "a")
	if err != nil {
		t.Fatal(err)
	}
	input := domain.SidebarLayout{Sections: []domain.SidebarSection{{ID: "work", Name: " 仕事 ", AgentIDs: []string{"c", "a"}}, {ID: "empty", Name: "Empty"}}, UnsectionedAgentIDs: []string{"b"}}
	saved, err := s.UpdateSidebar(ctx, input)
	if err != nil || saved.Revision != 1 || saved.Sections[0].Name != "仕事" || saved.Sections[1].AgentIDs == nil {
		t.Fatalf("saved=%+v err=%v", saved, err)
	}
	if input.Sections[0].Name != " 仕事 " {
		t.Fatal("update mutated caller")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := s.Sidebar(ctx)
	if err != nil || !reflect.DeepEqual(saved, loaded) {
		t.Fatalf("loaded=%+v want=%+v err=%v", loaded, saved, err)
	}
	after, err := s.Agent(ctx, "a")
	if err != nil || !reflect.DeepEqual(after, agentBefore) {
		t.Fatalf("agent changed: before=%+v after=%+v err=%v", agentBefore, after, err)
	}
	if err := s.ArchiveAgent(ctx, "c"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM agents WHERE id='b'`); err != nil {
		t.Fatal(err)
	}
	createSidebarAgents(t, s, "d", "e")
	loaded, err = s.Sidebar(ctx)
	if err != nil || !reflect.DeepEqual(loaded.Sections[0].AgentIDs, []string{"a"}) || loaded.Sections[1].AgentIDs == nil || !reflect.DeepEqual(loaded.UnsectionedAgentIDs, []string{"d", "e"}) {
		t.Fatalf("normalized=%+v err=%v", loaded, err)
	}
	// Deleting a section is a presentation change: its agents are returned to
	// the unsectioned list, with all requested order retained.
	loaded.UnsectionedAgentIDs = []string{"e", "a", "d"}
	loaded.Sections = nil
	saved, err = s.UpdateSidebar(ctx, loaded)
	if err != nil || saved.Revision != 2 || saved.Sections == nil || len(saved.Sections) != 0 {
		t.Fatalf("section deletion=%+v err=%v", saved, err)
	}
}

func TestSidebarRejectsInvalidAndStaleLayoutsAtomically(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	createSidebarAgents(t, s, "a", "b")
	base, err := s.UpdateSidebar(ctx, domain.SidebarLayout{UnsectionedAgentIDs: []string{"b", "a"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		layout domain.SidebarLayout
		want   error
	}{
		{"stale revision", domain.SidebarLayout{UnsectionedAgentIDs: []string{"a", "b"}}, ErrSidebarConflict},
		{"missing agent", domain.SidebarLayout{Revision: 1, UnsectionedAgentIDs: []string{"a"}}, ErrSidebarConflict},
		{"unknown agent", domain.SidebarLayout{Revision: 1, UnsectionedAgentIDs: []string{"a", "b", "x"}}, ErrInvalidSidebar},
		{"duplicate membership", domain.SidebarLayout{Revision: 1, Sections: []domain.SidebarSection{{ID: "s", Name: "S", AgentIDs: []string{"a"}}}, UnsectionedAgentIDs: []string{"a", "b"}}, ErrInvalidSidebar},
		{"duplicate section", domain.SidebarLayout{Revision: 1, Sections: []domain.SidebarSection{{ID: "s", Name: "S"}, {ID: " s ", Name: "T"}}, UnsectionedAgentIDs: []string{"a", "b"}}, ErrInvalidSidebar},
		{"empty name", domain.SidebarLayout{Revision: 1, Sections: []domain.SidebarSection{{ID: "s", Name: " \t "}}, UnsectionedAgentIDs: []string{"a", "b"}}, ErrInvalidSidebar},
		{"long name", domain.SidebarLayout{Revision: 1, Sections: []domain.SidebarSection{{ID: "s", Name: strings.Repeat("あ", 101)}}, UnsectionedAgentIDs: []string{"a", "b"}}, ErrInvalidSidebar},
		{"negative revision", domain.SidebarLayout{Revision: -1}, ErrInvalidSidebar},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := s.UpdateSidebar(ctx, tc.layout); !errors.Is(err, tc.want) {
				t.Fatalf("err=%v want=%v", err, tc.want)
			}
			current, err := s.Sidebar(ctx)
			if err != nil || !reflect.DeepEqual(current, base) {
				t.Fatalf("rejected update changed layout: %+v err=%v", current, err)
			}
		})
	}
	createSidebarAgents(t, s, "new")
	if _, err := s.UpdateSidebar(ctx, base); !errors.Is(err, ErrSidebarConflict) {
		t.Fatalf("new agent omitted: %v", err)
	}
	base, err = s.Sidebar(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ArchiveAgent(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateSidebar(ctx, base); !errors.Is(err, ErrInvalidSidebar) {
		t.Fatalf("archived agent submitted: %v", err)
	}
}

func TestSidebarConcurrentWritersUseRevisionCAS(t *testing.T) {
	path := filepath.Join(t.TempDir(), "concurrent.db")
	first, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	createSidebarAgents(t, first, "a", "b")
	start := make(chan struct{})
	errorsCh := make(chan error, 2)
	var wg sync.WaitGroup
	for i, st := range []*Store{first, second} {
		wg.Add(1)
		go func(i int, st *Store) {
			defer wg.Done()
			<-start
			ids := []string{"a", "b"}
			if i == 1 {
				ids = []string{"b", "a"}
			}
			_, err := st.UpdateSidebar(context.Background(), domain.SidebarLayout{UnsectionedAgentIDs: ids})
			errorsCh <- err
		}(i, st)
	}
	close(start)
	wg.Wait()
	close(errorsCh)
	var successes, conflicts int
	for err := range errorsCh {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, ErrSidebarConflict):
			conflicts++
		default:
			t.Fatal(err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("successes=%d conflicts=%d", successes, conflicts)
	}
}
