package api

import (
	"errors"
	"io"
	"net/http"
	"strconv"

	"github.com/Grano22/aiculator/internal/auth"
	"github.com/Grano22/aiculator/internal/core"
	"github.com/Grano22/aiculator/internal/meta"
	"github.com/Grano22/aiculator/internal/store"
	"github.com/Grano22/aiculator/internal/swarm"
)

func canWriteMeta(c auth.Claims) bool { return c.Admin || c.Planner }

// ImportReq is a batch of mapped tasks from another system.
type ImportReq struct {
	Items  []swarm.ImportItem `json:"items"`
	Update bool               `json:"update,omitempty"`
}

func (s *Server) metaRoutes(m *http.ServeMux) {
	m.HandleFunc("GET /v1/doctor", s.authed(isAdmin, func(w http.ResponseWriter, r *http.Request, _ auth.Claims) {
		h, err := s.S.Doctor(r.Context())
		reply(w, h, err)
	}))
	m.HandleFunc("POST /v1/doctor/fix", s.authed(isAdmin, func(w http.ResponseWriter, r *http.Request, _ auth.Claims) {
		removed, err := s.S.DoctorFix(r.Context())
		reply(w, map[string]any{"removed_worktrees": removed}, err)
	}))
	m.HandleFunc("GET /v1/backup", s.authed(isAdmin, func(w http.ResponseWriter, _ *http.Request, _ auth.Claims) {
		if s.Backup == nil {
			writeJSON(w, http.StatusOK, []any{})
			return
		}
		writeJSON(w, http.StatusOK, s.Backup.BackupStatus())
	}))
	m.HandleFunc("POST /v1/backup/snapshot", s.authed(isAdmin, func(w http.ResponseWriter, r *http.Request, _ auth.Claims) {
		if s.Backup == nil {
			writeJSON(w, http.StatusNotFound, Error{"no backups are configured"})
			return
		}
		req, ok := decode[struct {
			Rule string `json:"rule"`
		}](w, r)
		if !ok {
			return
		}
		files, err := s.Backup.SnapshotNow(r.Context(), req.Rule)
		reply(w, map[string]any{"files": files}, err)
	}))
	// A restore takes the whole copy from the caller, who read it from the backup files,
	// so the server never reads paths that a request names.
	m.HandleFunc("POST /v1/tasks/{id}/restore", s.authed(isAdmin, func(w http.ResponseWriter, r *http.Request, _ auth.Claims) {
		t, ok := decode[core.Task](w, r)
		if !ok {
			return
		}
		if t.ID != r.PathValue("id") {
			writeJSON(w, http.StatusBadRequest, Error{"the copy is of another task"})
			return
		}
		res, err := s.S.Engine.Restore(r.Context(), t)
		reply(w, res, err)
	}))
	m.HandleFunc("GET /v1/sync", s.authed(canView, func(w http.ResponseWriter, _ *http.Request, _ auth.Claims) {
		if s.Sync == nil {
			writeJSON(w, http.StatusOK, []any{})
			return
		}
		writeJSON(w, http.StatusOK, s.Sync.SyncStatus())
	}))
	m.HandleFunc("GET /v1/sync/{source}/hook-token", s.authed(isAdmin, func(w http.ResponseWriter, r *http.Request, _ auth.Claims) {
		if s.Sync == nil {
			writeJSON(w, http.StatusNotFound, Error{"nothing is being synced"})
			return
		}
		tok, err := s.Sync.HookToken(r.PathValue("source"))
		reply(w, map[string]string{"token": tok}, err)
	}))
	// No bearer token: the other system cannot have one. Each delivery is checked against
	// its signature instead, before anything in it is believed.
	m.HandleFunc("POST /v1/hooks/{source}", func(w http.ResponseWriter, r *http.Request) {
		if s.Sync == nil {
			writeJSON(w, http.StatusNotFound, Error{"nothing is being synced"})
			return
		}
		s.Sync.ServeHook(w, r, r.PathValue("source"))
	})
	m.HandleFunc("POST /v1/import", s.authed(isAdmin, func(w http.ResponseWriter, r *http.Request, _ auth.Claims) {
		q, ok := decode[ImportReq](w, r)
		if !ok {
			return
		}
		res, err := s.S.Import(r.Context(), q.Items, q.Update)
		reply(w, res, err)
	}))
	m.HandleFunc("GET /v1/tasks/{id}/meta", s.authed(anyone, func(w http.ResponseWriter, r *http.Request, _ auth.Claims) {
		v, err := s.S.GetMeta(r.Context(), r.PathValue("id"))
		reply(w, v, err)
	}))
	m.HandleFunc("POST /v1/tasks/{id}/meta", s.authed(canWriteMeta, func(w http.ResponseWriter, r *http.Request, _ auth.Claims) {
		q, ok := decode[meta.Patch](w, r)
		if !ok {
			return
		}
		v, err := s.S.PatchMeta(r.Context(), r.PathValue("id"), q)
		reply(w, v, err)
	}))
	m.HandleFunc("POST /v1/tasks/{id}/resources/{name...}", s.authed(anyone, func(w http.ResponseWriter, r *http.Request, _ auth.Claims) {
		ref, err := s.S.FetchResource(r.Context(), r.PathValue("id"), r.PathValue("name"))
		reply(w, ref, err)
	}))
	m.HandleFunc("POST /v1/blobs", s.authed(canWriteMeta, func(w http.ResponseWriter, r *http.Request, _ auth.Claims) {
		body := http.MaxBytesReader(w, r.Body, swarm.MaxBlob+1)
		ref, err := s.S.PutBlob(r.Context(), body, r.URL.Query().Get("name"), r.Header.Get("Content-Type"))
		var tooBig *http.MaxBytesError
		if errors.Is(err, store.ErrTooLarge) || errors.As(err, &tooBig) {
			writeJSON(w, http.StatusRequestEntityTooLarge, Error{"blob is over the size limit"})
			return
		}
		reply(w, ref, err)
	}))
	m.HandleFunc("GET /v1/blobs/{sha}", s.authed(anyone, func(w http.ResponseWriter, r *http.Request, _ auth.Claims) {
		body, size, err := s.S.OpenBlob(r.Context(), r.PathValue("sha"))
		if err != nil {
			writeJSON(w, status(err), Error{err.Error()})
			return
		}
		defer body.Close()
		// These bytes can come from anywhere, so never let a browser interpret them.
		h := w.Header()
		h.Set("Content-Type", "application/octet-stream")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Content-Disposition", "attachment")
		h.Set("Content-Length", strconv.FormatInt(size, 10))
		_, _ = io.Copy(w, body)
	}))
}
