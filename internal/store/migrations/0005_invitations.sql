-- Invitation links (App-Guidance user-management.md): an administrator makes a link, the guest opens it and chooses their own
-- password or passkey, so nobody but the guest ever knows a credential.
--
-- The link is the secret and only its SHA-256 is kept. An invitation is used once and lives seven days; an expired one is unusable at once
-- and is purged the next time any invitation is made. A 'new' invitation becomes an account; a 'reset' one brings back a user who can no
-- longer sign in, and replaces their credentials only at the moment the guest has set the new ones.
CREATE TABLE invitations (
    id         bigserial PRIMARY KEY,
    token_hash bytea NOT NULL UNIQUE,
    kind       text NOT NULL CHECK (kind IN ('new', 'reset')),
    user_id    bigint REFERENCES users(id) ON DELETE CASCADE,       -- the user a reset is for; deleting the user removes the invitation
    username   text NOT NULL DEFAULT '',                            -- 'new': a name suggested to and reserved for the guest; 'reset': the existing name
    role       text NOT NULL CHECK (role IN ('admin', 'viewer')),   -- decided by the administrator who makes the link, never by the guest
    created_by bigint REFERENCES users(id) ON DELETE SET NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz NOT NULL,
    CHECK ((kind = 'reset') = (user_id IS NOT NULL))
);
CREATE INDEX invitations_expires_idx ON invitations (expires_at);
-- One reset invitation per user (a new one replaces the old), and a reserved name is reserved once.
CREATE UNIQUE INDEX invitations_reset_user_idx ON invitations (user_id) WHERE kind = 'reset';
CREATE UNIQUE INDEX invitations_new_name_idx ON invitations (lower(username)) WHERE kind = 'new' AND username <> '';
