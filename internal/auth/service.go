package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/base64"
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/helmerznl/nas-storage-analyzer/internal/secrets"
)

// Config tunes sessions and lockout.
type Config struct {
	Issuer      string        // shown in authenticator apps
	IdleTTL     time.Duration // a session expires after this much inactivity
	AbsoluteTTL time.Duration // ... and at the latest after this long
	PendingTTL  time.Duration // time to finish the second step of a login
	// Failed password attempts within FailWindow before lockout starts, per
	// username and per client address. The lockout doubles with every further
	// failure (1, 2, 4 ... up to 30 minutes).
	UserFailLimit int
	IPFailLimit   int
	FailWindow    time.Duration
}

// DefaultConfig returns production defaults.
func DefaultConfig() Config {
	return Config{
		Issuer: "NAS Storage Analyzer", IdleTTL: 12 * time.Hour, AbsoluteTTL: 7 * 24 * time.Hour, PendingTTL: 5 * time.Minute,
		UserFailLimit: 5, IPFailLimit: 30, FailWindow: time.Hour,
	}
}

// Service holds everything about accounts and sessions.
type Service struct {
	pool *pgxpool.Pool
	box  *secrets.Box
	cfg  Config
	wa   *webAuthn
}

// NewService builds the service on a read-write pool.
func NewService(pool *pgxpool.Pool, box *secrets.Box, cfg Config) *Service {
	return &Service{pool: pool, box: box, cfg: cfg}
}

// Errors returned to callers; handlers map them to HTTP statuses.
var (
	ErrInvalidCredentials = errors.New("invalid username or password")
	ErrInvalidCode        = errors.New("invalid code")
	ErrNotFound           = errors.New("not found")
	ErrConflict           = errors.New("already exists")
	ErrLastAdmin          = errors.New("this would leave no active administrator")
	ErrLastFactor         = errors.New("a second factor is required: add another one first")
	ErrSetupDone          = errors.New("setup has already been completed")
	ErrBadUsername        = errors.New("username must be 3-64 characters: letters, digits and . _ @ -")
)

// LockedError means too many failed attempts; Until says when to retry.
type LockedError struct{ Until time.Time }

func (e *LockedError) Error() string { return "too many failed attempts, try again later" }

var usernameRE = regexp.MustCompile(`^[A-Za-z0-9._@-]{3,64}$`)

// User is an account as exposed by the API (never contains secrets).
type User struct {
	ID            int64      `json:"id"`
	Username      string     `json:"username"`
	DisplayName   string     `json:"display_name"`
	Role          string     `json:"role"`
	AuthSource    string     `json:"auth_source"`
	TOTPEnabled   bool       `json:"totp_enabled"`
	Passkeys      int        `json:"passkeys"`
	RecoveryCodes int        `json:"recovery_codes_left"`
	Disabled      bool       `json:"disabled"`
	CreatedAt     time.Time  `json:"created_at"`
	LastLoginAt   *time.Time `json:"last_login_at"`
	// Prefs are the user's interface settings: units (binary|decimal),
	// language (nl|en), theme (system|light|dark), accent (#rrggbb, empty: the default).
	Prefs map[string]string `json:"prefs"`

	passwordHash *string
}

const userSelect = `
	SELECT u.id, u.username, u.display_name, u.role, u.auth_source, u.password_hash, u.totp_enabled, u.disabled,
	       u.created_at, u.last_login_at, u.prefs,
	       (SELECT count(*) FROM webauthn_credentials w WHERE w.user_id = u.id),
	       (SELECT count(*) FROM recovery_codes r WHERE r.user_id = u.id AND r.used_at IS NULL)
	FROM users u`

func scanUser(row pgx.Row) (*User, error) {
	var u User
	if err := row.Scan(&u.ID, &u.Username, &u.DisplayName, &u.Role, &u.AuthSource, &u.passwordHash, &u.TOTPEnabled,
		&u.Disabled, &u.CreatedAt, &u.LastLoginAt, &u.Prefs, &u.Passkeys, &u.RecoveryCodes); err != nil {
		return nil, err
	}
	if u.Prefs == nil {
		u.Prefs = map[string]string{}
	}
	return &u, nil
}

