package runtimeclient

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestStartAgentTimeoutDoesNotChangeConcurrentRequests(t *testing.T) {
	client := New("http://runtime-manager:8081", "token")
	type observedRequest struct {
		path      string
		remaining time.Duration
	}
	observed := make(chan observedRequest, 2)
	release := make(chan struct{})
	defer close(release)
	client.http.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		deadline, ok := req.Context().Deadline()
		if !ok {
			return nil, errors.New("request has no deadline")
		}
		observed <- observedRequest{path: req.URL.Path, remaining: time.Until(deadline)}
		<-release
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader("")), Header: make(http.Header)}, nil
	})
	errs := make(chan error, 2)
	go func() { errs <- client.StartAgent(context.Background(), AgentConfig{ID: "agent"}) }()
	go func() { errs <- client.StopAgent(context.Background(), "agent") }()
	for range 2 {
		select {
		case req := <-observed:
			want := 30 * time.Second
			if strings.HasSuffix(req.path, "/start") {
				want = 90 * time.Second
			}
			if req.remaining <= want-time.Second || req.remaining > want {
				t.Errorf("%s deadline remaining = %s, want approximately %s", req.path, req.remaining, want)
			}
		case err := <-errs:
			t.Fatalf("request returned before observation: %v", err)
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for concurrent requests")
		}
	}
	release <- struct{}{}
	release <- struct{}{}
	for range 2 {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
}

func TestStartAgentPreservesCallerDeadlineAndCancellation(t *testing.T) {
	client := New("http://runtime-manager:8081", "token")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	wantDeadline, _ := ctx.Deadline()
	started := make(chan time.Time, 1)
	client.http.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		deadline, _ := req.Context().Deadline()
		started <- deadline
		<-req.Context().Done()
		return nil, req.Context().Err()
	})
	errCh := make(chan error, 1)
	go func() { errCh <- client.StartAgent(ctx, AgentConfig{ID: "agent"}) }()
	select {
	case gotDeadline := <-started:
		if !gotDeadline.Equal(wantDeadline) {
			t.Errorf("deadline = %v, want caller deadline %v", gotDeadline, wantDeadline)
		}
	case <-ctx.Done():
		t.Fatal("request did not reach transport")
	}
	cancel()
	select {
	case err := <-errCh:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("error = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("caller cancellation did not interrupt startup")
	}
}

func TestSteerTurnPreservesDefiniteRejectionStatus(t *testing.T) {
	client := New("http://runtime-manager:8081", "token")
	client.http.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path != "/v1/agents/agent/steer" || req.Method != "POST" {
			t.Fatalf("request=%s %s", req.Method, req.URL.Path)
		}
		return &http.Response{StatusCode: 409, Body: io.NopCloser(strings.NewReader("active turn changed")), Header: make(http.Header)}, nil
	})
	_, err := client.SteerTurn(context.Background(), "agent", SteerRequest{MessageID: "message", RunID: "run", ThreadID: "thread", ExpectedTurnID: "turn", Prompt: "hello"})
	var httpErr *HTTPError
	if !errors.As(err, &httpErr) || httpErr.StatusCode != 409 {
		t.Fatalf("error=%v", err)
	}
}
