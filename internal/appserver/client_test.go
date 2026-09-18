package appserver

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"strings"
	"testing"
	"time"
)

type fakeServer struct {
	t      *testing.T
	conn   net.Conn
	reader *bufio.Reader
}

func newFakePair(t *testing.T) (*Client, *fakeServer) {
	t.Helper()
	clientConn, serverConn := net.Pipe()
	client := newClient(clientConn, clientConn, 1<<20)
	server := &fakeServer{t: t, conn: serverConn, reader: bufio.NewReader(serverConn)}
	t.Cleanup(func() {
		_ = serverConn.Close()
		_ = clientConn.Close()
		select {
		case <-client.Done():
		case <-time.After(time.Second):
			t.Error("client did not stop")
		}
	})
	return client, server
}

func (s *fakeServer) read() map[string]json.RawMessage {
	s.t.Helper()
	line, err := s.reader.ReadBytes('\n')
	if err != nil {
		s.t.Fatalf("read client message: %v", err)
	}
	var message map[string]json.RawMessage
	if err := json.Unmarshal(line, &message); err != nil {
		s.t.Fatalf("decode client message: %v", err)
	}
	return message
}

func (s *fakeServer) write(value any) {
	s.t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		s.t.Fatalf("encode server message: %v", err)
	}
	data = append(data, '\n')
	if _, err := s.conn.Write(data); err != nil {
		s.t.Fatalf("write server message: %v", err)
	}
}

func rawString(t *testing.T, raw json.RawMessage) string {
	t.Helper()
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatalf("decode string: %v", err)
	}
	return value
}

