package store

import (
	"context"
	"database/sql"
	"errors"

	"github.com/common-creation/codexbot/internal/domain"
)

// ScheduleRuns returns every execution state, newest first. The cursor is an
// execution ID from this schedule, so changing run status cannot move a page.
// Callers may request 101 rows to detect another page of at most 100 entries.
func (s *Store) ScheduleRuns(ctx context.Context, scheduleID, beforeRunID string, limit int) ([]domain.Run, error) {
	if limit < 1 {
		limit = 20
	}
	if limit > 101 {
		limit = 101
	}
	source := "schedule:" + scheduleID
	query := `SELECT id,agent_id,conversation_id,codex_turn_id,source,prompt,status,scheduled_for,started_at,finished_at,error FROM runs WHERE source=?`
	args := []any{source}
	if beforeRunID != "" {
		var scheduledFor string
		if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(scheduled_for,'') FROM runs WHERE id=? AND source=?`, beforeRunID, source).Scan(&scheduledFor); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil, ErrNotFound
			}
			return nil, err
		}
		query += ` AND (COALESCE(scheduled_for,'')<? OR (COALESCE(scheduled_for,'')=? AND id<?))`
		args = append(args, scheduledFor, scheduledFor, beforeRunID)
	}
	query += ` ORDER BY COALESCE(scheduled_for,'') DESC,id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.Run{}
	for rows.Next() {
		var r domain.Run
		var sf, st, ft sql.NullString
		if err = rows.Scan(&r.ID, &r.AgentID, &r.ConversationID, &r.CodexTurnID, &r.Source, &r.Prompt, &r.Status, &sf, &st, &ft, &r.Error); err != nil {
			return nil, err
		}
		r.ScheduledFor = nullTS(sf)
		r.StartedAt = nullTS(st)
		r.FinishedAt = nullTS(ft)
		out = append(out, r)
	}
	return out, rows.Err()
}
