package appserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

func (c *Client) ConfigRead(ctx context.Context, params ConfigReadParams) (ConfigReadResponse, error) {
	var result ConfigReadResponse
	err := c.call(ctx, MethodConfigRead, params, &result)
	return result, err
}

func (c *Client) ThreadStart(ctx context.Context, params ThreadStartParams) (ThreadStartResponse, error) {
	if err := validateThreadOptions(params.CWD, params.ApprovalPolicy, params.ApprovalsReviewer, params.Sandbox, params.Personality); err != nil {
		return ThreadStartResponse{}, err
	}
	var result ThreadStartResponse
	err := c.call(ctx, MethodThreadStart, params, &result)
	return result, err
}

func (c *Client) ThreadResume(ctx context.Context, params ThreadResumeParams) (ThreadResumeResponse, error) {
	if strings.TrimSpace(params.ThreadID) == "" {
		return ThreadResumeResponse{}, errors.New("thread id is required")
	}
	if err := validateThreadOptions(params.CWD, params.ApprovalPolicy, params.ApprovalsReviewer, params.Sandbox, params.Personality); err != nil {
		return ThreadResumeResponse{}, err
	}
	var result ThreadResumeResponse
	err := c.call(ctx, MethodThreadResume, params, &result)
	return result, err
}

func (c *Client) ThreadRead(ctx context.Context, params ThreadReadParams) (ThreadReadResponse, error) {
	if strings.TrimSpace(params.ThreadID) == "" {
		return ThreadReadResponse{}, errors.New("thread id is required")
	}
	var result ThreadReadResponse
	err := c.call(ctx, MethodThreadRead, params, &result)
	return result, err
}

func (c *Client) TurnStart(ctx context.Context, params TurnStartParams) (TurnStartResponse, error) {
	if strings.TrimSpace(params.ThreadID) == "" {
		return TurnStartResponse{}, errors.New("thread id is required")
	}
	if err := validateInputs(params.Input); err != nil {
		return TurnStartResponse{}, err
	}
	if err := validateThreadOptions(params.CWD, params.ApprovalPolicy, params.ApprovalsReviewer, "", params.Personality); err != nil {
		return TurnStartResponse{}, err
	}
	if len(params.OutputSchema) != 0 && !json.Valid(params.OutputSchema) {
		return TurnStartResponse{}, errors.New("output schema is not valid JSON")
	}
	var result TurnStartResponse
	err := c.call(ctx, MethodTurnStart, params, &result)
	return result, err
}

func (c *Client) TurnSteer(ctx context.Context, params TurnSteerParams) (TurnSteerResponse, error) {
	if strings.TrimSpace(params.ThreadID) == "" {
		return TurnSteerResponse{}, errors.New("thread id is required")
	}
	if strings.TrimSpace(params.ExpectedTurnID) == "" {
		return TurnSteerResponse{}, errors.New("expected turn id is required")
	}
	if err := validateInputs(params.Input); err != nil {
		return TurnSteerResponse{}, err
	}
	var result TurnSteerResponse
	err := c.call(ctx, MethodTurnSteer, params, &result)
	return result, err
}

func (c *Client) TurnInterrupt(ctx context.Context, params TurnInterruptParams) error {
	if strings.TrimSpace(params.ThreadID) == "" || strings.TrimSpace(params.TurnID) == "" {
		return errors.New("thread id and turn id are required")
	}
	var result struct{}
	return c.call(ctx, MethodTurnInterrupt, params, &result)
}

func (c *Client) AccountRead(ctx context.Context, refreshToken bool) (AccountReadResponse, error) {
	params := struct {
		RefreshToken bool `json:"refreshToken,omitempty"`
	}{refreshToken}
	var result AccountReadResponse
	err := c.call(ctx, MethodAccountRead, params, &result)
	return result, err
}

func (c *Client) AccountLoginDeviceCode(ctx context.Context) (DeviceCodeLoginResponse, error) {
	params := struct {
		Type string `json:"type"`
	}{"chatgptDeviceCode"}
	var result DeviceCodeLoginResponse
	if err := c.call(ctx, MethodAccountLogin, params, &result); err != nil {
		return DeviceCodeLoginResponse{}, err
	}
	if result.Type != "chatgptDeviceCode" || result.LoginID == "" || result.VerificationURL == "" || result.UserCode == "" {
		return DeviceCodeLoginResponse{}, errors.New("invalid device-code login response")
	}
	return result, nil
}