// HasSecondFactor reports whether the user can complete a second step.
func (u *User) HasSecondFactor() bool { return u.TOTPEnabled || u.Passkeys > 0 }

// GetUser loads a user by id.
func (s *Service) GetUser(ctx context.Context, id int64) (*User, error) {
	u, err := scanUser(s.pool.QueryRow(ctx, userSelect+` WHERE u.id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return u, err
}

func (s *Service) getUserByName(ctx context.Context, name string) (*User, error) {
	u, err := scanUser(s.pool.QueryRow(ctx, userSelect+` WHERE lower(u.username) = lower($1)`, name))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return u, err
}

// ListUsers returns all users by name.
func (s *Service) ListUsers(ctx context.Context) ([]User, error) {
	rows, err := s.pool.Query(ctx, userSelect+` ORDER BY lower(u.username)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []User{}
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *u)
	}
	return out, rows.Err()
}

// UserCount returns the number of accounts; zero means setup is required.
func (s *Service) UserCount(ctx context.Context) (int, error) {
	var n int
	err := s.pool.QueryRow(ctx, `SELECT count(*) FROM users`).Scan(&n)
	return n, err
}

func isUnique(err error) bool {
	var pg *pgconn.PgError
	return errors.As(err, &pg) && pg.Code == "23505"
}

// CreateFirstAdmin creates the initial administrator. It succeeds only while
// no account exists, even when called concurrently.
func (s *Service) CreateFirstAdmin(ctx context.Context, username, password string) (*User, error) {
	if !usernameRE.MatchString(username) {
		return nil, ErrBadUsername
	}
	if err := CheckPasswordPolicy(password, username); err != nil {
		return nil, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(context.Background())
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(7001)`); err != nil {
		return nil, err
	}
	var n int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM users`).Scan(&n); err != nil {
		return nil, err
	}
	if n > 0 {
		return nil, ErrSetupDone
	}
	var id int64
	if err := tx.QueryRow(ctx, `INSERT INTO users (username, role, password_hash) VALUES ($1, 'admin', $2) RETURNING id`,
		username, HashPassword(password)).Scan(&id); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return s.GetUser(ctx, id)
}

// ---- password sign-in and lockout ------------------------------------------

