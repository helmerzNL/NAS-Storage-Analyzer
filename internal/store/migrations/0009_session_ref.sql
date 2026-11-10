-- A reference for a session that can be shown and named. The key of a session is the hash of its cookie, which stays on the
-- server; the account screen lists a person's sessions by this reference and signs one of them out by it. Random, so it says
-- nothing about how many sessions there have been.
ALTER TABLE sessions ADD COLUMN ref uuid NOT NULL DEFAULT gen_random_uuid();
CREATE UNIQUE INDEX sessions_ref_idx ON sessions (ref);
