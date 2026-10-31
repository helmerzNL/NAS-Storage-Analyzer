package auth

// Invitation links (App-Guidance user-management.md): the administrator makes a link, the guest opens it and chooses their own password or
// passkey on their own device. Nobody else ever learns a credential, and the same mechanism, a "reset", brings back a user who can no longer
// sign in.
//
//  1. The link is the secret and only its SHA-256 is kept. It is shown once, when it is made; a lost link is revoked and made again.
//  2. One use, seven days. Using it deletes it; an expired one is unusable at once and is purged the next time any invitation is made.
//  3. The role is decided by the administrator who makes the link, never by the guest.
//  4. Nothing changes until the guest has finished: a reset replaces the old credentials in the same transaction that stores the new ones.
//  5. Unknown, expired, used and malformed tokens are one and the same answer (ErrInvalidInvitation), so the route cannot be used to ask which
//     tokens exist.

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
	"github.com/jackc/pgx/v5"
)

const (
	// InviteTTL is how long a link works.
	InviteTTL = 7 * 24 * time.Hour
	// MaxUsers caps the accounts, counting pending "new" invitations as users (they are future users: without that the cap is a suggestion).
	MaxUsers = 50
	// MaxInvitations caps the open invitations.
	MaxInvitations = 50
)

var (
	// ErrInvalidInvitation is every way a token can be no good: unknown, expired, used, revoked, malformed, or for a user who is gone or disabled.
	ErrInvalidInvitation = errors.New("this invitation is not valid (anymore)")
	// ErrTooMany is the cap on users or on open invitations.
	ErrTooMany = errors.New("the limit on users or open invitations is reached")
	// ErrSelfReset is a reset for the administrator who is signed in: they have the password change for that, and a reset would sign them out.
	ErrSelfReset = errors.New("you cannot make a login link for yourself: change your password instead")
	// ErrBadRole is a role other than admin or viewer.
	ErrBadRole = errors.New("role must be admin or viewer")
	// ErrUserDisabled is a reset for a disabled user: enable them first.
	ErrUserDisabled = errors.New("this user is disabled: enable them before making a login link")
)

// Invitation is what the administrator sees of an open invitation. The token is not here: it exists only in the answer that creates it.
type Invitation struct {
	ID        int64     `json:"id"`
	Kind      string    `json:"kind"` // new | reset
	UserID    *int64    `json:"user_id"`
	Username  string    `json:"username"`
	Role      string    `json:"role"`
	CreatedBy string    `json:"created_by"`
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at"`
}

// InviteInfo is what the guest sees: only what the page needs.
type InviteInfo struct {
	Kind      string    `json:"kind"`
	Username  string    `json:"username"`
	Role      string    `json:"role"`
	InvitedBy string    `json:"invited_by"`
	ExpiresAt time.Time `json:"expires_at"`
}

func tokenDigest(token string) []byte { h := sha256.Sum256([]byte(token)); return h[:] }

// rowQuerier is what a pool and a transaction have in common for a single-row query.
type rowQuerier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// invitationRow is the stored invitation as the completing steps need it.
type invitationRow struct {
	ID        int64
	Kind      string
	UserID    *int64
	Username  string
	Role      string
	ExpiresAt time.Time
}

// findInvitation looks a token up. Anything that is not a live invitation is ErrInvalidInvitation. lock takes the row for update.
func findInvitation(ctx context.Context, q rowQuerier, token string, lock bool) (*invitationRow, error) {
	if len(token) < 20 || len(token) > 200 { // a token is 43 characters: anything else is not worth a query
		return nil, ErrInvalidInvitation
	}
	sql := `SELECT id, kind, user_id, username, role, expires_at FROM invitations WHERE token_hash = $1 AND expires_at > now()`
	if lock {
		sql += ` FOR UPDATE`
	}
	var r invitationRow
	err := q.QueryRow(ctx, sql, tokenDigest(token)).Scan(&r.ID, &r.Kind, &r.UserID, &r.Username, &r.Role, &r.ExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrInvalidInvitation
	}
	return &r, err
}

