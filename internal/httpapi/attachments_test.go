package httpapi

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/common-creation/codexbot/internal/domain"
	"github.com/common-creation/codexbot/internal/runtimeclient"
)

func TestMessageForwardsAttachmentOnlyAndPersistsNames(t *testing.T) {
	st := permissionTestStore(t)
	ctx := context.Background()
	a := domain.Agent{ID: "agent", Name: "Agent", RolePrompt: "Role", Permission: domain.PermissionAuto, RoleVersion: 1, Status: "stopped", CreatedAt: time.Now(), UpdatedAt: time.Now()}
	if err := st.CreateAgent(ctx, a); err != nil {
		t.Fatal(err)
	}
	var forwarded runtimeclient.TurnRequest
	runtime := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/turns") {
			if err := json.NewDecoder(r.Body).Decode(&forwarded); err != nil {
				t.Error(err)
			}
			http.Error(w, "test runtime unavailable", 503)
			return
		}
		w.WriteHeader(204)
	}))
	defer runtime.Close()
	s := New(st, runtimeclient.New(runtime.URL, "token"), Config{}, nil)
	file := domain.Attachment{Name: "notes.txt", Data: base64.StdEncoding.EncodeToString(bytes.Repeat([]byte("x"), 1<<20))}
	data, _ := json.Marshal(map[string]any{"prompt": "", "attachments": []domain.Attachment{file}})
	req := httptest.NewRequest("POST", "/api/agents/agent/messages", bytes.NewReader(data))
	req.SetPathValue("agentID", a.ID)
	response := httptest.NewRecorder()
	s.message(response, req)
	if response.Code != 502 {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if len(forwarded.Attachments) != 1 || forwarded.Attachments[0] != file || forwarded.Prompt != "" {
		t.Fatal("attachment-only turn was not forwarded intact")
	}
	run, err := st.Run(ctx, forwarded.RunID)
	if err != nil || run.Prompt != "Attached file: notes.txt" {
		t.Fatalf("run prompt=%q err=%v", run.Prompt, err)
	}
	conversation, err := st.Conversation(ctx, forwarded.ConversationID)
	if err != nil || conversation.Title != "Attached file: notes.txt" {
		t.Fatalf("title=%q err=%v", conversation.Title, err)
	}
}
func TestMessageRejectsInvalidAttachmentsBeforeRuntime(t *testing.T) {
	s := &Server{}
	for _, body := range []string{`{"attachments":[{"name":"../x","data":""}]}`, `{"attachments":[{"name":"x","data":"invalid!"}]}`, `{"prompt":""}`} {
		response := httptest.NewRecorder()
		s.message(response, httptest.NewRequest("POST", "/", strings.NewReader(body)))
		if response.Code != 400 {
			t.Fatalf("status=%d", response.Code)
		}
	}
}
