package auth_test

import (
	"errors"
	"testing"

	"github.com/helmerznl/nas-storage-analyzer/internal/auth"
)

var rp = auth.RelyingParty{ID: "nas.example.test", Name: "NAS", Origins: []string{"https://nas.example.test"}}

const origin = "https://nas.example.test"

func registerPasskey(t *testing.T, s *auth.Service, u *auth.User, va *virtualAuthenticator, name string) {
	t.Helper()
	creation, id, err := s.BeginPasskeyRegistration(ctx, rp, u)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.FinishPasskeyRegistration(ctx, rp, u, id, name, va.create(creation, origin)); err != nil {
		t.Fatalf("register: %v", err)
	}
}

func TestPasskeyRegistrationAndManagement(t *testing.T) {
	s, _ := newService(t)
	u := mustAdmin(t, s, "alice")
	va := newVirtualAuthenticator()
	registerPasskey(t, s, u, va, "Laptop")
	list, err := s.ListPasskeys(ctx, u.ID)
	if err != nil || len(list) != 1 || list[0].Name != "Laptop" {
		t.Fatalf("list: %+v %v", list, err)
	}
	u, _ = s.GetUser(ctx, u.ID)
	if u.Passkeys != 1 || !u.HasSecondFactor() {
		t.Errorf("user = %+v", u)
	}
	// The same authenticator cannot be registered twice (exclusion list).
	if creation, id, err := s.BeginPasskeyRegistration(ctx, rp, u); err == nil {
		if len(creation.Response.CredentialExcludeList) != 1 {
			t.Errorf("exclude list = %v", creation.Response.CredentialExcludeList)
		}
		_ = id
	}
	if err := s.RenamePasskey(ctx, u.ID, list[0].ID, "Werklaptop"); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.ListPasskeys(ctx, u.ID); got[0].Name != "Werklaptop" {
		t.Errorf("name = %s", got[0].Name)
	}
	// The only second factor cannot be deleted.
	if err := s.DeletePasskey(ctx, u, list[0].ID); !errors.Is(err, auth.ErrLastFactor) {
		t.Errorf("last factor deleted: %v", err)
	}
	// With a second passkey the first can go.
	registerPasskey(t, s, u, newVirtualAuthenticator(), "Telefoon")
	u, _ = s.GetUser(ctx, u.ID)
	if err := s.DeletePasskey(ctx, u, list[0].ID); err != nil {
		t.Errorf("delete: %v", err)
	}
	// Another user cannot touch it.
	bob, _ := s.CreateUser(ctx, "bob", "", goodPW, "viewer")
	rest, _ := s.ListPasskeys(ctx, u.ID)
	if err := s.RenamePasskey(ctx, bob.ID, rest[0].ID, "mine"); !errors.Is(err, auth.ErrNotFound) {
		t.Errorf("renamed someone else's passkey: %v", err)
	}
}

func TestPasskeyAsSecondFactor(t *testing.T) {
	s, _ := newService(t)
	u := mustAdmin(t, s, "alice")
	va := newVirtualAuthenticator()
	registerPasskey(t, s, u, va, "Laptop")
	u, _ = s.GetUser(ctx, u.ID)

	assertion, id, err := s.BeginPasskeySecondFactor(ctx, rp, u)
	if err != nil {
		t.Fatal(err)
	}
	if len(assertion.Response.AllowedCredentials) != 1 {
		t.Errorf("allowed credentials = %v", assertion.Response.AllowedCredentials)
	}
	if err := s.FinishPasskeySecondFactor(ctx, rp, u, id, va.get(assertion, origin)); err != nil {
		t.Fatalf("second factor: %v", err)
	}
	// The ceremony is single use.
	if err := s.FinishPasskeySecondFactor(ctx, rp, u, id, va.get(assertion, origin)); err == nil {
		t.Error("ceremony reused")
	}
	// A passkey of another user does not satisfy this user's check.
	bob, _ := s.CreateUser(ctx, "bob", "", goodPW, "viewer")
	registerPasskey(t, s, bob, newVirtualAuthenticator(), "Bob")
	a2, id2, _ := s.BeginPasskeySecondFactor(ctx, rp, u)
	bobVA := newVirtualAuthenticator()
	if err := s.FinishPasskeySecondFactor(ctx, rp, u, id2, bobVA.get(a2, origin)); err == nil {
		t.Error("unknown authenticator accepted")
	}
	// A ceremony started for one user cannot be finished as another.
	bob, _ = s.GetUser(ctx, bob.ID)
	a3, id3, _ := s.BeginPasskeySecondFactor(ctx, rp, u)
	if err := s.FinishPasskeySecondFactor(ctx, rp, bob, id3, va.get(a3, origin)); err == nil {
		t.Error("ceremony finished as another user")
	}
	// A user without passkeys gets a clear error.
	if _, _, err := s.BeginPasskeySecondFactor(ctx, rp, mustAdmin(t, s, "carol")); err == nil {
		t.Error("second factor started without passkeys")
	}
}

