package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/common-creation/codexbot/internal/auth"
	"github.com/common-creation/codexbot/internal/domain"
)

func TestSidebarAPIAuthenticationPersistenceAndConflicts(t *testing.T) {
	st := permissionTestStore(t)
	ctx := context.Background()
	if err := st.CreateAdmin(ctx, "admin", "unused"); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateSession(ctx, auth.TokenHash("session"), "csrf", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	handler := New(st, nil, Config{}, nil).Handler()
	request := func(method, body string, authenticated, csrf bool) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, "/api/sidebar", strings.NewReader(body))
		if authenticated {
			req.AddCookie(&http.Cookie{Name: sessionCookie, Value: "session"})
		}
		if csrf {
			req.Header.Set("X-CSRF-Token", "csrf")
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, req)
		return response
	}
	for _, method := range []string{"GET", "PUT"} {
		if response := request(method, `{}`, false, false); response.Code != 401 {
			t.Fatalf("unauthenticated %s: %d", method, response.Code)
		}
	}
	if response := request("PUT", `{}`, true, false); response.Code != 403 {
		t.Fatalf("CSRF status=%d", response.Code)
	}
	if response := request("GET", "", true, false); response.Code != 200 || strings.Contains(response.Body.String(), "null") {
		t.Fatalf("initial=%d %s", response.Code, response.Body.String())
	}
	for _, id := range []string{"a", "b"} {
		if err := st.CreateAgent(ctx, domain.Agent{ID: id, Name: id, RolePrompt: "role", RoleVersion: 1, Status: "stopped", ProviderAuthState: "disconnected", CreatedAt: time.Now(), UpdatedAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
	}
	body := `{"revision":0,"sections":[{"id":"work","name":"仕事","agentIds":["b","a"]}],"unsectionedAgentIds":[]}`
	response := request("PUT", body, true, true)
	if response.Code != 200 {
		t.Fatalf("save=%d %s", response.Code, response.Body.String())
	}
	var saved domain.SidebarLayout
	if err := json.Unmarshal(response.Body.Bytes(), &saved); err != nil {
		t.Fatal(err)
	}
	if saved.Revision != 1 || len(saved.Sections) != 1 || strings.Join(saved.Sections[0].AgentIDs, ",") != "b,a" {
		t.Fatalf("saved=%+v", saved)
	}
	if response := request("GET", "", true, false); response.Code != 200 || !strings.Contains(response.Body.String(), `"agentIds":["b","a"]`) {
		t.Fatalf("get=%d %s", response.Code, response.Body.String())
	}
	if response := request("PUT", body, true, true); response.Code != 409 {
		t.Fatalf("stale=%d %s", response.Code, response.Body.String())
	}
	if response := request("PUT", `{"revision":1,"sections":[],"unsectionedAgentIds":["a"]}`, true, true); response.Code != 409 {
		t.Fatalf("missing=%d %s", response.Code, response.Body.String())
	}
	if response := request("PUT", `{"revision":1,"sections":[],"unsectionedAgentIds":["a","a","b"]}`, true, true); response.Code != 400 {
		t.Fatalf("duplicate=%d %s", response.Code, response.Body.String())
	}
	if response := request("PUT", `not JSON`, true, true); response.Code != 400 {
		t.Fatalf("invalid JSON=%d", response.Code)
	}
}
