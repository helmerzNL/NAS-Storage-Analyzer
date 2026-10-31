-- In-app backup and restore (App-Guidance backup-restore.md).
--
-- Neither table is part of a backup: they describe work in progress, and a restore never touches them, so the job's own row
-- survives the swap it is performing. A factory reset empties them with every other table.

-- A backup file that has been uploaded and checked and waits for the owner's confirmation. It lives in the database and not on
-- disk because the application container's file system is read-only: there is no writable folder to stage it in. The token is
-- 32 hexadecimal characters, handed to the browser once and used once; a file that is never used is removed after an hour.
CREATE TABLE backup_stages (
    token      text PRIMARY KEY CHECK (token ~ '^[0-9a-f]{32}$'),
    created_by text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    size       int NOT NULL,
    data       bytea NOT NULL
);
CREATE INDEX backup_stages_created_idx ON backup_stages (created_at);

-- One row per restore. The state lives in the database, not in the memory of the process, so the screen can pick the work up again
-- after a reload and a restart cannot leave a job that looks alive.
CREATE TABLE backup_jobs (
    id             bigserial PRIMARY KEY,
    status         text NOT NULL DEFAULT 'running' CHECK (status IN ('running', 'done', 'failed')),
    phase          text NOT NULL DEFAULT 'checking' CHECK (phase IN ('checking', 'replacing')),
    step           int NOT NULL DEFAULT 0,
    steps          int NOT NULL DEFAULT 0,          -- 0 = cannot be counted
    error_key      text NOT NULL DEFAULT '',        -- worded when read, in the reader's language
    error_params   jsonb NOT NULL DEFAULT '{}',
    summary        jsonb,                           -- what was restored, shown on success
    checking_ms    int,
    replacing_ms   int,
    started_by     text NOT NULL,
    started_at     timestamptz NOT NULL DEFAULT now(),
    updated_at     timestamptz NOT NULL DEFAULT now(),
    finished_at    timestamptz,
    dismissed      boolean NOT NULL DEFAULT false
);
-- At most one restore runs at a time, whatever process starts it.
CREATE UNIQUE INDEX backup_jobs_one_running_idx ON backup_jobs ((true)) WHERE status = 'running';
