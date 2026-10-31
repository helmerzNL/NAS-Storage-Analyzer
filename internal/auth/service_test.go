package auth_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pquerna/otp/totp"

	"github.com/helmerznl/nas-storage-analyzer/internal/auth"
	"github.com/helmerznl/nas-storage-analyzer/internal/secrets"
	"github.com/helmerznl/nas-storage-analyzer/internal/store"
	ts "github.com/helmerznl/nas-storage-analyzer/internal/testsupport"
)

var ctx = context.Background()

const goodPW = "correct horse battery"

func newService(t *testing.T) (*auth.Service, *store.Store) {
	t.Helper()
	auth.SetFastHashingForTests()
	st := ts.NewStore(t)
	box, err := secrets.New(make([]byte, secrets.KeySize))
	if err != nil {
		t.Fatal(err)
	}
	cfg := auth.DefaultConfig()
	cfg.UserFailLimit, cfg.IPFailLimit = 3, 6
	return auth.NewService(st.Pool, box, cfg), st
}

func mustAdmin(t *testing.T, s *auth.Service, name string) *auth.User {
	t.Helper()
	u, err := s.CreateUser(ctx, name, "", goodPW, "admin")
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func TestFirstAdminOnlyOnceEvenConcurrently(t *testing.T) {
	s, _ := newService(t)
	var ok, done atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := s.CreateFirstAdmin(ctx, "admin"+string(rune('a'+i)), goodPW)
			switch {
			case err == nil:
				ok.Add(1)
			case errors.Is(err, auth.ErrSetupDone):
				done.Add(1)
			default:
				t.Errorf("unexpected error: %v", err)
			}
		}(i)
	}
	wg.Wait()
	if ok.Load() != 1 || done.Load() != 5 {
		t.Errorf("created %d admins, %d refused; want 1 and 5", ok.Load(), done.Load())
	}
	if n, _ := s.UserCount(ctx); n != 1 {
		t.Errorf("user count = %d", n)
	}
}

