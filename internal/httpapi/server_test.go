package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/common-creation/codexbot/internal/domain"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/common-creation/codexbot/internal/runtimeclient"
	"github.com/common-creation/codexbot/internal/store"
)

func TestSetupLoginAndCreateAgent(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "api.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	handler := New(st, runtimeclient.New("http://127.0.0.1:1", "token"), Config{BootstrapToken: "bootstrap", SecureCookies: false}, nil).Handler()
	var cookie *http.Cookie
	request := func(method, path string, body any, headers map[string]string) *http.Response {
		t.Helper()
		var b bytes.Buffer
		if body != nil {
			if err := json.NewEncoder(&b).Encode(body); err != nil {
				t.Fatal(err)
			}
		}
		req := httptest.NewRequest(method, "http://codexbot.test"+path, &b)
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		if cookie != nil {
			req.AddCookie(cookie)
		}
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, req)
		resp := recorder.Result()
		for _, next := range resp.Cookies() {
			if next.Name == sessionCookie {
				cookie = next
			}
		}
		return resp
	}
	const username = "sidebar-user"
	resp := request("POST", "/api/setup", map[string]string{"username": username, "password": "correct horse battery staple"}, map[string]string{"X-Bootstrap-Token": "bootstrap"})
	if resp.StatusCode != 201 {
		t.Fatalf("setup status=%d", resp.StatusCode)
	}
	resp.Body.Close()
	resp = request("POST", "/api/session", map[string]string{"username": username, "password": "correct horse battery staple"}, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("login status=%d", resp.StatusCode)
	}
	var session map[string]any
	if err = json.NewDecoder(resp.Body).Decode(&session); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	csrf, _ := session["csrfToken"].(string)
	if csrf == "" {
		t.Fatal("missing csrf token")
	}
	if session["username"] != username {
		t.Fatalf("login username=%v, want %q", session["username"], username)
	}
	resp = request("GET", "/api/session", nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("session status=%d", resp.StatusCode)
	}
	session = map[string]any{}
	if err = json.NewDecoder(resp.Body).Decode(&session); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if session["authenticated"] != true || session["username"] != username || session["csrfToken"] != csrf {
		t.Fatalf("restored session=%v", session)
	}
	resp = request("POST", "/api/agents", map[string]string{"name": "Researcher", "rolePrompt": "Research reliable primary sources.", "model": "test-model", "effort": "high"}, map[string]string{"X-CSRF-Token": csrf})
	if resp.StatusCode != 201 {
		t.Fatalf("create agent status=%d", resp.StatusCode)
	}
	resp.Body.Close()
	resp = request("GET", "/api/agents", nil, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("list status=%d", resp.StatusCode)
	}
	var agents []map[string]any
	if err = json.NewDecoder(resp.Body).Decode(&agents); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if len(agents) != 1 || agents[0]["name"] != "Researcher" {
		t.Fatalf("agents=%v", agents)
	}
	agentID, _ := agents[0]["id"].(string)
	rolePrompt, _ := agents[0]["rolePrompt"].(string)
	resp = request("PATCH", "/api/agents/"+agentID, map[string]string{"name": "Web Researcher", "rolePrompt": rolePrompt}, map[string]string{"X-CSRF-Token": csrf})
	if resp.StatusCode != 200 {
		t.Fatalf("update agent status=%d", resp.StatusCode)
	}
	var updated map[string]any
	if err = json.NewDecoder(resp.Body).Decode(&updated); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if updated["name"] != "Web Researcher" || updated["roleVersion"] != float64(1) || updated["model"] != "test-model" || updated["effort"] != "high" {
		t.Fatalf("updated agent=%v", updated)
	}
	resp = request("POST", "/api/agents/"+agentID+"/conversations", nil, map[string]string{"X-CSRF-Token": csrf})
	if resp.StatusCode != 201 {
		t.Fatalf("new conversation status=%d", resp.StatusCode)
	}
	var conversation map[string]any
	if err = json.NewDecoder(resp.Body).Decode(&conversation); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	conversationID, _ := conversation["id"].(string)
	if conversationID == "" {
		t.Fatal("new conversation has no id")
	}
	resp = request("GET", "/api/agents/"+agentID+"/history?conversationId="+conversationID, nil, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("conversation history status=%d", resp.StatusCode)
	}
	var history map[string]any
	if err = json.NewDecoder(resp.Body).Decode(&history); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if history["conversationId"] != conversationID {
		t.Fatalf("history=%v", history)
	}
	resp = request("PATCH", "/api/agents/"+agentID, map[string]string{"name": "Web Researcher", "rolePrompt": "Use primary sources only."}, map[string]string{"X-CSRF-Token": csrf})
	if resp.StatusCode != 200 {
		t.Fatalf("role update status=%d", resp.StatusCode)
	}
	updated = map[string]any{}
	if err = json.NewDecoder(resp.Body).Decode(&updated); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if updated["roleVersion"] != float64(2) {
		t.Fatalf("role update=%v", updated)
	}
	resp = request("POST", "/api/agents/"+agentID+"/messages", map[string]string{"prompt": "continue", "conversationId": conversationID}, map[string]string{"X-CSRF-Token": csrf})
	if resp.StatusCode != 502 {
		t.Fatalf("existing conversation should reach unavailable runtime: status=%d", resp.StatusCode)
	}
	resp.Body.Close()
	resp = request("POST", "/api/agents/"+agentID+"/conversations", nil, map[string]string{"X-CSRF-Token": csrf})
	if resp.StatusCode != 201 {
		t.Fatalf("new conversation after role update status=%d", resp.StatusCode)
	}
	conversation = map[string]any{}
	if err = json.NewDecoder(resp.Body).Decode(&conversation); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if conversation["roleVersion"] != float64(2) || conversation["id"] == conversationID {
		t.Fatalf("explicit New chat must reset conversation=%v", conversation)
	}
	if err = st.SetSharedAuthState(context.Background(), "connected"); err != nil {
		t.Fatal(err)
	}
	resp = request("POST", "/api/agents", map[string]string{"name": "Writer", "rolePrompt": "Draft reports."}, map[string]string{"X-CSRF-Token": csrf})
	if resp.StatusCode != 201 {
		t.Fatalf("create agent with shared auth status=%d", resp.StatusCode)
	}
	var sharedAgent map[string]any
	if err = json.NewDecoder(resp.Body).Decode(&sharedAgent); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if sharedAgent["providerAuthState"] != "connected" {
		t.Fatalf("new agent did not inherit shared auth=%v", sharedAgent)
	}
}

