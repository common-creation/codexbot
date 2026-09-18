package agentworker

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/common-creation/codexbot/internal/appserver"
	"github.com/common-creation/codexbot/internal/domain"
	"github.com/common-creation/codexbot/internal/runtimeclient"
)

func TestSteerAppServerHelper(t *testing.T) {
	if os.Getenv("CODEXBOT_TEST_STEER_HELPER") != "1" {
		return
	}
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 4096), domain.MaxMessageRequestBytes)
	for scanner.Scan() {
		var request struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if json.Unmarshal(scanner.Bytes(), &request) != nil {
			os.Exit(2)
		}
		if len(request.ID) == 0 {
			continue
		}
		result := map[string]any{}
		if request.Method == "turn/steer" {
			f, err := os.OpenFile(os.Getenv("CODEXBOT_TEST_STEER_LOG"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
			if err != nil {
				os.Exit(3)
			}
			_ = json.NewEncoder(f).Encode(request.Params)
			_ = f.Close()
			switch os.Getenv("CODEXBOT_TEST_STEER_MODE") {
			case "completed-before-response":
				_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"method": "item/completed", "params": map[string]any{"threadId": "thread-1", "turnId": "turn-1", "item": map[string]any{"id": "answer", "type": "agentMessage", "text": "追加情報を確認しました"}}})
				_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"method": "turn/completed", "params": map[string]any{"threadId": "thread-1", "turn": map[string]any{"id": "turn-1", "status": "completed"}}})
				if os.WriteFile(os.Getenv("CODEXBOT_TEST_STEER_RELEASE")+".notified", nil, 0600) != nil {
					os.Exit(5)
				}
				deadline := time.Now().Add(10 * time.Second)
				for {
					if _, err := os.Stat(os.Getenv("CODEXBOT_TEST_STEER_RELEASE")); err == nil {
						break
					}
					if time.Now().After(deadline) {
						os.Exit(4)
					}
					time.Sleep(5 * time.Millisecond)
				}
			case "unknown":
				os.Exit(0)
			case "reject":
				_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"id": request.ID, "error": map[string]any{"code": -32600, "message": "turn changed"}})
				continue
			}
			result["turnId"] = "turn-1"
		}
		_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"id": request.ID, "result": result})
	}
	os.Exit(0)
}

func TestSteerCompletionDoesNotFinalizeEventsBeforeInputIsPersisted(t *testing.T) {
	dir := t.TempDir()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(dir, "server")
	if err := os.WriteFile(script, []byte(fmt.Sprintf("#!/bin/sh\nexec '%s' -test.run=^TestSteerAppServerHelper$\n", strings.ReplaceAll(exe, "'", "'\"'\"'"))), 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	release := filepath.Join(dir, "release")
	client, err := appserver.Start(ctx, appserver.Config{Executable: script, Env: []string{"CODEXBOT_TEST_STEER_HELPER=1", "CODEXBOT_TEST_STEER_LOG=" + filepath.Join(dir, "requests"), "CODEXBOT_TEST_STEER_MODE=completed-before-response", "CODEXBOT_TEST_STEER_RELEASE=" + release}, ClientInfo: appserver.ClientInfo{Name: "test", Version: "1"}})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if err := os.Mkdir(filepath.Join(dir, "outbox"), 0700); err != nil {
		t.Fatal(err)
	}
	runID := "00000000-0000-0000-0000-000000000001"
	w := &Worker{cfg: Config{ProfileDir: dir}, client: client, logger: slog.Default(), active: &activeRun{RunID: runID, ThreadID: "thread-1", TurnID: "turn-1", Status: "running"}}
	go w.consumeNotifications(ctx)
	in := runtimeclient.SteerRequest{MessageID: "00000000-0000-0000-0000-000000000002", RunID: runID, ThreadID: "thread-1", ExpectedTurnID: "turn-1", Prompt: "追加のディレクトリを確認してください"}
	body, _ := json.Marshal(in)
	steerDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		rec := httptest.NewRecorder()
		w.steer(rec, httptest.NewRequest("POST", "/v1/steer", bytes.NewReader(body)))
		steerDone <- rec
	}()
	for {
		if _, err := os.Stat(release + ".notified"); err == nil {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("helper did not send output notifications")
		case <-time.After(5 * time.Millisecond):
		}
	}
	poll := func() *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("GET", "/v1/runs/"+runID+"/events", nil)
		req.SetPathValue("runID", runID)
		w.events(rec, req)
		return rec
	}
	pollDone := make(chan *httptest.ResponseRecorder, 1)
	go func() { pollDone <- poll() }()
	select {
	case rec := <-pollDone:
		var batch runtimeclient.EventBatch
		if err := json.Unmarshal(rec.Body.Bytes(), &batch); err != nil {
			t.Fatal(err)
		}
		if batch.Status != "running" || len(batch.Events) != 0 {
			t.Fatalf("output became visible before accepted input: %+v", batch)
		}
	case <-time.After(time.Second):
		t.Fatal("event polling blocked on steer RPC")
	}
	if err := os.WriteFile(release, nil, 0600); err != nil {
		t.Fatal(err)
	}
	select {
	case rec := <-steerDone:
		if rec.Code != 200 {
			t.Fatalf("steer status=%d body=%s", rec.Code, rec.Body.String())
		}
	case <-ctx.Done():
		t.Fatal("steer response timed out")
	}
	for {
		w.mu.Lock()
		completed := w.active.Status == "completed"
		w.mu.Unlock()
		if completed {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("buffered completion was not consumed after acknowledgement")
		case <-time.After(5 * time.Millisecond):
		}
	}
	var batch runtimeclient.EventBatch
	if err := json.Unmarshal(poll().Body.Bytes(), &batch); err != nil {
		t.Fatal(err)
	}
	if batch.Status != "completed" || len(batch.Events) != 4 || batch.Events[0].Type != "message.user" || batch.Events[1].Type != "run.steered" || batch.Events[2].Type != "item/completed" || batch.Events[3].Type != "turn/completed" {
		t.Fatalf("final batch missing accepted input: %+v", batch)
	}
}

