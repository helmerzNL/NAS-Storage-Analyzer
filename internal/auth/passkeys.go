package auth

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// webAuthn is reserved for per-service state; the relying party is passed per
// call so it can follow the configured RP ID / origins or, in a development
// setup without configuration, the address the browser used.
type webAuthn struct{}

// RelyingParty identifies the site to authenticators. Passkeys are bound to
// ID (a registrable domain such as "nas.example.com"): changing it later
// invalidates every registered passkey.
type RelyingParty struct {
	ID      string
	Name    string
	Origins []string
}

func (rp RelyingParty) instance() (*webauthn.WebAuthn, error) {
	return webauthn.New(&webauthn.Config{RPDisplayName: rp.Name, RPID: rp.ID, RPOrigins: rp.Origins})
}

// waUser adapts a User and its credentials to the library.
type waUser struct {
	u     *User
	creds []webauthn.Credential
}

func userHandle(id int64) []byte {
	b := make([]byte, 8)
	binary.BigEndian.PutUint64(b, uint64(id))
	return b
}

func (w waUser) WebAuthnID() []byte                         { return userHandle(w.u.ID) }
func (w waUser) WebAuthnName() string                       { return w.u.Username }
func (w waUser) WebAuthnDisplayName() string                { return firstNonEmpty(w.u.DisplayName, w.u.Username) }
func (w waUser) WebAuthnCredentials() []webauthn.Credential { return w.creds }

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func (s *Service) loadCreds(ctx context.Context, userID int64) ([]webauthn.Credential, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT credential_id, public_key, attestation_type, aaguid, sign_count, transports, backup_eligible, backup_state, user_verified
		FROM webauthn_credentials WHERE user_id = $1 ORDER BY created_at`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []webauthn.Credential
	for rows.Next() {
		var c webauthn.Credential
		var signCount int64
		var transports []string
		var uv bool
		if err := rows.Scan(&c.ID, &c.PublicKey, &c.AttestationType, &c.Authenticator.AAGUID, &signCount, &transports,
			&c.Flags.BackupEligible, &c.Flags.BackupState, &uv); err != nil {
			return nil, err
		}
		c.Authenticator.SignCount = uint32(signCount)
		c.Flags.UserPresent, c.Flags.UserVerified = true, uv
		for _, t := range transports {
			c.Transport = append(c.Transport, protocol.AuthenticatorTransport(t))
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Service) waUser(ctx context.Context, u *User) (waUser, error) {
	creds, err := s.loadCreds(ctx, u.ID)
	return waUser{u: u, creds: creds}, err
}

// Ceremonies keep the server's half of a WebAuthn exchange between the "begin"
// and "finish" request. They are single use and short lived.
func (s *Service) saveCeremony(ctx context.Context, kind string, userID *int64, sd *webauthn.SessionData) (string, error) {
	return s.saveCeremonyData(ctx, kind, userID, sd)
}

// saveCeremonyData stores any JSON payload as a ceremony: an invitation keeps the chosen name with it.
func (s *Service) saveCeremonyData(ctx context.Context, kind string, userID *int64, v any) (string, error) {
	id := randToken(18)
	data, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	_, err = s.pool.Exec(ctx, `INSERT INTO webauthn_ceremonies (id, user_id, kind, data, expires_at)
		VALUES ($1, $2, $3, $4, now() + interval '5 minutes')`, id, userID, kind, data)
	return id, err
}

func (s *Service) takeCeremony(ctx context.Context, id, kind string, wantUser *int64) (*webauthn.SessionData, error) {
	var sd webauthn.SessionData
	if err := s.takeCeremonyData(ctx, id, kind, wantUser, &sd); err != nil {
		return nil, err
	}
	return &sd, nil
}

func (s *Service) takeCeremonyData(ctx context.Context, id, kind string, wantUser *int64, into any) error {
	var data []byte
	var uid *int64
	err := s.pool.QueryRow(ctx, `DELETE FROM webauthn_ceremonies WHERE id = $1 AND kind = $2 AND expires_at > now() RETURNING data, user_id`,
		id, kind).Scan(&data, &uid)
	if errors.Is(err, pgx.ErrNoRows) {
		return errors.New("the passkey request expired, start again")
	}
	if err != nil {
		return err
	}
	if wantUser != nil && (uid == nil || *uid != *wantUser) {
		return errors.New("the passkey request belongs to another user")
	}
	return json.Unmarshal(data, into)
}

// beginRegistration is the ceremony for adding a passkey to wu. Passkeys must be discoverable and verify the user (PIN or biometrics),
// so that one passkey can replace password and second factor. An invitation uses it too: one code path, one set of rules.
func beginRegistration(rp RelyingParty, wu waUser) (*protocol.CredentialCreation, *webauthn.SessionData, error) {
	wa, err := rp.instance()
	if err != nil {
		return nil, nil, err
	}
	var exclude []protocol.CredentialDescriptor
	for _, c := range wu.creds {
		exclude = append(exclude, c.Descriptor())
	}
	req := true
	return wa.BeginRegistration(wu,
		webauthn.WithAuthenticatorSelection(protocol.AuthenticatorSelection{
			RequireResidentKey: &req, ResidentKey: protocol.ResidentKeyRequirementRequired,
			UserVerification: protocol.VerificationRequired,
		}),
		webauthn.WithExclusions(exclude),
		webauthn.WithConveyancePreference(protocol.PreferNoAttestation))
}

// verifyRegistration checks the authenticator's answer (the request body) against the ceremony and returns the new credential.
func verifyRegistration(rp RelyingParty, wu waUser, sd webauthn.SessionData, r *http.Request) (*webauthn.Credential, error) {
	wa, err := rp.instance()
	if err != nil {
		return nil, err
	}
	cred, err := wa.FinishRegistration(wu, sd, r)
	if err != nil {
		return nil, fmt.Errorf("passkey rejected: %w", err)
	}
	if !cred.Flags.UserVerified {
		return nil, errors.New("the passkey must verify you with a PIN or biometrics")
	}
	return cred, nil
}

func passkeyName(name string) string {
	if name == "" {
		name = "Passkey"
	}
	if len(name) > 60 {
		name = name[:60]
	}
	return name
}

// execer is what a pool and a transaction have in common.
type execer interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

func storeCredential(ctx context.Context, db execer, userID int64, name string, cred *webauthn.Credential) error {
	transports := make([]string, len(cred.Transport))
	for i, t := range cred.Transport {
		transports[i] = string(t)
	}
	_, err := db.Exec(ctx, `
		INSERT INTO webauthn_credentials (credential_id, user_id, name, public_key, attestation_type, aaguid, sign_count, transports,
			backup_eligible, backup_state, user_verified)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`,
		cred.ID, userID, passkeyName(name), cred.PublicKey, cred.AttestationType, cred.Authenticator.AAGUID, int64(cred.Authenticator.SignCount),
		transports, cred.Flags.BackupEligible, cred.Flags.BackupState, cred.Flags.UserVerified)
	return err
}

// BeginPasskeyRegistration starts adding a passkey to a signed-in user.
func (s *Service) BeginPasskeyRegistration(ctx context.Context, rp RelyingParty, u *User) (*protocol.CredentialCreation, string, error) {
	wu, err := s.waUser(ctx, u)
	if err != nil {
		return nil, "", err
	}
	creation, sd, err := beginRegistration(rp, wu)
	if err != nil {
		return nil, "", err
	}
	id, err := s.saveCeremony(ctx, "register", &u.ID, sd)
	return creation, id, err
}

// FinishPasskeyRegistration verifies the authenticator's answer (the request
// body) and stores the new passkey under name.
func (s *Service) FinishPasskeyRegistration(ctx context.Context, rp RelyingParty, u *User, ceremony, name string, r *http.Request) error {
	sd, err := s.takeCeremony(ctx, ceremony, "register", &u.ID)
	if err != nil {
		return err
	}
	wu, err := s.waUser(ctx, u)
	if err != nil {
		return err
	}
	cred, err := verifyRegistration(rp, wu, *sd, r)
	if err != nil {
		return err
	}
	return storeCredential(ctx, s.pool, u.ID, name, cred)
}

func (s *Service) touchCredential(ctx context.Context, c *webauthn.Credential) error {
	if c.Authenticator.CloneWarning {
		return errors.New("this passkey may have been cloned and was refused")
	}
	_, err := s.pool.Exec(ctx, `UPDATE webauthn_credentials SET sign_count = $2, backup_state = $3, last_used_at = now() WHERE credential_id = $1`,
		c.ID, int64(c.Authenticator.SignCount), c.Flags.BackupState)
	return err
}

// BeginPasskeySecondFactor starts a passkey check for a user who already gave
// the right password.
func (s *Service) BeginPasskeySecondFactor(ctx context.Context, rp RelyingParty, u *User) (*protocol.CredentialAssertion, string, error) {
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
	id, err := s.saveCeremony(ctx, "second-factor", &u.ID, sd)
	return assertion, id, err
}

// FinishPasskeySecondFactor verifies the answer (request body).
func (s *Service) FinishPasskeySecondFactor(ctx context.Context, rp RelyingParty, u *User, ceremony string, r *http.Request) error {
	sd, err := s.takeCeremony(ctx, ceremony, "second-factor", &u.ID)
	if err != nil {
		return err
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
	if err != nil {
		return ErrInvalidCode
	}
	return s.touchCredential(ctx, cred)
}

// BeginPasskeyLogin starts a sign-in without username and password.
func (s *Service) BeginPasskeyLogin(ctx context.Context, rp RelyingParty) (*protocol.CredentialAssertion, string, error) {
	wa, err := rp.instance()
	if err != nil {
		return nil, "", err
	}
	assertion, sd, err := wa.BeginDiscoverableLogin(webauthn.WithUserVerification(protocol.VerificationRequired))
	if err != nil {
		return nil, "", err
	}
	id, err := s.saveCeremony(ctx, "passkey-login", nil, sd)
	return assertion, id, err
}

// FinishPasskeyLogin verifies a passwordless sign-in and returns the user. The
// passkey must have verified the user (PIN/biometrics): that is what makes a
// single passkey as strong as password plus second factor.
func (s *Service) FinishPasskeyLogin(ctx context.Context, rp RelyingParty, ceremony string, r *http.Request) (*User, error) {
	sd, err := s.takeCeremony(ctx, ceremony, "passkey-login", nil)
	if err != nil {
		return nil, err
	}
	wa, err := rp.instance()
	if err != nil {
		return nil, err
	}
	var found *User
	cred, err := wa.FinishDiscoverableLogin(func(rawID, handle []byte) (webauthn.User, error) {
		if len(handle) != 8 {
			return nil, errors.New("unknown passkey")
		}
		u, err := s.GetUser(ctx, int64(binary.BigEndian.Uint64(handle)))
		if err != nil {
			return nil, errors.New("unknown passkey")
		}
		found = u
		return s.waUser(ctx, u)
	}, *sd, r)
	if err != nil || found == nil {
		return nil, ErrInvalidCode
	}
	if found.Disabled {
		return nil, ErrInvalidCode
	}
	if !cred.Flags.UserVerified {
		return nil, errors.New("this passkey did not verify you with a PIN or biometrics")
	}
	if err := s.touchCredential(ctx, cred); err != nil {
		return nil, err
	}
	return found, nil
}

// Passkey is a registered passkey as shown to its owner.
type Passkey struct {
	ID         string     `json:"id"` // base64url credential id
	Name       string     `json:"name"`
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at"`
	Synced     bool       `json:"synced"` // backup eligible: lives in a password manager / cloud keychain
}

