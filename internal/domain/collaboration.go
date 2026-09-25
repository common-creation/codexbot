package domain

import "time"

// CollaborationTask is a durable instruction from one agent to another. Its
// lifecycle is separate from Run so waiting instructions do not reserve turns.
type CollaborationTask struct {
	ID                    string    `json:"id"`
	SenderAgentID         string    `json:"senderAgentId"`
	SenderRunID           string    `json:"senderRunId,omitempty"`
	TargetAgentID         string    `json:"targetAgentId"`
	IdempotencyKey        string    `json:"idempotencyKey"`
	Prompt                string    `json:"prompt"`
	Mode                  string    `json:"mode"`
	CompletionMode        string    `json:"completionMode"`
	CompletionTaskID      string    `json:"completionTaskId,omitempty"`
	NotificationForTaskID string    `json:"notificationForTaskId,omitempty"`
	SteerFallback         bool      `json:"steerFallback"`
	DeliveryConfirmed     bool      `json:"deliveryConfirmed"`
	Status                string    `json:"status"`
	RunID                 string    `json:"runId,omitempty"`
	ResultAfterSequence   int64     `json:"resultAfterSequence"`
	CreatedAt             time.Time `json:"createdAt"`
	UpdatedAt             time.Time `json:"updatedAt"`
	Error                 string    `json:"error,omitempty"`
}
