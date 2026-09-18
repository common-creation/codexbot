package runtimeclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/common-creation/codexbot/internal/domain"
)

type Client struct {
	base  string
	token string
	http  *http.Client
}

func New(base, token string) *Client {
	return &Client{base: strings.TrimRight(base, "/"), token: token, http: &http.Client{Timeout: 30 * time.Second}}
}

type AgentConfig struct {
	ID, Name, RolePrompt string
	RoleVersion          int
}
type TurnRequest struct {
	Attachments                                                            []domain.Attachment   `json:"attachments,omitempty"`
	Model                                                                  string                `json:"model,omitempty"`
	Effort                                                                 string                `json:"effort,omitempty"`
	Permission                                                             domain.PermissionMode `json:"permission"`
	RunID, ConversationID, ThreadID, Prompt, DeveloperInstructions, Source string
}
type SteerRequest struct {
	Attachments    []domain.Attachment `json:"attachments,omitempty"`
	SenderAgentID  string              `json:"senderAgentId,omitempty"`
	TaskID         string              `json:"taskId,omitempty"`
	MessageID      string              `json:"messageId"`
	RunID          string              `json:"runId"`
	ThreadID       string              `json:"threadId"`
	ExpectedTurnID string              `json:"expectedTurnId"`
	Prompt         string              `json:"prompt"`
}

// HTTPError preserves whether the worker definitively rejected delivery.
type HTTPError struct {
	StatusCode int
	Message    string
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("runtime manager HTTP %d: %s", e.StatusCode, e.Message)
}

type TurnResponse struct {
	AfterSequence int64  `json:"afterSequence,omitempty"`
	ThreadID      string `json:"threadId"`
	TurnID        string `json:"turnId"`
}
type DeviceCode struct {
	LoginID         string `json:"loginId"`
	VerificationURL string `json:"verificationUrl"`
	UserCode        string `json:"userCode"`
}
type ProviderStatus struct {
	State        string `json:"state"`
	Method       string `json:"method,omitempty"`
	AccountLabel string `json:"accountLabel,omitempty"`
}
type WorkerEvent struct {
	Sequence int64           `json:"sequence"`
	Type     string          `json:"type"`
	Payload  json.RawMessage `json:"payload"`
}
type EventBatch struct {
	Events []WorkerEvent `json:"events"`
	Status string        `json:"status"`
	Error  string        `json:"error,omitempty"`
}

func (c *Client) do(ctx context.Context, method, path string, in, out any) error {
	return c.doWithHTTPClient(ctx, c.http, method, path, in, out)
}

func (c *Client) doWithHTTPClient(ctx context.Context, client *http.Client, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return &HTTPError{StatusCode: resp.StatusCode, Message: strings.TrimSpace(string(b))}
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}

func (c *Client) StartAgent(ctx context.Context, a AgentConfig) error {
	// Container startup includes waiting for worker readiness. Give it a larger
	// budget without changing the timeout of concurrent ordinary requests.
	client := *c.http
	client.Timeout = 90 * time.Second
	return c.doWithHTTPClient(ctx, &client, http.MethodPost, "/v1/agents/"+url.PathEscape(a.ID)+"/start", a, nil)
}
func (c *Client) StopAgent(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodPost, "/v1/agents/"+url.PathEscape(id)+"/stop", nil, nil)
}
func (c *Client) StartTurn(ctx context.Context, agentID string, in TurnRequest) (TurnResponse, error) {
	var out TurnResponse
	err := c.do(ctx, http.MethodPost, "/v1/agents/"+url.PathEscape(agentID)+"/turns", in, &out)
	return out, err
}
func (c *Client) SteerTurn(ctx context.Context, agentID string, in SteerRequest) (TurnResponse, error) {
	var out TurnResponse
	err := c.do(ctx, http.MethodPost, "/v1/agents/"+url.PathEscape(agentID)+"/steer", in, &out)
	return out, err
}
func (c *Client) Interrupt(ctx context.Context, agentID, threadID, turnID string) error {
	return c.do(ctx, http.MethodPost, "/v1/agents/"+url.PathEscape(agentID)+"/interrupt", map[string]string{"threadId": threadID, "turnId": turnID}, nil)
}
func (c *Client) DeviceLogin(ctx context.Context, agentID string) (DeviceCode, error) {
	var out DeviceCode
	err := c.do(ctx, http.MethodPost, "/v1/agents/"+url.PathEscape(agentID)+"/auth/device", nil, &out)
	return out, err
}
func (c *Client) APIKeyLogin(ctx context.Context, agentID, key string) error {
	return c.do(ctx, http.MethodPost, "/v1/agents/"+url.PathEscape(agentID)+"/auth/api-key", map[string]string{"apiKey": key}, nil)
}
func (c *Client) Logout(ctx context.Context, agentID string) error {
	return c.do(ctx, http.MethodPost, "/v1/agents/"+url.PathEscape(agentID)+"/auth/logout", nil, nil)
}
func (c *Client) AuthStatus(ctx context.Context, agentID string) (ProviderStatus, error) {
	var out ProviderStatus
	err := c.do(ctx, http.MethodGet, "/v1/agents/"+url.PathEscape(agentID)+"/auth/status", nil, &out)
	return out, err
}
func (c *Client) Events(ctx context.Context, agentID, runID string, after int64) (EventBatch, error) {
	var out EventBatch
	err := c.do(ctx, http.MethodGet, fmt.Sprintf("/v1/agents/%s/runs/%s/events?after=%d", url.PathEscape(agentID), url.PathEscape(runID), after), nil, &out)
	return out, err
}
func (c *Client) SetDesktopLease(ctx context.Context, agentID, holder string, generation int64) error {
	return c.do(ctx, http.MethodPost, "/v1/agents/"+url.PathEscape(agentID)+"/desktop/lease", map[string]any{"holder": holder, "generation": generation}, nil)
}
func (c *Client) ProxyURL(agentID, kind, rest string) *url.URL {
	u, _ := url.Parse(c.base + "/v1/agents/" + url.PathEscape(agentID) + "/" + kind + "/" + strings.TrimLeft(rest, "/"))
	return u
}
func (c *Client) Token() string { return c.token }