func TestPasswordlessPasskeyLogin(t *testing.T) {
	s, _ := newService(t)
	u := mustAdmin(t, s, "alice")
	va := newVirtualAuthenticator()
	registerPasskey(t, s, u, va, "Laptop")

	assertion, id, err := s.BeginPasskeyLogin(ctx, rp)
	if err != nil {
		t.Fatal(err)
	}
	if len(assertion.Response.AllowedCredentials) != 0 {
		t.Error("a discoverable login must not list credentials (it would reveal usernames)")
	}
	got, err := s.FinishPasskeyLogin(ctx, rp, id, va.get(assertion, origin))
	if err != nil || got.Username != "alice" {
		t.Fatalf("passkey login: %v %v", got, err)
	}
	// Replay of the same ceremony fails.
	if _, err := s.FinishPasskeyLogin(ctx, rp, id, va.get(assertion, origin)); err == nil {
		t.Error("ceremony replayed")
	}
	// A disabled account cannot sign in this way either.
	dis := true
	mustAdmin(t, s, "bob")
	if _, err := s.UpdateUser(ctx, u.ID, auth.UserPatch{Disabled: &dis}); err != nil {
		t.Fatal(err)
	}
	a2, id2, _ := s.BeginPasskeyLogin(ctx, rp)
	if _, err := s.FinishPasskeyLogin(ctx, rp, id2, va.get(a2, origin)); err == nil {
		t.Error("disabled user signed in with a passkey")
	}
}

func TestPasskeyRejectsWrongOriginAndMissingUserVerification(t *testing.T) {
	s, _ := newService(t)
	u := mustAdmin(t, s, "alice")

	// Registration from a phishing origin.
	creation, id, _ := s.BeginPasskeyRegistration(ctx, rp, u)
	if err := s.FinishPasskeyRegistration(ctx, rp, u, id, "x", newVirtualAuthenticator().create(creation, "https://evil.example")); err == nil {
		t.Error("registration accepted from another origin")
	}
	// An authenticator that does not verify the user (no PIN / biometrics).
	noUV := newVirtualAuthenticator()
	noUV.uv = false
	creation, id, _ = s.BeginPasskeyRegistration(ctx, rp, u)
	if err := s.FinishPasskeyRegistration(ctx, rp, u, id, "x", noUV.create(creation, origin)); err == nil {
		t.Error("registered a passkey without user verification")
	}
	// Login from the wrong origin.
	va := newVirtualAuthenticator()
	registerPasskey(t, s, u, va, "Laptop")
	assertion, lid, _ := s.BeginPasskeyLogin(ctx, rp)
	if _, err := s.FinishPasskeyLogin(ctx, rp, lid, va.get(assertion, "https://evil.example")); err == nil {
		t.Error("login accepted from another origin")
	}
	// Login with a signature over a different challenge.
	a1, lid1, _ := s.BeginPasskeyLogin(ctx, rp)
	a2, _, _ := s.BeginPasskeyLogin(ctx, rp)
	if _, err := s.FinishPasskeyLogin(ctx, rp, lid1, va.get(a2, origin)); err == nil {
		t.Error("login accepted with a response to another challenge")
	}
	_ = a1
	// A passkey login that does not verify the user is refused: that is what
	// makes one passkey as strong as password plus second factor.
	va.uv = false
	a3, lid3, _ := s.BeginPasskeyLogin(ctx, rp)
	if _, err := s.FinishPasskeyLogin(ctx, rp, lid3, va.get(a3, origin)); err == nil {
		t.Error("passwordless login accepted without user verification")
	}
}
