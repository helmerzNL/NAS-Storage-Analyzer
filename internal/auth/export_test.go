package auth

import (
	"context"
	"fmt"
)

// CreateUser adds an account with a password, for tests. No route does this any more: an administrator makes an invitation link and the guest
// chooses their own password or passkey (invitations.go), so nobody else ever learns a credential.
func (s *Service) CreateUser(ctx context.Context, username, displayName, password, role string) (*User, error) {
	if !usernameRE.MatchString(username) {
		return nil, ErrBadUsername
	}
	if role != "admin" && role != "viewer" {
		return nil, fmt.Errorf("role must be admin or viewer")
	}
	if err := CheckPasswordPolicy(password, username); err != nil {
		return nil, err
	}
	var id int64
	err := s.pool.QueryRow(ctx, `INSERT INTO users (username, display_name, role, password_hash) VALUES ($1, $2, $3, $4) RETURNING id`,
		username, displayName, role, HashPassword(password)).Scan(&id)
	if isUnique(err) {
		return nil, ErrConflict
	}
	if err != nil {
		return nil, err
	}
	return s.GetUser(ctx, id)
}
