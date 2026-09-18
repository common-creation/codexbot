package httpapi

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/common-creation/codexbot/internal/domain"
)

func (s *Server) getSchedule(w http.ResponseWriter, r *http.Request) {
	schedule, err := s.store.Schedule(r.Context(), r.PathValue("scheduleID"))
	if err != nil {
		writeError(w, 404, "schedule not found")
		return
	}
	writeJSON(w, 200, schedule)
}

func (s *Server) scheduleRuns(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("scheduleID")
	if _, err := s.store.Schedule(r.Context(), id); err != nil {
		writeError(w, 404, "schedule not found")
		return
	}
	limit := 20
	if value := r.URL.Query().Get("limit"); value != "" {
		var err error
		limit, err = strconv.Atoi(value)
		if err != nil || limit < 1 || limit > 100 {
			writeError(w, 400, "limit must be 1-100")
			return
		}
	}
	before := r.URL.Query().Get("before")
	if before != "" {
		run, err := s.store.Run(r.Context(), before)
		if err != nil || run.Source != "schedule:"+id {
			writeError(w, 400, "invalid schedule history cursor")
			return
		}
	}
	runs, err := s.store.ScheduleRuns(r.Context(), id, before, limit+1)
	if err != nil {
		writeError(w, 500, "could not read schedule history")
		return
	}
	if runs == nil {
		runs = []domain.Run{}
	}
	next := ""
	if len(runs) > limit {
		runs = runs[:limit]
		next = runs[len(runs)-1].ID
	}
	writeJSON(w, 200, map[string]any{"runs": runs, "nextCursor": next})
}

func (s *Server) scheduleRunEvents(w http.ResponseWriter, r *http.Request) {
	schedule, err := s.store.Schedule(r.Context(), r.PathValue("scheduleID"))
	if err != nil {
		writeError(w, 404, "schedule not found")
		return
	}
	run, err := s.store.Run(r.Context(), r.PathValue("runID"))
	if err != nil || run.Source != "schedule:"+schedule.ID || run.AgentID != schedule.AgentID {
		writeError(w, 404, "schedule run not found")
		return
	}
	after := int64(0)
	if value := r.URL.Query().Get("after"); value != "" {
		after, err = strconv.ParseInt(value, 10, 64)
		if err != nil || after < 0 {
			writeError(w, 400, "after must be a nonnegative integer")
			return
		}
	}
	limit := 100
	if value := r.URL.Query().Get("limit"); value != "" {
		limit, err = strconv.Atoi(value)
		if err != nil || limit < 1 || limit > 100 {
			writeError(w, 400, "limit must be 1-100")
			return
		}
	}
	events, err := s.store.EventsAfterLimit(r.Context(), run.ID, after, limit+1)
	if err != nil {
		writeError(w, 500, "could not read schedule output")
		return
	}
	if events == nil {
		events = []domain.Event{}
	}
	more := len(events) > limit
	if more {
		events = events[:limit]
	}
	bytes := 0
	for i, event := range events {
		encoded, _ := json.Marshal(event)
		if i > 0 && bytes+len(encoded) > collaborationPageBytes {
			events = events[:i]
			more = true
			break
		}
		bytes += len(encoded)
	}
	if len(events) > 0 {
		after = events[len(events)-1].Sequence
	}
	writeJSON(w, 200, map[string]any{"run": run, "events": events, "nextSequence": after, "hasMore": more})
}
