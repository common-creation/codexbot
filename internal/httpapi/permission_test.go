package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/common-creation/codexbot/internal/domain"
	"github.com/common-creation/codexbot/internal/runtimeclient"
	"github.com/common-creation/codexbot/internal/store"
)

func permissionTestStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "permissions.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func TestCreateAgentPermission(t *testing.T) {
	st := permissionTestStore(t)
	runtime := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer runtime.Close()
	s := New(st, runtimeclient.New(runtime.URL, "token"), Config{}, nil)
	for _, tc := range []struct {
		name, field string
		status      int
		permission  domain.PermissionMode
	}{
		{"omitted", "", 201, domain.PermissionAuto},
		{"auto", `,"permission":"auto"`, 201, domain.PermissionAuto},
		{"full access", `,"permission":"full-access"`, 201, domain.PermissionFullAccess},
		{"empty", `,"permission":""`, 400, ""},
		{"null", `,"permission":null`, 400, ""},
		{"unknown", `,"permission":"manual"`, 400, ""},
		{"wrong type", `,"permission":true`, 400, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("POST", "/api/agents", strings.NewReader(`{"name":"Agent","rolePrompt":"Role"`+tc.field+`}`))
			response := httptest.NewRecorder()
			s.createAgent(response, req)
			if response.Code != tc.status {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			if tc.status != 201 {
				return
			}
			var created domain.Agent
			if err := json.Unmarshal(response.Body.Bytes(), &created); err != nil {
				t.Fatal(err)
			}
			stored, err := st.Agent(context.Background(), created.ID)
			if err != nil || created.Permission != tc.permission || stored.Permission != tc.permission {
				t.Fatalf("created=%+v stored=%+v err=%v", created, stored, err)
			}
		})
	}
}