func normKey(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

// lockedUntil returns when a lockout for (scope, key) ends, or the zero time.
func (s *Service) lockedUntil(ctx context.Context, scope, key string, limit int) (time.Time, error) {
	var n int
	var last *time.Time
	var now time.Time
	err := s.pool.QueryRow(ctx, `
		SELECT count(*), max(at), now() FROM login_failures
		WHERE scope = $1 AND key = $2 AND at > now() - make_interval(secs => $3)`,
		scope, key, s.cfg.FailWindow.Seconds()).Scan(&n, &last, &now)
	if err != nil || n < limit || last == nil {
		return time.Time{}, err
	}
	back := time.Minute << min(n-limit, 5)
	if back > 30*time.Minute {
		back = 30 * time.Minute
	}
	if until := last.Add(back); until.After(now) {
		return until, nil
	}
	return time.Time{}, nil
}

func (s *Service) recordFailure(ctx context.Context, scope, key string) {
	_, _ = s.pool.Exec(ctx, `INSERT INTO login_failures (scope, key) VALUES ($1, $2)`, scope, key)
}

// Authenticate checks a username and password. It never reveals whether the
// username exists: unknown users cost the same hashing time and give the same
// error. Attempts during a lockout are refused without checking the password.
func (s *Service) Authenticate(ctx context.Context, username, password, ip string) (*User, error) {
	uk, ik := normKey(username), ip
	for _, c := range []struct {
		scope, key string
		limit      int
	}{{"user", uk, s.cfg.UserFailLimit}, {"ip", ik, s.cfg.IPFailLimit}} {
		until, err := s.lockedUntil(ctx, c.scope, c.key, c.limit)
		if err != nil {
			return nil, err
		}
		if !until.IsZero() {
			return nil, &LockedError{Until: until}
		}
	}
	u, err := s.getUserByName(ctx, username)
	ok := false
	switch {
	case errors.Is(err, ErrNotFound):
		VerifyPassword(password, dummyHash)
	case err != nil:
		return nil, err
	case u.passwordHash == nil || u.Disabled:
		VerifyPassword(password, dummyHash)
	default:
		ok = VerifyPassword(password, *u.passwordHash)
	}
	if !ok {
		s.recordFailure(ctx, "user", uk)
		s.recordFailure(ctx, "ip", ik)
		return nil, ErrInvalidCredentials
	}
	_, _ = s.pool.Exec(ctx, `DELETE FROM login_failures WHERE scope = 'user' AND key = $1`, uk)
	return u, nil
}

// RecordSecondFactorFailure counts a wrong TOTP/recovery code toward the same
// lockout as wrong passwords, so codes cannot be brute-forced.
func (s *Service) RecordSecondFactorFailure(ctx context.Context, u *User, ip string) {
	s.recordFailure(ctx, "user", normKey(u.Username))
	s.recordFailure(ctx, "ip", ip)
}

// CheckLocked returns a *LockedError while the user or address is locked out.
func (s *Service) CheckLocked(ctx context.Context, u *User, ip string) error {
	for _, c := range []struct {
		scope, key string
		limit      int
	}{{"user", normKey(u.Username), s.cfg.UserFailLimit}, {"ip", ip, s.cfg.IPFailLimit}} {
		until, err := s.lockedUntil(ctx, c.scope, c.key, c.limit)
		if err != nil {
			return err
		}
		if !until.IsZero() {
			return &LockedError{Until: until}
		}
	}
	return nil
}

// ClearFailures forgets a user's failed attempts (after a completed sign-in).
func (s *Service) ClearFailures(ctx context.Context, u *User) {
	_, _ = s.pool.Exec(ctx, `DELETE FROM login_failures WHERE scope = 'user' AND key = $1`, normKey(u.Username))
}

// ---- sessions ---------------------------------------------------------------

// Stage of a session.
const (
	StagePending = "pending" // password verified, second factor outstanding
	StageEnroll  = "enroll"  // password verified, user must add a second factor
	StageActive  = "active"
)

// Session is a signed-in (or half signed-in) browser.
type Session struct {
	User      *User
	Stage     string
	CSRFToken string
	ExpiresAt time.Time
	hash      []byte
}

// NewSetupToken is a fresh token for the setup wizard: whoever reaches a new installation first cannot take it without the one in the log.
func NewSetupToken() string {
	b := make([]byte, 15)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b)
}

