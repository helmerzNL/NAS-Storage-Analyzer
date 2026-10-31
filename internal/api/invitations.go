package api

// Invitation links (App-Guidance user-management.md). An administrator makes a link; the guest opens it and chooses their own password or
// passkey, so nobody else ever learns a credential. The same mechanism, a reset, brings back a user who can no longer sign in.
//
// The token is the secret: it travels in the URL fragment (a browser never sends that to a server), in a header or the request body, never in
// a path or query string, and it is never logged or put in the audit log. Everything the guest can reach here is public, and each handler
// checks the token itself.

import (
	"errors"
	"net/http"
	"strings"

	"github.com/helmerznl/nas-storage-analyzer/internal/auth"
)

func (s *Server) invitationRoutes(h routeFn, v string) {
	h("GET "+v+"/invitations", lvAdmin, s.listInvitations)
	h("POST "+v+"/invitations", lvAdmin, s.createInvitation)
	h("DELETE "+v+"/invitations/{id}", lvAdmin, s.revokeInvitation)
	h("POST "+v+"/users/{id}/reset", lvAdmin, s.resetUser)
	// The guest has no session: the token is the credential.
	h("GET "+v+"/invitations/info", lvPublic, s.invitationInfo)
	h("POST "+v+"/invitations/password", lvPublic, s.invitationPassword)
	h("POST "+v+"/invitations/passkey/begin", lvPublic, s.invitationPasskeyBegin)
	h("POST "+v+"/invitations/passkey/finish", lvPublic, s.invitationPasskeyFinish)
}

// inviteURL is the link the administrator hands over: the app's public address, then the first passkey origin, then nothing (the interface
// then builds it from the address the administrator is using). The request's own host is never used: behind a proxy it can be an internal address.
func (s *Server) inviteURL(token string) string {
	base := strings.TrimRight(s.opts.PublicURL, "/")
	if base == "" && len(s.opts.RP.Origins) > 0 {
		base = strings.TrimRight(s.opts.RP.Origins[0], "/")
	}
	if base == "" {
		return ""
	}
	return base + "/#invite=" + token
}

// inviteCreated is shown once: the token and the link are in no later answer.
type inviteCreated struct {
	auth.Invitation
	Token string `json:"token"`
	URL   string `json:"url"`
}

func (s *Server) listInvitations(w http.ResponseWriter, r *http.Request) error {
	list, err := s.auth.ListInvitations(r.Context())
	if err != nil {
		return err
	}
	return writeJSON(w, items[auth.Invitation]{Items: list})
}

func (s *Server) createInvitation(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Username string `json:"username"`
		Role     string `json:"role"`
	}
	if err := readJSON(r, &in); err != nil {
		return err
	}
	if in.Role == "" {
		in.Role = "viewer" // the safe default; an administrator is a deliberate choice
	}
	inv, token, err := s.auth.CreateInvitation(r.Context(), sessionOf(r).User, in.Username, in.Role)
	if err != nil {
		return s.inviteError(err)
	}
	s.audit(r, "invitation.create", inv.Username, map[string]any{"kind": inv.Kind, "role": inv.Role, "id": inv.ID})
	return writeJSONStatus(w, http.StatusCreated, inviteCreated{Invitation: *inv, Token: token, URL: s.inviteURL(token)})
}

func (s *Server) resetUser(w http.ResponseWriter, r *http.Request) error {
	id, err := userID(r)
	if err != nil {
		return err
	}
	inv, token, err := s.auth.CreateResetInvitation(r.Context(), sessionOf(r).User, id)
	if err != nil {
		return s.inviteError(err)
	}
	s.audit(r, "invitation.create", inv.Username, map[string]any{"kind": inv.Kind, "role": inv.Role, "id": inv.ID})
	return writeJSONStatus(w, http.StatusCreated, inviteCreated{Invitation: *inv, Token: token, URL: s.inviteURL(token)})
}

