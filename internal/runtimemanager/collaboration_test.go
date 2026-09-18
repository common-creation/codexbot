package runtimemanager

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCollaborationAuthIsBoundToSenderAndRoute(t *testing.T) {
	const id = "00000000-0000-0000-0000-000000000001"
	const other = "00000000-0000-0000-0000-000000000002"
	calls := 0
	cp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/internal/agents/"+id+"/collaboration/tasks" {
			t.Errorf("path=%s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer runtime-secret" {
			t.Error("runtime token not attached")
		}
		if r.Header.Get("Cookie") != "" || r.Header.Get("X-Agent-ID") != "" {
			t.Error("untrusted headers forwarded")
		}
		w.WriteHeader(202)
		_, _ = io.WriteString(w, `{"id":"task"}`)
	}))
	defer cp.Close()
	manager := New(ManagerConfig{RuntimeToken: "runtime-secret", ControlPlaneURL: cp.URL}, nil)
	for _, test := range []struct {
		name, path, token string
		want              int
	}{
		{"valid", "/v1/agents/" + id + "/collaboration/tasks", derivedSecret("runtime-secret", "worker:"+id), 202},
		{"other agent", "/v1/agents/" + other + "/collaboration/tasks", derivedSecret("runtime-secret", "worker:"+id), 401},
		{"admin route", "/v1/agents/" + id + "/stop", derivedSecret("runtime-secret", "worker:"+id), 401},
		{"runtime cannot impersonate", "/v1/agents/" + id + "/collaboration/tasks", "runtime-secret", 401},
		{"missing", "/v1/agents/" + id + "/collaboration/tasks", "", 401},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest("POST", test.path, strings.NewReader(`{"senderAgentId":"spoof"}`))
			request.Header.Set("Authorization", "Bearer "+test.token)
			request.Header.Set("Cookie", "session=admin")
			request.Header.Set("X-Agent-ID", other)
			rec := httptest.NewRecorder()
			manager.Handler().ServeHTTP(rec, request)
			if rec.Code != test.want {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
		})
	}
	if calls != 1 {
		t.Fatalf("control plane received %d requests", calls)
	}
}

func TestCollaborationProxyRejectsOversizedResponseAndDoesNotFollowRedirect(t *testing.T) {
	const id = "00000000-0000-0000-0000-000000000001"
	for _, scenario := range []string{"oversized", "redirect"} {
		t.Run(scenario, func(t *testing.T) {
			followed := false
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/unexpected" {
					followed = true
					w.WriteHeader(200)
					return
				}
				if scenario == "redirect" {
					w.Header().Set("Location", "/unexpected")
					w.WriteHeader(307)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `{"value":"`+strings.Repeat("x", 8<<20)+`"}`)
			}))
			defer upstream.Close()
			manager := New(ManagerConfig{RuntimeToken: "token", ControlPlaneURL: upstream.URL}, nil)
			request := httptest.NewRequest("GET", "/v1/agents/"+id+"/collaboration/agents", nil)
			request.Header.Set("Authorization", "Bearer "+derivedSecret("token", "worker:"+id))
			rec := httptest.NewRecorder()
			manager.Handler().ServeHTTP(rec, request)
			if scenario == "oversized" {
				if rec.Code != 502 || !strings.Contains(rec.Body.String(), "exceeds 8 MiB") {
					t.Fatalf("status=%d length=%d", rec.Code, rec.Body.Len())
				}
				if rec.Body.Len() > 1024 {
					t.Fatal("truncated upstream body leaked")
				}
			} else if rec.Code != 307 || followed {
				t.Fatalf("status=%d followed=%v", rec.Code, followed)
			}
		})
	}
}
