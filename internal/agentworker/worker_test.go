package agentworker

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/common-creation/codexbot/internal/appserver"
	"github.com/common-creation/codexbot/internal/domain"
)

func TestNotificationErrorDistinguishesRetries(t *testing.T) {
	message, retry := notificationError(json.RawMessage(`{"error":{"message":"Reconnecting... 2/5"},"willRetry":true}`))
	if message != "Reconnecting... 2/5" || !retry {
		t.Fatalf("message=%q retry=%v", message, retry)
	}
	message, retry = notificationError(json.RawMessage(`{"error":{"message":"Connection failed"},"willRetry":false}`))
	if message != "Connection failed" || retry {
		t.Fatalf("message=%q retry=%v", message, retry)
	}
}

func TestTurnCompletionPreservesNestedError(t *testing.T) {
	status, message := turnCompletion(json.RawMessage(`{"turn":{"status":"failed","error":{"message":"upstream unavailable"}}}`))
	if status != "failed" || message != "upstream unavailable" {
		t.Fatalf("status=%q message=%q", status, message)
	}
}

func TestModelSettingsAppServerHelper(t *testing.T) {
	if os.Getenv("CODEXBOT_TEST_APP_SERVER") != "1" {
		return
	}
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params map[string]any  `json:"params"`
		}
		if json.Unmarshal(scanner.Bytes(), &req) != nil {
			os.Exit(2)
		}
		if len(req.ID) == 0 {
			continue
		}
		result := map[string]any{}
		switch req.Method {
		case "thread/start", "thread/resume":
			if req.Params["model"] != "selected-model" {
				os.Exit(3)
			}
			result["thread"] = map[string]string{"id": "thread-1"}
		case "turn/start":
			if os.Getenv("CODEXBOT_TEST_START_INTERNAL_ERROR") == "1" {
				_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"id": req.ID, "error": map[string]any{"code": -32603, "message": "internal failure after dispatch"}})
				continue
			}
			if req.Params["model"] != "selected-model" || req.Params["effort"] != "ultra" {
				os.Exit(4)
			}
			result["turn"] = map[string]string{"id": "turn-1"}
		}
		if path := os.Getenv("CODEXBOT_TEST_REQUEST_LOG"); path != "" && req.Method != "initialize" {
			file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
			if err != nil {
				os.Exit(5)
			}
			if err := json.NewEncoder(file).Encode(req); err != nil {
				os.Exit(6)
			}
			_ = file.Close()
		}
		_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"id": req.ID, "result": result})
	}
	os.Exit(0)
}