func TestUpdateAgentPermissionValidationAndContext(t *testing.T) {
	st := permissionTestStore(t)
	ctx := context.Background()
	a := domain.Agent{ID: "agent", Name: "Agent", RolePrompt: "Role", Permission: domain.PermissionFullAccess, RoleVersion: 1, Status: "stopped", ProviderAuthState: "disconnected", CreatedAt: time.Now(), UpdatedAt: time.Now()}
	if err := st.CreateAgent(ctx, a); err != nil {
		t.Fatal(err)
	}
	c := domain.Conversation{ID: "conversation", AgentID: a.ID, Kind: "manual", RoleVersion: 1, CodexThreadID: "old-thread", CreatedAt: time.Now()}
	if err := st.CreateConversation(ctx, c); err != nil {
		t.Fatal(err)
	}
	s := New(st, runtimeclient.New("http://127.0.0.1:1", "token"), Config{}, nil)
	patch := func(field string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("PATCH", "/api/agents/agent", strings.NewReader(`{"name":"Renamed","rolePrompt":"Role"`+field+`}`))
		req.SetPathValue("agentID", a.ID)
		response := httptest.NewRecorder()
		s.updateAgent(response, req)
		return response
	}
	if response := patch(""); response.Code != 200 {
		t.Fatalf("omitted permission status=%d body=%s", response.Code, response.Body.String())
	}
	stored, err := st.Agent(ctx, a.ID)
	if err != nil || stored.Permission != domain.PermissionFullAccess || stored.RoleVersion != 1 {
		t.Fatalf("omitted permission changed settings: %+v err=%v", stored, err)
	}
	for _, value := range []string{`""`, `null`, `"manual"`, `false`} {
		if response := patch(`,"permission":` + value); response.Code != 400 {
			t.Errorf("permission=%s status=%d body=%s", value, response.Code, response.Body.String())
		}
	}
	if response := patch(`,"permission":"auto"`); response.Code != 200 {
		t.Fatalf("permission update status=%d body=%s", response.Code, response.Body.String())
	}
	stored, err = st.Agent(ctx, a.ID)
	if err != nil || stored.Permission != domain.PermissionAuto || stored.RoleVersion != 2 {
		t.Fatalf("updated=%+v err=%v", stored, err)
	}
	req := httptest.NewRequest("POST", "/api/agents/agent/messages", strings.NewReader(`{"prompt":"continue","conversationId":"conversation"}`))
	req.SetPathValue("agentID", a.ID)
	response := httptest.NewRecorder()
	s.message(response, req)
	if response.Code != 502 {
		t.Fatalf("same conversation must reach unavailable runtime: status=%d body=%s", response.Code, response.Body.String())
	}
	current, err := st.Conversation(ctx, c.ID)
	if err != nil || current.RoleVersion != 2 || current.CodexThreadID != "old-thread" {
		t.Fatalf("context lost after permission update %+v %v", current, err)
	}
	if _, err := s.startContinuation(ctx, stored, c.ID); err == nil || strings.Contains(err.Error(), "settings changed") {
		t.Fatalf("same-context desktop continuation err=%v", err)
	}
	if err := st.CreateRun(ctx, domain.Run{ID: "run", AgentID: a.ID, ConversationID: c.ID, Source: "manual", Prompt: "hello", Status: "queued"}); err != nil {
		t.Fatal(err)
	}
	if response := patch(`,"permission":"full-access"`); response.Code != 409 {
		t.Fatalf("active run permission update status=%d", response.Code)
	}
	if response := patch(`,"permission":"auto"`); response.Code != 200 {
		t.Fatalf("unchanged permission during active run status=%d", response.Code)
	}
	if err := st.FinishRun(ctx, "run", "completed", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AcquireDesktopLease(ctx, a.ID, "human", time.Minute); err != nil {
		t.Fatal(err)
	}
	if response := patch(`,"permission":"full-access"`); response.Code != 409 {
		t.Fatalf("human desktop permission update status=%d", response.Code)
	}
	stored, err = st.Agent(ctx, a.ID)
	if err != nil || stored.Permission != domain.PermissionAuto || stored.RoleVersion != 2 {
		t.Fatalf("rejected updates changed settings: %+v err=%v", stored, err)
	}
}

func TestAgentPermissionReachesEveryTurnPath(t *testing.T) {
	for _, permission := range []domain.PermissionMode{domain.PermissionAuto, domain.PermissionFullAccess} {
		for _, path := range []string{"new", "resumed", "desktop continuation", "desktop scheduled continuation", "scheduled"} {
			t.Run(string(permission)+"/"+path, func(t *testing.T) {
				st := permissionTestStore(t)
				ctx := context.Background()
				a := domain.Agent{ID: "agent", Name: "Agent", RolePrompt: "Role", Model: "model-a", Effort: "high", Permission: permission, RoleVersion: 1, Status: "stopped", ProviderAuthState: "disconnected", CreatedAt: time.Now(), UpdatedAt: time.Now()}
				if err := st.CreateAgent(ctx, a); err != nil {
					t.Fatal(err)
				}
				requests := make(chan map[string]json.RawMessage, 1)
				runtime := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if strings.HasSuffix(r.URL.Path, "/turns") {
						var raw map[string]json.RawMessage
						if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
							t.Errorf("decode turn: %v", err)
						}
						requests <- raw
						// Stop after forwarding so the test does not start event relay goroutines.
						http.Error(w, "test runtime unavailable", http.StatusServiceUnavailable)
						return
					}
					w.WriteHeader(http.StatusNoContent)
				}))
				defer runtime.Close()
				s := New(st, runtimeclient.New(runtime.URL, "token"), Config{}, nil)
				c := domain.Conversation{ID: "conversation", AgentID: a.ID, Kind: "manual", RoleVersion: 1, CodexThreadID: "existing-thread", CreatedAt: time.Now()}
				desktopContinuation := strings.HasPrefix(path, "desktop ")
				if path == "desktop scheduled continuation" {
					c.Kind = "scheduled"
				}
				if path == "resumed" || desktopContinuation {
					if err := st.CreateConversation(ctx, c); err != nil {
						t.Fatal(err)
					}
				}
				switch path {
				case "new", "resumed":
					body := map[string]string{"prompt": "hello"}
					if path == "resumed" {
						body["conversationId"] = c.ID
					}
					data, _ := json.Marshal(body)
					req := httptest.NewRequest("POST", "/api/agents/agent/messages", bytes.NewReader(data))
					req.SetPathValue("agentID", a.ID)
					response := httptest.NewRecorder()
					s.message(response, req)
					if response.Code != 502 {
						t.Fatalf("runtime response status=%d body=%s", response.Code, response.Body.String())
					}
				case "desktop scheduled continuation":
					stored, err := st.Agent(ctx, a.ID)
					if err != nil {
						t.Fatal(err)
					}
					if runID, err := s.startContinuation(ctx, stored, c.ID); err != nil || runID != "" {
						t.Fatalf("schedule resumed as chat: %s %v", runID, err)
					}
					select {
					case <-requests:
						t.Fatal("schedule resumed through chat runtime")
					default:
					}
					return
				case "desktop continuation":
					stored, err := st.Agent(ctx, a.ID)
					if err != nil {
						t.Fatal(err)
					}
					if _, err := s.startContinuation(ctx, stored, c.ID); err == nil {
						t.Fatal("expected runtime unavailable")
					}
				case "scheduled":
					due := time.Now().Add(-time.Minute)
					sc := domain.Schedule{ID: "schedule", AgentID: a.ID, Name: "Schedule", Prompt: "hello", Kind: "once", Expression: due.Format(time.RFC3339), Timezone: "UTC", Enabled: true, NextRunAt: &due, CreatedAt: time.Now(), UpdatedAt: time.Now()}
					if err := st.CreateSchedule(ctx, sc); err != nil {
						t.Fatal(err)
					}
					s.runDueSchedule(ctx, sc)
				}
				var raw map[string]json.RawMessage
				select {
				case raw = <-requests:
				default:
					t.Fatal("turn was not forwarded")
				}
				var turn runtimeclient.TurnRequest
				data, _ := json.Marshal(raw)
				if err := json.Unmarshal(data, &turn); err != nil {
					t.Fatal(err)
				}
				if turn.Permission != permission || turn.Model != a.Model || turn.Effort != a.Effort {
					t.Fatalf("turn settings=%+v", turn)
				}
				if _, exists := raw["AutoApprove"]; exists {
					t.Fatalf("legacy AutoApprove sent: %s", data)
				}
				wantThread := ""
				if path == "resumed" || desktopContinuation {
					wantThread = c.CodexThreadID
				}
				if turn.ThreadID != wantThread {
					t.Fatalf("thread=%q want=%q", turn.ThreadID, wantThread)
				}
				wantSource := "manual"
				if path == "scheduled" {
					wantSource = "scheduled"
				}
				if turn.Source != wantSource || !strings.Contains(turn.DeveloperInstructions, "Do not ask the user for permission approval.") {
					t.Fatalf("turn context=%+v", turn)
				}
				if string(raw["permission"]) != fmt.Sprintf("%q", permission) {
					t.Fatalf("permission JSON=%s", raw["permission"])
				}
			})
		}
	}
}
