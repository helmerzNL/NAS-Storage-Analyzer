package auth

import (
	"context"
	"errors"
	"net/http"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
)

// Proving again who is asking (App-Guidance factory-reset.md). A session is not enough for a request that destroys everything: the request has to
// carry a fresh proof, by whichever way the person can give one, and a wrong proof counts against the same lockout as signing in, or this
// would be a second place to guess a password without limit.

// ErrReauthRequired: the proof is missing or incomplete. Nothing was wrong with what was sent, so it does not count as a failed attempt.
var ErrReauthRequired = errors.New("confirm with your password and a code, or with a passkey")

// ReauthMethods says which proofs a person can give again.
type ReauthMethods struct {
	// PasswordAndCode: the password, and with it a code from the authenticator app or a recovery code. A password alone is never enough: the second
	// factor is mandatory, so a proof without one would be weaker than signing in.
	PasswordAndCode bool `json:"password_and_code"`
	// Recovery: unused recovery codes are left, so a code can be one of those.
	Recovery bool `json:"recovery"`
	// Passkey: a passkey proves it alone (it verified the person with a PIN or biometrics), so it needs nothing else.
	Passkey bool `json:"passkey"`
}

// ReauthMethodsOf lists the ways u can prove who they are again.
func ReauthMethodsOf(u *User) ReauthMethods {
	return ReauthMethods{
		PasswordAndCode: u.passwordHash != nil && u.TOTPEnabled,
		Recovery:        u.passwordHash != nil && u.RecoveryCodes > 0,
		Passkey:         u.Passkeys > 0,
	}
}

// Reauthenticate checks the password path: the password, and the code of the authenticator app or a recovery code. A wrong password or a wrong
// code counts toward the lockout of signing in; a missing one does not.
func (s *Service) Reauthenticate(ctx context.Context, u *User, password, code, recovery, ip string) error {
	if err := s.CheckLocked(ctx, u, ip); err != nil {
		return err
	}
	m := ReauthMethodsOf(u)
	if !m.PasswordAndCode && !(m.Recovery && u.passwordHash != nil) {
		return ErrReauthRequired // this person has no password to give: they use a passkey
	}
	if password == "" {
		return ErrReauthRequired
	}
	if !s.CheckPassword(u, password) {
		s.RecordSecondFactorFailure(ctx, u, ip)
		return ErrInvalidCredentials
	}
	switch {
	case code != "":
		if err := s.VerifyTOTP(ctx, u, code); err != nil {
			s.RecordSecondFactorFailure(ctx, u, ip)
			return ErrInvalidCode
		}
	case recovery != "":
		if err := s.UseRecoveryCode(ctx, u.ID, recovery); err != nil {
			s.RecordSecondFactorFailure(ctx, u, ip)
			return ErrInvalidCode
		}
	default:
		return ErrReauthRequired
	}
	s.ClearFailures(ctx, u)
	return nil
}

// BeginPasskeyReauth starts a passkey check for a person who is signed in. The challenge is stored for this person and works once.
func (s *Service) BeginPasskeyReauth(ctx context.Context, rp RelyingParty, u *User) (*protocol.CredentialAssertion, string, error) {
	wa, err := rp.instance()
	if err != nil {
		return nil, "", err
	}
	wu, err := s.waUser(ctx, u)
	if err != nil {
		return nil, "", err
	}
	if len(wu.creds) == 0 {
		return nil, "", errors.New("no passkeys registered")
	}
	assertion, sd, err := wa.BeginLogin(wu, webauthn.WithUserVerification(protocol.VerificationRequired))
	if err != nil {
		return nil, "", err
	}
	id, err := s.saveCeremony(ctx, "reauth", &u.ID, sd)
	return assertion, id, err
}

// FinishPasskeyReauth verifies the signed challenge (the body of r) against this person's own passkeys. A challenge that was not issued to this
// person, one that has been used, and a passkey of somebody else are all refused, and a refusal counts toward the lockout.
func (s *Service) FinishPasskeyReauth(ctx context.Context, rp RelyingParty, u *User, ceremony string, r *http.Request, ip string) error {
	if err := s.CheckLocked(ctx, u, ip); err != nil {
		return err
	}
	if ceremony == "" {
		return ErrReauthRequired
	}
	fail := func() error { s.RecordSecondFactorFailure(ctx, u, ip); return ErrInvalidCode }
	sd, err := s.takeCeremony(ctx, ceremony, "reauth", &u.ID)
	if err != nil {
		return fail()
	}
	wa, err := rp.instance()
	if err != nil {
		return err
	}
	wu, err := s.waUser(ctx, u)
	if err != nil {
		return err
	}
	cred, err := wa.FinishLogin(wu, *sd, r)
	if err != nil || !cred.Flags.UserVerified {
		return fail()
	}
	if err := s.touchCredential(ctx, cred); err != nil {
		return err
	}
	s.ClearFailures(ctx, u)
	return nil
}
