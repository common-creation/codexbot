package agentworker

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/common-creation/codexbot/internal/appserver"
	"github.com/common-creation/codexbot/internal/domain"
)

// Captured from Codex 0.153.4 with a local Responses provider and a harmless
// MCP server. Codex synthesizes this elicitation before calling the MCP tool.
const observedMCPApprovalParams = `{
	"threadId":"thread-1","turnId":"turn-1","serverName":"probe","mode":"form",
	"_meta":{"codex_approval_kind":"mcp_tool_call","persist":["session","always"],
		"tool_description":"Return a fixed harmless string; no external access.",
		"tool_params":{},"tool_params_display":[]},
	"message":"Allow the probe MCP server to run tool \"snapshot\"?",
	"requestedSchema":{"type":"object","properties":{}}
}`

func TestMCPApprovalAppServerHelper(t *testing.T) {
	if os.Getenv("CODEXBOT_TEST_MCP_APPROVAL_SERVER") != "1" {
		return
	}
	encoder := json.NewEncoder(os.Stdout)
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		var message struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		if json.Unmarshal(scanner.Bytes(), &message) != nil {
			os.Exit(2)
		}
		switch message.Method {
		case "initialize":
			_ = encoder.Encode(map[string]any{"id": message.ID, "result": map[string]any{}})
		case "initialized":
			if os.Getenv("CODEXBOT_TEST_APPROVAL_METHOD") == "probe/notifications" {
				var notifications []json.RawMessage
				if json.Unmarshal([]byte(os.Getenv("CODEXBOT_TEST_APPROVAL_PARAMS")), &notifications) != nil {
					os.Exit(3)
				}
				for _, notification := range notifications {
					_ = encoder.Encode(notification)
				}
				continue
			}
			_ = encoder.Encode(map[string]any{
				"id": 0, "method": os.Getenv("CODEXBOT_TEST_APPROVAL_METHOD"),
				"params": json.RawMessage(os.Getenv("CODEXBOT_TEST_APPROVAL_PARAMS")),
			})
		case "":
			if len(message.ID) == 0 {
				continue
			}
			// Report the actual wire response back through the notification channel.
			_ = encoder.Encode(map[string]any{"method": "probe/approvalResponse", "params": json.RawMessage(scanner.Bytes())})
		}
	}
	os.Exit(0)
}

func newMCPApprovalWorker(t *testing.T, active *activeRun, method, params string) (*Worker, context.Context) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	script := filepath.Join(dir, "app-server")
	contents := fmt.Sprintf("#!/bin/sh\nexec '%s' -test.run=^TestMCPApprovalAppServerHelper$\n", strings.ReplaceAll(exe, "'", "'\"'\"'"))
	if err := os.WriteFile(script, []byte(contents), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "outbox"), 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	client, err := appserver.Start(ctx, appserver.Config{
		Executable: script,
		Env: []string{
			"CODEXBOT_TEST_MCP_APPROVAL_SERVER=1",
			"CODEXBOT_TEST_APPROVAL_METHOD=" + method,
			"CODEXBOT_TEST_APPROVAL_PARAMS=" + params,
		},
		ClientInfo: appserver.ClientInfo{Name: "test", Version: "1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	if active != nil {
		active.runContext, active.cancelRun = context.WithCancel(ctx)
		t.Cleanup(active.cancelRun)
	}
	w := &Worker{
		cfg: Config{ProfileDir: dir}, client: client,
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		active: active,
	}
	go w.consumeApprovals(ctx)
	return w, ctx
}

func readMCPApprovalEvents(t *testing.T, w *Worker) []workerEvent {
	t.Helper()
	w.mu.Lock()
	data, err := os.ReadFile(w.eventPath("run-1"))
	w.mu.Unlock()
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var events []workerEvent
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line == "" {
			continue
		}
		var event workerEvent
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatal(err)
		}
		events = append(events, event)
	}
	return events
}

func waitForMCPApprovalEvent(t *testing.T, ctx context.Context, w *Worker, eventType string) workerEvent {
	t.Helper()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		for _, event := range readMCPApprovalEvents(t, w) {
			if event.Type == eventType {
				return event
			}
		}
		select {
		case <-ctx.Done():
			t.Fatalf("waiting for %s: %v", eventType, ctx.Err())
		case <-ticker.C:
		}
	}
}

