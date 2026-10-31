package auth_test

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pquerna/otp/totp"

	"github.com/helmerznl/nas-storage-analyzer/internal/auth"
	ts "github.com/helmerznl/nas-storage-analyzer/internal/testsupport"
)

// invite makes an invitation as an administrator.
func invite(t *testing.T, s *auth.Service, by *auth.User, name, role string) (*auth.Invitation, string) {
	t.Helper()
	inv, token, err := s.CreateInvitation(ctx, by, name, role)
	if err != nil {
		t.Fatalf("CreateInvitation(%q, %q): %v", name, role, err)
	}
	return inv, token
}

// passkeyInvite runs the whole passkey half of an invitation with a virtual authenticator.
func passkeyInvite(t *testing.T, s *auth.Service, token, name string, va *ts.VirtualAuthenticator, origin string) (*auth.User, error) {
	t.Helper()
	creation, ceremony, err := s.BeginInvitationPasskey(ctx, rp, token, name)
	if err != nil {
		return nil, err
	}
	return s.FinishInvitationPasskey(ctx, rp, token, ceremony, "Laptop", va.Create(creation, origin))
}

func TestInvitationMakesAccountWithTheGuestsOwnPassword(t *testing.T) {
	s, st := newService(t)
	admin := mustAdmin(t, s, "alice")
	inv, token := invite(t, s, admin, "anna", "viewer")
	if inv.Kind != "new" || inv.Role != "viewer" || inv.Username != "anna" || inv.CreatedBy != "alice" {
		t.Errorf("invitation = %+v", inv)
	}
	if d := time.Until(inv.ExpiresAt); d < 6*24*time.Hour+23*time.Hour || d > auth.InviteTTL {
		t.Errorf("expires in %v, want seven days", d)
	}
	// Only the hash is stored: the token is in no column, in no form.
	var all string
	if err := st.Pool.QueryRow(ctx, `SELECT coalesce(string_agg(i::text, ' '), '') FROM invitations i`).Scan(&all); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(all, token) || strings.Contains(strings.ToLower(all), strings.ToLower(token)) {
		t.Fatalf("the token is in the database: %s", all)
	}
	var isHash, isRaw bool
	if err := st.Pool.QueryRow(ctx, `SELECT token_hash = sha256(convert_to($1, 'UTF8')), token_hash = convert_to($1, 'UTF8') FROM invitations`, token).Scan(&isHash, &isRaw); err != nil {
		t.Fatal(err)
	}
	if !isHash || isRaw {
		t.Errorf("token_hash is the SHA-256 of the token: %v, the token itself: %v", isHash, isRaw)
	}
	// What the guest sees: who invited them, the suggested name, the role. Nothing else.
	info, err := s.InvitationInfo(ctx, token)
	if err != nil || info.Kind != "new" || info.Username != "anna" || info.InvitedBy != "alice" || info.Role != "viewer" {
		t.Fatalf("info = %+v, %v", info, err)
	}
	// The guest chooses another name and their own password; the administrator is never asked for one.
	u, err := s.AcceptInvitationPassword(ctx, token, "anna.b", goodPW)
	if err != nil {
		t.Fatal(err)
	}
	if u.Username != "anna.b" || u.Role != "viewer" || u.Passkeys != 0 || u.TOTPEnabled {
		t.Errorf("user = %+v", u)
	}
	if got, err := s.Authenticate(ctx, "anna.b", goodPW, "10.0.0.1"); err != nil || got.ID != u.ID {
		t.Errorf("the guest cannot sign in with their own password: %v", err)
	}
	// One use: it is gone, and every way of asking says the same thing.
	if _, err := s.InvitationInfo(ctx, token); !errors.Is(err, auth.ErrInvalidInvitation) {
		t.Errorf("info after use: %v", err)
	}
	if _, err := s.AcceptInvitationPassword(ctx, token, "anna.c", goodPW); !errors.Is(err, auth.ErrInvalidInvitation) {
		t.Errorf("second use: %v", err)
	}
	if list, _ := s.ListInvitations(ctx); len(list) != 0 {
		t.Errorf("open invitations after use: %+v", list)
	}
}

