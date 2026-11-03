package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/helmerznl/nas-storage-analyzer/internal/auth"
	"github.com/helmerznl/nas-storage-analyzer/internal/reset"
)

// Factory reset (App-Guidance factory-reset.md): the one request that destroys everything on purpose, so the design is about making sure the
// administrator asked and knows what it costs. A session is not enough: the request carries the typed word AND a fresh proof of who is asking.
func (s *Server) resetRoutes(h routeFn, v string) {
	if s.opts.Reset == nil {
		return
	}
	h("GET "+v+"/system/reset", lvAdmin, s.resetInfo)
	h("POST "+v+"/system/reset/passkey", lvAdmin, s.resetPasskeyBegin)
	h("POST "+v+"/system/reset", lvAdmin, s.resetRun)
}

func (s *Server) setupToken() string {
	s.tokMu.RLock()
	defer s.tokMu.RUnlock()
	return s.setupTok
}

// afterReset gives the new installation a setup token and says where to find it. A token that was configured stays; one that was made up at
// start is replaced, because the log it was printed in may have been read by the previous owner.
func (s *Server) afterReset() (configured bool) {
	if s.opts.SetupTokenFixed {
		return true
	}
	tok := auth.NewSetupToken()
	s.tokMu.Lock()
	s.setupTok = tok
	s.tokMu.Unlock()
	s.log.Warn("no account exists yet: open the web interface and create the first administrator", "setup_token", tok)
	return false
}

// resetInfo tells the form which fields to show: the word to type (defined in one place, here), and which proofs this person can give.
func (s *Server) resetInfo(w http.ResponseWriter, r *http.Request) error {
	busy, err := s.opts.Reset.Busy(r.Context())
	if err != nil {
		return err
	}
	return writeJSON(w, map[string]any{"phrase": reset.Phrase, "methods": auth.ReauthMethodsOf(sessionOf(r).User), "scans_active": busy})
}

func (s *Server) resetPasskeyBegin(w http.ResponseWriter, r *http.Request) error {
	opts, id, err := s.auth.BeginPasskeyReauth(r.Context(), s.rp(r), sessionOf(r).User)
	if err != nil {
		return bad("%s", err.Error())
	}
	return writeJSON(w, map[string]any{"options": opts, "ceremony": id})
}

func (s *Server) resetRun(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Phrase       string          `json:"phrase"`
		Password     string          `json:"password"`
		Code         string          `json:"code"`
		RecoveryCode string          `json:"recovery_code"`
		Ceremony     string          `json:"ceremony"`
		Passkey      json.RawMessage `json:"passkey"`
	}
	if err := readJSON(r, &in); err != nil {
		return err
	}
	sess := sessionOf(r)
	ctx, ip := r.Context(), s.clientIP(r)

	// 1. The typed word. A wrong or missing one wipes nothing, and is not a failed attempt to prove who is asking.
	if in.Phrase != reset.Phrase {
		s.problemCode(w, r, http.StatusBadRequest, "type the word to confirm", "The word typed does not match.", "reset_phrase")
		return nil
	}
	// 2. Scans first: asking for a proof and then refusing would spend a code for nothing.
	if busy, err := s.opts.Reset.Busy(ctx); err != nil {
		return err
	} else if busy {
		return reset.ErrBusy
	}
	// 3. A fresh proof of who is asking: a passkey alone, or the password with a code. Wrong ones count toward the lockout of signing in.
	var err error
	if len(bytes.TrimSpace(in.Passkey)) > 0 {
		pr, e := http.NewRequestWithContext(ctx, http.MethodPost, "/", bytes.NewReader(in.Passkey))
		if e != nil {
			return e
		}
		err = s.auth.FinishPasskeyReauth(ctx, s.rp(r), sess.User, in.Ceremony, pr, ip)
	} else {
		err = s.auth.Reauthenticate(ctx, sess.User, in.Password, in.Code, in.RecoveryCode, ip)
	}
	if err != nil {
		var locked *auth.LockedError
		switch {
		case errors.As(err, &locked):
			s.audit(r, "reset.locked", "", nil)
		case errors.Is(err, auth.ErrInvalidCredentials), errors.Is(err, auth.ErrInvalidCode):
			s.audit(r, "reset.denied", "", nil)
		}
		return err
	}
	// 4. The wipe, in one transaction. The audit log goes with the database, so the process log is the only record.
	res, err := s.opts.Reset.Wipe(ctx)
	if err != nil {
		return err
	}
	s.opts.Reset.LogResult(sess.User.Username, ip, res)
	configured := s.afterReset()
	s.clearSessionCookie(w, r)
	// restarting is always false: everything this installation keeps is in the database, and the running process picks the empty one up by
	// itself. There is nothing to restart, and so nothing that needs a restart policy.
	return writeJSON(w, map[string]any{"reset": true, "restarting": false, "setup_token_configured": configured})
}

// resetFail maps the errors of the reset. It reports whether it did.
func (s *Server) resetFail(w http.ResponseWriter, r *http.Request, err error) bool {
	switch {
	case errors.Is(err, reset.ErrBusy):
		s.problemCode(w, r, http.StatusConflict, "a scan is queued or running", err.Error(), "busy")
	case errors.Is(err, auth.ErrReauthRequired):
		s.problemCode(w, r, http.StatusUnauthorized, "confirm who you are", err.Error(), "reauth_required")
	default:
		return false
	}
	return true
}