// CreateInvitation makes a link for a new account. username is optional: a name to suggest to the guest, who may change it, and that is reserved
// meanwhile. The token is returned once and never stored.
func (s *Service) CreateInvitation(ctx context.Context, by *User, username, role string) (*Invitation, string, error) {
	username = strings.TrimSpace(username)
	if role != "admin" && role != "viewer" {
		return nil, "", ErrBadRole
	}
	if username != "" && !usernameRE.MatchString(username) {
		return nil, "", ErrBadUsername
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, "", err
	}
	defer tx.Rollback(context.Background())
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(7004)`); err != nil { // the caps must hold for concurrent administrators too
		return nil, "", err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM invitations WHERE expires_at <= now()`); err != nil {
		return nil, "", err
	}
	if username != "" {
		if err := nameFree(ctx, tx, username, 0); err != nil {
			return nil, "", err
		}
	}
	var users, pendingNew, open int
	if err := tx.QueryRow(ctx, `SELECT (SELECT count(*) FROM users), (SELECT count(*) FROM invitations WHERE kind = 'new'), (SELECT count(*) FROM invitations)`).
		Scan(&users, &pendingNew, &open); err != nil {
		return nil, "", err
	}
	if users+pendingNew >= MaxUsers || open >= MaxInvitations {
		return nil, "", ErrTooMany
	}
	return insertInvitation(ctx, tx, by, "new", nil, username, role)
}

// CreateResetInvitation makes a link that brings back a user who can no longer sign in. It carries no chosen name and no role: both are the
// user's own. There is one per user at a time; making a new one replaces the old. Nothing about the user changes until the guest has finished.
func (s *Service) CreateResetInvitation(ctx context.Context, by *User, userID int64) (*Invitation, string, error) {
	if by != nil && by.ID == userID {
		return nil, "", ErrSelfReset
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, "", err
	}
	defer tx.Rollback(context.Background())
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(7004)`); err != nil {
		return nil, "", err
	}
	var name, role string
	var disabled bool
	if err := tx.QueryRow(ctx, `SELECT username, role, disabled FROM users WHERE id = $1`, userID).Scan(&name, &role, &disabled); errors.Is(err, pgx.ErrNoRows) {
		return nil, "", ErrNotFound
	} else if err != nil {
		return nil, "", err
	}
	if disabled {
		return nil, "", ErrUserDisabled
	}
	if _, err := tx.Exec(ctx, `DELETE FROM invitations WHERE expires_at <= now() OR (kind = 'reset' AND user_id = $1)`, userID); err != nil {
		return nil, "", err
	}
	var open int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM invitations`).Scan(&open); err != nil {
		return nil, "", err
	}
	if open >= MaxInvitations {
		return nil, "", ErrTooMany
	}
	return insertInvitation(ctx, tx, by, "reset", &userID, name, role)
}

func insertInvitation(ctx context.Context, tx pgx.Tx, by *User, kind string, userID *int64, username, role string) (*Invitation, string, error) {
	token := randToken(32) // 32 random bytes, URL-safe
	var createdBy *int64
	var byName string
	if by != nil {
		createdBy, byName = &by.ID, by.Username
	}
	inv := Invitation{Kind: kind, UserID: userID, Username: username, Role: role, CreatedBy: byName}
	err := tx.QueryRow(ctx, `INSERT INTO invitations (token_hash, kind, user_id, username, role, created_by, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6, now() + $7::interval) RETURNING id, created_at, expires_at`,
		tokenDigest(token), kind, userID, username, role, createdBy, fmt.Sprintf("%d seconds", int(InviteTTL.Seconds()))).Scan(&inv.ID, &inv.CreatedAt, &inv.ExpiresAt)
	if isUnique(err) {
		return nil, "", ErrConflict // the name is reserved by another invitation
	}
	if err != nil {
		return nil, "", err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, "", err
	}
	return &inv, token, nil
}

// nameFree reports ErrConflict when username is taken by an account or reserved by another pending invitation (exceptInvitation is the
// invitation being completed, whose own reservation does not count; 0 for none).
func nameFree(ctx context.Context, q rowQuerier, username string, exceptInvitation int64) error {
	var taken bool
	err := q.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM users WHERE lower(username) = lower($1))
		OR EXISTS (SELECT 1 FROM invitations WHERE kind = 'new' AND lower(username) = lower($1) AND id <> $2 AND expires_at > now())`,
		username, exceptInvitation).Scan(&taken)
	if err != nil {
		return err
	}
	if taken {
		return ErrConflict
	}
	return nil
}

// ListInvitations returns the open invitations, oldest first.
func (s *Service) ListInvitations(ctx context.Context) ([]Invitation, error) {
	rows, err := s.pool.Query(ctx, `SELECT i.id, i.kind, i.user_id, i.username, i.role, coalesce(c.username, ''), i.created_at, i.expires_at
		FROM invitations i LEFT JOIN users c ON c.id = i.created_by WHERE i.expires_at > now() ORDER BY i.created_at, i.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Invitation{}
	for rows.Next() {
		var i Invitation
		if err := rows.Scan(&i.ID, &i.Kind, &i.UserID, &i.Username, &i.Role, &i.CreatedBy, &i.CreatedAt, &i.ExpiresAt); err != nil {
			return nil, err
		}
		out = append(out, i)
	}
	return out, rows.Err()
}

