package api

import (
	"crypto/subtle"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/helmerznl/nas-storage-analyzer/internal/auth"
)

type routeFn = func(string, level, func(http.ResponseWriter, *http.Request) error)

func (s *Server) authRoutes(h routeFn, v string) {
	a := v + "/auth"
	h("GET "+a+"/status", lvPublic, s.authStatus)
	h("POST "+a+"/setup", lvPublic, s.authSetup)
	h("POST "+a+"/login", lvPublic, s.authLogin)
	h("POST "+a+"/logout", lvAnySession, s.authLogout)
	h("GET "+a+"/me", lvViewer, s.authMe)

	h("POST "+a+"/mfa/totp", lvPending, s.mfaTOTP)
	h("POST "+a+"/mfa/recovery", lvPending, s.mfaRecovery)
	h("POST "+a+"/mfa/passkey/begin", lvPending, s.mfaPasskeyBegin)
	h("POST "+a+"/mfa/passkey/finish", lvPending, s.mfaPasskeyFinish)
	h("POST "+a+"/passkey/login/begin", lvPublic, s.passkeyLoginBegin)
	h("POST "+a+"/passkey/login/finish", lvPublic, s.passkeyLoginFinish)

	h("POST "+a+"/totp/begin", lvEnroll, s.totpBegin)
	h("POST "+a+"/totp/confirm", lvEnroll, s.totpConfirm)
	h("DELETE "+a+"/totp", lvViewer, s.totpDisable)
	h("POST "+a+"/passkeys/register/begin", lvEnroll, s.passkeyRegisterBegin)
	h("POST "+a+"/passkeys/register/finish", lvEnroll, s.passkeyRegisterFinish)
	h("GET "+a+"/passkeys", lvViewer, s.passkeyList)
	h("PATCH "+a+"/passkeys/{id}", lvViewer, s.passkeyRename)
	h("DELETE "+a+"/passkeys/{id}", lvViewer, s.passkeyDelete)
	h("POST "+a+"/recovery-codes", lvViewer, s.recoveryCodes)
	h("POST "+a+"/password", lvViewer, s.changePassword)

	h("GET "+v+"/users", lvAdmin, s.listUsers)
	h("POST "+v+"/users", lvAdmin, s.createUser)
	h("PATCH "+v+"/users/{id}", lvAdmin, s.patchUser)
	h("DELETE "+v+"/users/{id}", lvAdmin, s.deleteUser)
	h("POST "+v+"/users/{id}/reset-mfa", lvAdmin, s.resetUserMFA)
	h("POST "+v+"/users/{id}/password", lvAdmin, s.setUserPassword)
	h("GET "+v+"/audit", lvAdmin, s.auditList)
}

// authFail maps account errors to HTTP statuses. It reports whether it did.
func (s *Server) authFail(w http.ResponseWriter, r *http.Request, err error) bool {
	var locked *auth.LockedError
	pc := func(status int, title, code string) bool {
		s.problemCode(w, r, status, title, err.Error(), code)
		return true
	}
	switch {
	case errors.As(err, &locked):
		secs := int(time.Until(locked.Until).Seconds()) + 1
		w.Header().Set("Retry-After", strconv.Itoa(max(secs, 1)))
		s.problemCode(w, r, http.StatusTooManyRequests, "too many failed attempts", "Try again in "+strconv.Itoa(max(secs/60, 1))+" minute(s).", "locked")
		return true
	case errors.Is(err, auth.ErrInvalidCredentials):
		return pc(http.StatusUnauthorized, "invalid username or password", "invalid_credentials")
	case errors.Is(err, auth.ErrInvalidCode):
		return pc(http.StatusUnauthorized, "invalid code", "invalid_code")
	case errors.Is(err, auth.ErrNotFound):
		return pc(http.StatusNotFound, "not found", "not_found")
	case errors.Is(err, auth.ErrConflict):
		return pc(http.StatusConflict, "already exists", "conflict")
	case errors.Is(err, auth.ErrLastAdmin):
		return pc(http.StatusConflict, "last administrator", "last_admin")
	case errors.Is(err, auth.ErrLastFactor):
		return pc(http.StatusConflict, "second factor required", "last_factor")
	case errors.Is(err, auth.ErrSetupDone):
		return pc(http.StatusConflict, "setup already completed", "setup_done")
	case errors.Is(err, auth.ErrBadUsername), errors.Is(err, auth.ErrWeakPassword):
		return pc(http.StatusBadRequest, "invalid request", "invalid_request")
	}
	return false
}

