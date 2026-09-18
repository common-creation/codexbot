package agentworker

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/common-creation/codexbot/internal/appserver"
	"github.com/common-creation/codexbot/internal/domain"
	"github.com/common-creation/codexbot/internal/runtimeclient"
	"github.com/google/uuid"
)

type steerDelivery struct {
	Fingerprint string                     `json:"fingerprint"`
	State       string                     `json:"state"`
	Response    runtimeclient.TurnResponse `json:"response"`
	InputEvent  json.RawMessage            `json:"inputEvent,omitempty"`
	Recorded    bool                       `json:"recorded,omitempty"`
}

func (w *Worker) steer(rw http.ResponseWriter, r *http.Request) {
	var in runtimeclient.SteerRequest
	if domain.DecodeMessage(r.Body, &in) != nil || in.RunID == "" || in.ThreadID == "" || in.ExpectedTurnID == "" || (strings.TrimSpace(in.Prompt) == "" && len(in.Attachments) == 0) {
		http.Error(rw, "invalid steer request", 400)
		return
	}
	if err := domain.ValidateAttachments(in.Attachments); err != nil {
		http.Error(rw, err.Error(), 400)
		return
	}
	if _, err := uuid.Parse(in.MessageID); err != nil {
		http.Error(rw, "invalid message id", 400)
		return
	}
	if _, err := uuid.Parse(in.RunID); err != nil {
		http.Error(rw, "invalid run id", 400)
		return
	}
	w.operationMu.Lock()
	defer w.operationMu.Unlock()
	raw, _ := json.Marshal(in)
	digest := sha256.Sum256(raw)
	fingerprint := hex.EncodeToString(digest[:])
	path := filepath.Join(w.cfg.ProfileDir, "steering", in.MessageID+".json")
	if raw, err := os.ReadFile(path); err == nil {
		var delivery steerDelivery
		if json.Unmarshal(raw, &delivery) != nil {
			http.Error(rw, "steer delivery state unreadable; delivery unknown", 503)
			return
		}
		if delivery.Fingerprint != fingerprint {
			http.Error(rw, "message id reused with different input", 422)
			return
		}
		switch delivery.State {
		case "accepted":
			if err := w.recordSteerInput(in.RunID, path, &delivery); err != nil {
				http.Error(rw, "steer accepted but input event could not be persisted", 503)
				return
			}
			writeJSON(rw, 200, delivery.Response)
		case "rejected":
			http.Error(rw, "steer was rejected", 409)
		default:
			http.Error(rw, "steer delivery unknown; message will not be delivered again", 503)
		}
		return
	} else if !errors.Is(err, os.ErrNotExist) {
		http.Error(rw, "steer delivery state unavailable", 503)
		return
	}
	// App-server may emit the answer before acknowledging turn/steer. Its
	// notification relay buffers independently of RPC responses, so hold these
	// notifications until the accepted input has its earlier timeline sequence.
	w.inputMu.Lock()
	defer w.inputMu.Unlock()
	w.mu.Lock()
	active := w.active
	afterSequence := int64(0)
	if active != nil {
		afterSequence = active.Sequence
	}
	matches := active != nil && active.Status == "running" && active.RunID == in.RunID && active.ThreadID == in.ThreadID && active.TurnID == in.ExpectedTurnID
	w.mu.Unlock()
	if !matches {
		http.Error(rw, "active turn changed", 409)
		return
	}
	inputs, attachmentDir, err := materializeAttachments(w.cfg.agentHome(), in.Prompt, in.Attachments)
	if err != nil {
		http.Error(rw, "could not save attachments", 500)
		return
	}
	// Once dispatch begins, retain files unless the server definitively rejects
	// the input. A lost response may still leave the model reading these paths.
	retainAttachments := false
	defer func() {
		if !retainAttachments && attachmentDir != "" {
			_ = os.RemoveAll(attachmentDir)
		}
	}()
	delivery := steerDelivery{Fingerprint: fingerprint, State: "pending"}
	if err := saveSteerDelivery(path, delivery); err != nil {
		http.Error(rw, "could not persist steer delivery", 503)
		return
	}
	retainAttachments = true
	out, err := w.client.TurnSteer(r.Context(), appserver.TurnSteerParams{ThreadID: in.ThreadID, ExpectedTurnID: in.ExpectedTurnID, ClientUserMessageID: in.MessageID, Input: inputs})
	if err != nil {
		var rpcErr *appserver.RPCError
		// Invalid requests/parameters are rejected before input is enqueued. Server
		// and transport failures may occur after enqueueing, so preserve pending.
		if errors.As(err, &rpcErr) && (rpcErr.Code == -32600 || rpcErr.Code == -32602) {
			retainAttachments = false
			delivery.State = "rejected"
			if saveSteerDelivery(path, delivery) == nil {
				http.Error(rw, err.Error(), 409)
				return
			}
		}
		http.Error(rw, "steer delivery unknown: "+err.Error(), 503)
		return
	}
	delivery.State = "accepted"
	delivery.Response = runtimeclient.TurnResponse{ThreadID: in.ThreadID, TurnID: out.TurnID, AfterSequence: afterSequence}
	inputEvent := map[string]string{"id": "steer-" + in.MessageID, "text": domain.AttachmentPrompt(in.Prompt, in.Attachments), "messageId": in.MessageID, "threadId": in.ThreadID, "turnId": out.TurnID, "source": "steer"}
	if in.SenderAgentID != "" {
		inputEvent["senderAgentId"] = in.SenderAgentID
	}
	if in.TaskID != "" {
		inputEvent["taskId"] = in.TaskID
		inputEvent["source"] = "collaboration"
	}
	delivery.InputEvent, _ = json.Marshal(inputEvent)
	if err := saveSteerDelivery(path, delivery); err != nil {
		http.Error(rw, "steer accepted but delivery record could not be persisted; delivery unknown", 503)
		return
	}
	w.mu.Lock()
	if w.active != nil && w.active.RunID == in.RunID {
		w.active.Prompt += "\n\n" + domain.AttachmentPrompt(in.Prompt, in.Attachments)
	}
	w.mu.Unlock()
	// The accepted ledger repairs an interrupted event append on retry without
	// dispatching the input again or duplicating the visible timeline message.
	if err := w.recordSteerInput(in.RunID, path, &delivery); err != nil {
		http.Error(rw, "steer accepted but input event could not be persisted", 503)
		return
	}
	if err := w.appendEvent(in.RunID, "run.steered", map[string]string{"messageId": in.MessageID, "threadId": in.ThreadID, "turnId": out.TurnID}); err != nil {
		w.logger.Error("record steer event", "messageId", in.MessageID, "error", err)
	}
	writeJSON(rw, 200, delivery.Response)
}