func TestUserValidation(t *testing.T) {
	s, _ := newService(t)
	if _, err := s.CreateUser(ctx, "a b", "", goodPW, "viewer"); !errors.Is(err, auth.ErrBadUsername) {
		t.Errorf("bad username: %v", err)
	}
	if _, err := s.CreateUser(ctx, "bob", "", "short", "viewer"); !errors.Is(err, auth.ErrWeakPassword) {
		t.Errorf("weak password: %v", err)
	}
	if _, err := s.CreateUser(ctx, "bob", "", goodPW, "root"); err == nil {
		t.Error("unknown role accepted")
	}
	if _, err := s.CreateUser(ctx, "Bob", "", goodPW, "viewer"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateUser(ctx, "bOb", "", goodPW, "viewer"); !errors.Is(err, auth.ErrConflict) {
		t.Errorf("usernames must be unique case-insensitively, got %v", err)
	}
}

func TestAuthenticateAndLockout(t *testing.T) {
	s, _ := newService(t)
	mustAdmin(t, s, "alice")
	if u, err := s.Authenticate(ctx, "ALICE", goodPW, "10.0.0.1"); err != nil || u.Username != "alice" {
		t.Fatalf("login failed: %v", err)
	}
	// Unknown user and wrong password give the same error.
	_, e1 := s.Authenticate(ctx, "nobody", "whatever12345", "10.0.0.2")
	_, e2 := s.Authenticate(ctx, "alice", "wrong-password!", "10.0.0.2")
	if !errors.Is(e1, auth.ErrInvalidCredentials) || !errors.Is(e2, auth.ErrInvalidCredentials) {
		t.Errorf("errors differ: %v / %v", e1, e2)
	}
	// Two more failures reach the limit of 3 (one already counted above); from
	// then on even the right password is refused.
	s.Authenticate(ctx, "alice", "wrong-again-12", "10.0.0.2")
	s.Authenticate(ctx, "alice", "wrong-again-13", "10.0.0.2")
	_, err := s.Authenticate(ctx, "alice", goodPW, "10.0.0.3")
	var locked *auth.LockedError
	if !errors.As(err, &locked) || !locked.Until.After(time.Now()) {
		t.Fatalf("expected lockout, got %v", err)
	}
	// Another user from another address is not affected.
	mustAdmin(t, s, "carol")
	if _, err := s.Authenticate(ctx, "carol", goodPW, "10.0.0.9"); err != nil {
		t.Errorf("unrelated user locked: %v", err)
	}
}

func TestSuccessfulLoginClearsUserFailures(t *testing.T) {
	s, _ := newService(t)
	mustAdmin(t, s, "alice")
	for i := 0; i < 2; i++ {
		s.Authenticate(ctx, "alice", "bad-password-1", "10.1.0.1")
	}
	if _, err := s.Authenticate(ctx, "alice", goodPW, "10.1.0.1"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ { // two more failures must not trip a limit of 3
		s.Authenticate(ctx, "alice", "bad-password-2", "10.1.0.1")
	}
	if _, err := s.Authenticate(ctx, "alice", goodPW, "10.1.0.1"); err != nil {
		t.Errorf("failures were not cleared by the successful login: %v", err)
	}
}

func TestIPLockoutAcrossUsernames(t *testing.T) {
	s, _ := newService(t)
	mustAdmin(t, s, "alice")
	for i := 0; i < 6; i++ { // guessing many usernames from one address
		s.Authenticate(ctx, "ghost"+string(rune('a'+i)), "bad-password-1", "10.2.0.1")
	}
	_, err := s.Authenticate(ctx, "alice", goodPW, "10.2.0.1")
	var locked *auth.LockedError
	if !errors.As(err, &locked) {
		t.Errorf("address not locked: %v", err)
	}
	if _, err := s.Authenticate(ctx, "alice", goodPW, "10.2.0.2"); err != nil {
		t.Errorf("another address must still work: %v", err)
	}
}

func TestSessionsLifecycle(t *testing.T) {
	s, st := newService(t)
	u := mustAdmin(t, s, "alice")
	tok, csrf, err := s.CreateSession(ctx, u.ID, auth.StagePending, "1.2.3.4", "UA")
	if err != nil || len(tok) < 40 || csrf == "" {
		t.Fatalf("create: %v", err)
	}
	var stored int
	st.Pool.QueryRow(ctx, `SELECT count(*) FROM sessions WHERE encode(token_hash, 'escape') LIKE '%' || $1 || '%'`, tok).Scan(&stored)
	if stored != 0 {
		t.Error("the session token is stored in clear")
	}
	sess, err := s.LookupSession(ctx, tok)
	if err != nil || sess.Stage != auth.StagePending || sess.User.ID != u.ID || sess.CSRFToken != csrf {
		t.Fatalf("lookup: %+v %v", sess, err)
	}
	// Promotion issues a new token and kills the old one (no session fixation).
	newTok, newCSRF, err := s.Promote(ctx, sess, "1.2.3.4", "UA")
	if err != nil || newTok == tok || newCSRF == csrf {
		t.Fatalf("promote: %v", err)
	}
	if _, err := s.LookupSession(ctx, tok); !errors.Is(err, auth.ErrNotFound) {
		t.Error("old token still valid after promotion")
	}
	active, err := s.LookupSession(ctx, newTok)
	if err != nil || active.Stage != auth.StageActive {
		t.Fatalf("active session: %v", err)
	}
	// Idle expiry.
	st.Pool.Exec(ctx, `UPDATE sessions SET idle_expires_at = now() - interval '1 second'`)
	if _, err := s.LookupSession(ctx, newTok); !errors.Is(err, auth.ErrNotFound) {
		t.Error("idle-expired session accepted")
	}
	// Absolute expiry and disabled users.
	tok2, _, _ := s.CreateSession(ctx, u.ID, auth.StageActive, "", "")
	st.Pool.Exec(ctx, `UPDATE sessions SET absolute_expires_at = now() - interval '1 second' WHERE true`)
	if _, err := s.LookupSession(ctx, tok2); !errors.Is(err, auth.ErrNotFound) {
		t.Error("absolutely expired session accepted")
	}
	bob, _ := s.CreateUser(ctx, "bob", "", goodPW, "viewer")
	tok3, _, _ := s.CreateSession(ctx, bob.ID, auth.StageActive, "", "")
	dis := true
	if _, err := s.UpdateUser(ctx, bob.ID, auth.UserPatch{Disabled: &dis}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.LookupSession(ctx, tok3); !errors.Is(err, auth.ErrNotFound) {
		t.Error("session of a disabled user accepted")
	}
	if _, err := s.Authenticate(ctx, "bob", goodPW, "9.9.9.9"); err == nil {
		t.Error("disabled user can sign in")
	}
	if _, err := s.LookupSession(ctx, "garbage"); !errors.Is(err, auth.ErrNotFound) {
		t.Error("garbage token accepted")
	}
}

func TestTOTPEnrolmentAndReplayProtection(t *testing.T) {
	s, st := newService(t)
	u := mustAdmin(t, s, "alice")
	secret, url, err := s.BeginTOTP(ctx, u)
	if err != nil || !strings.HasPrefix(url, "otpauth://totp/") || !strings.Contains(url, "alice") {
		t.Fatalf("begin: %q %v", url, err)
	}
	var raw []byte
	st.Pool.QueryRow(ctx, `SELECT totp_secret_enc FROM users WHERE id = $1`, u.ID).Scan(&raw)
	if strings.Contains(string(raw), secret) {
		t.Error("TOTP secret stored in clear")
	}
	if err := s.ConfirmTOTP(ctx, u, "000000"); !errors.Is(err, auth.ErrInvalidCode) {
		t.Errorf("wrong code confirmed: %v", err)
	}
	code, _ := totp.GenerateCode(secret, time.Now())
	if err := s.ConfirmTOTP(ctx, u, code); err != nil {
		t.Fatalf("confirm: %v", err)
	}
	u, _ = s.GetUser(ctx, u.ID)
	if !u.TOTPEnabled {
		t.Fatal("TOTP not enabled after confirmation")
	}
	// The confirming code was spent; the same code cannot sign in.
	if err := s.VerifyTOTP(ctx, u, code); !errors.Is(err, auth.ErrInvalidCode) {
		t.Errorf("replayed code accepted: %v", err)
	}
	// A code from the next step works once.
	next, _ := totp.GenerateCode(secret, time.Now().Add(30*time.Second))
	if err := s.VerifyTOTP(ctx, u, " "+next[:3]+" "+next[3:]+" "); err != nil {
		t.Errorf("code with spaces from the next step rejected: %v", err)
	}
	if err := s.VerifyTOTP(ctx, u, next); !errors.Is(err, auth.ErrInvalidCode) {
		t.Errorf("code accepted twice: %v", err)
	}
	// A code from far in the past is refused.
	old, _ := totp.GenerateCode(secret, time.Now().Add(-10*time.Minute))
	if err := s.VerifyTOTP(ctx, u, old); !errors.Is(err, auth.ErrInvalidCode) {
		t.Errorf("stale code accepted: %v", err)
	}
	// TOTP is the only factor: it cannot be removed.
	if err := s.DisableTOTP(ctx, u); !errors.Is(err, auth.ErrLastFactor) {
		t.Errorf("last factor removed: %v", err)
	}
	if _, _, err := s.BeginTOTP(ctx, u); err == nil {
		t.Error("TOTP can be re-enrolled while enabled")
	}
}

func TestRecoveryCodes(t *testing.T) {
	s, _ := newService(t)
	u := mustAdmin(t, s, "alice")
	codes, err := s.NewRecoveryCodes(ctx, u.ID)
	if err != nil || len(codes) != 10 {
		t.Fatalf("codes: %v %v", codes, err)
	}
	seen := map[string]bool{}
	for _, c := range codes {
		if len(c) != 19 || strings.Count(c, "-") != 3 || seen[c] {
			t.Errorf("bad or duplicate code %q", c)
		}
		seen[c] = true
	}
	// Case, dashes and spaces do not matter; each code works once.
	if err := s.UseRecoveryCode(ctx, u.ID, strings.ToLower(strings.ReplaceAll(codes[0], "-", " "))); err != nil {
		t.Errorf("normalised code rejected: %v", err)
	}
	if err := s.UseRecoveryCode(ctx, u.ID, codes[0]); !errors.Is(err, auth.ErrInvalidCode) {
		t.Errorf("code used twice: %v", err)
	}
	if err := s.UseRecoveryCode(ctx, u.ID, "AAAA-BBBB-CCCC-DDDD"); !errors.Is(err, auth.ErrInvalidCode) {
		t.Errorf("made-up code accepted: %v", err)
	}
	if err := s.UseRecoveryCode(ctx, u.ID, "short"); !errors.Is(err, auth.ErrInvalidCode) {
		t.Errorf("short code: %v", err)
	}
	if got, _ := s.GetUser(ctx, u.ID); got.RecoveryCodes != 9 {
		t.Errorf("codes left = %d", got.RecoveryCodes)
	}
	// A code belongs to its user.
	bob, _ := s.CreateUser(ctx, "bob", "", goodPW, "viewer")
	if err := s.UseRecoveryCode(ctx, bob.ID, codes[1]); !errors.Is(err, auth.ErrInvalidCode) {
		t.Errorf("code of another user accepted: %v", err)
	}
	// Regenerating invalidates the old set.
	fresh, _ := s.NewRecoveryCodes(ctx, u.ID)
	if err := s.UseRecoveryCode(ctx, u.ID, codes[2]); !errors.Is(err, auth.ErrInvalidCode) {
		t.Errorf("old code still valid after regeneration: %v", err)
	}
	if err := s.UseRecoveryCode(ctx, u.ID, fresh[0]); err != nil {
		t.Errorf("new code rejected: %v", err)
	}
}

func TestLastAdminIsProtected(t *testing.T) {
	s, _ := newService(t)
	a := mustAdmin(t, s, "alice")
	viewer := "viewer"
	if _, err := s.UpdateUser(ctx, a.ID, auth.UserPatch{Role: &viewer}); !errors.Is(err, auth.ErrLastAdmin) {
		t.Errorf("demoted the last admin: %v", err)
	}
	dis := true
	if _, err := s.UpdateUser(ctx, a.ID, auth.UserPatch{Disabled: &dis}); !errors.Is(err, auth.ErrLastAdmin) {
		t.Errorf("disabled the last admin: %v", err)
	}
	if err := s.DeleteUser(ctx, a.ID); !errors.Is(err, auth.ErrLastAdmin) {
		t.Errorf("deleted the last admin: %v", err)
	}
	b := mustAdmin(t, s, "bob")
	if _, err := s.UpdateUser(ctx, a.ID, auth.UserPatch{Role: &viewer}); err != nil {
		t.Errorf("demoting one of two admins failed: %v", err)
	}
	if err := s.DeleteUser(ctx, b.ID); !errors.Is(err, auth.ErrLastAdmin) {
		t.Errorf("deleted the now-last admin: %v", err)
	}
	if err := s.DeleteUser(ctx, 99999); !errors.Is(err, auth.ErrNotFound) {
		t.Errorf("delete unknown: %v", err)
	}
}

func TestPasswordChangeEndsOtherSessions(t *testing.T) {
	s, _ := newService(t)
	u := mustAdmin(t, s, "alice")
	t1, _, _ := s.CreateSession(ctx, u.ID, auth.StageActive, "", "")
	t2, _, _ := s.CreateSession(ctx, u.ID, auth.StageActive, "", "")
	keep, _ := s.LookupSession(ctx, t1)
	cur, _ := s.GetUser(ctx, u.ID)
	if err := s.ChangePassword(ctx, cur, "wrong-current-pw", "a-brand-new-password", keep); !errors.Is(err, auth.ErrInvalidCredentials) {
		t.Errorf("wrong current password accepted: %v", err)
	}
	if err := s.ChangePassword(ctx, cur, goodPW, "short", keep); !errors.Is(err, auth.ErrWeakPassword) {
		t.Errorf("weak new password accepted: %v", err)
	}
	if err := s.ChangePassword(ctx, cur, goodPW, "a-brand-new-password", keep); err != nil {
		t.Fatal(err)
	}
	if _, err := s.LookupSession(ctx, t1); err != nil {
		t.Error("the current session was ended")
	}
	if _, err := s.LookupSession(ctx, t2); err == nil {
		t.Error("another session survived the password change")
	}
	if _, err := s.Authenticate(ctx, "alice", goodPW, "5.5.5.5"); err == nil {
		t.Error("old password still works")
	}
	if _, err := s.Authenticate(ctx, "alice", "a-brand-new-password", "5.5.5.5"); err != nil {
		t.Errorf("new password rejected: %v", err)
	}
}

func TestResetSecondFactors(t *testing.T) {
	s, _ := newService(t)
	u := mustAdmin(t, s, "alice")
	secret, _, _ := s.BeginTOTP(ctx, u)
	code, _ := totp.GenerateCode(secret, time.Now())
	s.ConfirmTOTP(ctx, u, code)
	s.NewRecoveryCodes(ctx, u.ID)
	tok, _, _ := s.CreateSession(ctx, u.ID, auth.StageActive, "", "")
	if err := s.ResetSecondFactors(ctx, u.ID); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetUser(ctx, u.ID)
	if got.TOTPEnabled || got.RecoveryCodes != 0 || got.Passkeys != 0 || got.HasSecondFactor() {
		t.Errorf("factors remain: %+v", got)
	}
	if _, err := s.LookupSession(ctx, tok); err == nil {
		t.Error("sessions survived the reset")
	}
	if err := s.ResetSecondFactors(ctx, 424242); !errors.Is(err, auth.ErrNotFound) {
		t.Errorf("reset of unknown user: %v", err)
	}
}

func TestAuditLog(t *testing.T) {
	s, _ := newService(t)
	a := auth.Actor{UserID: 0, Username: "system", IP: "1.1.1.1"}
	for _, act := range []string{"login.success", "login.failure", "share.create"} {
		if err := s.Audit(ctx, a, act, "t", map[string]any{"k": 1}); err != nil {
			t.Fatal(err)
		}
	}
	all, err := s.AuditPage(ctx, 0, 10, "")
	if err != nil || len(all) != 3 || all[0].Action != "share.create" {
		t.Fatalf("page: %+v %v", all, err)
	}
	if got, _ := s.AuditPage(ctx, 0, 10, "login."); len(got) != 2 {
		t.Errorf("prefix filter returned %d", len(got))
	}
	if got, _ := s.AuditPage(ctx, all[0].ID, 10, ""); len(got) != 2 {
		t.Errorf("paging returned %d", len(got))
	}
	if all[0].Detail["k"] != float64(1) || all[0].UserID != nil {
		t.Errorf("entry = %+v", all[0])
	}
}
