package appserver

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

var allowedClientMethods = map[string]struct{}{
	MethodInitialize:    {},
	MethodThreadStart:   {},
	MethodThreadResume:  {},
	MethodThreadRead:    {},
	MethodTurnStart:     {},
	MethodTurnSteer:     {},
	MethodTurnInterrupt: {},
	MethodAccountLogin:  {},
	MethodAccountRead:   {},
	MethodAccountLogout: {},
	MethodConfigRead:    {},
}

var allowedServerRequests = map[string]struct{}{
	MethodCommandApproval:  {},
	MethodFileApproval:     {},
	MethodPermissionReview: {},
	MethodMCPElicitation:   {},
}

type pendingResponse struct {
	result json.RawMessage
	err    *RPCError
}

type wireMessage struct {
	Method string          `json:"method,omitempty"`
	ID     json.RawMessage `json:"id,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *wireError      `json:"error,omitempty"`
}

type wireError struct {
	Code    int64           `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

// Client owns one Codex App Server child process.
type Client struct {
	stdin  io.WriteCloser
	stdout io.ReadCloser
	cmd    *exec.Cmd
	cancel context.CancelFunc

	writeMu sync.Mutex
	nextID  atomic.Uint64

	pendingMu sync.Mutex
	pending   map[string]chan pendingResponse

	requestsMu sync.Mutex
	requests   map[string]string

	eventIn    chan Notification
	events     chan Notification
	requestIn  chan ServerRequest
	requestOut chan ServerRequest

	done     chan struct{}
	doneOnce sync.Once
	errMu    sync.Mutex
	err      error

	stdinOnce sync.Once
	readDone  chan struct{}
	readErrMu sync.Mutex
	readErr   error

	maxMessageBytes int
	initialize      InitializeResponse
}

// Start launches "codex app-server --stdio", performs initialize, and sends
// the initialized notification before returning. ctx controls the lifetime of
// the child process and therefore must be scoped to the agent worker rather
// than to an individual HTTP request.
func Start(ctx context.Context, cfg Config) (*Client, error) {
	if ctx == nil {
		return nil, errors.New("start context is nil")
	}
	if err := validateConfig(&cfg); err != nil {
		return nil, err
	}

	processContext, cancel := context.WithCancel(ctx)
	args := append([]string{"app-server", "--stdio"}, cfg.Args...)
	cmd := exec.CommandContext(processContext, cfg.Executable, args...)
	cmd.Dir = cfg.Dir
	cmd.Env = append(os.Environ(), cfg.Env...)
	if cfg.Stderr == nil {
		cmd.Stderr = io.Discard
	} else {
		cmd.Stderr = cfg.Stderr
	}

	stdin, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("create app-server stdin: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		_ = stdin.Close()
		return nil, fmt.Errorf("create app-server stdout: %w", err)
	}
	if err := cmd.Start(); err != nil {
		cancel()
		_ = stdin.Close()
		_ = stdout.Close()
		return nil, fmt.Errorf("start app-server: %w", err)
	}

	client := newClientWithProcess(stdout, stdin, cfg.MaxMessageBytes, cmd, cancel)
	go client.waitForProcess(processContext)

	if err := client.initializeProtocol(ctx, cfg.ClientInfo, cfg.Capabilities); err != nil {
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 2*time.Second)
		_ = client.Shutdown(shutdownCtx)
		shutdownCancel()
		return nil, fmt.Errorf("initialize app-server: %w", err)
	}
	return client, nil
}

func newClient(stdout io.ReadCloser, stdin io.WriteCloser, maxMessageBytes int) *Client {
	return newClientWithProcess(stdout, stdin, maxMessageBytes, nil, nil)
}

func newClientWithProcess(stdout io.ReadCloser, stdin io.WriteCloser, maxMessageBytes int, cmd *exec.Cmd, cancel context.CancelFunc) *Client {
	if maxMessageBytes == 0 {
		maxMessageBytes = 16 << 20
	}
	c := &Client{
		stdin:           stdin,
		stdout:          stdout,
		cmd:             cmd,
		cancel:          cancel,
		pending:         make(map[string]chan pendingResponse),
		requests:        make(map[string]string),
		eventIn:         make(chan Notification),
		events:          make(chan Notification),
		requestIn:       make(chan ServerRequest),
		requestOut:      make(chan ServerRequest),
		done:            make(chan struct{}),
		readDone:        make(chan struct{}),
		maxMessageBytes: maxMessageBytes,
	}
	go relay(c.eventIn, c.events, c.done)
	go relay(c.requestIn, c.requestOut, c.done)
	go c.readLoop()
	return c
}

