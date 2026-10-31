package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/helmerznl/nas-storage-analyzer/internal/jobs"
	"github.com/helmerznl/nas-storage-analyzer/internal/query"
	"github.com/helmerznl/nas-storage-analyzer/internal/shares"
)

func (s *Server) adminRoutes(h routeFn, v string) {
	if s.opts.Shares != nil && s.opts.Queue != nil {
		h("POST "+v+"/shares", lvAdmin, s.createShare)
		h("PATCH "+v+"/shares/{share}", lvAdmin, s.patchShare)
		h("DELETE "+v+"/shares/{share}", lvAdmin, s.deleteShare)
		h("POST "+v+"/shares/{share}/scan", lvAdmin, s.startScan)
		h("DELETE "+v+"/shares/{share}/scan", lvAdmin, s.cancelScan)
		h("POST "+v+"/shares/{share}/recategorize", lvAdmin, s.recategorize)
		h("GET "+v+"/shares/{share}/jobs", lvViewer, s.listJobs)
		h("PUT "+v+"/extension-map/{ext}", lvAdmin, s.putExtension)
		h("DELETE "+v+"/extension-map/{ext}", lvAdmin, s.deleteExtension)
		h("GET "+v+"/settings", lvViewer, s.getSettings)
	}
	h("GET "+v+"/shares/{share}/history", lvViewer, s.history)
	h("GET "+v+"/shares/{share}/dirs/{dir}/history", lvViewer, s.dirHistory)
	h("PUT "+v+"/auth/prefs", lvViewer, s.putPrefs)
}

// adminFail maps share-management errors. It reports whether it did.
func (s *Server) adminFail(w http.ResponseWriter, r *http.Request, err error) bool {
	var ve *shares.ValidationError
	switch {
	case errors.As(err, &ve):
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(problemBody{Type: "about:blank", Title: "invalid share settings", Status: 400,
			Detail: ve.Msg, Code: "invalid_request", Field: ve.Field, RequestID: requestID(r)})
	case errors.Is(err, shares.ErrNotFound):
		s.problemCode(w, r, http.StatusNotFound, "not found", "", "not_found")
	case errors.Is(err, shares.ErrConflict):
		s.problemCode(w, r, http.StatusConflict, "a share with this name already exists", "", "conflict")
	case errors.Is(err, shares.ErrBusy):
		s.problemCode(w, r, http.StatusConflict, "a scan is queued or running", err.Error(), "busy")
	case errors.Is(err, jobs.ErrAlreadyActive):
		s.problemCode(w, r, http.StatusConflict, "already queued or running", err.Error(), "already_active")
	default:
		return false
	}
	return true
}

func (s *Server) createShare(w http.ResponseWriter, r *http.Request) error {
	in := shares.Defaults()
	if err := readJSON(r, &in); err != nil {
		return err
	}
	id, err := s.opts.Shares.Create(r.Context(), in)
	if err != nil {
		return err
	}
	s.audit(r, "share.create", in.Name, map[string]any{"kind": in.Kind, "id": id})
	info, err := s.q.ShareByID(r.Context(), id)
	if err != nil {
		return err
	}
	return writeJSONStatus(w, http.StatusCreated, info)
}

func (s *Server) patchShare(w http.ResponseWriter, r *http.Request) error {
	id, err := shareID(r)
	if err != nil {
		return err
	}
	var raw json.RawMessage
	if err := readJSON(r, &raw); err != nil {
		return err
	}
	if err := s.opts.Shares.Update(r.Context(), id, raw); err != nil {
		return err
	}
	info, err := s.q.ShareByID(r.Context(), id)
	if err != nil {
		return err
	}
	// Never log the password; list only which fields changed.
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(raw, &fields)
	changed := make([]string, 0, len(fields))
	for k := range fields {
		changed = append(changed, k)
	}
	s.audit(r, "share.update", info.Name, map[string]any{"fields": changed})
	return writeJSON(w, info)
}