func randToken(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

func hashToken(t string) []byte {
	h := sha256.Sum256([]byte(t))
	return h[:]
}

// CreateSession stores a new session and returns the secret cookie value.
// Only a hash of the token is kept, so a database leak does not yield sessions.
func (s *Service) CreateSession(ctx context.Context, userID int64, stage, ip, userAgent string) (token, csrf string, err error) {
	token, csrf = randToken(32), randToken(24)
	idle := s.cfg.IdleTTL
	if stage == StagePending {
		idle = s.cfg.PendingTTL
	}
	if len(userAgent) > 300 {
		userAgent = userAgent[:300]
	}
	_, err = s.pool.Exec(ctx, `
		INSERT INTO sessions (token_hash, user_id, stage, csrf_token, idle_expires_at, absolute_expires_at, ip, user_agent)
		VALUES ($1, $2, $3, $4, now() + make_interval(secs => $5), now() + make_interval(secs => $6), $7, $8)`,
		hashToken(token), userID, stage, csrf, idle.Seconds(), s.cfg.AbsoluteTTL.Seconds(), ip, userAgent)
	return token, csrf, err
}

// LookupSession resolves a cookie value to a live session and extends its idle
// timeout. Sessions of disabled users are ignored.
func (s *Service) LookupSession(ctx context.Context, token string) (*Session, error) {
	if token == "" {
		return nil, ErrNotFound
	}
	h := hashToken(token)
	var sess Session
	var userID int64
	var idleExp time.Time
	err := s.pool.QueryRow(ctx, `
		SELECT s.user_id, s.stage, s.csrf_token, s.idle_expires_at FROM sessions s JOIN users u ON u.id = s.user_id
		WHERE s.token_hash = $1 AND s.idle_expires_at > now() AND s.absolute_expires_at > now() AND NOT u.disabled`, h).
		Scan(&userID, &sess.Stage, &sess.CSRFToken, &idleExp)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if sess.User, err = s.GetUser(ctx, userID); err != nil {
		return nil, err
	}
	sess.ExpiresAt, sess.hash = idleExp, h
	if sess.Stage == StageActive { // slide the idle window, but write at most once a minute
		_, _ = s.pool.Exec(ctx, `
			UPDATE sessions SET last_seen_at = now(),
				idle_expires_at = least(now() + make_interval(secs => $2), absolute_expires_at)
			WHERE token_hash = $1 AND last_seen_at < now() - interval '1 minute'`, h, s.cfg.IdleTTL.Seconds())
	}
	return &sess, nil
}

// Promote turns the session into a fully signed-in one with a fresh token and
// CSRF token (the token changes at the privilege change to prevent fixation).
func (s *Service) Promote(ctx context.Context, old *Session, ip, userAgent string) (token, csrf string, err error) {
	if err = s.DeleteSession(ctx, old); err != nil {
		return "", "", err
	}
	return s.CreateSession(ctx, old.User.ID, StageActive, ip, userAgent)
}

// DeleteSession signs one session out.
func (s *Service) DeleteSession(ctx context.Context, sess *Session) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM sessions WHERE token_hash = $1`, sess.hash)
	return err
}

// DeleteUserSessions signs a user out everywhere, optionally keeping one session.
func (s *Service) DeleteUserSessions(ctx context.Context, userID int64, keep *Session) error {
	if keep != nil {
		_, err := s.pool.Exec(ctx, `DELETE FROM sessions WHERE user_id = $1 AND token_hash <> $2`, userID, keep.hash)
		return err
	}
	_, err := s.pool.Exec(ctx, `DELETE FROM sessions WHERE user_id = $1`, userID)
	return err
}

// TouchLogin records the time of a completed sign-in.
func (s *Service) TouchLogin(ctx context.Context, userID int64) {
	_, _ = s.pool.Exec(ctx, `UPDATE users SET last_login_at = now() WHERE id = $1`, userID)
}

// Maintain removes expired sessions, ceremonies and old failure records.
func (s *Service) Maintain(ctx context.Context) error {
	for _, q := range []string{
		`DELETE FROM sessions WHERE idle_expires_at < now() OR absolute_expires_at < now()`,
		`DELETE FROM webauthn_ceremonies WHERE expires_at < now()`,
		`DELETE FROM login_failures WHERE at < now() - interval '1 day'`,
	} {
		if _, err := s.pool.Exec(ctx, q); err != nil {
			return err
		}
	}
	return nil
}

// ---- audit --------------------------------------------------------------------

// Actor identifies who did something, for the audit log.
type Actor struct {
	UserID   int64
	Username string
	IP       string
}

// AuditEntry is one row of the audit log.
type AuditEntry struct {
	ID       int64          `json:"id"`
	At       time.Time      `json:"at"`
	UserID   *int64         `json:"user_id"`
	Username string         `json:"username"`
	IP       string         `json:"ip"`
	Action   string         `json:"action"`
	Target   string         `json:"target"`
	Detail   map[string]any `json:"detail"`
}

// Audit appends to the audit log. Failures are returned but callers normally
// only log them: an audit write must not block the action itself.
func (s *Service) Audit(ctx context.Context, a Actor, action, target string, detail map[string]any) error {
	if detail == nil {
		detail = map[string]any{}
	}
	var uid *int64
	if a.UserID != 0 {
		uid = &a.UserID
	}
	_, err := s.pool.Exec(ctx, `INSERT INTO audit_log (user_id, username, ip, action, target, detail) VALUES ($1, $2, $3, $4, $5, $6)`,
		uid, a.Username, a.IP, action, target, detail)
	return err
}

// AuditPage returns audit entries newest first. before is an exclusive id bound (0 = newest).
func (s *Service) AuditPage(ctx context.Context, before int64, limit int, action string) ([]AuditEntry, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id, at, user_id, username, ip, action, target, detail FROM audit_log
		WHERE ($1 = 0 OR id < $1) AND ($2 = '' OR action LIKE $2 || '%') ORDER BY id DESC LIMIT $3`, before, action, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AuditEntry{}
	for rows.Next() {
		var e AuditEntry
		if err := rows.Scan(&e.ID, &e.At, &e.UserID, &e.Username, &e.IP, &e.Action, &e.Target, &e.Detail); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// scanOutcomes are the audit actions that say how a scan ended: the ones a person looks for when a scan shows an error.
var scanOutcomes = []string{"scan.finished", "scan.errors", "scan.empty", "scan.failed"}

// AuditForScan returns the audit entry that says how scan scanID ended, if there is one. The worker writes the scan's id into the entry
// ("scan_id"); an entry of a failed scan from before it did so is found through its job, which still knows the scan (jobs are kept for 30
// days). The entry that only says a scan was asked for (scan.start) is not an outcome and is never returned.
func (s *Service) AuditForScan(ctx context.Context, scanID int64) (*AuditEntry, error) {
	var e AuditEntry
	err := s.pool.QueryRow(ctx, `
		SELECT id, at, user_id, username, ip, action, target, detail FROM audit_log
		WHERE action = ANY($2)
		  AND (detail ->> 'scan_id' = ($1::bigint)::text
		       OR (detail ->> 'scan_id' IS NULL AND detail ->> 'job' IN (SELECT id::text FROM jobs WHERE scan_id = $1::bigint)))
		ORDER BY id DESC LIMIT 1`, scanID, scanOutcomes).Scan(&e.ID, &e.At, &e.UserID, &e.Username, &e.IP, &e.Action, &e.Target, &e.Detail)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &e, nil
}

func randEncode(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func randDecode(s string) ([]byte, error) { return base64.RawURLEncoding.DecodeString(s) }

// CheckIPLocked returns a *LockedError while a client address is locked out.
func (s *Service) CheckIPLocked(ctx context.Context, ip string) error {
	until, err := s.lockedUntil(ctx, "ip", ip, s.cfg.IPFailLimit)
	if err != nil {
		return err
	}
	if !until.IsZero() {
		return &LockedError{Until: until}
	}
	return nil
}

// RecordIPFailure counts a failed attempt that has no user (a wrong setup
// token, a failed passwordless passkey login).
func (s *Service) RecordIPFailure(ctx context.Context, ip string) { s.recordFailure(ctx, "ip", ip) }

var prefValues = map[string]map[string]bool{
	"units":    {"binary": true, "decimal": true},
	"language": {"nl": true, "en": true},
	"theme":    {"system": true, "light": true, "dark": true},
	// Which delete scripts the interface asks for: the advanced script (a summary, a typed confirmation, a check of every file) or the
	// simple one (a .txt with one command per file or folder, to paste into a terminal). See ADR-0011.
	"delete_scripts": {"advanced": true, "simple": true},
}

// accentRE is the accent colour a person may pick: #rrggbb. Anything else would end up in a CSS custom property.
var accentRE = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

// validPref checks one preference and returns the value to store. "accent" is a free colour (#rrggbb, stored in
// lower case) or empty for the app's default; the other preferences are a fixed set.
func validPref(key, value string) (string, bool) {
	if key == "accent" {
		if value == "" {
			return "", true
		}
		return strings.ToLower(value), accentRE.MatchString(value)
	}
	return value, prefValues[key][value]
}

// SetPrefs stores interface settings; unknown keys and values are rejected.
func (s *Service) SetPrefs(ctx context.Context, userID int64, prefs map[string]string) (map[string]string, error) {
	prefs, err := CleanPrefs(prefs)
	if err != nil {
		return nil, err
	}
	var out map[string]string
	err = s.pool.QueryRow(ctx, `UPDATE users SET prefs = prefs || $2::jsonb WHERE id = $1 RETURNING prefs`, userID, prefs).Scan(&out)
	return out, err
}
