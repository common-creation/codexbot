package domain

import (
	"encoding/json"
	"time"
)

type PermissionMode string

const (
	PermissionAuto       PermissionMode = "auto"
	PermissionFullAccess PermissionMode = "full-access"
)

func (p PermissionMode) Valid() bool {
	return p == PermissionAuto || p == PermissionFullAccess
}

type Agent struct {
	ID                string         `json:"id"`
	Name              string         `json:"name"`
	IconURL           string         `json:"iconUrl,omitempty"`
	RolePrompt        string         `json:"rolePrompt"`
	Model             string         `json:"model"`
	Effort            string         `json:"effort"`
	Permission        PermissionMode `json:"permission"`
	RoleVersion       int            `json:"roleVersion"`
	Status            string         `json:"status"`
	ProviderAuthState string         `json:"providerAuthState"`
	CreatedAt         time.Time      `json:"createdAt"`
	UpdatedAt         time.Time      `json:"updatedAt"`
}

type SharedAuth struct {
	State          string     `json:"state"`
	Verified       bool       `json:"verified"`
	PendingAgentID string     `json:"pendingAgentId,omitempty"`
	PendingLoginID string     `json:"pendingLoginId,omitempty"`
	PendingSince   *time.Time `json:"pendingSince,omitempty"`
	VerifiedAt     *time.Time `json:"verifiedAt,omitempty"`
}

type Conversation struct {
	ID            string    `json:"id"`
	AgentID       string    `json:"agentId"`
	CodexThreadID string    `json:"codexThreadId,omitempty"`
	Kind          string    `json:"kind"`
	RoleVersion   int       `json:"roleVersion"`
	Title         string    `json:"title"`
	CreatedAt     time.Time `json:"createdAt"`
}

type Run struct {
	ID             string     `json:"id"`
	AgentID        string     `json:"agentId"`
	ConversationID string     `json:"conversationId"`
	CodexTurnID    string     `json:"codexTurnId,omitempty"`
	Source         string     `json:"source"`
	Prompt         string     `json:"prompt"`
	Status         string     `json:"status"`
	ScheduledFor   *time.Time `json:"scheduledFor,omitempty"`
	StartedAt      *time.Time `json:"startedAt,omitempty"`
	FinishedAt     *time.Time `json:"finishedAt,omitempty"`
	Error          string     `json:"error,omitempty"`
}

type Event struct {
	RunID     string          `json:"runId"`
	Sequence  int64           `json:"sequence"`
	Type      string          `json:"type"`
	Payload   json.RawMessage `json:"payload"`
	CreatedAt time.Time       `json:"createdAt"`
}

type Schedule struct {
	ID         string     `json:"id"`
	AgentID    string     `json:"agentId"`
	Name       string     `json:"name"`
	Prompt     string     `json:"prompt"`
	Kind       string     `json:"kind"`
	Expression string     `json:"expression"`
	Timezone   string     `json:"timezone"`
	Enabled    bool       `json:"enabled"`
	NextRunAt  *time.Time `json:"nextRunAt,omitempty"`
	LastRunAt  *time.Time `json:"lastRunAt,omitempty"`
	CreatedAt  time.Time  `json:"createdAt"`
	UpdatedAt  time.Time  `json:"updatedAt"`
}

type DesktopLease struct {
	AgentID    string    `json:"agentId"`
	Holder     string    `json:"holder"`
	Generation int64     `json:"generation"`
	ExpiresAt  time.Time `json:"expiresAt"`
}