func (s *Server) revokeInvitation(w http.ResponseWriter, r *http.Request) error {
	id, err := parseID(r.PathValue("id"), "invitation id")
	if err != nil {
		return err
	}
	inv, err := s.auth.RevokeInvitation(r.Context(), id)
	if err != nil {
		return err
	}
	s.audit(r, "invitation.revoke", inv.Username, map[string]any{"kind": inv.Kind, "id": inv.ID})
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// inviteError turns the errors of making an invitation into client errors; anything else is ours.
func (s *Server) inviteError(err error) error {
	switch {
	case errors.Is(err, auth.ErrBadRole), errors.Is(err, auth.ErrSelfReset), errors.Is(err, auth.ErrUserDisabled):
		return bad("%s", err.Error())
	}
	return err // ErrBadUsername, ErrConflict, ErrNotFound, ErrTooMany are mapped by authFail
}

// ---- the guest's side (public) ---------------------------------------------------------------------------------------------

// guestGuard is what every public invitation route starts with: sign-in over https when required (a password must not cross plain http), and the
// same lockout of an address that keeps presenting bad tokens as everywhere else.
func (s *Server) guestGuard(w http.ResponseWriter, r *http.Request) (done bool, err error) {
	if s.opts.RequireHTTPS && s.scheme(r) != "https" {
		s.problemCode(w, r, http.StatusBadRequest, "https required", "Open this link over https (through the reverse proxy).", "https_required")
		return true, nil
	}
	return false, s.auth.CheckIPLocked(r.Context(), s.clientIP(r))
}

// badToken answers every way a token can be no good with the same answer, and counts it against the address.
func (s *Server) badToken(r *http.Request, err error) error {
	if errors.Is(err, auth.ErrInvalidInvitation) {
		s.auth.RecordIPFailure(r.Context(), s.clientIP(r))
	}
	return err
}

func (s *Server) invitationInfo(w http.ResponseWriter, r *http.Request) error {
	if done, err := s.guestGuard(w, r); done || err != nil {
		return err
	}
	info, err := s.auth.InvitationInfo(r.Context(), r.Header.Get("X-Invite-Token"))
	if err != nil {
		return s.badToken(r, err)
	}
	return writeJSON(w, info)
}

func (s *Server) invitationPassword(w http.ResponseWriter, r *http.Request) error {
	if done, err := s.guestGuard(w, r); done || err != nil {
		return err
	}
	var in struct{ Token, Username, Password string }
	if err := readJSON(r, &in); err != nil {
		return err
	}
	u, err := s.auth.AcceptInvitationPassword(r.Context(), in.Token, in.Username, in.Password)
	if err != nil {
		return s.badToken(r, err)
	}
	csrf, err := s.startSession(w, r, u, auth.StageEnroll) // a password alone is not enough: the guest enrols a second factor, like every account
	if err != nil {
		return err
	}
	s.audit(r, "invitation.accept", u.Username, map[string]any{"method": "password", "_user": u})
	return writeJSON(w, sessionResponse{Status: "enroll_required", CSRFToken: csrf, User: u})
}

func (s *Server) invitationPasskeyBegin(w http.ResponseWriter, r *http.Request) error {
	if done, err := s.guestGuard(w, r); done || err != nil {
		return err
	}
	var in struct{ Token, Username string }
	if err := readJSON(r, &in); err != nil {
		return err
	}
	opts, ceremony, err := s.auth.BeginInvitationPasskey(r.Context(), s.rp(r), in.Token, in.Username)
	if err != nil {
		if errors.Is(err, auth.ErrInvalidInvitation) || errors.Is(err, auth.ErrConflict) || errors.Is(err, auth.ErrBadUsername) {
			return s.badToken(r, err)
		}
		return bad("%s", err.Error())
	}
	return writeJSON(w, map[string]any{"options": opts, "ceremony": ceremony})
}

func (s *Server) invitationPasskeyFinish(w http.ResponseWriter, r *http.Request) error {
	if done, err := s.guestGuard(w, r); done || err != nil {
		return err
	}
	q := r.URL.Query()
	u, err := s.auth.FinishInvitationPasskey(r.Context(), s.rp(r), r.Header.Get("X-Invite-Token"), q.Get("ceremony"), q.Get("name"), r)
	if err != nil {
		if errors.Is(err, auth.ErrInvalidInvitation) || errors.Is(err, auth.ErrConflict) {
			return s.badToken(r, err)
		}
		return bad("%s", err.Error())
	}
	s.audit(r, "invitation.accept", u.Username, map[string]any{"method": "passkey", "_user": u})
	return s.completeLogin(w, r, nil, u, "passkey") // a passkey verifies the user: it is a complete sign-in
}
