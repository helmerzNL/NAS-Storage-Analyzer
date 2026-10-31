package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/pquerna/otp"
	"github.com/pquerna/otp/totp"
)

var totpOpts = totp.ValidateOpts{Period: 30, Skew: 0, Digits: otp.DigitsSix, Algorithm: otp.AlgorithmSHA1}

// BeginTOTP generates a new authenticator secret for the user and stores it
// (encrypted) as not yet confirmed. The user proves possession by calling
// ConfirmTOTP with a code from the app. Replaces an earlier unconfirmed secret.
func (s *Service) BeginTOTP(ctx context.Context, u *User) (secret, otpauthURL string, err error) {
	if u.TOTPEnabled {
		return "", "", fmt.Errorf("an authenticator app is already set up: remove it first")
	}
	key, err := totp.Generate(totp.GenerateOpts{Issuer: s.cfg.Issuer, AccountName: u.Username, Period: 30, Digits: otp.DigitsSix,
		Algorithm: otp.AlgorithmSHA1})
	if err != nil {
		return "", "", err
	}
	_, err = s.pool.Exec(ctx, `UPDATE users SET totp_secret_enc = $2, totp_enabled = false, totp_last_step = 0 WHERE id = $1`,
		u.ID, s.box.Seal([]byte(key.Secret())))
	return key.Secret(), key.URL(), err
}

func (s *Service) totpSecret(ctx context.Context, userID int64) (string, error) {
	var enc []byte
	if err := s.pool.QueryRow(ctx, `SELECT totp_secret_enc FROM users WHERE id = $1`, userID).Scan(&enc); err != nil {
		return "", err
	}
	if len(enc) == 0 {
		return "", ErrInvalidCode
	}
	pt, err := s.box.Open(enc)
	return string(pt), err
}

// matchStep returns the 30-second step a code belongs to (current step or the
// one before/after, for clock drift), or 0.
func matchStep(secret, code string, now time.Time) int64 {
	code = strings.ReplaceAll(strings.TrimSpace(code), " ", "")
	if len(code) != 6 {
		return 0
	}
	for _, d := range []int64{0, -1, 1} {
		t := now.Add(time.Duration(d) * 30 * time.Second)
		want, err := totp.GenerateCodeCustom(secret, t, totpOpts)
		if err == nil && want == code {
			return t.Unix() / 30
		}
	}
	return 0
}

// ConfirmTOTP enables TOTP when the code matches the pending secret.
func (s *Service) ConfirmTOTP(ctx context.Context, u *User, code string) error {
	secret, err := s.totpSecret(ctx, u.ID)
	if err != nil {
		return err
	}
	step := matchStep(secret, code, time.Now())
	if step == 0 {
		return ErrInvalidCode
	}
	_, err = s.pool.Exec(ctx, `UPDATE users SET totp_enabled = true, totp_last_step = $2 WHERE id = $1`, u.ID, step)
	return err
}

// VerifyTOTP checks a code at sign-in. A code works once: the step is stored
// atomically, so a captured code cannot be replayed within its validity.
func (s *Service) VerifyTOTP(ctx context.Context, u *User, code string) error {
	if !u.TOTPEnabled {
		return ErrInvalidCode
	}
	secret, err := s.totpSecret(ctx, u.ID)
	if err != nil {
		return err
	}
	step := matchStep(secret, code, time.Now())
	if step == 0 {
		return ErrInvalidCode
	}
	tag, err := s.pool.Exec(ctx, `UPDATE users SET totp_last_step = $2 WHERE id = $1 AND totp_last_step < $2`, u.ID, step)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrInvalidCode
	}
	return nil
}

// DisableTOTP removes the authenticator app, provided another factor remains.
func (s *Service) DisableTOTP(ctx context.Context, u *User) error {
	if u.Passkeys == 0 {
		return ErrLastFactor
	}
	_, err := s.pool.Exec(ctx, `UPDATE users SET totp_enabled = false, totp_secret_enc = NULL, totp_last_step = 0 WHERE id = $1`, u.ID)
	return err
}

// ---- recovery codes ----------------------------------------------------------

const recoveryCount = 10

var b32 = base32.StdEncoding.WithPadding(base32.NoPadding)

func normalizeRecovery(code string) string {
	return strings.ToUpper(strings.NewReplacer("-", "", " ", "").Replace(strings.TrimSpace(code)))
}

func hashRecovery(code string) []byte {
	h := sha256.Sum256([]byte(normalizeRecovery(code)))
	return h[:]
}

// NewRecoveryCodes replaces the user's recovery codes with ten fresh ones and
// returns them. They are shown once; only hashes are stored. Each code has 80
// bits of entropy, so an unsalted hash is sufficient.
func (s *Service) NewRecoveryCodes(ctx context.Context, userID int64) ([]string, error) {
	codes := make([]string, recoveryCount)
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(context.Background())
	if _, err := tx.Exec(ctx, `DELETE FROM recovery_codes WHERE user_id = $1`, userID); err != nil {
		return nil, err
	}
	for i := range codes {
		raw := make([]byte, 10)
		if _, err := rand.Read(raw); err != nil {
			return nil, err
		}
		c := b32.EncodeToString(raw) // 16 characters
		codes[i] = c[0:4] + "-" + c[4:8] + "-" + c[8:12] + "-" + c[12:16]
		if _, err := tx.Exec(ctx, `INSERT INTO recovery_codes (user_id, code_hash) VALUES ($1, $2)`, userID, hashRecovery(codes[i])); err != nil {
			return nil, err
		}
	}
	return codes, tx.Commit(ctx)
}

