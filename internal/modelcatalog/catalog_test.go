package modelcatalog

import (
	"context"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"
)

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func catalogClient(status int, body string) *http.Client {
	return &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{}}, nil
	})}
}

func TestFetchFiltersCatalog(t *testing.T) {
	body := `{"models":[
	 {"slug":"future-model","display_name":"Future Model","visibility":"list","supported_reasoning_levels":[{"effort":"low"},{"effort":"future-level"},{"effort":"low"},{"effort":""},{"effort":"bad level"}]},
	 {"slug":"future-model","visibility":"list","supported_reasoning_levels":[{"effort":"high"}]},
	 {"slug":"hidden","visibility":"hide","supported_reasoning_levels":[{"effort":"high"}]},
	 {"slug":"unknown-visibility","supported_reasoning_levels":[{"effort":"high"}]},
	 {"slug":"","visibility":"list","supported_reasoning_levels":[{"effort":"high"}]},
	 {"slug":"bad slug","visibility":"list","supported_reasoning_levels":[{"effort":"high"}]},
	 {"slug":"empty-efforts","visibility":"list","supported_reasoning_levels":[]},
	 {"slug":"fallback-name","visibility":"list","supported_reasoning_levels":[{"effort":"medium"}]}
	]}`
	models, err := Fetch(context.Background(), catalogClient(200, body), URL)
	if err != nil {
		t.Fatal(err)
	}
	want := []Model{{ID: "future-model", Name: "Future Model", Efforts: []string{"low", "future-level"}}, {ID: "fallback-name", Name: "fallback-name", Efforts: []string{"medium"}}}
	if !reflect.DeepEqual(models, want) {
		t.Fatalf("models=%+v", models)
	}
}

func TestFetchRejectsFailedOrUnusableCatalog(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
	}{
		{"HTTP failure", 503, "unavailable"},
		{"malformed JSON", 200, "{"},
		{"empty models", 200, `{"models":[]}`},
		{"wrong schema", 200, `{"models":"invalid"}`},
		{"trailing data", 200, `{"models":[]} extra`},
		{"oversized body", 200, strings.Repeat(" ", maxBytes+1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Fetch(context.Background(), catalogClient(tc.status, tc.body), URL); err == nil {
				t.Fatal("expected error")
			}
		})
	}
	client := &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) { return nil, errors.New("network unavailable") })}
	if _, err := Fetch(context.Background(), client, URL); err == nil {
		t.Fatal("expected network error")
	}
}

func TestFetchHonorsDeadline(t *testing.T) {
	client := &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		deadline, ok := r.Context().Deadline()
		if !ok || time.Until(deadline) > 5*time.Second {
			t.Error("missing bounded request deadline")
		}
		<-r.Context().Done()
		return nil, r.Context().Err()
	})}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	started := time.Now()
	if _, err := Fetch(ctx, client, URL); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error=%v", err)
	}
	if time.Since(started) > time.Second {
		t.Fatal("request did not respect shorter caller deadline")
	}
}

func TestFallbackReturnsIndependentModels(t *testing.T) {
	models := Fallback()
	if len(models) != 7 || models[0].ID != "gpt-6-astra" || models[0].Efforts[5] != "ultra" {
		t.Fatalf("models=%+v", models)
	}
	models[0].Efforts[0] = "changed"
	if Fallback()[0].Efforts[0] != "low" {
		t.Fatal("fallback shares mutable slices")
	}
}