func assertMCPJSONEqual(t *testing.T, got json.RawMessage, want string) {
	t.Helper()
	var actual, expected any
	if err := json.Unmarshal(got, &actual); err != nil {
		t.Fatalf("invalid JSON %q: %v", got, err)
	}
	if err := json.Unmarshal([]byte(want), &expected); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(actual, expected) {
		t.Fatalf("JSON = %s; want %s", got, want)
	}
}

func assertMCPWireResponse(t *testing.T, ctx context.Context, w *Worker, want string) {
	t.Helper()
	select {
	case notification := <-w.client.Notifications():
		if notification.Method != "probe/approvalResponse" {
			t.Fatalf("unexpected notification: %#v", notification)
		}
		var response struct {
			ID     json.RawMessage `json:"id"`
			Result json.RawMessage `json:"result"`
			Error  json.RawMessage `json:"error"`
		}
		if err := json.Unmarshal(notification.Params, &response); err != nil {
			t.Fatal(err)
		}
		if string(response.ID) != "0" || len(response.Error) != 0 {
			t.Fatalf("unexpected wire response: %s", notification.Params)
		}
		assertMCPJSONEqual(t, response.Result, want)
	case <-ctx.Done():
		t.Fatalf("waiting for MCP response: %v", ctx.Err())
	}
}

func automaticRun(mode domain.PermissionMode) *activeRun {
	return &activeRun{RunID: "run-1", ThreadID: "thread-1", TurnID: "turn-1", Status: "running", Permission: mode, Prompt: "Open Wikipedia in the browser."}
}

func assertNoManualApprovalEvents(t *testing.T, w *Worker) {
	t.Helper()
	for _, event := range readMCPApprovalEvents(t, w) {
		if event.Type == "approval.requested" || event.Type == "approval.resolved" {
			t.Fatalf("manual approval event emitted: %+v", event)
		}
	}
}

func TestAutoDeclinesAllUnreviewedPermissionTypesWithoutUserInput(t *testing.T) {
	for _, tc := range []struct{ method, params, response string }{
		{appserver.MethodMCPElicitation, observedMCPApprovalParams, `{"action":"decline","content":null}`},
		{appserver.MethodCommandApproval, `{"threadId":"thread-1","turnId":"turn-1","command":"npm test"}`, `{"decision":"decline"}`},
		{appserver.MethodFileApproval, `{"threadId":"thread-1","turnId":"turn-1","grantRoot":"/home/agent"}`, `{"decision":"decline"}`},
		{appserver.MethodPermissionReview, `{"threadId":"thread-1","turnId":"turn-1","permissions":{"network":{"enabled":true}}}`, `{"permissions":{},"scope":"turn"}`},
	} {
		t.Run(tc.method, func(t *testing.T) {
			w, ctx := newMCPApprovalWorker(t, automaticRun(domain.PermissionAuto), tc.method, tc.params)
			assertMCPWireResponse(t, ctx, w, tc.response)
			event := waitForMCPApprovalEvent(t, ctx, w, "approval.autoDeclined")
			if !strings.Contains(string(event.Payload), "Manual approval is unavailable") {
				t.Fatalf("missing fail-closed reason: %s", event.Payload)
			}
			assertNoManualApprovalEvents(t, w)
		})
	}
}

func TestFullAccessAcceptsEveryPermissionType(t *testing.T) {
	for _, tc := range []struct{ method, params, response string }{
		{appserver.MethodMCPElicitation, observedMCPApprovalParams, `{"action":"accept","content":{}}`},
		{appserver.MethodCommandApproval, `{"threadId":"thread-1","turnId":"turn-1","command":"npm test"}`, `{"decision":"accept"}`},
		{appserver.MethodFileApproval, `{"threadId":"thread-1","turnId":"turn-1","grantRoot":"/home/agent"}`, `{"decision":"accept"}`},
		{appserver.MethodPermissionReview, `{"threadId":"thread-1","turnId":"turn-1","permissions":{"network":{"enabled":true},"fileSystem":{"write":["/home/agent"]}}}`, `{"permissions":{"network":{"enabled":true},"fileSystem":{"write":["/home/agent"]}},"scope":"turn"}`},
	} {
		t.Run(tc.method, func(t *testing.T) {
			w, ctx := newMCPApprovalWorker(t, automaticRun(domain.PermissionFullAccess), tc.method, tc.params)
			assertMCPWireResponse(t, ctx, w, tc.response)
			_ = waitForMCPApprovalEvent(t, ctx, w, "approval.autoAccepted")
			assertNoManualApprovalEvents(t, w)
		})
	}
}

