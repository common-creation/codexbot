package httpapi

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/common-creation/codexbot/internal/domain"
	"github.com/common-creation/codexbot/internal/store"
)

// Cursor positions belong to the agent timeline, not any one app-server turn.
// The initial/older pages return the most recent entries in ascending order;
// incremental pages return the first entries after the supplied cursor.
func (s *Server) agentTimeline(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("agentID")
	a, err := s.store.Agent(r.Context(), id)
	if err != nil {
		writeError(w, 404, "agent not found")
		return
	}
	if previousID := r.URL.Query().Get("conversationId"); previousID != "" {
		previous, err := s.store.Conversation(r.Context(), previousID)
		if err != nil || previous.AgentID != id {
			writeError(w, 404, "conversation not found")
			return
		}
	}
	parseCursor := func(name string) (int64, error) {
		v := r.URL.Query().Get(name)
		if v == "" {
			return 0, nil
		}
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < 0 {
			return 0, errors.New(name + " must be a nonnegative integer")
		}
		return n, nil
	}
	after, err := parseCursor("after")
	if err != nil {
		writeError(w, 400, err.Error())
		return
	}
	before, err := parseCursor("before")
	if err != nil || (after > 0 && before > 0) {
		writeError(w, 400, "provide a valid after or before cursor, not both")
		return
	}
	limit := 100
	if raw := r.URL.Query().Get("limit"); raw != "" {
		limit, err = strconv.Atoi(raw)
		if err != nil || limit < 1 || limit > 1000 {
			writeError(w, 400, "limit must be 1-1000")
			return
		}
	}
	c, err := s.store.EnsureAgentConversation(r.Context(), a)
	if err != nil {
		writeError(w, 500, "could not load agent conversation")
		return
	}
	if err = s.store.ReconcileCollaborationTasks(r.Context()); err != nil {
		writeError(w, 500, "could not refresh collaboration status")
		return
	}
	if err = s.store.SyncCollaborationTimeline(r.Context()); err != nil {
		writeError(w, 500, "could not refresh collaboration timeline")
		return
	}
	var events []domain.Event
	incremental := r.URL.Query().Has("after")
	if incremental && r.URL.Query().Has("before") {
		writeError(w, 400, "provide after or before, not both")
		return
	}
	if incremental {
		events, err = s.store.AgentTimelineAfter(r.Context(), id, after, limit+1)
	} else {
		events, err = s.store.AgentTimeline(r.Context(), id, after, before, limit+1)
	}
	if err != nil {
		writeError(w, 500, "could not read agent timeline")
		return
	}
	hasMore := len(events) > limit
	if hasMore {
		if incremental {
			events = events[:limit]
		} else {
			events = events[len(events)-limit:]
		}
	}
	if events == nil {
		events = []domain.Event{}
	}
	lastSequence, beforeSequence := after, int64(0)
	if len(events) > 0 {
		beforeSequence = events[0].Sequence
		lastSequence = events[len(events)-1].Sequence
	}
	activeID := ""
	runtimeBusy := false
	if active, err := s.store.ActiveRun(r.Context(), id); err == nil {
		runtimeBusy = true
		if !strings.HasPrefix(active.Source, "schedule:") {
			activeID = active.ID
		}
	} else if !errors.Is(err, store.ErrNotFound) {
		writeError(w, 500, "could not inspect active run")
		return
	}
	writeJSON(w, 200, map[string]any{"events": events, "conversationId": c.ID, "activeRunId": activeID, "runtimeBusy": runtimeBusy, "lastSequence": lastSequence, "beforeSequence": beforeSequence, "hasMore": hasMore})
}