func relay[T any](input <-chan T, output chan<- T, done <-chan struct{}) {
	defer close(output)
	var queue []T
	for input != nil || len(queue) > 0 {
		var out chan<- T
		var first T
		if len(queue) > 0 {
			out = output
			first = queue[0]
		}
		select {
		case <-done:
			return
		case value, ok := <-input:
			if !ok {
				input = nil
				continue
			}
			queue = append(queue, value)
		case out <- first:
			var zero T
			queue[0] = zero
			queue = queue[1:]
		}
	}
}

func (c *Client) initializeProtocol(ctx context.Context, info ClientInfo, capabilities Capabilities) error {
	params := struct {
		ClientInfo   ClientInfo   `json:"clientInfo"`
		Capabilities Capabilities `json:"capabilities"`
	}{info, capabilities}
	if err := c.call(ctx, MethodInitialize, params, &c.initialize); err != nil {
		return err
	}
	return c.notify("initialized", nil)
}

func (c *Client) InitializeResponse() InitializeResponse { return c.initialize }

func (c *Client) Notifications() <-chan Notification { return c.events }

func (c *Client) ApprovalRequests() <-chan ServerRequest { return c.requestOut }

func (c *Client) Done() <-chan struct{} { return c.done }

func (c *Client) Err() error {
	c.errMu.Lock()
	defer c.errMu.Unlock()
	return c.err
}

func (c *Client) Wait(ctx context.Context) error {
	if ctx == nil {
		return errors.New("wait context is nil")
	}
	select {
	case <-c.done:
		return c.Err()
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *Client) Shutdown(ctx context.Context) error {
	if ctx == nil {
		return errors.New("shutdown context is nil")
	}
	c.stdinOnce.Do(func() { _ = c.stdin.Close() })
	select {
	case <-c.done:
		return c.Err()
	case <-ctx.Done():
		c.terminate()
		select {
		case <-c.done:
		case <-time.After(250 * time.Millisecond):
		}
		return ctx.Err()
	}
}

func (c *Client) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return c.Shutdown(ctx)
}

func (c *Client) terminate() {
	if c.cancel != nil {
		c.cancel()
	}
	if c.cmd != nil && c.cmd.Process != nil {
		_ = c.cmd.Process.Kill()
	}
	_ = c.stdout.Close()
}

func (c *Client) waitForProcess(processContext context.Context) {
	<-c.readDone
	waitErr := c.cmd.Wait()
	c.readErrMu.Lock()
	readErr := c.readErr
	c.readErrMu.Unlock()
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		c.finish(readErr)
		return
	}
	if processContext.Err() != nil {
		c.finish(processContext.Err())
		return
	}
	if waitErr != nil {
		c.finish(fmt.Errorf("app-server exited: %w", waitErr))
		return
	}
	c.finish(nil)
}

func (c *Client) readLoop() {
	defer close(c.readDone)
	defer close(c.eventIn)
	defer close(c.requestIn)

	scanner := bufio.NewScanner(c.stdout)
	scanner.Buffer(make([]byte, 64<<10), c.maxMessageBytes)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(strings.TrimSpace(string(line))) == 0 {
			continue
		}
		if err := c.handleLine(line); err != nil {
			c.setReadErr(err)
			c.terminate()
			if c.cmd == nil {
				c.finish(err)
			}
			return
		}
	}
	err := scanner.Err()
	if err == nil {
		err = io.EOF
	}
	c.setReadErr(err)
	if c.cmd == nil {
		if errors.Is(err, io.EOF) {
			c.finish(nil)
		} else {
			c.finish(err)
		}
	}
}

func (c *Client) setReadErr(err error) {
	c.readErrMu.Lock()
	c.readErr = err
	c.readErrMu.Unlock()
}

func (c *Client) handleLine(line []byte) error {
	var message wireMessage
	if err := json.Unmarshal(line, &message); err != nil {
		return fmt.Errorf("decode app-server message: %w", err)
	}
	if message.Method != "" {
		if len(message.ID) > 0 {
			return c.handleServerRequest(message)
		}
		c.eventIn <- Notification{Method: message.Method, Params: cloneRaw(message.Params)}
		return nil
	}
	if len(message.ID) == 0 {
		return errors.New("app-server message has neither method nor id")
	}
	key, err := requestIDKey(message.ID)
	if err != nil {
		return err
	}
	c.pendingMu.Lock()
	pending, ok := c.pending[key]
	if ok {
		delete(c.pending, key)
	}
	c.pendingMu.Unlock()
	if !ok {
		return nil
	}
	response := pendingResponse{result: cloneRaw(message.Result)}
	if message.Error != nil {
		response.err = &RPCError{Code: message.Error.Code, Message: message.Error.Message, Data: cloneRaw(message.Error.Data)}
	}
	pending <- response
	return nil
}

