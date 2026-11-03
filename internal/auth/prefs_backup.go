package auth

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// CleanPrefs checks interface preferences and returns the values to store: an unknown key or value is an error. SetPrefs uses it for what a
// person picks, and a restore uses it for what a backup file says, so a file is held to the same rules as typing the choice in.
func CleanPrefs(prefs map[string]string) (map[string]string, error) {
	clean := make(map[string]string, len(prefs))
	for k, v := range prefs {
		nv, ok := validPref(k, v)
		if !ok {
			return nil, fmt.Errorf("invalid preference %s=%q", k, v)
		}
		clean[k] = nv
	}
	return clean, nil
}

// UserPrefs is one person's interface preferences, by user name. It is all a backup says about a person: no role, no credential.
type UserPrefs struct {
	Username string            `json:"username"`
	Prefs    map[string]string `json:"prefs"`
}

// ExportPrefs lists the preferences of every person who has chosen any, for a backup.
func (s *Service) ExportPrefs(ctx context.Context) ([]UserPrefs, error) {
	rows, err := s.pool.Query(ctx, `SELECT username, prefs FROM users WHERE prefs <> '{}'::jsonb ORDER BY lower(username)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []UserPrefs{}
	for rows.Next() {
		var u UserPrefs
		if err := rows.Scan(&u.Username, &u.Prefs); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// Querier is what a connection pool and a transaction have in common.
type Querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// PrefsStatus says whether a user with this name exists (any case) and whether these preferences differ from theirs. prefs must have been
// through CleanPrefs.
func PrefsStatus(ctx context.Context, q Querier, username string, prefs map[string]string) (exists, differs bool, err error) {
	err = q.QueryRow(ctx, `SELECT true, prefs IS DISTINCT FROM $2::jsonb FROM users WHERE lower(username) = lower($1)`, username, prefs).Scan(&exists, &differs)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, false, nil
	}
	return exists, differs, err
}

// ReplacePrefsTx replaces the preferences of the user with this name (any case), only when they differ, and reports whether that user exists
// and whether anything changed.
func ReplacePrefsTx(ctx context.Context, tx pgx.Tx, username string, prefs map[string]string) (exists, changed bool, err error) {
	exists, differs, err := PrefsStatus(ctx, tx, username, prefs)
	if err != nil || !exists || !differs {
		return exists, false, err
	}
	if _, err := tx.Exec(ctx, `UPDATE users SET prefs = $2::jsonb WHERE lower(username) = lower($1)`, username, prefs); err != nil {
		return true, false, err
	}
	return true, true, nil
}
