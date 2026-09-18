package agentworker

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCollaborationSocketForwardsOnlyScopedCapability(t *testing.T) {
	const id = "00000000-0000-0000-0000-000000000001"
	manager := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/agents/"+id+"/collaboration/agents" || r.URL.RawQuery != "limit=2" {
			t.Errorf("url=%s", r.URL)
		}
		if r.Header.Get("Authorization") != "Bearer worker-secret" {
			t.Error("missing worker auth")
		}
		if r.Header.Get("Cookie") != "" || r.Header.Get("X-Forwarded-For") != "" || r.Header.Get("X-Agent-ID") != "" {
			t.Errorf("unexpected headers: %v", r.Header)
		}
		w.WriteHeader(200)
		_, _ = io.WriteString(w, `{"agents":[]}`)
	}))
	defer manager.Close()
	socketDir, err := os.MkdirTemp("/tmp", "cb-sock-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(socketDir)
	path := filepath.Join(socketDir, "collaboration.sock")
	worker := &Worker{cfg: Config{AgentID: id, Token: "worker-secret", CollaborationURL: manager.URL, CollaborationSocket: path}, logger: slog.Default()}
	if err := worker.startCollaborationSocket(); err != nil {
		t.Fatal(err)
	}
	defer worker.collaborationServer.Close()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("socket permissions=%v", info.Mode())
	}
	client := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", path)
	}}}
	request, _ := http.NewRequest("GET", "http://localhost/agents?limit=2", nil)
	request.Header.Set("Authorization", "Bearer spoof")
	request.Header.Set("Cookie", "admin=session")
	request.Header.Set("X-Agent-ID", "other")
	request.Header.Set("X-Forwarded-For", "other")
	resp, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || !strings.Contains(string(body), "agents") {
		t.Fatalf("status=%d body=%s", resp.StatusCode, body)
	}
	recorder := httptest.NewRecorder()
	worker.CollaborationHandler().ServeHTTP(recorder, httptest.NewRequest("POST", "/v1/turns", nil))
	if recorder.Code != 404 {
		t.Fatalf("administrative route exposed: %d", recorder.Code)
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
			worker := &Worker{cfg: Config{AgentID: id, Token: "token", CollaborationURL: upstream.URL}}
			rec := httptest.NewRecorder()
			worker.CollaborationHandler().ServeHTTP(rec, httptest.NewRequest("GET", "/agents", nil))
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