func TestUpdateAgentModelSettings(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "settings.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	a := domain.Agent{ID: "agent", Name: "Agent", RolePrompt: "Role", Model: "model-a", Effort: "high", RoleVersion: 1, Status: "stopped", ProviderAuthState: "disconnected", CreatedAt: time.Now(), UpdatedAt: time.Now()}
	if err := st.CreateAgent(ctx, a); err != nil {
		t.Fatal(err)
	}
	server := New(st, runtimeclient.New("http://127.0.0.1:1", "token"), Config{}, nil)
	patch := func(body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("PATCH", "/api/agents/agent", bytes.NewBufferString(body))
		req.SetPathValue("agentID", a.ID)
		response := httptest.NewRecorder()
		server.updateAgent(response, req)
		return response
	}
	response := patch(`{"name":"Agent","rolePrompt":"Role","model":"model-b","effort":"ultra"}`)
	if response.Code != 200 {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	updated, err := st.Agent(ctx, a.ID)
	if err != nil || updated.Model != "model-b" || updated.Effort != "ultra" || updated.RoleVersion != 2 {
		t.Fatalf("updated=%+v err=%v", updated, err)
	}
	response = patch(`{"name":"Agent","rolePrompt":"Role","model":"","effort":""}`)
	if response.Code != 200 {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	updated, err = st.Agent(ctx, a.ID)
	if err != nil || updated.Model != "" || updated.Effort != "" || updated.RoleVersion != 3 {
		t.Fatalf("updated=%+v err=%v", updated, err)
	}
	response = patch(`{"name":"Agent","rolePrompt":"Role","effort":"unsupported"}`)
	if response.Code != 400 {
		t.Fatalf("invalid effort status=%d", response.Code)
	}
	c := domain.Conversation{ID: "conversation", AgentID: a.ID, Kind: "manual", RoleVersion: updated.RoleVersion, CreatedAt: time.Now()}
	if err := st.CreateConversation(ctx, c); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateRun(ctx, domain.Run{ID: "run", AgentID: a.ID, ConversationID: c.ID, Source: "manual", Prompt: "hello", Status: "queued"}); err != nil {
		t.Fatal(err)
	}
	response = patch(`{"name":"Agent","rolePrompt":"Role","model":"model-c"}`)
	if response.Code != 409 {
		t.Fatalf("busy model update status=%d", response.Code)
	}
}
