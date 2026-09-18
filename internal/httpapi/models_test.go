package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/common-creation/codexbot/internal/auth"
	"github.com/common-creation/codexbot/internal/modelcatalog"
	"github.com/common-creation/codexbot/internal/store"
)

func TestModelsEndpointRequiresSessionAndReturnsConfiguredCatalog(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "models.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	if err := st.CreateAdmin(ctx, "admin", "unused"); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateSession(ctx, auth.TokenHash("session"), "csrf", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	models := []modelcatalog.Model{{ID: "future-model", Name: "Future Model", Efforts: []string{"future-level"}}}
	s := New(st, nil, Config{Models: models}, nil)
	request := func(authenticated bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "/api/models", nil)
		if authenticated {
			r.AddCookie(&http.Cookie{Name: sessionCookie, Value: "session"})
		}
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		return w
	}
	if w := request(false); w.Code != 401 {
		t.Fatalf("unauthenticated status=%d", w.Code)
	}
	w := request(true)
	if w.Code != 200 {
		t.Fatalf("authenticated status=%d body=%s", w.Code, w.Body.String())
	}
	var got []modelcatalog.Model
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, models) {
		t.Fatalf("catalog=%+v", got)
	}
	if !s.validModelSettings("future-model", "future-level") {
		t.Fatal("new effort from catalog rejected")
	}
	if s.validModelSettings("other-model", "future-level") {
		t.Fatal("unknown effort accepted for unrelated model")
	}
	if !s.validModelSettings("custom-model", "ultra") {
		t.Fatal("custom model compatibility lost")
	}
	if defaults := New(st, nil, Config{}, nil); !reflect.DeepEqual(defaults.models, modelcatalog.Fallback()) {
		t.Fatalf("fallback=%+v", defaults.models)
	}
}