func (c *Client) handleServerRequest(message wireMessage) error {
	key, err := requestIDKey(message.ID)
	if err != nil {
		return err
	}
	if _, ok := allowedServerRequests[message.Method]; !ok {
		return c.writeError(RequestID(key), -32601, "method not found", nil)
	}
	c.requestsMu.Lock()
	if _, duplicate := c.requests[key]; duplicate {
		c.requestsMu.Unlock()
		return c.writeError(RequestID(key), -32600, "duplicate request id", nil)
	}
	c.requests[key] = message.Method
	c.requestsMu.Unlock()
	c.requestIn <- ServerRequest{ID: RequestID(key), Method: message.Method, Params: cloneRaw(message.Params)}
	return nil
}

func (c *Client) call(ctx context.Context, method string, params any, result any) error {
	if ctx == nil {
		return errors.New("call context is nil")
	}
	if _, ok := allowedClientMethods[method]; !ok {
		return fmt.Errorf("%w: %s", ErrMethodNotAllowed, method)
	}
	select {
	case <-c.done:
		return c.closedError()
	default:
	}

	id := c.nextID.Add(1)
	key := strconv.FormatUint(id, 10)
	paramsJSON, err := json.Marshal(params)
	if err != nil {
		return fmt.Errorf("encode %s params: %w", method, err)
	}
	pending := make(chan pendingResponse, 1)
	c.pendingMu.Lock()
	c.pending[key] = pending
	c.pendingMu.Unlock()

	request := struct {
		Method string          `json:"method"`
		ID     uint64          `json:"id"`
		Params json.RawMessage `json:"params"`
	}{method, id, paramsJSON}
	if err := c.writeJSON(request); err != nil {
		c.removePending(key)
		c.terminate()
		return err
	}

	select {
	case response := <-pending:
		if response.err != nil {
			return response.err
		}
		if result == nil {
			return nil
		}
		if len(response.result) == 0 {
			return fmt.Errorf("%s response has no result", method)
		}
		if err := json.Unmarshal(response.result, result); err != nil {
			return fmt.Errorf("decode %s result: %w", method, err)
		}
		return nil
	case <-ctx.Done():
		c.removePending(key)
		return ctx.Err()
	case <-c.done:
		c.removePending(key)
		return c.closedError()
	}
}

func (c *Client) notify(method string, params any) error {
	if method != "initialized" {
		return fmt.Errorf("%w: %s", ErrMethodNotAllowed, method)
	}
	message := struct {
		Method string `json:"method"`
	}{Method: method}
	return c.writeJSON(message)
}

func (c *Client) writeJSON(value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("encode app-server message: %w", err)
	}
	data = append(data, '\n')
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	select {
	case <-c.done:
		return c.closedError()
	default:
	}
	if _, err := c.stdin.Write(data); err != nil {
		return fmt.Errorf("write app-server message: %w", err)
	}
	return nil
}

func (c *Client) writeError(id RequestID, code int64, message string, data any) error {
	var raw json.RawMessage
	if data != nil {
		encoded, err := json.Marshal(data)
		if err != nil {
			return err
		}
		raw = encoded
	}
	response := struct {
		ID    RequestID `json:"id"`
		Error wireError `json:"error"`
	}{id, wireError{Code: code, Message: message, Data: raw}}
	return c.writeJSON(response)
}

func (c *Client) removePending(key string) {
	c.pendingMu.Lock()
	delete(c.pending, key)
	c.pendingMu.Unlock()
}

func (c *Client) closedError() error {
	if err := c.Err(); err != nil {
		return fmt.Errorf("%w: %v", ErrClosed, err)
	}
	return ErrClosed
}

func (c *Client) finish(err error) {
	c.doneOnce.Do(func() {
		c.errMu.Lock()
		c.err = err
		c.errMu.Unlock()
		close(c.done)
	})
}

func requestIDKey(raw json.RawMessage) (string, error) {
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return "", fmt.Errorf("%w: %v", ErrInvalidRequestID, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return "", ErrInvalidRequestID
	}
	switch value := value.(type) {
	case string:
		encoded, _ := json.Marshal(value)
		return string(encoded), nil
	case json.Number:
		if strings.ContainsAny(value.String(), ".eE") {
			return "", ErrInvalidRequestID
		}
		if _, err := strconv.ParseInt(value.String(), 10, 64); err != nil {
			return "", ErrInvalidRequestID
		}
		return value.String(), nil
	default:
		return "", ErrInvalidRequestID
	}
}

func cloneRaw(raw json.RawMessage) json.RawMessage {
	if raw == nil {
		return nil
	}
	return append(json.RawMessage(nil), raw...)
}
