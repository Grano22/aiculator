package api

import (
	"net/http"
	"strconv"

	"github.com/Grano22/aiculator/internal/auth"
)

func intParam(r *http.Request, name string, def int) int {
	if v, err := strconv.Atoi(r.URL.Query().Get(name)); err == nil && v > 0 {
		return v
	}
	return def
}

func (s *Server) memoryRoutes(m *http.ServeMux) {
	off := func(w http.ResponseWriter) bool {
		if s.Memory == nil {
			writeJSON(w, http.StatusServiceUnavailable, Error{"shared memory is off on this server"})
			return true
		}
		return false
	}
	m.HandleFunc("GET /v1/memory/status", s.authed(anyone, func(w http.ResponseWriter, _ *http.Request, _ auth.Claims) {
		if off(w) {
			return
		}
		writeJSON(w, http.StatusOK, s.Memory.MemoryStatus())
	}))
	m.HandleFunc("GET /v1/code", s.authed(anyone, func(w http.ResponseWriter, r *http.Request, _ auth.Claims) {
		if off(w) {
			return
		}
		a, err := s.Memory.CodeQuery(r.URL.Query().Get("q"), intParam(r, "budget", 0))
		reply(w, a, err)
	}))
	m.HandleFunc("GET /v1/code/symbol", s.authed(anyone, func(w http.ResponseWriter, r *http.Request, _ auth.Claims) {
		if off(w) {
			return
		}
		a, err := s.Memory.CodeSymbol(r.URL.Query().Get("name"), intParam(r, "budget", 0))
		reply(w, a, err)
	}))
	// With a question or files, the notes that bear on them; without, the newest notes.
	m.HandleFunc("GET /v1/memory", s.authed(anyone, func(w http.ResponseWriter, r *http.Request, _ auth.Claims) {
		if off(w) {
			return
		}
		q, files := r.URL.Query().Get("q"), r.URL.Query()["file"]
		if q == "" && len(files) == 0 {
			writeJSON(w, http.StatusOK, s.Memory.Notes(intParam(r, "limit", 50)))
			return
		}
		writeJSON(w, http.StatusOK, s.Memory.Recall(q, files, intParam(r, "k", 5)))
	}))
	// Agents, planners and admins write notes; a viewer only reads.
	m.HandleFunc("POST /v1/memory", s.authed(func(c auth.Claims) bool { return isAgent(c) || isPlanner(c) }, func(w http.ResponseWriter, r *http.Request, c auth.Claims) {
		if off(w) {
			return
		}
		q, ok := decode[NoteReq](w, r)
		if !ok {
			return
		}
		n, err := s.Memory.Note(c.Agent, q.TaskID, q)
		reply(w, n, err)
	}))
	m.HandleFunc("DELETE /v1/memory/{id}", s.authed(isAdmin, func(w http.ResponseWriter, r *http.Request, _ auth.Claims) {
		if off(w) {
			return
		}
		err := s.Memory.Forget(r.PathValue("id"))
		reply(w, map[string]string{"forgotten": r.PathValue("id")}, err)
	}))
	m.HandleFunc("POST /v1/memory/refresh", s.authed(isAdmin, func(w http.ResponseWriter, r *http.Request, _ auth.Claims) {
		if off(w) {
			return
		}
		st, err := s.Memory.Refresh(r.Context())
		reply(w, st, err)
	}))
}
