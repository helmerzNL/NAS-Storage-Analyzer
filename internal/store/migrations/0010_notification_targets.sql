-- Where to send a message when a scan needs attention: a webhook of Home Assistant, ntfy, Gotify or anything that takes a POST.
-- The address is stored sealed (secrets.Box, like an SMB password): it carries the token of the receiver, in its path or its query.
-- last_* is the outcome of the most recent delivery, for the settings screen.
CREATE TABLE notification_targets (
    id          bigserial PRIMARY KEY,
    name        text NOT NULL UNIQUE CHECK (length(name) BETWEEN 1 AND 60),
    url_enc     bytea NOT NULL,
    format      text NOT NULL CHECK (format IN ('json', 'text')),
    lang        text NOT NULL CHECK (lang IN ('nl', 'en')),
    on_failed   boolean NOT NULL DEFAULT true,
    on_empty    boolean NOT NULL DEFAULT true,
    on_errors   boolean NOT NULL DEFAULT true,
    on_finished boolean NOT NULL DEFAULT false,
    enabled     boolean NOT NULL DEFAULT true,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now(),
    last_at     timestamptz,
    last_ok     boolean,
    last_status integer,
    last_error  text
);
