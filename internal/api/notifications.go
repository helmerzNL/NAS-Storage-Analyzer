package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/helmerznl/nas-storage-analyzer/internal/notify"
)

// The notification targets: where a message goes when a scan needs attention. Administrators only. The address of a target is a
// secret of the receiving service; it comes in with a request and never goes out again, in a response, the audit log or a log.
func (s *Server) notificationRoutes(h routeFn, v string) {
	if s.opts.Notify == nil {
		return
	}
	h("GET "+v+"/notifications", lvAdmin, s.notificationList)
	h("POST "+v+"/notifications", lvAdmin, s.notificationCreate)
	h("PATCH "+v+"/notifications/{id}", lvAdmin, s.notificationUpdate)
	h("DELETE "+v+"/notifications/{id}", lvAdmin, s.notificationDelete)
	h("POST "+v+"/notifications/{id}/test", lvAdmin, s.notificationTest)
}

// notifyFail maps the errors of the notification targets to HTTP statuses. It reports whether it did.
func (s *Server) notifyFail(w http.ResponseWriter, r *http.Request, err error) bool {
	var ve *notify.ValidationError
	switch {
	case errors.As(err, &ve):
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(problemBody{Type: "about:blank", Title: "invalid notification target", Status: 400,
			Detail: ve.Msg, Code: "invalid_request", Field: ve.Field, RequestID: requestID(r)})
	case errors.Is(err, notify.ErrNotFound):
		s.problemCode(w, r, http.StatusNotFound, "not found", "", "not_found")
	case errors.Is(err, notify.ErrConflict):
		s.problemCode(w, r, http.StatusConflict, "a notification target with this name already exists", "", "conflict")
	case errors.Is(err, notify.ErrTooMany):
		s.problemCode(w, r, http.StatusConflict, "limit reached", err.Error(), "limit_reached")
	default:
		return false
	}
	return true
}

func (s *Server) notificationList(w http.ResponseWriter, r *http.Request) error {
	items, err := s.opts.Notify.List(r.Context())
	if err != nil {
		return err
	}
	return writeJSON(w, map[string]any{"items": items, "max": notify.MaxTargets})
}

// auditTarget is what the audit log keeps of a target: how it is set up and which host it calls, never the address.
func auditTarget(t *notify.Target) map[string]any {
	return map[string]any{"id": t.ID, "host": t.URLHint, "format": t.Format, "enabled": t.Enabled, "events": t.Events}
}

func (s *Server) notificationCreate(w http.ResponseWriter, r *http.Request) error {
	in := notify.Defaults()
	if err := readJSON(r, &in); err != nil {
		return err
	}
	t, err := s.opts.Notify.Create(r.Context(), in)
	if err != nil {
		return err
	}
	s.audit(r, "notification.create", t.Name, auditTarget(t))
	return writeJSONStatus(w, http.StatusCreated, t)
}

// notificationUpdate changes what the request names and keeps the rest; an address that is not sent stays the stored one.
func (s *Server) notificationUpdate(w http.ResponseWriter, r *http.Request) error {
	id, err := parseID(r.PathValue("id"), "notification target id")
	if err != nil {
		return err
	}
	cur, err := s.opts.Notify.Get(r.Context(), id)
	if err != nil {
		return err
	}
	in := notify.Input{Name: cur.Name, Format: cur.Format, Lang: cur.Lang, Events: cur.Events, Enabled: cur.Enabled}
	if err := readJSON(r, &in); err != nil {
		return err
	}
	t, err := s.opts.Notify.Update(r.Context(), id, in)
	if err != nil {
		return err
	}
	detail := auditTarget(t)
	detail["address_changed"] = in.URL != nil
	s.audit(r, "notification.update", t.Name, detail)
	return writeJSON(w, t)
}

func (s *Server) notificationDelete(w http.ResponseWriter, r *http.Request) error {
	id, err := parseID(r.PathValue("id"), "notification target id")
	if err != nil {
		return err
	}
	name, err := s.opts.Notify.Delete(r.Context(), id)
	if err != nil {
		return err
	}
	s.audit(r, "notification.delete", name, nil)
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// notificationTest sends a test message now and answers with how it went: delivered or not, the status code and a reason. The
// receiver's own answer is never passed on.
func (s *Server) notificationTest(w http.ResponseWriter, r *http.Request) error {
	id, err := parseID(r.PathValue("id"), "notification target id")
	if err != nil {
		return err
	}
	d, err := s.opts.Notify.Test(r.Context(), id)
	if err != nil {
		return err
	}
	s.audit(r, "notification.test", d.Target, map[string]any{"ok": d.OK, "status": d.Status, "error": d.Error})
	return writeJSON(w, d)
}