func TestInitializeAndThreadStart(t *testing.T) {
	client, server := newFakePair(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	initDone := make(chan error, 1)
	go func() {
		initDone <- client.initializeProtocol(ctx, ClientInfo{Name: "codexbot", Version: "test"}, Capabilities{})
	}()

	initialize := server.read()
	if method := rawString(t, initialize["method"]); method != MethodInitialize {
		t.Fatalf("method = %q", method)
	}
	var initParams struct {
		ClientInfo ClientInfo `json:"clientInfo"`
	}
	if err := json.Unmarshal(initialize["params"], &initParams); err != nil {
		t.Fatal(err)
	}
	if initParams.ClientInfo.Name != "codexbot" {
		t.Fatalf("client name = %q", initParams.ClientInfo.Name)
	}
	server.write(map[string]any{
		"id": json.RawMessage(initialize["id"]),
		"result": map[string]any{
			"userAgent":      "codex/0.152.1",
			"codexHome":      "/profile/codex",
			"platformFamily": "unix",
			"platformOs":     "linux",
		},
	})
	initialized := server.read()
	if method := rawString(t, initialized["method"]); method != "initialized" {
		t.Fatalf("notification method = %q", method)
	}
	if _, exists := initialized["id"]; exists {
		t.Fatal("initialized notification unexpectedly has an id")
	}
	if err := <-initDone; err != nil {
		t.Fatalf("initialize: %v", err)
	}
	if got := client.InitializeResponse().CodexHome; got != "/profile/codex" {
		t.Fatalf("codex home = %q", got)
	}

	resultCh := make(chan ThreadStartResponse, 1)
	errCh := make(chan error, 1)
	go func() {
		result, err := client.ThreadStart(ctx, ThreadStartParams{
			CWD:                   "/home/agent",
			ApprovalPolicy:        "never",
			Sandbox:               "workspace-write",
			DeveloperInstructions: "You are the sales agent.",
		})
		resultCh <- result
		errCh <- err
	}()
	request := server.read()
	if method := rawString(t, request["method"]); method != MethodThreadStart {
		t.Fatalf("method = %q", method)
	}
	var params map[string]json.RawMessage
	if err := json.Unmarshal(request["params"], &params); err != nil {
		t.Fatal(err)
	}
	if got := rawString(t, params["developerInstructions"]); got != "You are the sales agent." {
		t.Fatalf("developer instructions = %q", got)
	}
	if _, exists := params["baseInstructions"]; exists {
		t.Fatal("baseInstructions must not be exposed by the adapter")
	}
	for _, experimentalField := range []string{"runtimeWorkspaceRoots", "permissions", "historyMode", "responsesapiClientMetadata"} {
		if _, exists := params[experimentalField]; exists {
			t.Fatalf("stable client sent experimental field %q", experimentalField)
		}
	}
	server.write(map[string]any{
		"id": json.RawMessage(request["id"]),
		"result": map[string]any{
			"thread":        map[string]any{"id": "thread-1", "status": map[string]any{"type": "idle"}},
			"model":         "gpt-5.6-codex",
			"modelProvider": "openai",
			"cwd":           "/home/agent",
		},
	})
	if err := <-errCh; err != nil {
		t.Fatalf("thread start: %v", err)
	}
	if got := (<-resultCh).Thread.ID; got != "thread-1" {
		t.Fatalf("thread id = %q", got)
	}
}

func TestNotificationAndCommandApproval(t *testing.T) {
	client, server := newFakePair(t)
	server.write(map[string]any{
		"method": "item/agentMessage/delta",
		"params": map[string]any{"delta": "hello"},
	})
	select {
	case notification := <-client.Notifications():
		if notification.Method != "item/agentMessage/delta" || !strings.Contains(string(notification.Params), "hello") {
			t.Fatalf("unexpected notification: %#v", notification)
		}
	case <-time.After(time.Second):
		t.Fatal("notification was not delivered")
	}

	server.write(map[string]any{
		"id":     "approval-1",
		"method": MethodCommandApproval,
		"params": map[string]any{
			"kind": "command", "threadId": "thread-1", "turnId": "turn-1",
			"itemId": "item-1", "startedAtMs": 123, "environmentId": nil,
			"command": "date", "cwd": "/home/agent",
		},
	})
	var request ServerRequest
	select {
	case request = <-client.ApprovalRequests():
	case <-time.After(time.Second):
		t.Fatal("approval request was not delivered")
	}
	if request.Method != MethodCommandApproval || request.ID.String() != `"approval-1"` {
		t.Fatalf("unexpected approval request: %#v", request)
	}
	var params CommandApprovalParams
	if err := request.DecodeParams(&params); err != nil {
		t.Fatal(err)
	}
	if params.Command == nil || *params.Command != "date" {
		t.Fatalf("command params = %#v", params)
	}

	responseDone := make(chan error, 1)
	go func() {
		responseDone <- client.RespondCommandApproval(context.Background(), request, DecisionAccept)
	}()
	response := server.read()
	if id := rawString(t, response["id"]); id != "approval-1" {
		t.Fatalf("response id = %q", id)
	}
	var result struct {
		Decision ApprovalDecision `json:"decision"`
	}
	if err := json.Unmarshal(response["result"], &result); err != nil {
		t.Fatal(err)
	}
	if result.Decision != DecisionAccept {
		t.Fatalf("decision = %q", result.Decision)
	}
	if err := <-responseDone; err != nil {
		t.Fatalf("respond approval: %v", err)
	}
	if err := client.RespondCommandApproval(context.Background(), request, DecisionAccept); err == nil {
		t.Fatal("second approval response unexpectedly succeeded")
	}
}

func TestUnknownServerRequestIsRejected(t *testing.T) {
	client, server := newFakePair(t)
	server.write(map[string]any{
		"id": 77, "method": "item/tool/call", "params": map[string]any{},
	})
	response := server.read()
	var rpcErr wireError
	if err := json.Unmarshal(response["error"], &rpcErr); err != nil {
		t.Fatal(err)
	}
	if rpcErr.Code != -32601 {
		t.Fatalf("error code = %d", rpcErr.Code)
	}
	select {
	case request := <-client.ApprovalRequests():
		t.Fatalf("unknown request was exposed: %#v", request)
	case <-time.After(30 * time.Millisecond):
	}
}

func TestRPCError(t *testing.T) {
	client, server := newFakePair(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	errCh := make(chan error, 1)
	go func() {
		_, err := client.ThreadRead(ctx, ThreadReadParams{ThreadID: "missing"})
		errCh <- err
	}()
	request := server.read()
	server.write(map[string]any{
		"id":    json.RawMessage(request["id"]),
		"error": map[string]any{"code": -32000, "message": "thread missing", "data": map[string]any{"threadId": "missing"}},
	})
	err := <-errCh
	var rpcErr *RPCError
	if !errors.As(err, &rpcErr) {
		t.Fatalf("error type = %T (%v)", err, err)
	}
	if rpcErr.Code != -32000 || !strings.Contains(rpcErr.Error(), "thread missing") {
		t.Fatalf("rpc error = %#v", rpcErr)
	}
}

func TestMethodValidationDoesNotWrite(t *testing.T) {
	client, _ := newFakePair(t)
	ctx := context.Background()
	if _, err := client.ThreadResume(ctx, ThreadResumeParams{}); err == nil {
		t.Fatal("empty thread id was accepted")
	}
	if _, err := client.ThreadStart(ctx, ThreadStartParams{CWD: "relative"}); err == nil {
		t.Fatal("relative cwd was accepted")
	}
	if _, err := client.TurnStart(ctx, TurnStartParams{ThreadID: "thread-1"}); err == nil {
		t.Fatal("empty turn input was accepted")
	}
	if err := client.AccountLoginAPIKey(ctx, "  "); err == nil {
		t.Fatal("empty API key was accepted")
	}
	if err := client.call(ctx, "fs/remove", struct{}{}, nil); !errors.Is(err, ErrMethodNotAllowed) {
		t.Fatalf("unsafe raw method error = %v", err)
	}
}

func TestInvalidMessageStopsClient(t *testing.T) {
	client, server := newFakePair(t)
	if _, err := server.conn.Write([]byte("not-json\n")); err != nil {
		t.Fatal(err)
	}
	select {
	case <-client.Done():
	case <-time.After(time.Second):
		t.Fatal("client did not stop after malformed input")
	}
	if client.Err() == nil || !strings.Contains(client.Err().Error(), "decode app-server message") {
		t.Fatalf("client error = %v", client.Err())
	}
}

func TestConfigValidation(t *testing.T) {
	cfg := Config{Args: []string{"--listen=ws://127.0.0.1:9000"}}
	if err := validateConfig(&cfg); err == nil {
		t.Fatal("--listen was accepted")
	}
	cfg = Config{Dir: "relative"}
	if err := validateConfig(&cfg); err == nil {
		t.Fatal("relative process directory was accepted")
	}
	cfg = Config{}
	if err := validateConfig(&cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Executable != "codex" || cfg.ClientInfo.Name != "codexbot" || cfg.MaxMessageBytes == 0 {
		t.Fatalf("defaults were not applied: %#v", cfg)
	}
}

func TestStartRealCodex(t *testing.T) {
	if os.Getenv("CODEXBOT_APP_SERVER_INTEGRATION") == "" {
		t.Skip("set CODEXBOT_APP_SERVER_INTEGRATION=1 to run against the local codex binary")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client, err := Start(ctx, Config{
		Env:        []string{"CODEX_HOME=" + t.TempDir()},
		ClientInfo: ClientInfo{Name: "codexbot-integration-test", Version: "test"},
	})
	if err != nil {
		t.Fatalf("start real app-server: %v", err)
	}
	if got := client.InitializeResponse(); got.PlatformOS == "" || got.CodexHome == "" {
		t.Fatalf("incomplete initialize response: %#v", got)
	}
	if _, err := client.AccountRead(ctx, false); err != nil {
		t.Fatalf("account/read: %v", err)
	}
	workspace := t.TempDir()
	started, err := client.ThreadStart(ctx, ThreadStartParams{CWD: workspace, ApprovalPolicy: "never", Sandbox: "read-only", DeveloperInstructions: "You are a Codexbot integration test agent."})
	if err != nil {
		t.Fatalf("stable thread/start: %v", err)
	}
	if started.Thread.ID == "" {
		t.Fatal("stable thread/start returned an empty thread id")
	}
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer shutdownCancel()
	if err := client.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("shutdown real app-server: %v", err)
	}
}

func TestMCPElicitationRoundTrip(t *testing.T) {
	for _, action := range []string{"accept", "decline", "cancel"} {
		t.Run(action, func(t *testing.T) {
			client, server := newFakePair(t)
			server.write(map[string]any{
				"id": 42, "method": MethodMCPElicitation,
				"params": map[string]any{"threadId": "thread-1", "turnId": "turn-1", "serverName": "codexbot_browser", "mode": "form", "message": "Allow browser_snapshot?", "requestedSchema": map[string]any{"type": "object", "properties": map[string]any{}}, "_meta": map[string]any{"codex_approval_kind": "mcp_tool_call", "persist": []string{"session", "always"}}},
			})
			var request ServerRequest
			select {
			case request = <-client.ApprovalRequests():
			case <-time.After(time.Second):
				t.Fatal("MCP elicitation was not delivered")
			}
			var params MCPElicitationParams
			if err := request.DecodeParams(&params); err != nil {
				t.Fatal(err)
			}
			if params.ServerName != "codexbot_browser" || params.Mode != "form" || params.ThreadID != "thread-1" {
				t.Fatalf("unexpected MCP params: %#v", params)
			}
			response := MCPElicitationResponse{Action: action, Content: json.RawMessage(`{}`), Meta: json.RawMessage(`{"persist":"session"}`)}
			invalid := response
			invalid.Action = "acceptForSession"
			if err := client.RespondMCPElicitation(context.Background(), request, invalid); err == nil {
				t.Fatal("invalid MCP action accepted")
			}
			done := make(chan error, 1)
			go func() { done <- client.RespondMCPElicitation(context.Background(), request, response) }()
			wire := server.read()
			if string(wire["id"]) != "42" {
				t.Fatalf("response id = %s", wire["id"])
			}
			var got MCPElicitationResponse
			if err := json.Unmarshal(wire["result"], &got); err != nil {
				t.Fatal(err)
			}
			if got.Action != action || string(got.Meta) != `{"persist":"session"}` {
				t.Fatalf("response = %#v", got)
			}
			expectedContent := "null"
			if action == "accept" {
				expectedContent = `{}`
			}
			if string(got.Content) != expectedContent {
				t.Fatalf("content = %s", got.Content)
			}
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			if err := client.RespondMCPElicitation(context.Background(), request, response); err == nil {
				t.Fatal("duplicate MCP response accepted")
			}
		})
	}
}