// ListPasskeys returns the user's passkeys.
func (s *Service) ListPasskeys(ctx context.Context, userID int64) ([]Passkey, error) {
	rows, err := s.pool.Query(ctx, `SELECT credential_id, name, created_at, last_used_at, backup_eligible FROM webauthn_credentials
		WHERE user_id = $1 ORDER BY created_at`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Passkey{}
	for rows.Next() {
		var p Passkey
		var id []byte
		if err := rows.Scan(&id, &p.Name, &p.CreatedAt, &p.LastUsedAt, &p.Synced); err != nil {
			return nil, err
		}
		p.ID = randEncode(id)
		out = append(out, p)
	}
	return out, rows.Err()
}

// RenamePasskey changes a passkey's label.
func (s *Service) RenamePasskey(ctx context.Context, userID int64, id, name string) error {
	raw, err := randDecode(id)
	if err != nil || name == "" || len(name) > 60 {
		return errors.New("invalid passkey id or name")
	}
	tag, err := s.pool.Exec(ctx, `UPDATE webauthn_credentials SET name = $3 WHERE credential_id = $1 AND user_id = $2`, raw, userID, name)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// DeletePasskey removes a passkey unless it is the user's only second factor.
func (s *Service) DeletePasskey(ctx context.Context, u *User, id string) error {
	raw, err := randDecode(id)
	if err != nil {
		return ErrNotFound
	}
	if u.Passkeys <= 1 && !u.TOTPEnabled {
		return ErrLastFactor
	}
	tag, err := s.pool.Exec(ctx, `DELETE FROM webauthn_credentials WHERE credential_id = $1 AND user_id = $2`, raw, u.ID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