// RevokeInvitation deletes an invitation; the link stops working at once.
func (s *Service) RevokeInvitation(ctx context.Context, id int64) (*Invitation, error) {
	var i Invitation
	err := s.pool.QueryRow(ctx, `DELETE FROM invitations WHERE id = $1 RETURNING id, kind, user_id, username, role, created_at, expires_at`, id).
		Scan(&i.ID, &i.Kind, &i.UserID, &i.Username, &i.Role, &i.CreatedAt, &i.ExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &i, err
}

// InvitationInfo is what the guest's page needs to show the form.
func (s *Service) InvitationInfo(ctx context.Context, token string) (*InviteInfo, error) {
	row, err := findInvitation(ctx, s.pool, token, false)
	if err != nil {
		return nil, err
	}
	info := InviteInfo{Kind: row.Kind, Username: row.Username, Role: row.Role, ExpiresAt: row.ExpiresAt}
	if err := s.pool.QueryRow(ctx, `SELECT coalesce(c.username, '') FROM invitations i LEFT JOIN users c ON c.id = i.created_by WHERE i.id = $1`, row.ID).
		Scan(&info.InvitedBy); err != nil {
		return nil, err
	}
	return &info, nil
}

// resolveName is the name the account will have: the guest's choice (a new account) or the user's own (a reset, whatever was sent).
func resolveName(ctx context.Context, q rowQuerier, row *invitationRow, chosen string) (string, error) {
	if row.Kind == "reset" {
		var name string
		var disabled bool
		if err := q.QueryRow(ctx, `SELECT username, disabled FROM users WHERE id = $1`, *row.UserID).Scan(&name, &disabled); errors.Is(err, pgx.ErrNoRows) || disabled {
			return "", ErrInvalidInvitation // the user was deleted or disabled meanwhile
		} else if err != nil {
			return "", err
		}
		return name, nil
	}
	name := strings.TrimSpace(chosen)
	if name == "" {
		name = row.Username
	}
	if !usernameRE.MatchString(name) {
		return "", ErrBadUsername
	}
	if err := nameFree(ctx, q, name, row.ID); err != nil {
		return "", err
	}
	return name, nil
}

// replaceCredentials clears everything a user could sign in with (password, second factor, passkeys, recovery codes) and signs them out
// everywhere. It runs in the transaction that stores the new credential, never before (rule 4).
func replaceCredentials(ctx context.Context, tx pgx.Tx, userID int64, passwordHash *string) error {
	if _, err := tx.Exec(ctx, `UPDATE users SET password_hash = $2, totp_enabled = false, totp_secret_enc = NULL, totp_last_step = 0 WHERE id = $1`, userID, passwordHash); err != nil {
		return err
	}
	for _, q := range []string{`DELETE FROM webauthn_credentials WHERE user_id = $1`, `DELETE FROM recovery_codes WHERE user_id = $1`,
		`DELETE FROM sessions WHERE user_id = $1`, `DELETE FROM login_failures WHERE scope = 'user' AND key = (SELECT lower(username) FROM users WHERE id = $1)`} {
		if _, err := tx.Exec(ctx, q, userID); err != nil {
			return err
		}
	}
	return nil
}

// AcceptInvitationPassword completes an invitation with a password the guest chose. For a new account the guest must then enrol a second
// factor at the first sign-in, like every account; a reset also clears the old second factor.
func (s *Service) AcceptInvitationPassword(ctx context.Context, token, username, password string) (*User, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(context.Background())
	row, err := findInvitation(ctx, tx, token, true) // check again that it still exists: a second tab, a revoke, a clock
	if err != nil {
		return nil, err
	}
	name, err := resolveName(ctx, tx, row, username)
	if err != nil {
		return nil, err
	}
	if err := CheckPasswordPolicy(password, name); err != nil {
		return nil, err
	}
	hash := HashPassword(password)
	var id int64
	if row.Kind == "new" {
		err = tx.QueryRow(ctx, `INSERT INTO users (username, role, password_hash) VALUES ($1, $2, $3) RETURNING id`, name, row.Role, hash).Scan(&id)
		if isUnique(err) {
			return nil, ErrConflict // two invitations for the same name finishing at once
		}
		if err != nil {
			return nil, err
		}
	} else {
		id = *row.UserID
		if err := replaceCredentials(ctx, tx, id, &hash); err != nil {
			return nil, err
		}
	}
	if _, err := tx.Exec(ctx, `DELETE FROM invitations WHERE id = $1`, row.ID); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return s.GetUser(ctx, id)
}

// inviteCeremony is the server's half of a passkey ceremony for an invitation. It keeps the user the passkey is for: an id reserved from the
// sequence for a new account (the account itself is only made when the passkey has been verified), and the chosen name.
type inviteCeremony struct {
	SD       webauthn.SessionData `json:"sd"`
	UserID   int64                `json:"user_id"`
	Username string               `json:"username"`
}

func inviteKind(invitationID int64) string { return fmt.Sprintf("invite:%d", invitationID) }

// BeginInvitationPasskey starts the passkey ceremony for an invitation: the normal registration ceremony, for the user the invitation is for.
// A taken name is refused here, before a passkey is made.
func (s *Service) BeginInvitationPasskey(ctx context.Context, rp RelyingParty, token, username string) (*protocol.CredentialCreation, string, error) {
	row, err := findInvitation(ctx, s.pool, token, false)
	if err != nil {
		return nil, "", err
	}
	name, err := resolveName(ctx, s.pool, row, username)
	if err != nil {
		return nil, "", err
	}
	var id int64
	if row.Kind == "new" {
		if err := s.pool.QueryRow(ctx, `SELECT nextval(pg_get_serial_sequence('users', 'id'))`).Scan(&id); err != nil {
			return nil, "", err
		}
	} else {
		id = *row.UserID
	}
	// Nothing to exclude for a reset either: the old credentials are replaced when the guest has finished, and an authenticator that already
	// holds a passkey for this user must be allowed to make a new one.
	creation, sd, err := beginRegistration(rp, waUser{u: &User{ID: id, Username: name}})
	if err != nil {
		return nil, "", err
	}
	ceremony, err := s.saveCeremonyData(ctx, inviteKind(row.ID), nil, inviteCeremony{SD: *sd, UserID: id, Username: name})
	return creation, ceremony, err
}

// FinishInvitationPasskey verifies the authenticator's answer (the request body) and, in one transaction, creates the account (or replaces
// the credentials of the user), stores the passkey and consumes the invitation. A passkey verifies the user, so it is a complete sign-in.
func (s *Service) FinishInvitationPasskey(ctx context.Context, rp RelyingParty, token, ceremony, name string, r *http.Request) (*User, error) {
	row, err := findInvitation(ctx, s.pool, token, false)
	if err != nil {
		return nil, err
	}
	var ic inviteCeremony
	if err := s.takeCeremonyData(ctx, ceremony, inviteKind(row.ID), nil, &ic); err != nil {
		return nil, err
	}
	cred, err := verifyRegistration(rp, waUser{u: &User{ID: ic.UserID, Username: ic.Username}}, ic.SD, r)
	if err != nil {
		return nil, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(context.Background())
	if row, err = findInvitation(ctx, tx, token, true); err != nil { // check again: it may have been revoked while the authenticator was asked
		return nil, err
	}
	if row.Kind == "new" {
		if err := nameFree(ctx, tx, ic.Username, row.ID); err != nil {
			return nil, err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO users (id, username, role) VALUES ($1, $2, $3)`, ic.UserID, ic.Username, row.Role); err != nil {
			if isUnique(err) {
				return nil, ErrConflict
			}
			return nil, err
		}
	} else {
		if _, err := resolveName(ctx, tx, row, ""); err != nil { // the user may be gone or disabled
			return nil, err
		}
		if ic.UserID != *row.UserID {
			return nil, ErrInvalidInvitation
		}
		if err := replaceCredentials(ctx, tx, ic.UserID, nil); err != nil {
			return nil, err
		}
	}
	if err := storeCredential(ctx, tx, ic.UserID, name, cred); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM invitations WHERE id = $1`, row.ID); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return s.GetUser(ctx, ic.UserID)
}