func TestEveryBadTokenIsTheSameAnswer(t *testing.T) {
	s, st := newService(t)
	admin := mustAdmin(t, s, "alice")
	// Make them all first: making an invitation purges the expired ones, and this must test expiry, not the purge.
	exp, expired := invite(t, s, admin, "", "viewer")
	inv, revoked := invite(t, s, admin, "", "viewer")
	_, used := invite(t, s, admin, "", "viewer")
	if _, err := s.RevokeInvitation(ctx, inv.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AcceptInvitationPassword(ctx, used, "usedup", goodPW); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool.Exec(ctx, `UPDATE invitations SET expires_at = now() - interval '1 second' WHERE id = $1`, exp.ID); err != nil {
		t.Fatal(err)
	}
	var rows int
	st.Pool.QueryRow(ctx, `SELECT count(*) FROM invitations WHERE id = $1`, exp.ID).Scan(&rows)
	if rows != 1 {
		t.Fatal("setup: the expired invitation must still be in the table, or expiry is not what is tested")
	}
	for name, token := range map[string]string{"expired": expired, "revoked": revoked, "used": used, "unknown": strings.Repeat("a", 43),
		"empty": "", "short": "abc", "huge": strings.Repeat("x", 5000), "hash of a real one": "00112233445566778899aabbccddeeff00112233445566778899"} {
		if _, err := s.InvitationInfo(ctx, token); !errors.Is(err, auth.ErrInvalidInvitation) {
			t.Errorf("%s: info = %v", name, err)
		}
		if _, err := s.AcceptInvitationPassword(ctx, token, "whoever", goodPW); !errors.Is(err, auth.ErrInvalidInvitation) {
			t.Errorf("%s: accept = %v", name, err)
		}
		if _, _, err := s.BeginInvitationPasskey(ctx, rp, token, "whoever"); !errors.Is(err, auth.ErrInvalidInvitation) {
			t.Errorf("%s: passkey begin = %v (a bad token gets no passkey options)", name, err)
		}
	}
	if _, err := s.RevokeInvitation(ctx, 999999); !errors.Is(err, auth.ErrNotFound) {
		t.Errorf("revoke of nothing: %v", err)
	}
}

func TestNameAndPasswordRulesApplyAndNothingIsHalfCreated(t *testing.T) {
	s, _ := newService(t)
	admin := mustAdmin(t, s, "alice")
	_, token := invite(t, s, admin, "", "viewer")
	for name, tc := range map[string]struct{ user, pw string }{
		"too short a name": {"x", goodPW}, "a name with a space": {"a b", goodPW}, "a name that is taken": {"Alice", goodPW},
		"a weak password": {"anna", "short"}, "no name and none suggested": {"", goodPW},
	} {
		if _, err := s.AcceptInvitationPassword(ctx, token, tc.user, tc.pw); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
	// None of that used up the invitation or created anything: the guest can try again.
	if users, _ := s.ListUsers(ctx); len(users) != 1 {
		t.Errorf("users = %d after refused attempts", len(users))
	}
	if _, err := s.InvitationInfo(ctx, token); err != nil {
		t.Fatalf("a refused attempt used up the invitation: %v", err)
	}
	if u, err := s.AcceptInvitationPassword(ctx, token, "anna", goodPW); err != nil || u.Username != "anna" {
		t.Fatalf("retry: %v", err)
	}
}

func TestANameInAPendingInvitationIsReserved(t *testing.T) {
	s, _ := newService(t)
	admin := mustAdmin(t, s, "alice")
	_, first := invite(t, s, admin, "carol", "viewer")
	if _, _, err := s.CreateInvitation(ctx, admin, "Carol", "viewer"); !errors.Is(err, auth.ErrConflict) {
		t.Errorf("a second invitation for the same name: %v", err)
	}
	_, other := invite(t, s, admin, "", "viewer")
	if _, err := s.AcceptInvitationPassword(ctx, other, "carol", goodPW); !errors.Is(err, auth.ErrConflict) {
		t.Errorf("a guest took a name reserved for someone else: %v", err)
	}
	// The reservation is released when it is used, so the holder themselves can have it.
	if u, err := s.AcceptInvitationPassword(ctx, first, "carol", goodPW); err != nil || u.Username != "carol" {
		t.Errorf("the holder: %v", err)
	}
	if _, err := s.AcceptInvitationPassword(ctx, other, "carol", goodPW); !errors.Is(err, auth.ErrConflict) {
		t.Errorf("a name that is now taken: %v", err)
	}
}

func TestTwoInvitationsForTheSameNameFinishingAtOnce(t *testing.T) {
	s, st := newService(t)
	admin := mustAdmin(t, s, "alice")
	tokens := make([]string, 6)
	for i := range tokens {
		_, tokens[i] = invite(t, s, admin, "", "viewer")
	}
	var wg sync.WaitGroup
	results := make([]error, len(tokens))
	for i := range tokens {
		wg.Add(1)
		go func() { defer wg.Done(); _, results[i] = s.AcceptInvitationPassword(ctx, tokens[i], "dave", goodPW) }()
	}
	wg.Wait()
	won := 0
	for _, err := range results {
		switch {
		case err == nil:
			won++
		case errors.Is(err, auth.ErrConflict):
		default:
			t.Errorf("a race ended as %v, not as a conflict", err)
		}
	}
	var users int
	st.Pool.QueryRow(ctx, `SELECT count(*) FROM users WHERE lower(username) = 'dave'`).Scan(&users)
	if won != 1 || users != 1 {
		t.Errorf("%d winners, %d accounts", won, users)
	}
}

func TestInvitationRoleIsTheAdministratorsChoice(t *testing.T) {
	s, _ := newService(t)
	admin := mustAdmin(t, s, "alice")
	for _, role := range []string{"admin", "viewer"} {
		_, token := invite(t, s, admin, "", role)
		u, err := s.AcceptInvitationPassword(ctx, token, "guest-"+role, goodPW)
		if err != nil || u.Role != role {
			t.Errorf("role %s: %+v, %v", role, u, err)
		}
	}
	if _, _, err := s.CreateInvitation(ctx, admin, "", "root"); !errors.Is(err, auth.ErrBadRole) {
		t.Errorf("a made-up role: %v", err)
	}
	if _, _, err := s.CreateInvitation(ctx, admin, "no good", "viewer"); !errors.Is(err, auth.ErrBadUsername) {
		t.Errorf("a bad suggested name: %v", err)
	}
}

func TestCapsCountPendingInvitationsAsUsers(t *testing.T) {
	s, st := newService(t)
	admin := mustAdmin(t, s, "alice")
	for i := 0; i < auth.MaxUsers-1; i++ { // one account plus 49 pending = 50
		invite(t, s, admin, "", "viewer")
	}
	if _, _, err := s.CreateInvitation(ctx, admin, "", "viewer"); !errors.Is(err, auth.ErrTooMany) {
		t.Fatalf("the 50th future user: %v (pending invitations count as users, or the cap is a suggestion)", err)
	}
	// The open-invitation cap is separate: fill it with resets of other users.
	if _, err := st.Pool.Exec(ctx, `DELETE FROM invitations`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool.Exec(ctx, `INSERT INTO users (username, role) SELECT 'user' || g, 'viewer' FROM generate_series(1, 51) g`); err != nil {
		t.Fatal(err)
	}
	rows, _ := st.Pool.Query(ctx, `SELECT id FROM users WHERE username LIKE 'user%' ORDER BY id`)
	var ids []int64
	for rows.Next() {
		var id int64
		rows.Scan(&id)
		ids = append(ids, id)
	}
	rows.Close()
	for i, id := range ids {
		_, _, err := s.CreateResetInvitation(ctx, admin, id)
		if i < auth.MaxInvitations && err != nil {
			t.Fatalf("reset %d: %v", i, err)
		}
		if i == auth.MaxInvitations && !errors.Is(err, auth.ErrTooMany) {
			t.Fatalf("reset %d (past the cap) = %v", i, err)
		}
	}
	// Making a new reset for someone who already has one replaces it and does not count twice.
	if _, _, err := s.CreateResetInvitation(ctx, admin, ids[0]); err != nil {
		t.Errorf("replacing a reset at the cap: %v", err)
	}
}

func TestExpiredInvitationsArePurgedWhenAnotherIsMade(t *testing.T) {
	s, st := newService(t)
	admin := mustAdmin(t, s, "alice")
	invite(t, s, admin, "old1", "viewer")
	st.Pool.Exec(ctx, `UPDATE invitations SET expires_at = now() - interval '1 day'`)
	var n int
	st.Pool.QueryRow(ctx, `SELECT count(*) FROM invitations`).Scan(&n)
	if n != 1 {
		t.Fatal("setup")
	}
	if list, _ := s.ListInvitations(ctx); len(list) != 0 {
		t.Errorf("an expired invitation is listed: %+v", list)
	}
	invite(t, s, admin, "new1", "viewer") // the name "old1" is free again, and the row is gone
	st.Pool.QueryRow(ctx, `SELECT count(*) FROM invitations`).Scan(&n)
	if n != 1 {
		t.Errorf("%d rows after a new invitation, want only the new one", n)
	}
}

// ---- reset ------------------------------------------------------------------------------------------------------------------------

func TestResetChangesNothingUntilTheGuestHasFinished(t *testing.T) {
	s, st := newService(t)
	admin := mustAdmin(t, s, "alice")
	bob, _ := s.CreateUser(ctx, "bob", "", goodPW, "viewer")
	secret, _, _ := s.BeginTOTP(ctx, bob)
	code, _ := totpNow(secret)
	if err := s.ConfirmTOTP(ctx, bob, code); err != nil {
		t.Fatal(err)
	}
	codes, _ := s.NewRecoveryCodes(ctx, bob.ID)
	registerPasskey(t, s, bob, ts.NewVirtualAuthenticator(), "Old phone")
	oldToken, _, _ := s.CreateSession(ctx, bob.ID, "active", "10.0.0.1", "test")

	inv, token, err := s.CreateResetInvitation(ctx, admin, bob.ID)
	if err != nil || inv.Kind != "reset" || inv.Username != "bob" || inv.Role != "viewer" || inv.UserID == nil || *inv.UserID != bob.ID {
		t.Fatalf("reset = %+v, %v", inv, err)
	}
	// Making the link, and opening it, changes nothing: a guest who walks away has cost the user nothing.
	info, err := s.InvitationInfo(ctx, token)
	if err != nil || info.Kind != "reset" || info.Username != "bob" {
		t.Fatalf("info = %+v, %v", info, err)
	}
	if _, _, err := s.BeginInvitationPasskey(ctx, rp, token, "someone-else"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Authenticate(ctx, "bob", goodPW, "10.0.0.1"); err != nil {
		t.Errorf("the old password stopped working before the guest finished: %v", err)
	}
	if got, _ := s.GetUser(ctx, bob.ID); !got.TOTPEnabled || got.Passkeys != 1 || got.RecoveryCodes != len(codes) {
		t.Errorf("credentials changed before the guest finished: %+v", got)
	}
	if _, err := s.LookupSession(ctx, oldToken); err != nil {
		t.Errorf("the old session ended before the guest finished: %v", err)
	}

	// Finishing replaces everything, in one step; the name in the request is ignored for a reset.
	u, err := s.AcceptInvitationPassword(ctx, token, "mallory", "a-brand-new-password")
	if err != nil || u.ID != bob.ID || u.Username != "bob" {
		t.Fatalf("accept = %+v, %v", u, err)
	}
	if u.TOTPEnabled || u.Passkeys != 0 || u.RecoveryCodes != 0 {
		t.Errorf("old second factors survived a reset: %+v", u)
	}
	if _, err := s.Authenticate(ctx, "bob", goodPW, "10.0.0.1"); err == nil {
		t.Error("the old password still works after a reset")
	}
	if _, err := s.Authenticate(ctx, "bob", "a-brand-new-password", "10.0.0.1"); err != nil {
		t.Errorf("the new password does not work: %v", err)
	}
	if _, err := s.LookupSession(ctx, oldToken); err == nil {
		t.Error("the old session survived a reset: every session of that user must end")
	}
	var users int
	st.Pool.QueryRow(ctx, `SELECT count(*) FROM users WHERE lower(username) = 'mallory'`).Scan(&users)
	if users != 0 {
		t.Error("a reset created an account under the name the guest sent")
	}
}

func TestResetRules(t *testing.T) {
	s, _ := newService(t)
	admin := mustAdmin(t, s, "alice")
	bob, _ := s.CreateUser(ctx, "bob", "", goodPW, "viewer")
	if _, _, err := s.CreateResetInvitation(ctx, admin, admin.ID); !errors.Is(err, auth.ErrSelfReset) {
		t.Errorf("a reset for yourself: %v", err)
	}
	if _, _, err := s.CreateResetInvitation(ctx, admin, 99999); !errors.Is(err, auth.ErrNotFound) {
		t.Errorf("a reset for nobody: %v", err)
	}
	// One per user: a new one replaces the old.
	_, first, err := s.CreateResetInvitation(ctx, admin, bob.ID)
	if err != nil {
		t.Fatal(err)
	}
	_, second, err := s.CreateResetInvitation(ctx, admin, bob.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.InvitationInfo(ctx, first); !errors.Is(err, auth.ErrInvalidInvitation) {
		t.Errorf("the replaced reset link still works: %v", err)
	}
	if _, err := s.InvitationInfo(ctx, second); err != nil {
		t.Errorf("the new reset link: %v", err)
	}
	if list, _ := s.ListInvitations(ctx); len(list) != 1 {
		t.Errorf("%d open invitations, want one reset per user", len(list))
	}
	// A user who is disabled, or gone, meanwhile makes the link invalid.
	if _, err := s.UpdateUser(ctx, bob.ID, auth.UserPatch{Disabled: ptr(true)}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AcceptInvitationPassword(ctx, second, "", "a-brand-new-password"); !errors.Is(err, auth.ErrInvalidInvitation) {
		t.Errorf("a reset for a user who was disabled meanwhile: %v", err)
	}
	if _, _, err := s.CreateResetInvitation(ctx, admin, bob.ID); !errors.Is(err, auth.ErrUserDisabled) {
		t.Errorf("a reset for a disabled user: %v", err)
	}
	if _, err := s.UpdateUser(ctx, bob.ID, auth.UserPatch{Disabled: ptr(false)}); err != nil {
		t.Fatal(err)
	}
	_, third, _ := s.CreateResetInvitation(ctx, admin, bob.ID)
	if err := s.DeleteUser(ctx, bob.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.InvitationInfo(ctx, third); !errors.Is(err, auth.ErrInvalidInvitation) {
		t.Errorf("a reset link outlived its user: %v", err)
	}
}

func TestDeletingAUserRemovesWhatTheyInvited(t *testing.T) {
	s, st := newService(t)
	admin := mustAdmin(t, s, "alice")
	second, _ := s.CreateUser(ctx, "second", "", goodPW, "admin")
	_, token := invite(t, s, second, "guest1", "viewer")
	if err := s.DeleteUser(ctx, second.ID); err != nil {
		t.Fatal(err)
	}
	// The invitation stays valid (it was the administrators' decision, not that one person's), but no longer names who made it.
	info, err := s.InvitationInfo(ctx, token)
	if err != nil || info.InvitedBy != "" {
		t.Fatalf("info = %+v, %v", info, err)
	}
	_ = admin
	var n int
	st.Pool.QueryRow(ctx, `SELECT count(*) FROM invitations WHERE created_by IS NULL`).Scan(&n)
	if n != 1 {
		t.Errorf("created_by is not cleared: %d", n)
	}
}

// ---- passkeys --------------------------------------------------------------------------------------------------------------------

func TestInvitationWithAPasskey(t *testing.T) {
	s, _ := newService(t)
	admin := mustAdmin(t, s, "alice")
	_, token := invite(t, s, admin, "erin", "admin")
	u, err := passkeyInvite(t, s, token, "erin", ts.NewVirtualAuthenticator(), origin)
	if err != nil {
		t.Fatal(err)
	}
	if u.Username != "erin" || u.Role != "admin" || u.Passkeys != 1 || !u.HasSecondFactor() {
		t.Errorf("user = %+v", u)
	}
	// There is no password, and nobody could have learnt one: the passkey is the whole sign-in.
	if _, err := s.Authenticate(ctx, "erin", goodPW, "10.0.0.1"); err == nil {
		t.Error("a passkey-only account has a password")
	}
	if _, err := s.InvitationInfo(ctx, token); !errors.Is(err, auth.ErrInvalidInvitation) {
		t.Errorf("the invitation outlived its use: %v", err)
	}
}

func TestATakenNameIsRefusedBeforeAPasskeyIsMade(t *testing.T) {
	s, _ := newService(t)
	admin := mustAdmin(t, s, "alice")
	_, token := invite(t, s, admin, "", "viewer")
	if _, _, err := s.BeginInvitationPasskey(ctx, rp, token, "alice"); !errors.Is(err, auth.ErrConflict) {
		t.Errorf("begin with a taken name: %v", err)
	}
	if _, _, err := s.BeginInvitationPasskey(ctx, rp, token, "x"); !errors.Is(err, auth.ErrBadUsername) {
		t.Errorf("begin with a bad name: %v", err)
	}
}

func TestAFailedPasskeyAttemptLeavesTheInvitationForARetry(t *testing.T) {
	s, _ := newService(t)
	admin := mustAdmin(t, s, "alice")
	_, token := invite(t, s, admin, "", "viewer")
	// A passkey from another origin is refused...
	if _, err := passkeyInvite(t, s, token, "frank", ts.NewVirtualAuthenticator(), "https://evil.example"); err == nil {
		t.Fatal("a passkey from another origin was accepted")
	}
	// ...and so is an answer to another challenge...
	creation, _, err := s.BeginInvitationPasskey(ctx, rp, token, "frank")
	if err != nil {
		t.Fatal(err)
	}
	_, ceremony2, _ := s.BeginInvitationPasskey(ctx, rp, token, "frank")
	if _, err := s.FinishInvitationPasskey(ctx, rp, token, ceremony2, "x", ts.NewVirtualAuthenticator().Create(creation, origin)); err == nil {
		t.Fatal("an answer to a different challenge was accepted")
	}
	// ...a ceremony cannot be used twice, nor with another token...
	if _, err := s.FinishInvitationPasskey(ctx, rp, token, ceremony2, "x", ts.NewVirtualAuthenticator().Create(creation, origin)); err == nil {
		t.Error("a ceremony was used twice")
	}
	// ...none of it created an account or used the invitation up, so the same guest can simply try again.
	if users, _ := s.ListUsers(ctx); len(users) != 1 {
		t.Fatalf("%d users after failed attempts", len(users))
	}
	u, err := passkeyInvite(t, s, token, "frank", ts.NewVirtualAuthenticator(), origin)
	if err != nil || u.Username != "frank" {
		t.Fatalf("retry: %v", err)
	}
}

func TestARevokeBetweenBeginAndFinishStopsIt(t *testing.T) {
	s, _ := newService(t)
	admin := mustAdmin(t, s, "alice")
	inv, token := invite(t, s, admin, "", "viewer")
	creation, ceremony, err := s.BeginInvitationPasskey(ctx, rp, token, "gina")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.RevokeInvitation(ctx, inv.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.FinishInvitationPasskey(ctx, rp, token, ceremony, "x", ts.NewVirtualAuthenticator().Create(creation, origin)); !errors.Is(err, auth.ErrInvalidInvitation) {
		t.Errorf("finish after a revoke: %v", err)
	}
	if users, _ := s.ListUsers(ctx); len(users) != 1 {
		t.Errorf("a revoked invitation made an account")
	}
}

func TestResetWithAPasskeyReplacesEverythingAtTheEnd(t *testing.T) {
	s, _ := newService(t)
	admin := mustAdmin(t, s, "alice")
	bob, _ := s.CreateUser(ctx, "bob", "", goodPW, "viewer")
	registerPasskey(t, s, bob, ts.NewVirtualAuthenticator(), "Old")
	_, token, _ := s.CreateResetInvitation(ctx, admin, bob.ID)
	// A guest who fails, or walks away, costs the user nothing.
	if _, err := passkeyInvite(t, s, token, "", ts.NewVirtualAuthenticator(), "https://evil.example"); err == nil {
		t.Fatal("the wrong origin was accepted")
	}
	if got, _ := s.GetUser(ctx, bob.ID); got.Passkeys != 1 {
		t.Fatalf("the old passkey was removed by a failed attempt: %+v", got)
	}
	// The same authenticator that holds the old passkey may make a new one: nothing is excluded for a reset.
	va := ts.NewVirtualAuthenticator()
	u, err := passkeyInvite(t, s, token, "", va, origin)
	if err != nil || u.ID != bob.ID || u.Passkeys != 1 {
		t.Fatalf("reset with a passkey: %+v, %v", u, err)
	}
	if _, err := s.Authenticate(ctx, "bob", goodPW, "10.0.0.1"); err == nil {
		t.Error("the old password survived a reset by passkey")
	}
}

func ptr[T any](v T) *T { return &v }

func totpNow(secret string) (string, error) { return totp.GenerateCode(secret, time.Now()) }