func (s *Server) audit(r *http.Request, action, target string, detail map[string]any) {
	a := auth.Actor{IP: s.clientIP(r)}
	if sess := sessionOf(r); sess != nil {
		a.UserID, a.Username = sess.User.ID, sess.User.Username
	}
	if u, ok := detail["_user"].(*auth.User); ok {
		a.UserID, a.Username = u.ID, u.Username
		delete(detail, "_user")
	}
	if err := s.auth.Audit(r.Context(), a, action, target, detail); err != nil {
		s.log.Error("audit write failed", "action", action, "error", err)
	}
}

type sessionResponse struct {
	Status        string     `json:"status"` // ok | mfa_required | enroll_required
	CSRFToken     string     `json:"csrf_token"`
	User          *auth.User `json:"user,omitempty"`
	Methods       []string   `json:"methods,omitempty"` // for mfa_required: totp, passkey, recovery
	RecoveryCodes []string   `json:"recovery_codes,omitempty"`
}

func methodsOf(u *auth.User) []string {
	var m []string
	if u.TOTPEnabled {
		m = append(m, "totp")
	}
	if u.Passkeys > 0 {
		m = append(m, "passkey")
	}
	if u.RecoveryCodes > 0 {
		m = append(m, "recovery")
	}
	return m
}

func (s *Server) ttl(stage string) time.Duration {
	if stage == auth.StagePending {
		return 5 * time.Minute
	}
	return 7 * 24 * time.Hour
}

// startSession creates the session for a user at the given stage and sets the cookie.
func (s *Server) startSession(w http.ResponseWriter, r *http.Request, u *auth.User, stage string) (string, error) {
	token, csrf, err := s.auth.CreateSession(r.Context(), u.ID, stage, s.clientIP(r), r.UserAgent())
	if err != nil {
		return "", err
	}
	s.setSessionCookie(w, r, token, s.ttl(stage))
	return csrf, nil
}

// completeLogin promotes a half-open session (or opens a new one) to active.
func (s *Server) completeLogin(w http.ResponseWriter, r *http.Request, sess *auth.Session, u *auth.User, method string) error {
	var token, csrf string
	var err error
	if sess != nil {
		token, csrf, err = s.auth.Promote(r.Context(), sess, s.clientIP(r), r.UserAgent())
	} else {
		token, csrf, err = s.auth.CreateSession(r.Context(), u.ID, auth.StageActive, s.clientIP(r), r.UserAgent())
	}
	if err != nil {
		return err
	}
	s.setSessionCookie(w, r, token, s.ttl(auth.StageActive))
	s.auth.TouchLogin(r.Context(), u.ID)
	s.auth.ClearFailures(r.Context(), u)
	s.audit(r, "login.success", u.Username, map[string]any{"method": method, "_user": u})
	fresh, err := s.auth.GetUser(r.Context(), u.ID)
	if err != nil {
		return err
	}
	return writeJSON(w, sessionResponse{Status: "ok", CSRFToken: csrf, User: fresh})
}

// ---- status, setup, login ---------------------------------------------------------

func (s *Server) authStatus(w http.ResponseWriter, r *http.Request) error {
	n, err := s.auth.UserCount(r.Context())
	if err != nil {
		return err
	}
	out := map[string]any{
		"setup_required":      n == 0,
		"authenticated":       false,
		"min_password_length": auth.MinPasswordLen,
		"https_required":      s.opts.RequireHTTPS && s.scheme(r) != "https",
	}
	if sess := sessionOf(r); sess != nil {
		out["stage"] = sess.Stage
		out["csrf_token"] = sess.CSRFToken
		out["user"] = sess.User
		out["authenticated"] = sess.Stage == auth.StageActive
		if sess.Stage == auth.StagePending {
			out["methods"] = methodsOf(sess.User)
		}
	}
	return writeJSON(w, out)
}

