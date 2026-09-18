package appserver

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
)

const (
	MethodInitialize       = "initialize"
	MethodThreadStart      = "thread/start"
	MethodThreadResume     = "thread/resume"
	MethodThreadRead       = "thread/read"
	MethodTurnStart        = "turn/start"
	MethodTurnSteer        = "turn/steer"
	MethodTurnInterrupt    = "turn/interrupt"
	MethodAccountLogin     = "account/login/start"
	MethodAccountRead      = "account/read"
	MethodAccountLogout    = "account/logout"
	MethodConfigRead       = "config/read"
	MethodCommandApproval  = "item/commandExecution/requestApproval"
	MethodFileApproval     = "item/fileChange/requestApproval"
	MethodPermissionReview = "item/permissions/requestApproval"
	MethodMCPElicitation   = "mcpServer/elicitation/request"
)

var (
	ErrClosed           = errors.New("codex app-server client is closed")
	ErrMethodNotAllowed = errors.New("codex app-server method is not allowed")
	ErrInvalidRequestID = errors.New("invalid JSON-RPC request id")
)

// Config controls the stdio child process and protocol initialization.
type Config struct {
	// Executable defaults to "codex".
	Executable string
	// Args are appended after "app-server --stdio". Transport-changing args
	// such as --listen are rejected.
	Args []string
	// Dir is the child process working directory.
	Dir string
	// Env is appended to the inherited environment.
	Env []string
	// Stderr receives app-server diagnostic output. It defaults to io.Discard.
	Stderr io.Writer

	ClientInfo   ClientInfo
	Capabilities Capabilities

	// MaxMessageBytes limits one newline-delimited server message. It defaults
	// to 16 MiB.
	MaxMessageBytes int
}

type ClientInfo struct {
	Name    string  `json:"name"`
	Title   *string `json:"title"`
	Version string  `json:"version"`
}

type Capabilities struct {
	ExperimentalAPI    bool                       `json:"experimentalApi"`
	RequestAttestation bool                       `json:"requestAttestation"`
	OptOutMethods      []string                   `json:"optOutNotificationMethods,omitempty"`
	Extensions         map[string]json.RawMessage `json:"extensions,omitempty"`
}

type InitializeResponse struct {
	UserAgent      string `json:"userAgent"`
	CodexHome      string `json:"codexHome"`
	PlatformFamily string `json:"platformFamily"`
	PlatformOS     string `json:"platformOs"`
}

// Thread contains the stable fields needed by the control plane. Complex
// item payloads remain RawMessage so protocol additions remain forward
// compatible within the pinned Codex version.
type Thread struct {
	ID                   string          `json:"id"`
	SessionID            string          `json:"sessionId"`
	ForkedFromID         *string         `json:"forkedFromId"`
	ParentThreadID       *string         `json:"parentThreadId"`
	Preview              string          `json:"preview"`
	Ephemeral            bool            `json:"ephemeral"`
	ModelProvider        string          `json:"modelProvider"`
	CreatedAt            int64           `json:"createdAt"`
	UpdatedAt            int64           `json:"updatedAt"`
	Path                 *string         `json:"path"`
	CWD                  string          `json:"cwd"`
	Name                 *string         `json:"name"`
	CanAcceptDirectInput *bool           `json:"canAcceptDirectInput"`
	Status               ThreadStatus    `json:"status"`
	Turns                []Turn          `json:"turns"`
	Extra                json.RawMessage `json:"extra"`
	Source               json.RawMessage `json:"source"`
	GitInfo              json.RawMessage `json:"gitInfo"`
}

type ThreadStatus struct {
	Type        string            `json:"type"`
	ActiveFlags []json.RawMessage `json:"activeFlags,omitempty"`
}

type Turn struct {
	ID          string            `json:"id"`
	Items       []json.RawMessage `json:"items"`
	ItemsView   json.RawMessage   `json:"itemsView"`
	Status      string            `json:"status"`
	Error       json.RawMessage   `json:"error"`
	StartedAt   *int64            `json:"startedAt"`
	CompletedAt *int64            `json:"completedAt"`
	DurationMS  *int64            `json:"durationMs"`
}

type ThreadStartParams struct {
	Model                 string         `json:"model,omitempty"`
	ModelProvider         string         `json:"modelProvider,omitempty"`
	ServiceTier           string         `json:"serviceTier,omitempty"`
	CWD                   string         `json:"cwd,omitempty"`
	ApprovalPolicy        string         `json:"approvalPolicy,omitempty"`
	ApprovalsReviewer     string         `json:"approvalsReviewer,omitempty"`
	Sandbox               string         `json:"sandbox,omitempty"`
	DeveloperInstructions string         `json:"developerInstructions,omitempty"`
	Personality           string         `json:"personality,omitempty"`
	Ephemeral             *bool          `json:"ephemeral,omitempty"`
	BaseInstructions      string         `json:"baseInstructions,omitempty"`
	Config                map[string]any `json:"config,omitempty"`
}

