package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestInternalCollaborationReachesAPIInsteadOfStaticFallback(t *testing.T) {
	api := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/internal/agents/sender/collaboration/agents" {
			t.Error("internal path changed")
		}
		w.WriteHeader(http.StatusUnauthorized)
	})
	w := httptest.NewRecorder()
	withStatic(api, t.TempDir()).ServeHTTP(w, httptest.NewRequest("GET", "/internal/agents/sender/collaboration/agents", nil))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("internal route bypassed API: %d", w.Code)
	}
}