func (s *Server) authSetup(w http.ResponseWriter, r *http.Request) error {
	var in struct{ Token, Username, Password string }
	if err := readJSON(r, &in); err != nil {
		return err
	}
	ctx, ip := r.Context(), s.clientIP(r)
	if n, err := s.auth.UserCount(ctx); err != nil {
		return err
	} else if n > 0 {
		return auth.ErrSetupDone
	}
	if err := s.auth.CheckIPLocked(ctx, ip); err != nil {
		return err
	}
	if s.opts.SetupToken == "" || subtle.ConstantTimeCompare([]byte(in.Token), []byte(s.opts.SetupToken)) != 1 {
		s.auth.RecordIPFailure(ctx, ip)
		s.audit(r, "setup.bad_token", "", nil)
		s.problemCode(w, r, http.StatusForbidden, "wrong setup token", "Use the token printed in the application log (or NASA_SETUP_TOKEN).", "invalid_setup_token")
		return nil
	}
	u, err := s.auth.CreateFirstAdmin(ctx, in.Username, in.Password)
	if err != nil {
		return err
	}
	csrf, err := s.startSession(w, r, u, auth.StageEnroll)
	if err != nil {
		return err
	}
	s.audit(r, "setup.complete", u.Username, map[string]any{"_user": u})
	return writeJSON(w, sessionResponse{Status: "enroll_required", CSRFToken: csrf, User: u})
}

func (s *Server) authLogin(w http.ResponseWriter, r *http.Request) error {
	var in struct{ Username, Password string }
	if err := readJSON(r, &in); err != nil {
		return err
	}
	if s.opts.RequireHTTPS && s.scheme(r) != "https" {
		s.problemCode(w, r, http.StatusBadRequest, "https required", "Sign in over https (through the reverse proxy).", "https_required")
		return nil
	}
	u, err := s.auth.Authenticate(r.Context(), in.Username, in.Password, s.clientIP(r))
	if err != nil {
		var locked *auth.LockedError
		switch {
		case errors.As(err, &locked):
			s.audit(r, "login.locked", in.Username, nil)
		case errors.Is(err, auth.ErrInvalidCredentials):
			s.audit(r, "login.failure", in.Username, nil)
		}
		return err
	}
	stage, status := auth.StageEnroll, "enroll_required"
	if u.HasSecondFactor() {
		stage, status = auth.StagePending, "mfa_required"
	}
	csrf, err := s.startSession(w, r, u, stage)
	if err != nil {
		return err
	}
	s.audit(r, "login.password_ok", u.Username, map[string]any{"stage": stage, "_user": u})
	resp := sessionResponse{Status: status, CSRFToken: csrf}
	if stage == auth.StagePending {
		resp.Methods = methodsOf(u)
	}
	return writeJSON(w, resp)
}

