package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/common-creation/codexbot/internal/domain"
	"github.com/google/uuid"
)

var ErrConversationBusy = errors.New("finish the active run or release desktop control before starting a new chat")

// ResetAgentConversation rotates the context used by all future inputs without
// deleting previous conversations or their entries in the agent's timeline.
func (s *Store) ResetAgentConversation(ctx context.Context, agentID string) (domain.Conversation, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Conversation{}, err
	}
	defer tx.Rollback()
	var version int
	if err = tx.QueryRowContext(ctx, `SELECT role_version FROM agents WHERE id=? AND archived=0`, agentID).Scan(&version); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			err = ErrNotFound
		}
		return domain.Conversation{}, err
	}
	var busy bool
	err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM runs WHERE agent_id=? AND status IN ('queued','running')) OR EXISTS(SELECT 1 FROM desktop_leases WHERE agent_id=? AND holder='human' AND expires_at>?)`, agentID, agentID, ts(time.Now())).Scan(&busy)
	if err != nil {
		return domain.Conversation{}, err
	}
	if busy {
		return domain.Conversation{}, ErrConversationBusy
	}
	var previousID string
	err = tx.QueryRowContext(ctx, `SELECT conversation_id FROM agent_conversations WHERE agent_id=?`, agentID).Scan(&previousID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return domain.Conversation{}, err
	}
	c := domain.Conversation{ID: uuid.NewString(), AgentID: agentID, Kind: "agent", RoleVersion: version, CreatedAt: time.Now()}
	if _, err = tx.ExecContext(ctx, `INSERT INTO conversations(id,agent_id,kind,role_version,created_at) VALUES(?,?,?,?,?)`, c.ID, agentID, c.Kind, version, ts(c.CreatedAt)); err != nil {
		return c, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO agent_conversations(agent_id,conversation_id) VALUES(?,?) ON CONFLICT(agent_id) DO UPDATE SET conversation_id=excluded.conversation_id`, agentID, c.ID); err != nil {
		return c, err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM desktop_continuations WHERE agent_id=?`, agentID); err != nil {
		return c, err
	}
	payload, _ := json.Marshal(map[string]string{"id": "conversation-" + c.ID, "conversationId": c.ID, "previousConversationId": previousID, "text": "New chat started. Earlier messages remain visible, but are not included in the new context."})
	if _, err = appendTimelineEvent(ctx, tx, agentID, "", "conversation.reset", payload, c.CreatedAt, "conversation-reset:"+c.ID, nil); err != nil {
		return c, err
	}
	return c, tx.Commit()
}