func (w *Worker) recordSteerInput(runID, path string, delivery *steerDelivery) error {
	// Older accepted records have no timeline event to recover.
	if delivery.Recorded || len(delivery.InputEvent) == 0 {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	var input struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(delivery.InputEvent, &input); err != nil {
		return err
	}
	file, err := os.OpenFile(w.eventPath(runID), os.O_CREATE|os.O_RDWR|os.O_APPEND, 0600)
	if err != nil {
		return err
	}
	defer file.Close()
	sequence := delivery.Response.AfterSequence
	if w.active != nil && w.active.RunID == runID && w.active.Sequence > sequence {
		sequence = w.active.Sequence
	}
	decoder := json.NewDecoder(file)
	found := false
	for {
		var event workerEvent
		if err := decoder.Decode(&event); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			return err
		}
		if event.Sequence > sequence {
			sequence = event.Sequence
		}
		var existing struct {
			ID string `json:"id"`
		}
		if event.Type == "message.user" && json.Unmarshal(event.Payload, &existing) == nil && existing.ID == input.ID {
			found = true
		}
	}
	if !found {
		sequence++
		if err := json.NewEncoder(file).Encode(workerEvent{Sequence: sequence, Type: "message.user", Payload: delivery.InputEvent}); err != nil {
			return err
		}
	}
	if err := file.Sync(); err != nil {
		return err
	}
	// Recovery may have created the outbox file. Persist its directory entry
	// before allowing the ledger's recorded flag to skip future repair.
	directory, err := os.Open(filepath.Dir(w.eventPath(runID)))
	if err != nil {
		return err
	}
	err = directory.Sync()
	_ = directory.Close()
	if err != nil {
		return err
	}
	if w.active != nil && w.active.RunID == runID {
		w.active.Sequence = sequence
	}
	delivery.Recorded = true
	return saveSteerDelivery(path, *delivery)
}

// Sync both the record and directory before dispatch. A restart must never
// erase pending delivery and cause an already accepted message to be resent.
func saveSteerDelivery(path string, delivery steerDelivery) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	// Persist creation of the steering directory itself as well as its files.
	parent, err := os.Open(filepath.Dir(dir))
	if err != nil {
		return err
	}
	err = parent.Sync()
	_ = parent.Close()
	if err != nil {
		return err
	}
	data, err := json.Marshal(delivery)
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(dir, ".delivery-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if _, err = file.Write(data); err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err := os.Rename(file.Name(), path); err != nil {
		return err
	}
	directory, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}