func (s *Server) authLogout(w http.ResponseWriter, r *http.Request) error {
	if sess := sessionOf(r); sess != nil {
		if err := s.auth.DeleteSession(r.Context(), sess); err != nil {
			return err
		}
		s.audit(r, "logout", sess.User.Username, nil)
	}
	s.clearSessionCookie(w, r)
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func (s *Server) authMe(w http.ResponseWriter, r *http.Request) error {
	sess := sessionOf(r)
	return writeJSON(w, sessionResponse{Status: "ok", CSRFToken: sess.CSRFToken, User: sess.User})
}

// ---- second step of a sign-in -----------------------------------------------------------

func (s *Server) secondFactorFailed(r *http.Request, u *auth.User, method string) {
	s.auth.RecordSecondFactorFailure(r.Context(), u, s.clientIP(r))
	s.audit(r, "login.mfa_failure", u.Username, map[string]any{"method": method, "_user": u})
}

func (s *Server) mfaTOTP(w http.ResponseWriter, r *http.Request) error {
	var in struct{ Code string }
	if err := readJSON(r, &in); err != nil {
		return err
	}
	sess := sessionOf(r)
	if err := s.auth.CheckLocked(r.Context(), sess.User, s.clientIP(r)); err != nil {
		return err
	}
	if err := s.auth.VerifyTOTP(r.Context(), sess.User, in.Code); err != nil {
		if errors.Is(err, auth.ErrInvalidCode) {
			s.secondFactorFailed(r, sess.User, "totp")
		}
		return err
	}
	return s.completeLogin(w, r, sess, sess.User, "totp")
}

func (s *Server) mfaRecovery(w http.ResponseWriter, r *http.Request) error {
	var in struct{ Code string }
	if err := readJSON(r, &in); err != nil {
		return err
	}
	sess := sessionOf(r)
	if err := s.auth.CheckLocked(r.Context(), sess.User, s.clientIP(r)); err != nil {
		return err
	}
	if err := s.auth.UseRecoveryCode(r.Context(), sess.User.ID, in.Code); err != nil {
		if errors.Is(err, auth.ErrInvalidCode) {
			s.secondFactorFailed(r, sess.User, "recovery")
		}
		return err
	}
	return s.completeLogin(w, r, sess, sess.User, "recovery")
}

func (s *Server) mfaPasskeyBegin(w http.ResponseWriter, r *http.Request) error {
	sess := sessionOf(r)
	opts, id, err := s.auth.BeginPasskeySecondFactor(r.Context(), s.rp(r), sess.User)
	if err != nil {
		return bad("%s", err.Error())
	}
	return writeJSON(w, map[string]any{"options": opts, "ceremony": id})
}

func (s *Server) mfaPasskeyFinish(w http.ResponseWriter, r *http.Request) error {
	sess := sessionOf(r)
	if err := s.auth.CheckLocked(r.Context(), sess.User, s.clientIP(r)); err != nil {
		return err
	}
	if err := s.auth.FinishPasskeySecondFactor(r.Context(), s.rp(r), sess.User, r.URL.Query().Get("ceremony"), r); err != nil {
		s.secondFactorFailed(r, sess.User, "passkey")
		return auth.ErrInvalidCode
	}
	return s.completeLogin(w, r, sess, sess.User, "passkey")
}

func (s *Server) passkeyLoginBegin(w http.ResponseWriter, r *http.Request) error {
	if err := s.auth.CheckIPLocked(r.Context(), s.clientIP(r)); err != nil {
		return err
	}
	opts, id, err := s.auth.BeginPasskeyLogin(r.Context(), s.rp(r))
	if err != nil {
		return err
	}
	return writeJSON(w, map[string]any{"options": opts, "ceremony": id})
}

func (s *Server) passkeyLoginFinish(w http.ResponseWriter, r *http.Request) error {
	ip := s.clientIP(r)
	if err := s.auth.CheckIPLocked(r.Context(), ip); err != nil {
		return err
	}
	u, err := s.auth.FinishPasskeyLogin(r.Context(), s.rp(r), r.URL.Query().Get("ceremony"), r)
	if err != nil {
		s.auth.RecordIPFailure(r.Context(), ip)
		s.audit(r, "login.failure", "passkey", nil)
		return auth.ErrInvalidCode
	}
	return s.completeLogin(w, r, nil, u, "passkey")
}

// ---- enrolment and management of second factors ------------------------------------

// afterEnrol finishes an enrolment: a user who had no second factor yet becomes
// fully signed in and receives their recovery codes once.
func (s *Server) afterEnrol(w http.ResponseWriter, r *http.Request, sess *auth.Session) error {
	ctx := r.Context()
	u, err := s.auth.GetUser(ctx, sess.User.ID)
	if err != nil {
		return err
	}
	resp := sessionResponse{Status: "ok", CSRFToken: sess.CSRFToken, User: u}
	if u.RecoveryCodes == 0 {
		if resp.RecoveryCodes, err = s.auth.NewRecoveryCodes(ctx, u.ID); err != nil {
			return err
		}
		if resp.User, err = s.auth.GetUser(ctx, u.ID); err != nil {
			return err
		}
	}
	if sess.Stage == auth.StageEnroll {
		token, csrf, err := s.auth.Promote(ctx, sess, s.clientIP(r), r.UserAgent())
		if err != nil {
			return err
		}
		s.setSessionCookie(w, r, token, s.ttl(auth.StageActive))
		s.auth.TouchLogin(ctx, u.ID)
		resp.CSRFToken = csrf
		s.audit(r, "login.success", u.Username, map[string]any{"method": "enrolment", "_user": u})
	}
	return writeJSON(w, resp)
}

func (s *Server) totpBegin(w http.ResponseWriter, r *http.Request) error {
	secret, url, err := s.auth.BeginTOTP(r.Context(), sessionOf(r).User)
	if err != nil {
		return bad("%s", err.Error())
	}
	return writeJSON(w, map[string]string{"secret": secret, "otpauth_url": url})
}

func (s *Server) totpConfirm(w http.ResponseWriter, r *http.Request) error {
	var in struct{ Code string }
	if err := readJSON(r, &in); err != nil {
		return err
	}
	sess := sessionOf(r)
	if err := s.auth.CheckLocked(r.Context(), sess.User, s.clientIP(r)); err != nil {
		return err
	}
	if err := s.auth.ConfirmTOTP(r.Context(), sess.User, in.Code); err != nil {
		if errors.Is(err, auth.ErrInvalidCode) {
			s.secondFactorFailed(r, sess.User, "totp-enrol")
		}
		return err
	}
	s.audit(r, "mfa.totp_enabled", sess.User.Username, nil)
	return s.afterEnrol(w, r, sess)
}

// requirePassword re-checks the current password for sensitive changes.
func (s *Server) requirePassword(r *http.Request, password string) error {
	sess := sessionOf(r)
	if err := s.auth.CheckLocked(r.Context(), sess.User, s.clientIP(r)); err != nil {
		return err
	}
	if !s.auth.CheckPassword(sess.User, password) {
		s.auth.RecordSecondFactorFailure(r.Context(), sess.User, s.clientIP(r))
		s.audit(r, "auth.reauth_failure", sess.User.Username, nil)
		return auth.ErrInvalidCredentials
	}
	return nil
}

func (s *Server) totpDisable(w http.ResponseWriter, r *http.Request) error {
	var in struct{ Password string }
	if err := readJSON(r, &in); err != nil {
		return err
	}
	if err := s.requirePassword(r, in.Password); err != nil {
		return err
	}
	u := sessionOf(r).User
	if err := s.auth.DisableTOTP(r.Context(), u); err != nil {
		return err
	}
	s.audit(r, "mfa.totp_disabled", u.Username, nil)
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func (s *Server) passkeyRegisterBegin(w http.ResponseWriter, r *http.Request) error {
	opts, id, err := s.auth.BeginPasskeyRegistration(r.Context(), s.rp(r), sessionOf(r).User)
	if err != nil {
		return bad("%s", err.Error())
	}
	return writeJSON(w, map[string]any{"options": opts, "ceremony": id})
}

func (s *Server) passkeyRegisterFinish(w http.ResponseWriter, r *http.Request) error {
	sess := sessionOf(r)
	if err := s.auth.FinishPasskeyRegistration(r.Context(), s.rp(r), sess.User, r.URL.Query().Get("ceremony"),
		r.URL.Query().Get("name"), r); err != nil {
		return bad("%s", err.Error())
	}
	s.audit(r, "mfa.passkey_added", sess.User.Username, map[string]any{"name": r.URL.Query().Get("name")})
	return s.afterEnrol(w, r, sess)
}

func (s *Server) passkeyList(w http.ResponseWriter, r *http.Request) error {
	list, err := s.auth.ListPasskeys(r.Context(), sessionOf(r).User.ID)
	if err != nil {
		return err
	}
	return writeJSON(w, items[auth.Passkey]{Items: list})
}

func (s *Server) passkeyRename(w http.ResponseWriter, r *http.Request) error {
	var in struct{ Name string }
	if err := readJSON(r, &in); err != nil {
		return err
	}
	if err := s.auth.RenamePasskey(r.Context(), sessionOf(r).User.ID, r.PathValue("id"), in.Name); err != nil {
		if errors.Is(err, auth.ErrNotFound) {
			return err
		}
		return bad("%s", err.Error())
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func (s *Server) passkeyDelete(w http.ResponseWriter, r *http.Request) error {
	u := sessionOf(r).User
	if err := s.auth.DeletePasskey(r.Context(), u, r.PathValue("id")); err != nil {
		return err
	}
	s.audit(r, "mfa.passkey_removed", u.Username, nil)
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func (s *Server) recoveryCodes(w http.ResponseWriter, r *http.Request) error {
	var in struct{ Password string }
	if err := readJSON(r, &in); err != nil {
		return err
	}
	if err := s.requirePassword(r, in.Password); err != nil {
		return err
	}
	u := sessionOf(r).User
	if !u.HasSecondFactor() {
		return bad("set up a second factor first")
	}
	codes, err := s.auth.NewRecoveryCodes(r.Context(), u.ID)
	if err != nil {
		return err
	}
	s.audit(r, "mfa.recovery_codes_regenerated", u.Username, nil)
	return writeJSON(w, map[string]any{"recovery_codes": codes})
}

func (s *Server) changePassword(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Current string `json:"current"`
		New     string `json:"new"`
	}
	if err := readJSON(r, &in); err != nil {
		return err
	}
	sess := sessionOf(r)
	if err := s.requirePassword(r, in.Current); err != nil {
		return err
	}
	if err := s.auth.ChangePassword(r.Context(), sess.User, in.Current, in.New, sess); err != nil {
		return err
	}
	s.audit(r, "user.password_changed", sess.User.Username, nil)
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// ---- administration of users ---------------------------------------------------------------

func (s *Server) listUsers(w http.ResponseWriter, r *http.Request) error {
	list, err := s.auth.ListUsers(r.Context())
	if err != nil {
		return err
	}
	return writeJSON(w, items[auth.User]{Items: list})
}

func (s *Server) createUser(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Username    string `json:"username"`
		DisplayName string `json:"display_name"`
		Password    string `json:"password"`
		Role        string `json:"role"`
	}
	if err := readJSON(r, &in); err != nil {
		return err
	}
	u, err := s.auth.CreateUser(r.Context(), in.Username, in.DisplayName, in.Password, in.Role)
	if err != nil {
		if errors.Is(err, auth.ErrBadUsername) || errors.Is(err, auth.ErrWeakPassword) || errors.Is(err, auth.ErrConflict) {
			return err
		}
		return bad("%s", err.Error())
	}
	s.audit(r, "user.create", u.Username, map[string]any{"role": u.Role})
	w.WriteHeader(http.StatusCreated)
	return writeJSON(w, u)
}

func userID(r *http.Request) (int64, error) { return parseID(r.PathValue("id"), "user id") }

func (s *Server) patchUser(w http.ResponseWriter, r *http.Request) error {
	id, err := userID(r)
	if err != nil {
		return err
	}
	var in struct {
		DisplayName *string `json:"display_name"`
		Role        *string `json:"role"`
		Disabled    *bool   `json:"disabled"`
	}
	if err := readJSON(r, &in); err != nil {
		return err
	}
	u, err := s.auth.UpdateUser(r.Context(), id, auth.UserPatch{DisplayName: in.DisplayName, Role: in.Role, Disabled: in.Disabled})
	if err != nil {
		if errors.Is(err, auth.ErrNotFound) || errors.Is(err, auth.ErrLastAdmin) {
			return err
		}
		return bad("%s", err.Error())
	}
	s.audit(r, "user.update", u.Username, map[string]any{"role": u.Role, "disabled": u.Disabled})
	return writeJSON(w, u)
}

func (s *Server) deleteUser(w http.ResponseWriter, r *http.Request) error {
	id, err := userID(r)
	if err != nil {
		return err
	}
	target, err := s.auth.GetUser(r.Context(), id)
	if err != nil {
		return err
	}
	if err := s.auth.DeleteUser(r.Context(), id); err != nil {
		return err
	}
	s.audit(r, "user.delete", target.Username, nil)
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func (s *Server) resetUserMFA(w http.ResponseWriter, r *http.Request) error {
	id, err := userID(r)
	if err != nil {
		return err
	}
	target, err := s.auth.GetUser(r.Context(), id)
	if err != nil {
		return err
	}
	if err := s.auth.ResetSecondFactors(r.Context(), id); err != nil {
		return err
	}
	s.audit(r, "user.mfa_reset", target.Username, nil)
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func (s *Server) setUserPassword(w http.ResponseWriter, r *http.Request) error {
	id, err := userID(r)
	if err != nil {
		return err
	}
	var in struct{ Password string }
	if err := readJSON(r, &in); err != nil {
		return err
	}
	target, err := s.auth.GetUser(r.Context(), id)
	if err != nil {
		return err
	}
	if err := s.auth.SetPassword(r.Context(), id, in.Password); err != nil {
		return err
	}
	s.audit(r, "user.password_reset", target.Username, nil)
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func (s *Server) auditList(w http.ResponseWriter, r *http.Request) error {
	p, err := parseList(r, "", true)
	if err != nil {
		return err
	}
	before, err := parseIntParam(r, "before", 0)
	if err != nil {
		return err
	}
	list, err := s.auth.AuditPage(r.Context(), int64(before), p.Limit, r.URL.Query().Get("action"))
	if err != nil {
		return err
	}
	limit := p.Limit
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	var next *int64
	if len(list) == limit {
		last := list[len(list)-1].ID
		next = &last
	}
	return writeJSON(w, map[string]any{"items": list, "next_before": next})
}
