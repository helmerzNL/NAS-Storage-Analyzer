-- Local accounts, second factors, sessions, lockout and audit trail.

CREATE TABLE users (
    id               bigserial PRIMARY KEY,
    username         text NOT NULL CHECK (username ~ '^[A-Za-z0-9._@-]{3,64}$'),
    display_name     text NOT NULL DEFAULT '',
    password_hash    text,                      -- NULL for accounts that sign in elsewhere (OIDC later)
    role             text NOT NULL CHECK (role IN ('admin', 'viewer')),
    auth_source      text NOT NULL DEFAULT 'local',
    external_subject text,
    totp_secret_enc  bytea,
    totp_enabled     boolean NOT NULL DEFAULT false,
    totp_last_step   bigint NOT NULL DEFAULT 0, -- last accepted 30 s step, blocks replay
    disabled         boolean NOT NULL DEFAULT false,
    created_at       timestamptz NOT NULL DEFAULT now(),
    last_login_at    timestamptz
);
CREATE UNIQUE INDEX users_username_idx ON users (lower(username));
CREATE UNIQUE INDEX users_external_idx ON users (auth_source, external_subject) WHERE external_subject IS NOT NULL;

CREATE TABLE recovery_codes (
    id         bigserial PRIMARY KEY,
    user_id    bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    code_hash  bytea NOT NULL,
    used_at    timestamptz
);
CREATE INDEX recovery_codes_user_idx ON recovery_codes (user_id);

CREATE TABLE webauthn_credentials (
    credential_id   bytea PRIMARY KEY,
    user_id         bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name            text NOT NULL,
    public_key      bytea NOT NULL,
    attestation_type text NOT NULL DEFAULT '',
    aaguid          bytea NOT NULL DEFAULT '',
    sign_count      bigint NOT NULL DEFAULT 0,
    transports      text[] NOT NULL DEFAULT '{}',
    backup_eligible boolean NOT NULL DEFAULT false,
    backup_state    boolean NOT NULL DEFAULT false,
    user_verified   boolean NOT NULL DEFAULT false,
    created_at      timestamptz NOT NULL DEFAULT now(),
    last_used_at    timestamptz
);
CREATE INDEX webauthn_credentials_user_idx ON webauthn_credentials (user_id);

-- stage: pending = password ok, second factor still needed;
--        enroll  = password ok, user has no second factor yet and must add one;
--        active  = fully signed in.
CREATE TABLE sessions (
    token_hash          bytea PRIMARY KEY,
    user_id             bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    stage               text NOT NULL CHECK (stage IN ('pending', 'enroll', 'active')),
    csrf_token          text NOT NULL,
    created_at          timestamptz NOT NULL DEFAULT now(),
    last_seen_at        timestamptz NOT NULL DEFAULT now(),
    idle_expires_at     timestamptz NOT NULL,
    absolute_expires_at timestamptz NOT NULL,
    ip                  text NOT NULL DEFAULT '',
    user_agent          text NOT NULL DEFAULT ''
);
CREATE INDEX sessions_user_idx ON sessions (user_id);

-- Server-side state of a WebAuthn registration or login ceremony.
CREATE TABLE webauthn_ceremonies (
    id         text PRIMARY KEY,
    user_id    bigint REFERENCES users(id) ON DELETE CASCADE,
    kind       text NOT NULL,
    data       jsonb NOT NULL,
    expires_at timestamptz NOT NULL
);

CREATE TABLE login_failures (
    id     bigserial PRIMARY KEY,
    scope  text NOT NULL CHECK (scope IN ('user', 'ip')),
    key    text NOT NULL,
    at     timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX login_failures_key_idx ON login_failures (scope, key, at DESC);

CREATE TABLE audit_log (
    id        bigserial PRIMARY KEY,
    at        timestamptz NOT NULL DEFAULT now(),
    user_id   bigint,
    username  text NOT NULL DEFAULT '',
    ip        text NOT NULL DEFAULT '',
    action    text NOT NULL,
    target    text NOT NULL DEFAULT '',
    detail    jsonb NOT NULL DEFAULT '{}'
);
CREATE INDEX audit_log_at_idx ON audit_log (at DESC, id DESC);

CREATE TABLE app_settings (
    key   text PRIMARY KEY,
    value jsonb NOT NULL
);
