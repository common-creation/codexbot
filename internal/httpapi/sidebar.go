package httpapi

import (
	"errors"
	"net/http"

	"github.com/common-creation/codexbot/internal/domain"
	"github.com/common-creation/codexbot/internal/store"
)

func (s *Server) getSidebar(w http.ResponseWriter, r *http.Request) {
	layout, err := s.store.Sidebar(r.Context())
	if err != nil {
		writeError(w, 500, "could not load sidebar")
		return
	}
	writeJSON(w, 200, layout)
}

func (s *Server) updateSidebar(w http.ResponseWriter, r *http.Request) {
	var in domain.SidebarLayout
	if err := decode(r, &in); err != nil {
		writeError(w, 400, "invalid JSON")
		return
	}
	layout, err := s.store.UpdateSidebar(r.Context(), in)
	switch {
	case errors.Is(err, store.ErrSidebarConflict):
		writeError(w, 409, err.Error())
	case errors.Is(err, store.ErrInvalidSidebar):
		writeError(w, 400, err.Error())
	case err != nil:
		writeError(w, 500, "could not save sidebar")
	default:
		writeJSON(w, 200, layout)
	}
}