func TestAutomaticPermissionDeclinesStaleAndInactiveRequests(t *testing.T) {
	for _, tc := range []struct {
		name   string
		active *activeRun
	}{
		{"no active run", nil},
		{"different thread", &activeRun{RunID: "run-1", ThreadID: "other-thread", Status: "running", Permission: domain.PermissionFullAccess}},
		{"different turn", &activeRun{RunID: "run-1", ThreadID: "thread-1", TurnID: "other-turn", Status: "running", Permission: domain.PermissionFullAccess}},
		{"finished run", &activeRun{RunID: "run-1", ThreadID: "thread-1", TurnID: "turn-1", Status: "completed", Permission: domain.PermissionFullAccess}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w, ctx := newMCPApprovalWorker(t, tc.active, appserver.MethodMCPElicitation, observedMCPApprovalParams)
			assertMCPWireResponse(t, ctx, w, `{"action":"decline","content":null}`)
			assertNoManualApprovalEvents(t, w)
		})
	}
}

func TestAutomaticPermissionDoesNotInventMCPFormInput(t *testing.T) {
	for _, mode := range []domain.PermissionMode{domain.PermissionAuto, domain.PermissionFullAccess} {
		t.Run(string(mode), func(t *testing.T) {
			params := `{"threadId":"thread-1","turnId":"turn-1","serverName":"probe","mode":"form","message":"Enter a value","requestedSchema":{"type":"object","properties":{"value":{"type":"string"}},"required":["value"]}}`
			w, ctx := newMCPApprovalWorker(t, automaticRun(mode), appserver.MethodMCPElicitation, params)
			assertMCPWireResponse(t, ctx, w, `{"action":"decline","content":null}`)
			event := waitForMCPApprovalEvent(t, ctx, w, "approval.autoDeclined")
			if !strings.Contains(string(event.Payload), "input form") {
				t.Fatalf("input form was treated as a permission: %s", event.Payload)
			}
			assertNoManualApprovalEvents(t, w)
		})
	}
}

func TestNativeReviewNotificationsPreserveReviewsAndIgnoreOtherRuns(t *testing.T) {
	notifications := `[
		{"method":"turn/completed","params":{"threadId":"review-thread","turn":{"id":"review-turn","status":"completed"}}},
		{"method":"error","params":{"threadId":"review-thread","error":{"message":"review failed"},"willRetry":false}},
		{"method":"turn/completed","params":{"threadId":"thread-1","turn":{"id":"previous-turn","status":"failed"}}},
		{"method":"item/autoApprovalReview/started","params":{"threadId":"thread-1","turnId":"turn-1","reviewId":"review-1","review":{"status":"inProgress"}}},
		{"method":"item/autoApprovalReview/completed","params":{"threadId":"thread-1","turnId":"turn-1","reviewId":"review-1","review":{"status":"denied","rationale":"Unrelated external action"}}},
		{"method":"probe/finished","params":{"threadId":"thread-1","turnId":"turn-1"}}
	]`
	w, ctx := newMCPApprovalWorker(t, automaticRun(domain.PermissionAuto), "probe/notifications", notifications)
	go w.consumeNotifications(ctx)
	_ = waitForMCPApprovalEvent(t, ctx, w, "probe/finished")
	w.mu.Lock()
	status, runError := w.active.Status, w.active.Error
	w.mu.Unlock()
	if status != "running" || runError != "" {
		t.Fatalf("review subagent changed working run: status=%q error=%q", status, runError)
	}
	events := readMCPApprovalEvents(t, w)
	if len(events) != 3 || events[0].Type != "item/autoApprovalReview/started" || events[1].Type != "item/autoApprovalReview/completed" {
		t.Fatalf("unexpected forwarded notifications: %+v", events)
	}
	if !strings.Contains(string(events[1].Payload), "Unrelated external action") {
		t.Fatalf("native review reason was lost: %s", events[1].Payload)
	}
}