// UseRecoveryCode consumes a recovery code.
func (s *Service) UseRecoveryCode(ctx context.Context, userID int64, code string) error {
	if len(normalizeRecovery(code)) != 16 {
		return ErrInvalidCode
	}
	tag, err := s.pool.Exec(ctx, `UPDATE recovery_codes SET used_at = now() WHERE user_id = $1 AND code_hash = $2 AND used_at IS NULL`,
		userID, hashRecovery(code))
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrInvalidCode
	}
	return nil
}

// ---- password changes and administration ------------------------------------------

// ChangePassword sets a new password after verifying the current one, and
// signs the user out of all other sessions.
func (s *Service) ChangePassword(ctx context.Context, u *User, current, next string, keep *Session) error {
	if u.passwordHash == nil || !VerifyPassword(current, *u.passwordHash) {
		return ErrInvalidCredentials
	}
	return s.setPassword(ctx, u, next, keep)
}

// SetPassword is the administrator's reset; it does not need the old password.
func (s *Service) SetPassword(ctx context.Context, userID int64, next string) error {
	u, err := s.GetUser(ctx, userID)
	if err != nil {
		return err
	}
	return s.setPassword(ctx, u, next, nil)
}

func (s *Service) setPassword(ctx context.Context, u *User, next string, keep *Session) error {
	if err := CheckPasswordPolicy(next, u.Username); err != nil {
		return err
	}
	if _, err := s.pool.Exec(ctx, `UPDATE users SET password_hash = $2 WHERE id = $1`, u.ID, HashPassword(next)); err != nil {
		return err
	}
	return s.DeleteUserSessions(ctx, u.ID, keep)
}

// UserPatch lists the fields an administrator can change; nil means unchanged.
type UserPatch struct {
	DisplayName *string
	Role        *string
	Disabled    *bool
}

func (s *Service) activeAdmins(ctx context.Context, tx pgx.Tx, excluding int64) (int, error) {
	var n int
	err := tx.QueryRow(ctx, `SELECT count(*) FROM users WHERE role = 'admin' AND NOT disabled AND id <> $1`, excluding).Scan(&n)
	return n, err
}

// UpdateUser changes a user's role, display name or disabled flag. The last
// active administrator can be neither demoted nor disabled.
func (s *Service) UpdateUser(ctx context.Context, id int64, p UserPatch) (*User, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(context.Background())
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(7002)`); err != nil {
		return nil, err
	}
	var role string
	var disabled bool
	if err := tx.QueryRow(ctx, `SELECT role, disabled FROM users WHERE id = $1 FOR UPDATE`, id).Scan(&role, &disabled); errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	} else if err != nil {
		return nil, err
	}
	newRole, newDisabled := role, disabled
	if p.Role != nil {
		if *p.Role != "admin" && *p.Role != "viewer" {
			return nil, fmt.Errorf("role must be admin or viewer")
		}
		newRole = *p.Role
	}
	if p.Disabled != nil {
		newDisabled = *p.Disabled
	}
	if role == "admin" && !disabled && (newRole != "admin" || newDisabled) {
		if n, err := s.activeAdmins(ctx, tx, id); err != nil {
			return nil, err
		} else if n == 0 {
			return nil, ErrLastAdmin
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE users SET role = $2, disabled = $3, display_name = coalesce($4, display_name) WHERE id = $1`,
		id, newRole, newDisabled, p.DisplayName); err != nil {
		return nil, err
	}
	if newDisabled || newRole != role { // privileges changed: drop sessions so they are re-evaluated
		if _, err := tx.Exec(ctx, `DELETE FROM sessions WHERE user_id = $1`, id); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return s.GetUser(ctx, id)
}

// DeleteUser removes an account, except the last active administrator.
func (s *Service) DeleteUser(ctx context.Context, id int64) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(context.Background())
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(7002)`); err != nil {
		return err
	}
	var role string
	var disabled bool
	if err := tx.QueryRow(ctx, `SELECT role, disabled FROM users WHERE id = $1 FOR UPDATE`, id).Scan(&role, &disabled); errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	} else if err != nil {
		return err
	}
	if role == "admin" && !disabled {
		if n, err := s.activeAdmins(ctx, tx, id); err != nil {
			return err
		} else if n == 0 {
			return ErrLastAdmin
		}
	}
	if _, err := tx.Exec(ctx, `DELETE FROM users WHERE id = $1`, id); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// ResetSecondFactors removes TOTP, passkeys and recovery codes of a user and
// signs them out; at the next sign-in they must enrol again. This is the
// administrator's remedy for a lost phone and lost recovery codes.
func (s *Service) ResetSecondFactors(ctx context.Context, id int64) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(context.Background())
	tag, err := tx.Exec(ctx, `UPDATE users SET totp_enabled = false, totp_secret_enc = NULL, totp_last_step = 0 WHERE id = $1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	for _, q := range []string{`DELETE FROM webauthn_credentials WHERE user_id = $1`, `DELETE FROM recovery_codes WHERE user_id = $1`,
		`DELETE FROM sessions WHERE user_id = $1`, `DELETE FROM login_failures WHERE scope = 'user' AND key = (SELECT lower(username) FROM users WHERE id = $1)`} {
		if _, err := tx.Exec(ctx, q, id); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// CheckPassword verifies a user's current password (for sensitive changes).
func (s *Service) CheckPassword(u *User, password string) bool {
	return u.passwordHash != nil && VerifyPassword(password, *u.passwordHash)
}