func TestSteerDeliveryIsDurableAndNeverRetriedAfterUnknown(t *testing.T) {
	for _, scenario := range []struct {
		mode        string
		attachments bool
		delegated   bool
	}{{"accepted", false, false}, {"reject", false, false}, {"unknown", false, false}, {"accepted", true, false}, {"reject", true, false}, {"unknown", true, false}, {"accepted", false, true}} {
		mode := scenario.mode
		t.Run(fmt.Sprintf("%s/attachments=%t/delegated=%t", mode, scenario.attachments, scenario.delegated), func(t *testing.T) {
			dir := t.TempDir()
			exe, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			script := filepath.Join(dir, "server")
			if err := os.WriteFile(script, []byte(fmt.Sprintf("#!/bin/sh\nexec '%s' -test.run=^TestSteerAppServerHelper$\n", strings.ReplaceAll(exe, "'", "'\"'\"'"))), 0700); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			logfile := filepath.Join(dir, "requests")
			client, err := appserver.Start(ctx, appserver.Config{Executable: script, Env: []string{"CODEXBOT_TEST_STEER_HELPER=1", "CODEXBOT_TEST_STEER_LOG=" + logfile, "CODEXBOT_TEST_STEER_MODE=" + mode}, ClientInfo: appserver.ClientInfo{Name: "test", Version: "1"}})
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			if err := os.Mkdir(filepath.Join(dir, "outbox"), 0700); err != nil {
				t.Fatal(err)
			}
			runID := "00000000-0000-0000-0000-000000000001"
			input := runtimeclient.SteerRequest{MessageID: "00000000-0000-0000-0000-000000000002", RunID: runID, ThreadID: "thread-1", ExpectedTurnID: "turn-1", Prompt: "追加の指示"}
			if scenario.delegated {
				input.SenderAgentID = "sender-agent"
				input.TaskID = "delegated-task"
			}
			attachmentHome := t.TempDir()
			png := "\x89PNG\r\n\x1a\nimage-data"
			if scenario.attachments {
				input.Prompt = ""
				// Exercise the full attachment body limit rather than the old
				// generic 1 MiB JSON decoder used for text-only steering.
				input.Attachments = []domain.Attachment{{Name: "notes.txt", Data: base64.StdEncoding.EncodeToString([]byte(strings.Repeat("x", 1<<20)))}, {Name: "view.png", Data: base64.StdEncoding.EncodeToString([]byte(png))}}
			}
			worker := &Worker{cfg: Config{ProfileDir: dir, AgentHomeDir: attachmentHome}, client: client, logger: slog.Default(), active: &activeRun{RunID: runID, ThreadID: "thread-1", TurnID: "turn-1", Status: "running", Sequence: 17, Prompt: "original"}}
			expected := map[string]int{"accepted": 200, "reject": 409, "unknown": 503}[mode]
			call := func(w *Worker, in runtimeclient.SteerRequest) int {
				body, _ := json.Marshal(in)
				rec := httptest.NewRecorder()
				w.steer(rec, httptest.NewRequest("POST", "/v1/steer", bytes.NewReader(body)))
				return rec.Code
			}
			if got := call(worker, input); got != expected {
				t.Fatalf("initial status=%d want=%d", got, expected)
			}
			if mode == "accepted" {
				if want := "original\n\n" + domain.AttachmentPrompt(input.Prompt, input.Attachments); worker.active.Prompt != want {
					t.Fatalf("active prompt=%q want=%q", worker.active.Prompt, want)
				}
				raw, err := os.ReadFile(filepath.Join(dir, "steering", input.MessageID+".json"))
				if err != nil {
					t.Fatal(err)
				}
				var record steerDelivery
				if err := json.Unmarshal(raw, &record); err != nil {
					t.Fatal(err)
				}
				if record.Response.AfterSequence != 17 {
					t.Fatalf("afterSequence=%d", record.Response.AfterSequence)
				}
			} else if worker.active.Prompt != "original" {
				t.Fatalf("unaccepted input changed prompt: %q", worker.active.Prompt)
			}
			// Simulate restart without any active run or app-server client. The ledger
			// alone must determine the response and prevent a second dispatch.
			restarted := &Worker{cfg: Config{ProfileDir: dir}}
			if got := call(restarted, input); got != expected {
				t.Fatalf("retry status=%d want=%d", got, expected)
			}
			if mode == "accepted" {
				events, err := os.ReadFile(worker.eventPath(runID))
				if err != nil {
					t.Fatal(err)
				}
				var visible []map[string]string
				for _, line := range bytes.Split(bytes.TrimSpace(events), []byte("\n")) {
					var event workerEvent
					if err := json.Unmarshal(line, &event); err != nil {
						t.Fatal(err)
					}
					if event.Type == "message.user" {
						if event.Sequence <= 17 {
							t.Fatalf("input cursor lost: %+v", event)
						}
						var payload map[string]string
						if err := json.Unmarshal(event.Payload, &payload); err != nil {
							t.Fatal(err)
						}
						visible = append(visible, payload)
					}
				}
				if len(visible) != 1 || visible[0]["id"] != "steer-"+input.MessageID || visible[0]["text"] != domain.AttachmentPrompt(input.Prompt, input.Attachments) {
					t.Fatalf("visible inputs=%+v", visible)
				}
				wantSource := "steer"
				if scenario.delegated {
					wantSource = "collaboration"
				}
				if visible[0]["source"] != wantSource || visible[0]["senderAgentId"] != input.SenderAgentID || visible[0]["taskId"] != input.TaskID {
					t.Fatalf("input attribution changed after retry: %+v", visible[0])
				}
			}
			if scenario.attachments {
				changed := input
				changed.Attachments = append([]domain.Attachment(nil), input.Attachments...)
				changed.Attachments[0].Data = base64.StdEncoding.EncodeToString([]byte("changed bytes"))
				if got := call(restarted, changed); got != 422 {
					t.Fatalf("changed attachment status=%d", got)
				}
			}
			input.Prompt = "different"
			if got := call(restarted, input); got != 422 {
				t.Fatalf("changed input status=%d", got)
			}
			raw, err := os.ReadFile(logfile)
			if err != nil {
				t.Fatal(err)
			}
			if lines := strings.Count(string(raw), "\n"); lines != 1 {
				t.Fatalf("dispatched %d times: %s", lines, raw)
			}
			var params appserver.TurnSteerParams
			if err := json.Unmarshal(bytes.TrimSpace(raw), &params); err != nil {
				t.Fatal(err)
			}
			if params.ClientUserMessageID != input.MessageID || params.ExpectedTurnID != "turn-1" || params.ThreadID != "thread-1" {
				t.Fatalf("params=%+v", params)
			}
			if scenario.attachments {
				if len(params.Input) != 2 || params.Input[0].Type != appserver.InputText || !strings.Contains(params.Input[0].Text, "notes.txt") || params.Input[1].Type != appserver.InputLocalImage {
					t.Fatalf("attachment inputs=%+v", params.Input)
				}
				data, err := os.ReadFile(params.Input[1].Path)
				if mode == "reject" {
					if !os.IsNotExist(err) {
						t.Fatalf("rejected attachment retained: %v", err)
					}
				} else if err != nil || string(data) != png {
					t.Fatalf("accepted or unknown attachment lost: %q %v", data, err)
				}
				entries, err := os.ReadDir(attachmentHome)
				want := 1
				if mode == "reject" {
					want = 0
				}
				if err != nil || len(entries) != want {
					t.Fatalf("attachment dirs=%v err=%v", entries, err)
				}
			}
		})
	}
}