// ConfigReadResponse retains unknown config keys, including MCP servers, without
// coupling the client to the full evolving Codex configuration schema.
type ConfigReadResponse struct {
	Config map[string]json.RawMessage `json:"config"`
}

type ConfigReadParams struct {
	CWD           string `json:"cwd,omitempty"`
	IncludeLayers bool   `json:"includeLayers"`
}

type ThreadStartResponse struct {
	Thread                Thread          `json:"thread"`
	Model                 string          `json:"model"`
	ModelProvider         string          `json:"modelProvider"`
	ServiceTier           *string         `json:"serviceTier"`
	CWD                   string          `json:"cwd"`
	RuntimeWorkspaceRoots []string        `json:"runtimeWorkspaceRoots"`
	InstructionSources    []string        `json:"instructionSources"`
	ApprovalPolicy        json.RawMessage `json:"approvalPolicy"`
	ApprovalsReviewer     string          `json:"approvalsReviewer"`
	Sandbox               json.RawMessage `json:"sandbox"`
}

type ThreadResumeParams struct {
	ThreadID              string `json:"threadId"`
	Model                 string `json:"model,omitempty"`
	ModelProvider         string `json:"modelProvider,omitempty"`
	ServiceTier           string `json:"serviceTier,omitempty"`
	CWD                   string `json:"cwd,omitempty"`
	ApprovalPolicy        string `json:"approvalPolicy,omitempty"`
	ApprovalsReviewer     string `json:"approvalsReviewer,omitempty"`
	Sandbox               string `json:"sandbox,omitempty"`
	DeveloperInstructions string `json:"developerInstructions,omitempty"`
	Personality           string `json:"personality,omitempty"`
	ExcludeTurns          bool   `json:"excludeTurns,omitempty"`
}

type ThreadResumeResponse struct {
	ThreadStartResponse
	InitialTurnsPage json.RawMessage `json:"initialTurnsPage"`
	TurnsBackwards   *string         `json:"turnsBackwardsCursor"`
	ItemsBackwards   *string         `json:"itemsBackwardsCursor"`
}

type ThreadReadParams struct {
	ThreadID     string `json:"threadId"`
	IncludeTurns bool   `json:"includeTurns,omitempty"`
}

type ThreadReadResponse struct {
	Thread Thread `json:"thread"`
}

type InputType string

const (
	InputText       InputType = "text"
	InputImage      InputType = "image"
	InputLocalImage InputType = "localImage"
	InputAudio      InputType = "audio"
	InputLocalAudio InputType = "localAudio"
)

// UserInput models the input variants used by codexbot. Text inputs always
// encode the schema-required text_elements as an empty array.
type UserInput struct {
	Type   InputType
	Text   string
	URL    string
	Path   string
	Detail string
}

func TextInput(text string) UserInput { return UserInput{Type: InputText, Text: text} }

func (i UserInput) MarshalJSON() ([]byte, error) {
	switch i.Type {
	case InputText:
		return json.Marshal(struct {
			Type         InputType         `json:"type"`
			Text         string            `json:"text"`
			TextElements []json.RawMessage `json:"text_elements"`
		}{i.Type, i.Text, []json.RawMessage{}})
	case InputImage:
		return json.Marshal(struct {
			Type   InputType `json:"type"`
			Detail string    `json:"detail,omitempty"`
			URL    string    `json:"url"`
		}{i.Type, i.Detail, i.URL})
	case InputLocalImage:
		return json.Marshal(struct {
			Type   InputType `json:"type"`
			Detail string    `json:"detail,omitempty"`
			Path   string    `json:"path"`
		}{i.Type, i.Detail, i.Path})
	case InputLocalAudio:
		return json.Marshal(struct {
			Type InputType `json:"type"`
			Path string    `json:"path"`
		}{i.Type, i.Path})
	case InputAudio:
		return json.Marshal(struct {
			Type InputType `json:"type"`
			URL  string    `json:"url"`
		}{i.Type, i.URL})
	default:
		return nil, fmt.Errorf("unsupported input type %q", i.Type)
	}
}

// SandboxPolicy represents the sandbox modes used by Codexbot turns. CWD is
// writable in workspaceWrite mode; explicit roots are additional allowed paths.
type SandboxPolicy struct {
	Type          string   `json:"type"`
	WritableRoots []string `json:"writableRoots,omitempty"`
	NetworkAccess bool     `json:"networkAccess,omitempty"`
}

