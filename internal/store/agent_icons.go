package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
)

// AgentIconUpdate contains validated PNG data. A nil update preserves the icon;
// an update with empty Data removes it. Settings and icons share a transaction.
type AgentIconUpdate struct {
	Data []byte
}

func updateAgentIcon(ctx context.Context, tx *sql.Tx, id string, icon *AgentIconUpdate) error {
	if icon == nil {
		return nil
	}
	version := ""
	if len(icon.Data) == 0 {
		if _, err := tx.ExecContext(ctx, `DELETE FROM agent_icons WHERE agent_id=?`, id); err != nil {
			return err
		}
	} else {
		sum := sha256.Sum256(icon.Data)
		version = hex.EncodeToString(sum[:])
		if _, err := tx.ExecContext(ctx, `INSERT INTO agent_icons(agent_id,data) VALUES(?,?) ON CONFLICT(agent_id) DO UPDATE SET data=excluded.data`, id, icon.Data); err != nil {
			return err
		}
	}
	_, err := tx.ExecContext(ctx, `UPDATE agents SET icon_version=? WHERE id=?`, version, id)
	return err
}

func (s *Store) AgentIcon(ctx context.Context, id string) ([]byte, string, error) {
	var data []byte
	var version string
	err := s.db.QueryRowContext(ctx, `SELECT agent_icons.data,agents.icon_version FROM agent_icons JOIN agents ON agents.id=agent_icons.agent_id WHERE agents.id=? AND agents.archived=0`, id).Scan(&data, &version)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, "", ErrNotFound
	}
	return data, version, err
}