func TestSteerRejectsStaleTurnBeforeDispatch(t *testing.T) {
	w := &Worker{cfg: Config{ProfileDir: t.TempDir()}, active: &activeRun{RunID: "00000000-0000-0000-0000-000000000001", ThreadID: "thread-1", TurnID: "new-turn", Status: "running"}}
	in := runtimeclient.SteerRequest{MessageID: "00000000-0000-0000-0000-000000000002", RunID: w.active.RunID, ThreadID: w.active.ThreadID, ExpectedTurnID: "old-turn", Prompt: "hello"}
	body, _ := json.Marshal(in)
	rec := httptest.NewRecorder()
	w.steer(rec, httptest.NewRequest("POST", "/v1/steer", bytes.NewReader(body)))
	if rec.Code != 409 {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestSteerRepairsAcceptedInputAfterInterruptedEventPersistence(t *testing.T) {
	for _, alreadyAppended := range []bool{false, true} {
		t.Run(fmt.Sprint(alreadyAppended), func(t *testing.T) {
			dir := t.TempDir()
			if err := os.Mkdir(filepath.Join(dir, "outbox"), 0700); err != nil {
				t.Fatal(err)
			}
			in := runtimeclient.SteerRequest{MessageID: "00000000-0000-0000-0000-000000000002", RunID: "00000000-0000-0000-0000-000000000001", ThreadID: "thread", ExpectedTurnID: "turn", Prompt: "追加"}
			raw, _ := json.Marshal(in)
			digest := sha256.Sum256(raw)
			payload, _ := json.Marshal(map[string]string{"id": "steer-" + in.MessageID, "text": in.Prompt})
			delivery := steerDelivery{Fingerprint: hex.EncodeToString(digest[:]), State: "accepted", Response: runtimeclient.TurnResponse{ThreadID: in.ThreadID, TurnID: in.ExpectedTurnID, AfterSequence: 17}, InputEvent: payload}
			path := filepath.Join(dir, "steering", in.MessageID+".json")
			if err := saveSteerDelivery(path, delivery); err != nil {
				t.Fatal(err)
			}
			worker := &Worker{cfg: Config{ProfileDir: dir}}
			if alreadyAppended {
				event, _ := json.Marshal(workerEvent{Sequence: 18, Type: "message.user", Payload: payload})
				if err := os.WriteFile(worker.eventPath(in.RunID), append(event, '\n'), 0600); err != nil {
					t.Fatal(err)
				}
			}
			for i := 0; i < 2; i++ {
				rec := httptest.NewRecorder()
				worker.steer(rec, httptest.NewRequest("POST", "/v1/steer", bytes.NewReader(raw)))
				if rec.Code != 200 {
					t.Fatalf("retry %d status=%d body=%s", i, rec.Code, rec.Body.String())
				}
			}
			events, err := os.ReadFile(worker.eventPath(in.RunID))
			if err != nil {
				t.Fatal(err)
			}
			if bytes.Count(events, []byte("\n")) != 1 {
				t.Fatalf("duplicate input events: %s", events)
			}
			var event workerEvent
			if err := json.Unmarshal(bytes.TrimSpace(events), &event); err != nil {
				t.Fatal(err)
			}
			if event.Sequence != 18 || event.Type != "message.user" {
				t.Fatalf("event=%+v", event)
			}
		})
	}
}

func TestSteerRejectsInvalidAttachmentsBeforeDispatch(t *testing.T) {
	for _, attachments := range [][]domain.Attachment{{{Name: "../unsafe", Data: "eA=="}}, {{Name: "bad.txt", Data: "invalid base64"}}} {
		worker := &Worker{cfg: Config{ProfileDir: t.TempDir()}}
		in := runtimeclient.SteerRequest{MessageID: "00000000-0000-0000-0000-000000000002", RunID: "00000000-0000-0000-0000-000000000001", ThreadID: "thread", ExpectedTurnID: "turn", Attachments: attachments}
		body, _ := json.Marshal(in)
		rec := httptest.NewRecorder()
		worker.steer(rec, httptest.NewRequest("POST", "/v1/steer", bytes.NewReader(body)))
		if rec.Code != 400 {
			t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
		}
	}
}
