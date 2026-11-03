package api

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/helmerznl/nas-storage-analyzer/internal/auth"
	"github.com/helmerznl/nas-storage-analyzer/internal/backup"
)

// In-app backup and restore (App-Guidance backup-restore.md). Everything here is administrator-only: the file holds the configuration of the
// installation, and a restore replaces it.
func (s *Server) backupRoutes(h routeFn, v string) {
	if s.opts.Backup == nil {
		return
	}
	h("GET "+v+"/backup", lvAdmin, s.backupDownload)
	h("POST "+v+"/backup/stage", lvAdmin, s.backupStage)
	h("DELETE "+v+"/backup/stage/{token}", lvAdmin, s.backupUnstage)
	h("POST "+v+"/backup/jobs", lvAdmin, s.backupStart)
	h("GET "+v+"/backup/jobs/latest", lvAdmin, s.backupLatest)
	h("DELETE "+v+"/backup/jobs/{id}", lvAdmin, s.backupDismiss)
}

// backupFail maps the errors of the backup service. It reports whether it did.
func (s *Server) backupFail(w http.ResponseWriter, r *http.Request, err error) bool {
	var inv *backup.InvalidError
	switch {
	case errors.As(err, &inv):
		status := http.StatusBadRequest
		if inv.Key == "too_big" {
			status = http.StatusRequestEntityTooLarge
		}
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(status)
		writeProblem(w, problemBody{Type: "about:blank", Title: "the backup file is not accepted", Status: status, Detail: inv.Detail,
			Code: "backup_" + inv.Key, Field: inv.Field, Params: inv.Params, RequestID: requestID(r)})
	case errors.Is(err, backup.ErrNoStage):
		s.problemCode(w, r, http.StatusNotFound, "the staged backup is gone", err.Error(), "no_stage")
	case errors.Is(err, backup.ErrNoJob):
		s.problemCode(w, r, http.StatusNotFound, "not found", err.Error(), "not_found")
	case errors.Is(err, backup.ErrBusy):
		s.problemCode(w, r, http.StatusConflict, "a scan is queued or running", err.Error(), "busy")
	case errors.Is(err, backup.ErrRunning):
		s.problemCode(w, r, http.StatusConflict, "a restore is running", err.Error(), "already_running")
	default:
		return false
	}
	return true
}

func (s *Server) backupDownload(w http.ResponseWriter, r *http.Request) error {
	data, err := s.opts.Backup.Export(r.Context())
	if err != nil {
		return err
	}
	h := w.Header()
	h.Set("Content-Type", "application/json")
	h.Set("Content-Disposition", `attachment; filename="`+s.opts.Backup.FileName()+`"`)
	h.Set("Content-Length", strconv.Itoa(len(data)))
	h.Set("Cache-Control", "no-store") // the file is the owner's: nothing in between keeps a copy
	if _, err := w.Write(data); err != nil {
		return err
	}
	s.audit(r, "backup.create", "", map[string]any{"bytes": len(data)})
	return nil
}

func (s *Server) backupStage(w http.ResponseWriter, r *http.Request) error {
	st, err := s.opts.Backup.Stage(r.Context(), sessionOf(r).User.Username, r.Body)
	if err != nil {
		return err
	}
	return writeJSON(w, st)
}

func (s *Server) backupUnstage(w http.ResponseWriter, r *http.Request) error {
	if err := s.opts.Backup.Unstage(r.Context(), sessionOf(r).User.Username, r.PathValue("token")); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func (s *Server) backupStart(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Token string `json:"token"`
	}
	if err := readJSON(r, &in); err != nil {
		return err
	}
	u := sessionOf(r).User
	j, err := s.opts.Backup.Start(r.Context(), auth.Actor{UserID: u.ID, Username: u.Username, IP: s.clientIP(r)}, in.Token)
	if err != nil {
		return err
	}
	s.audit(r, "backup.restore.start", "", map[string]any{"job": j.ID})
	return writeJSONStatus(w, http.StatusAccepted, j)
}

func (s *Server) backupLatest(w http.ResponseWriter, r *http.Request) error {
	j, err := s.opts.Backup.Latest(r.Context())
	if err != nil {
		return err
	}
	return writeJSON(w, map[string]any{"job": j})
}

func (s *Server) backupDismiss(w http.ResponseWriter, r *http.Request) error {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id < 1 {
		return bad("the id of a restore is a positive number")
	}
	if err := s.opts.Backup.Dismiss(r.Context(), id); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}
