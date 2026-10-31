-- Share management, job queue, schedules, live progress and history.

ALTER TABLE shares
    ADD COLUMN secret_enc       bytea,                       -- encrypted SMB password
    ADD COLUMN cron             text NOT NULL DEFAULT '',    -- 5-field cron, empty = no schedule
    ADD COLUMN next_run_at      timestamptz,
    ADD COLUMN last_run_at      timestamptz,
    ADD COLUMN full_scan_every  int  NOT NULL DEFAULT 7 CHECK (full_scan_every >= 0), -- every Nth scan is a full one (0 = never automatic)
    ADD COLUMN max_ops_day      int  NOT NULL DEFAULT 0 CHECK (max_ops_day >= 0),     -- 0 = same limit all day
    ADD COLUMN day_start_hour   int  NOT NULL DEFAULT 8  CHECK (day_start_hour BETWEEN 0 AND 23),
    ADD COLUMN day_end_hour     int  NOT NULL DEFAULT 22 CHECK (day_end_hour BETWEEN 0 AND 24),
    ADD COLUMN retention_scans  int  NOT NULL DEFAULT 104 CHECK (retention_scans >= 0), -- history kept, by number of scans (0 = unlimited)
    ADD COLUMN retention_days   int  NOT NULL DEFAULT 0   CHECK (retention_days >= 0),  -- ... and by age (0 = unlimited)
    ADD COLUMN snapshot_depth   int  NOT NULL DEFAULT 4   CHECK (snapshot_depth BETWEEN 1 AND 12);

ALTER TABLE scans
    ADD COLUMN progress jsonb;                               -- live numbers while the scan runs

ALTER TABLE users
    ADD COLUMN prefs jsonb NOT NULL DEFAULT '{}';            -- units, language, theme

-- kind: scan = scan a share; recategorize = re-apply the extension map to a share's files.
CREATE TABLE jobs (
    id               bigserial PRIMARY KEY,
    kind             text NOT NULL CHECK (kind IN ('scan', 'recategorize')),
    share_id         bigint NOT NULL REFERENCES shares(id) ON DELETE CASCADE,
    state            text NOT NULL DEFAULT 'queued' CHECK (state IN ('queued', 'running', 'done', 'failed', 'cancelled')),
    mode             text NOT NULL DEFAULT 'auto' CHECK (mode IN ('auto', 'full')),
    trigger          text NOT NULL DEFAULT 'manual',
    requested_by     text NOT NULL DEFAULT '',
    requested_at     timestamptz NOT NULL DEFAULT now(),
    started_at       timestamptz,
    finished_at      timestamptz,
    locked_by        text,
    heartbeat_at     timestamptz,
    cancel_requested boolean NOT NULL DEFAULT false,
    scan_id          bigint,
    error            text
);
-- At most one waiting or running job per share and kind.
CREATE UNIQUE INDEX jobs_one_active_idx ON jobs (share_id, kind) WHERE state IN ('queued', 'running');
CREATE INDEX jobs_queue_idx ON jobs (id) WHERE state = 'queued';

-- Aggregates of every completed scan, kept for growth charts. The full file
-- rows are only kept for the latest scan.
CREATE TABLE snapshots (
    scan_id     bigint PRIMARY KEY REFERENCES scans(id) ON DELETE CASCADE,
    share_id    bigint NOT NULL REFERENCES shares(id) ON DELETE CASCADE,
    taken_at    timestamptz NOT NULL,
    size        bigint NOT NULL,
    alloc       bigint NOT NULL,
    files       bigint NOT NULL,
    dirs        bigint NOT NULL
);
CREATE INDEX snapshots_share_idx ON snapshots (share_id, taken_at);

CREATE TABLE snapshot_category (
    scan_id     bigint NOT NULL REFERENCES snapshots(scan_id) ON DELETE CASCADE,
    category_id smallint NOT NULL,
    size        bigint NOT NULL,
    files       bigint NOT NULL,
    PRIMARY KEY (scan_id, category_id)
);

CREATE TABLE snapshot_ext (
    scan_id bigint NOT NULL REFERENCES snapshots(scan_id) ON DELETE CASCADE,
    ext     text NOT NULL,
    size    bigint NOT NULL,
    files   bigint NOT NULL,
    PRIMARY KEY (scan_id, ext)
);

-- Large directories down to a configurable depth, so growth of a folder over time can be shown.
CREATE TABLE snapshot_dir (
    scan_id bigint NOT NULL REFERENCES snapshots(scan_id) ON DELETE CASCADE,
    dir_id  bigint NOT NULL,
    path    text NOT NULL,
    size    bigint NOT NULL,
    files   bigint NOT NULL,
    PRIMARY KEY (scan_id, dir_id)
);
CREATE INDEX snapshot_dir_dir_idx ON snapshot_dir (dir_id, scan_id);