type TurnStartParams struct {
	ThreadID            string          `json:"threadId"`
	ClientUserMessageID string          `json:"clientUserMessageId,omitempty"`
	Input               []UserInput     `json:"input"`
	TurnTrigger         string          `json:"turnTrigger,omitempty"`
	CWD                 string          `json:"cwd,omitempty"`
	ApprovalPolicy      string          `json:"approvalPolicy,omitempty"`
	ApprovalsReviewer   string          `json:"approvalsReviewer,omitempty"`
	SandboxPolicy       *SandboxPolicy  `json:"sandboxPolicy,omitempty"`
	Model               string          `json:"model,omitempty"`
	ServiceTierForTurn  string          `json:"serviceTierForTurn,omitempty"`
	Effort              string          `json:"effort,omitempty"`
	Personality         string          `json:"personality,omitempty"`
	OutputSchema        json.RawMessage `json:"outputSchema,omitempty"`
}

type TurnStartResponse struct {
	Turn Turn `json:"turn"`
}

type TurnSteerParams struct {
	ThreadID            string      `json:"threadId"`
	ClientUserMessageID string      `json:"clientUserMessageId,omitempty"`
	Input               []UserInput `json:"input"`
	ExpectedTurnID      string      `json:"expectedTurnId"`
}

type TurnSteerResponse struct {
	TurnID string `json:"turnId"`
}

type TurnInterruptParams struct {
	ThreadID string `json:"threadId"`
	TurnID   string `json:"turnId"`
}

type Account struct {
	Type                        string  `json:"type"`
	Email                       *string `json:"email,omitempty"`
	PlanType                    *string `json:"planType,omitempty"`
	UsesCodexManagedCredentials *bool   `json:"usesCodexManagedCredentials,omitempty"`
}

type AccountReadResponse struct {
	Account            *Account `json:"account"`
	RequiresOpenAIAuth bool     `json:"requiresOpenaiAuth"`
}

type DeviceCodeLoginResponse struct {
	Type            string `json:"type"`
	LoginID         string `json:"loginId"`
	VerificationURL string `json:"verificationUrl"`
	UserCode        string `json:"userCode"`
}

type APIKeyLoginResponse struct {
	Type string `json:"type"`
}

type Notification struct {
	Method string
	Params json.RawMessage
}

// RequestID is the canonical JSON encoding of a string or integer request id.
// Values are created from server requests and should be treated as opaque.
type RequestID string

func (id RequestID) MarshalJSON() ([]byte, error) {
	raw := []byte(id)
	if _, err := requestIDKey(raw); err != nil {
		return nil, err
	}
	return raw, nil
}

func (id RequestID) String() string { return string(id) }

type ServerRequest struct {
	ID     RequestID
	Method string
	Params json.RawMessage
}

func (r ServerRequest) DecodeParams(dst any) error {
	if dst == nil {
		return errors.New("approval params destination is nil")
	}
	return json.Unmarshal(r.Params, dst)
}

type CommandApprovalParams struct {
	Kind               string            `json:"kind"`
	ThreadID           string            `json:"threadId"`
	TurnID             string            `json:"turnId"`
	ItemID             string            `json:"itemId"`
	StartedAtMS        int64             `json:"startedAtMs"`
	ApprovalID         *string           `json:"approvalId,omitempty"`
	EnvironmentID      *string           `json:"environmentId"`
	Reason             *string           `json:"reason,omitempty"`
	Command            *string           `json:"command,omitempty"`
	CWD                *string           `json:"cwd,omitempty"`
	AvailableDecisions []json.RawMessage `json:"availableDecisions,omitempty"`
}

type FileApprovalParams struct {
	ThreadID    string  `json:"threadId"`
	TurnID      string  `json:"turnId"`
	ItemID      string  `json:"itemId"`
	StartedAtMS int64   `json:"startedAtMs"`
	Reason      *string `json:"reason,omitempty"`
	GrantRoot   *string `json:"grantRoot,omitempty"`
}

type PermissionApprovalParams struct {
	ThreadID      string          `json:"threadId"`
	TurnID        string          `json:"turnId"`
	ItemID        string          `json:"itemId"`
	EnvironmentID *string         `json:"environmentId"`
	StartedAtMS   int64           `json:"startedAtMs"`
	CWD           string          `json:"cwd"`
	Reason        *string         `json:"reason"`
	Permissions   json.RawMessage `json:"permissions"`
}

type ApprovalDecision string

const (
	DecisionAccept           ApprovalDecision = "accept"
	DecisionAcceptForSession ApprovalDecision = "acceptForSession"
	DecisionDecline          ApprovalDecision = "decline"
	DecisionCancel           ApprovalDecision = "cancel"
)