func TestStartTurnForwardsModelSettingsOnNewAndResumedThreads(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	script := filepath.Join(dir, "app-server")
	// The test binary supplies a deterministic JSON-RPC server, so this exercises
	// HTTP decoding through the actual app-server client without a Codex install.
	contents := fmt.Sprintf("#!/bin/sh\nexec '%s' -test.run=^TestModelSettingsAppServerHelper$\n", strings.ReplaceAll(exe, "'", "'\"'\"'"))
	if err := os.WriteFile(script, []byte(contents), 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client, err := appserver.Start(ctx, appserver.Config{Executable: script, Env: []string{"CODEXBOT_TEST_APP_SERVER=1"}, ClientInfo: appserver.ClientInfo{Name: "test", Version: "1"}})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if err := os.MkdirAll(filepath.Join(dir, "outbox"), 0700); err != nil {
		t.Fatal(err)
	}
	for _, threadID := range []string{"", "thread-1"} {
		w := &Worker{cfg: Config{ProfileDir: dir}, client: client}
		body := fmt.Sprintf(`{"runId":"11111111-1111-4111-8111-111111111111","threadId":%q,"prompt":"Hello","model":"selected-model","effort":"ultra"}`, threadID)
		req := httptest.NewRequest("POST", "/v1/turns", strings.NewReader(body)).WithContext(ctx)
		response := httptest.NewRecorder()
		w.startTurn(response, req)
		if response.Code != 202 {
			t.Fatalf("thread=%q status=%d body=%s", threadID, response.Code, response.Body.String())
		}
	}
}

func TestStartTurnForwardsPermissionForNewAndResumedThreads(t *testing.T) {
	for _, tc := range []struct {
		name, permission, policy, reviewer, sandbox, sandboxType string
		wantPermission                                           domain.PermissionMode
	}{
		{"auto", `,"permission":"auto"`, "on-request", "auto_review", "workspace-write", "workspaceWrite", domain.PermissionAuto},
		{"full access", `,"permission":"full-access"`, "never", "user", "danger-full-access", "dangerFullAccess", domain.PermissionFullAccess},
		{"legacy autoApprove defaults to Auto", `,"autoApprove":true`, "on-request", "auto_review", "workspace-write", "workspaceWrite", domain.PermissionAuto},
	} {
		for _, threadID := range []string{"", "thread-1"} {
			t.Run(tc.name+"/thread="+threadID, func(t *testing.T) {
				dir := t.TempDir()
				exe, err := os.Executable()
				if err != nil {
					t.Fatal(err)
				}
				script := filepath.Join(dir, "app-server")
				contents := fmt.Sprintf("#!/bin/sh\nexec '%s' -test.run=^TestModelSettingsAppServerHelper$\n", strings.ReplaceAll(exe, "'", "'\"'\"'"))
				if err := os.WriteFile(script, []byte(contents), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.MkdirAll(filepath.Join(dir, "outbox"), 0700); err != nil {
					t.Fatal(err)
				}
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				logPath := filepath.Join(dir, "requests.jsonl")
				client, err := appserver.Start(ctx, appserver.Config{
					Executable: script,
					Env:        []string{"CODEXBOT_TEST_APP_SERVER=1", "CODEXBOT_TEST_REQUEST_LOG=" + logPath},
					ClientInfo: appserver.ClientInfo{Name: "test", Version: "1"},
				})
				if err != nil {
					t.Fatal(err)
				}
				defer client.Close()
				w := &Worker{cfg: Config{ProfileDir: dir}, client: client, ctx: ctx}
				body := fmt.Sprintf(`{"runId":"11111111-1111-4111-8111-111111111111","threadId":%q,"prompt":"Hello","model":"selected-model","effort":"ultra"%s}`, threadID, tc.permission)
				requestCtx, cancelRequest := context.WithCancel(ctx)
				req := httptest.NewRequest(http.MethodPost, "/v1/turns", strings.NewReader(body)).WithContext(requestCtx)
				response := httptest.NewRecorder()
				w.startTurn(response, req)
				cancelRequest()
				if response.Code != http.StatusAccepted {
					t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
				}
				defer w.active.cancelRun()
				if w.active.Permission != tc.wantPermission || w.active.Prompt != "Hello" || w.active.ThreadID != "thread-1" || w.active.TurnID != "turn-1" {
					t.Fatalf("permission or context was not stored on run: %+v", w.active)
				}
				if w.active.runContext.Err() != nil {
					t.Fatal("run lifetime incorrectly tied to completed HTTP request")
				}
				data, err := os.ReadFile(logPath)
				if err != nil {
					t.Fatal(err)
				}
				lines := strings.Split(strings.TrimSpace(string(data)), "\n")
				if len(lines) != 2 {
					t.Fatalf("expected thread and turn calls: %s", data)
				}
				wantThreadMethod := "thread/start"
				if threadID != "" {
					wantThreadMethod = "thread/resume"
				}
				for i, line := range lines {
					var request struct {
						Method string         `json:"method"`
						Params map[string]any `json:"params"`
					}
					if err := json.Unmarshal([]byte(line), &request); err != nil {
						t.Fatal(err)
					}
					wantMethod := wantThreadMethod
					if i == 1 {
						wantMethod = "turn/start"
					}
					if request.Method != wantMethod || request.Params["approvalPolicy"] != tc.policy || request.Params["approvalsReviewer"] != tc.reviewer {
						t.Fatalf("permission routing lost on %s: %s", wantMethod, line)
					}
					if i == 0 && request.Params["sandbox"] != tc.sandbox {
						t.Fatalf("incorrect thread sandbox: %s", line)
					}
					if i == 1 {
						policy, ok := request.Params["sandboxPolicy"].(map[string]any)
						if !ok || policy["type"] != tc.sandboxType || policy["networkAccess"] == true {
							t.Fatalf("incorrect turn sandbox: %s", line)
						}
					}
				}
			})
		}
	}
}

func TestStartTurnRejectsInvalidPermissionBeforeCallingAppServer(t *testing.T) {
	w := &Worker{}
	req := httptest.NewRequest(http.MethodPost, "/v1/turns", strings.NewReader(`{"runId":"11111111-1111-4111-8111-111111111111","prompt":"Hello","permission":"manual"}`))
	response := httptest.NewRecorder()
	w.startTurn(response, req)
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "invalid permission") {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if w.active != nil {
		t.Fatal("invalid permission created a run")
	}
}

func TestEventsDoNotInventRunningState(t *testing.T) {
	const runID = "00000000-0000-0000-0000-000000000001"
	w := &Worker{cfg: Config{ProfileDir: t.TempDir()}}
	read := func() string {
		r := httptest.NewRequest("GET", "/v1/runs/"+runID+"/events", nil)
		r.SetPathValue("runID", runID)
		rec := httptest.NewRecorder()
		w.events(rec, r)
		var batch struct {
			Status string `json:"status"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &batch); err != nil {
			t.Fatal(err)
		}
		return batch.Status
	}
	if got := read(); got != "unknown" {
		t.Fatalf("missing run=%s", got)
	}
	w.active = &activeRun{RunID: runID, Status: "starting"}
	if got := read(); got != "starting" {
		t.Fatalf("active run=%s", got)
	}
	if err := os.MkdirAll(filepath.Join(w.cfg.ProfileDir, "outbox"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := atomicWrite(w.statusPath(runID), []byte(`{"status":"starting"}`), 0600); err != nil {
		t.Fatal(err)
	}
	w.active = nil
	recoverOutbox(filepath.Join(w.cfg.ProfileDir, "outbox"))
	if got := read(); got != "unknown" {
		t.Fatalf("restarted run=%s", got)
	}
}

func TestStartTurnInternalRPCFailureRemainsUnknown(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	script := filepath.Join(dir, "app-server")
	contents := fmt.Sprintf("#!/bin/sh\nexec '%s' -test.run=^TestModelSettingsAppServerHelper$\n", strings.ReplaceAll(exe, "'", "'\"'\"'"))
	if err := os.WriteFile(script, []byte(contents), 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client, err := appserver.Start(ctx, appserver.Config{Executable: script, Env: []string{"CODEXBOT_TEST_APP_SERVER=1", "CODEXBOT_TEST_START_INTERNAL_ERROR=1"}, ClientInfo: appserver.ClientInfo{Name: "test", Version: "1"}})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if err := os.MkdirAll(filepath.Join(dir, "outbox"), 0700); err != nil {
		t.Fatal(err)
	}
	w := &Worker{cfg: Config{ProfileDir: dir}, client: client}
	const runID = "11111111-1111-4111-8111-111111111111"
	req := httptest.NewRequest("POST", "/v1/turns", strings.NewReader(`{"runId":"`+runID+`","prompt":"Hello","model":"selected-model","effort":"ultra"}`)).WithContext(ctx)
	rec := httptest.NewRecorder()
	w.startTurn(rec, req)
	if rec.Code != 502 {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if w.active == nil || w.active.Status != "unknown" {
		t.Fatalf("active=%+v", w.active)
	}
	raw, err := os.ReadFile(w.statusPath(runID))
	if err != nil {
		t.Fatal(err)
	}
	var state struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(raw, &state); err != nil {
		t.Fatal(err)
	}
	if state.Status != "unknown" {
		t.Fatalf("persisted status=%s", state.Status)
	}
}
