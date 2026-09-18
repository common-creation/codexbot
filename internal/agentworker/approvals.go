package agentworker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/common-creation/codexbot/internal/appserver"
	"github.com/common-creation/codexbot/internal/domain"
	"github.com/google/uuid"
)

func (w *Worker) consumeApprovals(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case req, ok := <-w.client.ApprovalRequests():
			if !ok {
				return
			}
			// A response must not block other approvals or turn cancellation.
			go w.handleApproval(ctx, req)
		}
	}
}

func (w *Worker) handleApproval(ctx context.Context, req appserver.ServerRequest) {
	var origin struct {
		ThreadID string  `json:"threadId"`
		TurnID   *string `json:"turnId"`
	}
	decodeErr := req.DecodeParams(&origin)
	w.mu.Lock()
	current := w.active
	var active activeRun
	if current != nil {
		active = *current
	}
	w.mu.Unlock()
	if decodeErr != nil || current == nil || !active.isRunning() || origin.ThreadID != active.ThreadID ||
		(origin.TurnID != nil && active.TurnID != "" && *origin.TurnID != active.TurnID) {
		if err := w.respondApproval(ctx, req, appserver.DecisionDecline); err != nil {
			w.logger.Error("decline stale approval", "method", req.Method, "error", err)
		}
		return
	}
	if active.TurnID == "" && origin.TurnID != nil {
		active.TurnID = *origin.TurnID
	}
	if active.runContext != nil {
		ctx = active.runContext
	}
	id := uuid.NewString()
	payload := map[string]any{"approvalId": id, "method": req.Method, "params": json.RawMessage(req.Params)}
	decision := appserver.DecisionDecline
	reason := ""
	// Elicitations requesting user data are not permission requests. Never
	// invent form values, including when the agent has Full Access.
	var err error
	if req.Method == appserver.MethodMCPElicitation {
		_, err = mcpToolApprovalResponse(req, appserver.DecisionAccept)
	}
	if err != nil {
		reason = err.Error()
	} else if active.Permission == domain.PermissionFullAccess {
		decision = appserver.DecisionAccept
		reason = "Allowed by the agent's Full Access setting."
	} else {
		// Native auto_review owns Auto decisions inside Codex. A request reaching
		// this client requires manual intervention or was not reviewed; never
		// grant it or fall back to a second model or a human prompt.
		reason = "Codex automatic review did not authorize this request. Manual approval is unavailable in Auto mode."
	}
	// Never grant access after the run was stopped or replaced.
	w.mu.Lock()
	stillActive := w.active == current && w.active.isRunning()
	w.mu.Unlock()
	if !stillActive || ctx.Err() != nil {
		decision = appserver.DecisionDecline
		reason = "The run ended before the permission response was sent."
	}
	responseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := w.respondApproval(responseCtx, req, decision); err != nil {
		w.logger.Error("respond automatic approval", "method", req.Method, "error", err)
		_ = w.appendEvent(active.RunID, "warning", map[string]any{"message": "Could not send automatic permission decision: " + err.Error()})
		return
	}
	payload["reason"] = reason
	typ := "approval.autoDeclined"
	if decision == appserver.DecisionAccept {
		typ = "approval.autoAccepted"
	}
	_ = w.appendEvent(active.RunID, typ, payload)
}

func (a *activeRun) isRunning() bool { return a.Status == "running" || a.Status == "starting" }

func (w *Worker) respondApproval(ctx context.Context, req appserver.ServerRequest, d appserver.ApprovalDecision) error {
	var err error
	switch req.Method {
	case appserver.MethodCommandApproval:
		err = w.client.RespondCommandApproval(ctx, req, d)
	case appserver.MethodFileApproval:
		err = w.client.RespondFileApproval(ctx, req, d)
	case appserver.MethodPermissionReview:
		if d == appserver.DecisionAccept || d == appserver.DecisionAcceptForSession {
			var p appserver.PermissionApprovalParams
			if err := req.DecodeParams(&p); err != nil {
				return err
			}
			permissions := map[string]json.RawMessage{}
			if err := json.Unmarshal(p.Permissions, &permissions); err != nil {
				return err
			}
			scope := "turn"
			if d == appserver.DecisionAcceptForSession {
				scope = "session"
			}
			err = w.client.RespondPermissionsApproval(ctx, req, appserver.PermissionsApprovalResponse{Permissions: permissions, Scope: scope})
		} else {
			err = w.client.RespondPermissionsApproval(ctx, req, appserver.PermissionsApprovalResponse{Permissions: map[string]json.RawMessage{}, Scope: "turn"})
		}
	case appserver.MethodMCPElicitation:
		response, responseErr := mcpToolApprovalResponse(req, d)
		if responseErr != nil {
			return responseErr
		}
		err = w.client.RespondMCPElicitation(ctx, req, response)
	default:
		return fmt.Errorf("unsupported approval method %q", req.Method)
	}
	return err
}

// Codex emits its MCP tool confirmation as an empty form with this metadata.
// Other MCP forms can ask for arbitrary input and must not be auto-accepted as
// tool approvals. This shape is verified against Codex CLI 0.153.4.
func mcpToolApprovalResponse(req appserver.ServerRequest, d appserver.ApprovalDecision) (appserver.MCPElicitationResponse, error) {
	response := appserver.MCPElicitationResponse{Action: string(d)}
	if d == appserver.DecisionDecline || d == appserver.DecisionCancel {
		return response, nil
	}
	if d != appserver.DecisionAccept && d != appserver.DecisionAcceptForSession {
		return response, fmt.Errorf("unsupported MCP approval decision %q", d)
	}
	var p appserver.MCPElicitationParams
	if err := req.DecodeParams(&p); err != nil {
		return response, err
	}
	var meta struct {
		Kind    string   `json:"codex_approval_kind"`
		Persist []string `json:"persist"`
	}
	var schema struct {
		Type       string                     `json:"type"`
		Properties map[string]json.RawMessage `json:"properties"`
		Required   []string                   `json:"required"`
	}
	if json.Unmarshal(p.Meta, &meta) != nil || json.Unmarshal(p.RequestedSchema, &schema) != nil ||
		meta.Kind != "mcp_tool_call" || p.Mode != "form" || schema.Type != "object" ||
		schema.Properties == nil || len(schema.Properties) != 0 || len(schema.Required) != 0 {
		return response, fmt.Errorf("MCP server %q requested an input form that Codexbot does not support", p.ServerName)
	}
	response.Action = "accept"
	response.Content = json.RawMessage(`{}`)
	if d == appserver.DecisionAcceptForSession {
		for _, scope := range meta.Persist {
			if scope == "session" {
				response.Meta = json.RawMessage(`{"persist":"session"}`)
				return response, nil
			}
		}
		return response, errors.New("MCP tool approval does not offer session persistence; use Accept")
	}
	return response, nil
}