func (s *Server) deleteShare(w http.ResponseWriter, r *http.Request) error {
	id, err := shareID(r)
	if err != nil {
		return err
	}
	info, err := s.q.ShareByID(r.Context(), id)
	if err != nil {
		return err
	}
	if err := s.opts.Shares.Delete(r.Context(), id); err != nil {
		return err
	}
	s.audit(r, "share.delete", info.Name, nil)
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func (s *Server) startScan(w http.ResponseWriter, r *http.Request) error {
	id, err := shareID(r)
	if err != nil {
		return err
	}
	var in struct{ Mode string }
	if err := readJSON(r, &in); err != nil {
		return err
	}
	if in.Mode == "" {
		in.Mode = "auto"
	}
	if in.Mode != "auto" && in.Mode != "full" {
		return bad("mode must be auto or full")
	}
	info, err := s.q.ShareByID(r.Context(), id)
	if err != nil {
		return err
	}
	by := sessionOf(r).User.Username
	job, err := s.opts.Queue.Enqueue(r.Context(), jobs.KindScan, id, in.Mode, "manual", by)
	if err != nil {
		return err
	}
	s.audit(r, "scan.start", info.Name, map[string]any{"mode": in.Mode, "job": job.ID})
	return writeJSONStatus(w, http.StatusAccepted, job)
}

func (s *Server) cancelScan(w http.ResponseWriter, r *http.Request) error {
	id, err := shareID(r)
	if err != nil {
		return err
	}
	info, err := s.q.ShareByID(r.Context(), id)
	if err != nil {
		return err
	}
	ok, err := s.opts.Queue.Cancel(r.Context(), id, jobs.KindScan)
	if err != nil {
		return err
	}
	if !ok {
		return query.ErrNotFound
	}
	s.audit(r, "scan.cancel", info.Name, nil)
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func (s *Server) recategorize(w http.ResponseWriter, r *http.Request) error {
	id, err := shareID(r)
	if err != nil {
		return err
	}
	info, err := s.q.ShareByID(r.Context(), id)
	if err != nil {
		return err
	}
	job, err := s.opts.Queue.Enqueue(r.Context(), jobs.KindRecategorize, id, "auto", "manual", sessionOf(r).User.Username)
	if err != nil {
		return err
	}
	s.audit(r, "share.recategorize", info.Name, nil)
	return writeJSONStatus(w, http.StatusAccepted, job)
}

func (s *Server) listJobs(w http.ResponseWriter, r *http.Request) error {
	id, err := shareID(r)
	if err != nil {
		return err
	}
	if _, err := s.q.ShareByID(r.Context(), id); err != nil {
		return err
	}
	list, err := s.opts.Queue.Recent(r.Context(), id, 20)
	if err != nil {
		return err
	}
	return writeJSON(w, items[jobs.Job]{Items: list})
}

var extRE = regexp.MustCompile(`^[a-z0-9]{1,16}$`)

// scheduleRecategorize queues a re-categorisation of every share that has data.
func (s *Server) scheduleRecategorize(ctx context.Context, by string) (int, error) {
	list, err := s.q.Shares(ctx)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, sh := range list {
		if sh.RootDirID == nil {
			continue
		}
		if _, err := s.opts.Queue.Enqueue(ctx, jobs.KindRecategorize, sh.ID, "auto", "manual", by); err == nil {
			n++
		} else if !errors.Is(err, jobs.ErrAlreadyActive) {
			return n, err
		}
	}
	return n, nil
}

func (s *Server) putExtension(w http.ResponseWriter, r *http.Request) error {
	ext := strings.ToLower(strings.TrimPrefix(r.PathValue("ext"), "."))
	if !extRE.MatchString(ext) {
		return bad("an extension is 1-16 letters or digits, without a dot")
	}
	var in struct {
		CategoryID int16 `json:"category_id"`
	}
	if err := readJSON(r, &in); err != nil {
		return err
	}
	tag, err := s.opts.Shares.SetExtension(r.Context(), ext, in.CategoryID)
	if err != nil {
		return err
	}
	by := sessionOf(r).User.Username
	n, err := s.scheduleRecategorize(r.Context(), by)
	if err != nil {
		return err
	}
	s.audit(r, "extension.set", ext, map[string]any{"category_id": in.CategoryID, "new": tag})
	return writeJSON(w, map[string]any{"ext": ext, "category_id": in.CategoryID, "recategorize_jobs": n})
}

func (s *Server) deleteExtension(w http.ResponseWriter, r *http.Request) error {
	ext := strings.ToLower(strings.TrimPrefix(r.PathValue("ext"), "."))
	if !extRE.MatchString(ext) {
		return bad("an extension is 1-16 letters or digits, without a dot")
	}
	if err := s.opts.Shares.DeleteExtension(r.Context(), ext); err != nil {
		return err
	}
	n, err := s.scheduleRecategorize(r.Context(), sessionOf(r).User.Username)
	if err != nil {
		return err
	}
	s.audit(r, "extension.delete", ext, nil)
	return writeJSON(w, map[string]any{"ext": ext, "recategorize_jobs": n})
}

func (s *Server) getSettings(w http.ResponseWriter, r *http.Request) error {
	return writeJSON(w, map[string]any{
		"shares_root":     s.opts.SharesRoot,
		"version":         "1",
		"server_time":     time.Now().UTC(),
		"mount_supported": true,
	})
}

// ---- history -------------------------------------------------------------------------

func (s *Server) history(w http.ResponseWriter, r *http.Request) error {
	id, err := shareID(r)
	if err != nil {
		return err
	}
	var since *time.Time
	if v := r.URL.Query().Get("since"); v != "" {
		if since, err = parseTime(v, "since"); err != nil {
			return err
		}
	}
	pts, err := s.q.History(r.Context(), id, since)
	if err != nil {
		return err
	}
	return writeJSON(w, items[query.HistoryPoint]{Items: pts})
}

func (s *Server) dirHistory(w http.ResponseWriter, r *http.Request) error {
	id, err := shareID(r)
	if err != nil {
		return err
	}
	dir, err := parseDirID(r.PathValue("dir"))
	if err != nil {
		return err
	}
	pts, err := s.q.DirHistory(r.Context(), id, dir)
	if err != nil {
		return err
	}
	return writeJSON(w, items[query.DirHistoryPoint]{Items: pts})
}

// ---- preferences ----------------------------------------------------------------------

func (s *Server) putPrefs(w http.ResponseWriter, r *http.Request) error {
	var in map[string]string
	if err := readJSON(r, &in); err != nil {
		return err
	}
	out, err := s.auth.SetPrefs(r.Context(), sessionOf(r).User.ID, in)
	if err != nil {
		if strings.HasPrefix(err.Error(), "invalid preference") {
			return bad("%s", err.Error())
		}
		return err
	}
	return writeJSON(w, map[string]any{"prefs": out})
}

// ---- live status (server-sent events) ----------------------------------------------------------

// events streams the live state of all shares once a second, but only when it
// changed, so an idle page costs one tiny query per second per open tab and no
// traffic. The first message carries the full state.
func (s *Server) events(w http.ResponseWriter, r *http.Request) error {
	rc := http.NewResponseController(w)
	_ = rc.SetWriteDeadline(time.Time{})
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Accel-Buffering", "no") // let nginx pass events through unbuffered
	w.WriteHeader(http.StatusOK)

	var last string
	lastWrite := time.Now()
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		st, err := s.q.Statuses(r.Context())
		if err != nil {
			return nil // the connection ends; the browser reconnects
		}
		b, _ := json.Marshal(st)
		switch {
		case string(b) != last:
			last, lastWrite = string(b), time.Now()
			if _, err := w.Write([]byte("event: status\ndata: " + string(b) + "\n\n")); err != nil {
				return nil
			}
			_ = rc.Flush()
		case time.Since(lastWrite) > 15*time.Second:
			lastWrite = time.Now()
			if _, err := w.Write([]byte(": ping\n\n")); err != nil {
				return nil
			}
			_ = rc.Flush()
		}
		select {
		case <-r.Context().Done():
			return nil
		case <-tick.C:
		}
	}
}
