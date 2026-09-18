package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/common-creation/codexbot/internal/domain"
)

var (
	ErrSidebarConflict = errors.New("sidebar or agent list changed; reload and try again")
	ErrInvalidSidebar  = errors.New("invalid sidebar layout")
)

func sidebarAgentIDs(ctx context.Context, tx *sql.Tx) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id FROM agents WHERE archived=0 ORDER BY created_at, rowid`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// Sidebar returns a consistent snapshot, dropping inactive agents and placing
// newly created agents at the end of the unsectioned list.
func (s *Store) Sidebar(ctx context.Context) (domain.SidebarLayout, error) {
	var layout domain.SidebarLayout
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return layout, err
	}
	defer tx.Rollback()
	var data string
	var revision int64
	if err := tx.QueryRowContext(ctx, `SELECT revision,layout FROM sidebar_layout WHERE id=1`).Scan(&revision, &data); err != nil {
		return layout, err
	}
	if err := json.Unmarshal([]byte(data), &layout); err != nil {
		return layout, err
	}
	ids, err := sidebarAgentIDs(ctx, tx)
	if err != nil {
		return layout, err
	}
	layout.Revision = revision
	if layout.Sections == nil {
		layout.Sections = []domain.SidebarSection{}
	}
	active := make(map[string]bool, len(ids))
	for _, id := range ids {
		active[id] = true
	}
	filter := func(input []string) []string {
		result := []string{}
		for _, id := range input {
			if active[id] {
				result = append(result, id)
				delete(active, id)
			}
		}
		return result
	}
	for i := range layout.Sections {
		layout.Sections[i].AgentIDs = filter(layout.Sections[i].AgentIDs)
	}
	layout.UnsectionedAgentIDs = filter(layout.UnsectionedAgentIDs)
	for _, id := range ids {
		if active[id] {
			layout.UnsectionedAgentIDs = append(layout.UnsectionedAgentIDs, id)
		}
	}
	return layout, tx.Commit()
}

func validateSidebar(layout *domain.SidebarLayout) error {
	invalid := func(message string) error { return fmt.Errorf("%w: %s", ErrInvalidSidebar, message) }
	if layout.Revision < 0 {
		return invalid("revision must not be negative")
	}
	if len(layout.Sections) > 100 {
		return invalid("at most 100 sections are allowed")
	}
	if layout.Sections == nil {
		layout.Sections = []domain.SidebarSection{}
	}
	if layout.UnsectionedAgentIDs == nil {
		layout.UnsectionedAgentIDs = []string{}
	}
	sectionIDs := map[string]bool{}
	agentIDs := map[string]bool{}
	checkAgents := func(ids []string) error {
		for _, id := range ids {
			if id == "" || agentIDs[id] {
				return invalid("agent IDs must be nonempty and unique")
			}
			agentIDs[id] = true
		}
		return nil
	}
	for i := range layout.Sections {
		section := &layout.Sections[i]
		section.ID = strings.TrimSpace(section.ID)
		section.Name = strings.TrimSpace(section.Name)
		if section.ID == "" || len(section.ID) > 128 || sectionIDs[section.ID] {
			return invalid("section IDs must be unique and 1-128 bytes")
		}
		if section.Name == "" || utf8.RuneCountInString(section.Name) > 100 {
			return invalid("section names must be 1-100 characters")
		}
		sectionIDs[section.ID] = true
		if section.AgentIDs == nil {
			section.AgentIDs = []string{}
		}
		if err := checkAgents(section.AgentIDs); err != nil {
			return err
		}
	}
	return checkAgents(layout.UnsectionedAgentIDs)
}

func (s *Store) UpdateSidebar(ctx context.Context, layout domain.SidebarLayout) (domain.SidebarLayout, error) {
	// Copy sections before normalizing so a rejected update does not mutate the caller.
	layout.Sections = append([]domain.SidebarSection(nil), layout.Sections...)
	if err := validateSidebar(&layout); err != nil {
		return domain.SidebarLayout{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.SidebarLayout{}, err
	}
	defer tx.Rollback()
	// Acquire the write lock before reading agents, and compare the revision in
	// the same statement so concurrent clients cannot overwrite each other.
	result, err := tx.ExecContext(ctx, `UPDATE sidebar_layout SET revision=revision WHERE id=1 AND revision=?`, layout.Revision)
	if err != nil {
		return domain.SidebarLayout{}, err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return domain.SidebarLayout{}, err
	}
	if count != 1 {
		return domain.SidebarLayout{}, ErrSidebarConflict
	}
	ids, err := sidebarAgentIDs(ctx, tx)
	if err != nil {
		return domain.SidebarLayout{}, err
	}
	active := make(map[string]bool, len(ids))
	for _, id := range ids {
		active[id] = true
	}
	ordered := append([]string(nil), layout.UnsectionedAgentIDs...)
	for _, section := range layout.Sections {
		ordered = append(ordered, section.AgentIDs...)
	}
	for _, id := range ordered {
		if !active[id] {
			return domain.SidebarLayout{}, fmt.Errorf("%w: unknown or archived agent %q", ErrInvalidSidebar, id)
		}
		delete(active, id)
	}
	if len(active) != 0 {
		return domain.SidebarLayout{}, ErrSidebarConflict
	}
	layout.Revision++
	data, err := json.Marshal(layout)
	if err != nil {
		return domain.SidebarLayout{}, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE sidebar_layout SET revision=?,layout=? WHERE id=1`, layout.Revision, string(data)); err != nil {
		return domain.SidebarLayout{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.SidebarLayout{}, err
	}
	return layout, nil
}