type PermissionsApprovalResponse struct {
	Permissions      map[string]json.RawMessage `json:"permissions"`
	Scope            string                     `json:"scope"`
	StrictAutoReview bool                       `json:"strictAutoReview,omitempty"`
}

type MCPElicitationParams struct {
	ThreadID        string          `json:"threadId"`
	TurnID          *string         `json:"turnId"`
	ServerName      string          `json:"serverName"`
	Mode            string          `json:"mode"`
	Message         string          `json:"message"`
	RequestedSchema json.RawMessage `json:"requestedSchema"`
	Meta            json.RawMessage `json:"_meta"`
}

type MCPElicitationResponse struct {
	Action  string          `json:"action"`
	Content json.RawMessage `json:"content"`
	Meta    json.RawMessage `json:"_meta,omitempty"`
}

type RPCError struct {
	Code    int64
	Message string
	Data    json.RawMessage
}

func (e *RPCError) Error() string {
	if len(e.Data) == 0 || bytes.Equal(e.Data, []byte("null")) {
		return fmt.Sprintf("codex app-server RPC error %d: %s", e.Code, e.Message)
	}
	return fmt.Sprintf("codex app-server RPC error %d: %s (%s)", e.Code, e.Message, string(e.Data))
}

func validateConfig(cfg *Config) error {
	if cfg.Executable == "" {
		cfg.Executable = "codex"
	}
	if strings.IndexByte(cfg.Executable, 0) >= 0 {
		return errors.New("app-server executable contains NUL")
	}
	for _, arg := range cfg.Args {
		if strings.IndexByte(arg, 0) >= 0 {
			return errors.New("app-server argument contains NUL")
		}
		if arg == "--stdio" || arg == "--listen" || strings.HasPrefix(arg, "--listen=") {
			return fmt.Errorf("transport-changing app-server argument %q is not allowed", arg)
		}
	}
	if cfg.Dir != "" && !filepath.IsAbs(cfg.Dir) {
		return errors.New("app-server working directory must be absolute")
	}
	for _, env := range cfg.Env {
		if strings.IndexByte(env, 0) >= 0 || !strings.Contains(env, "=") {
			return fmt.Errorf("invalid app-server environment entry %q", env)
		}
	}
	if cfg.ClientInfo.Name == "" {
		cfg.ClientInfo.Name = "codexbot"
	}
	if cfg.ClientInfo.Version == "" {
		cfg.ClientInfo.Version = "dev"
	}
	if cfg.MaxMessageBytes == 0 {
		cfg.MaxMessageBytes = 16 << 20
	}
	if cfg.MaxMessageBytes < 1024 {
		return errors.New("MaxMessageBytes must be at least 1024")
	}
	return nil
}

func validateThreadOptions(cwd, approval, reviewer, sandbox, personality string) error {
	if cwd != "" && !filepath.IsAbs(cwd) {
		return errors.New("cwd must be absolute")
	}
	if approval != "" && approval != "untrusted" && approval != "on-request" && approval != "never" {
		return fmt.Errorf("unsupported approval policy %q", approval)
	}
	if reviewer != "" && reviewer != "user" && reviewer != "auto_review" && reviewer != "guardian_subagent" {
		return fmt.Errorf("unsupported approvals reviewer %q", reviewer)
	}
	if sandbox != "" && sandbox != "read-only" && sandbox != "workspace-write" && sandbox != "danger-full-access" {
		return fmt.Errorf("unsupported sandbox mode %q", sandbox)
	}
	if personality != "" && personality != "none" && personality != "friendly" && personality != "pragmatic" {
		return fmt.Errorf("unsupported personality %q", personality)
	}
	return nil
}

func validateInputs(inputs []UserInput) error {
	if len(inputs) == 0 {
		return errors.New("at least one user input is required")
	}
	for index, input := range inputs {
		switch input.Type {
		case InputText:
			if strings.TrimSpace(input.Text) == "" {
				return fmt.Errorf("input %d text is empty", index)
			}
		case InputImage, InputAudio:
			if input.URL == "" {
				return fmt.Errorf("input %d URL is empty", index)
			}
		case InputLocalImage, InputLocalAudio:
			if input.Path == "" || !filepath.IsAbs(input.Path) {
				return fmt.Errorf("input %d path must be absolute", index)
			}
		default:
			return fmt.Errorf("input %d has unsupported type %q", index, input.Type)
		}
		if input.Detail != "" && input.Type != InputImage && input.Type != InputLocalImage {
			return fmt.Errorf("input %d detail is only valid for images", index)
		}
		if input.Detail != "" && input.Detail != "auto" && input.Detail != "low" && input.Detail != "high" && input.Detail != "original" {
			return fmt.Errorf("input %d has unsupported detail %q", index, input.Detail)
		}
	}
	return nil
}