// AccountLoginAPIKey forwards the key once to app-server. Callers must not log
// params or retain the key after this method returns.
func (c *Client) AccountLoginAPIKey(ctx context.Context, apiKey string) error {
	if strings.TrimSpace(apiKey) == "" {
		return errors.New("API key is required")
	}
	params := struct {
		Type   string `json:"type"`
		APIKey string `json:"apiKey"`
	}{"apiKey", apiKey}
	var result APIKeyLoginResponse
	if err := c.call(ctx, MethodAccountLogin, params, &result); err != nil {
		return err
	}
	if result.Type != "apiKey" {
		return fmt.Errorf("unexpected API key login response type %q", result.Type)
	}
	return nil
}

func (c *Client) AccountLogout(ctx context.Context) error {
	var result struct{}
	return c.call(ctx, MethodAccountLogout, struct{}{}, &result)
}

func (c *Client) RespondCommandApproval(ctx context.Context, request ServerRequest, decision ApprovalDecision) error {
	if decision != DecisionAccept && decision != DecisionAcceptForSession && decision != DecisionDecline && decision != DecisionCancel {
		return fmt.Errorf("unsupported command approval decision %q", decision)
	}
	return c.respondApproval(ctx, request, MethodCommandApproval, struct {
		Decision ApprovalDecision `json:"decision"`
	}{decision})
}

func (c *Client) RespondFileApproval(ctx context.Context, request ServerRequest, decision ApprovalDecision) error {
	if decision != DecisionAccept && decision != DecisionAcceptForSession && decision != DecisionDecline && decision != DecisionCancel {
		return fmt.Errorf("unsupported file approval decision %q", decision)
	}
	return c.respondApproval(ctx, request, MethodFileApproval, struct {
		Decision ApprovalDecision `json:"decision"`
	}{decision})
}

func (c *Client) RespondPermissionsApproval(ctx context.Context, request ServerRequest, response PermissionsApprovalResponse) error {
	if response.Scope != "turn" && response.Scope != "session" {
		return fmt.Errorf("unsupported permission grant scope %q", response.Scope)
	}
	if response.Permissions == nil {
		response.Permissions = map[string]json.RawMessage{}
	}
	for key, value := range response.Permissions {
		if key != "network" && key != "fileSystem" {
			return fmt.Errorf("unsupported granted permissions key %q", key)
		}
		if !json.Valid(value) {
			return fmt.Errorf("granted permissions %q is not valid JSON", key)
		}
	}
	return c.respondApproval(ctx, request, MethodPermissionReview, response)
}

func (c *Client) RespondMCPElicitation(ctx context.Context, request ServerRequest, response MCPElicitationResponse) error {
	if response.Action != "accept" && response.Action != "decline" && response.Action != "cancel" {
		return fmt.Errorf("unsupported MCP elicitation action %q", response.Action)
	}
	if len(response.Content) > 0 && !json.Valid(response.Content) {
		return errors.New("MCP elicitation content is not valid JSON")
	}
	if len(response.Meta) > 0 && !json.Valid(response.Meta) {
		return errors.New("MCP elicitation metadata is not valid JSON")
	}
	if response.Action != "accept" {
		response.Content = nil
	}
	return c.respondApproval(ctx, request, MethodMCPElicitation, response)
}

func (c *Client) respondApproval(ctx context.Context, request ServerRequest, expectedMethod string, result any) error {
	if ctx == nil {
		return errors.New("approval context is nil")
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	key, err := requestIDKey(json.RawMessage(request.ID))
	if err != nil {
		return err
	}
	if request.Method != expectedMethod {
		return fmt.Errorf("approval request method %q does not match %q", request.Method, expectedMethod)
	}
	c.requestsMu.Lock()
	method, ok := c.requests[key]
	if !ok || method != expectedMethod {
		c.requestsMu.Unlock()
		return errors.New("approval request is not outstanding")
	}
	delete(c.requests, key)
	c.requestsMu.Unlock()
	response := struct {
		ID     RequestID `json:"id"`
		Result any       `json:"result"`
	}{request.ID, result}
	if err := c.writeJSON(response); err != nil {
		c.terminate()
		return err
	}
	return nil
}
