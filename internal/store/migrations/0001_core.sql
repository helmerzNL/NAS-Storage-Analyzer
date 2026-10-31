-- Core domain schema. Share-bound tables are LIST-partitioned on share_id;
-- partitions are created by ensure_share_partitions() when a share is added.

CREATE EXTENSION IF NOT EXISTS ltree;
CREATE EXTENSION IF NOT EXISTS pg_trgm;

CREATE TABLE categories (
    id  smallint PRIMARY KEY,
    key text NOT NULL UNIQUE
);

CREATE TABLE extension_map (
    ext         text PRIMARY KEY CHECK (ext = lower(ext) AND ext <> ''),
    category_id smallint NOT NULL REFERENCES categories(id)
);

CREATE TABLE shares (
    id               bigserial PRIMARY KEY,
    name             text NOT NULL UNIQUE,
    kind             text NOT NULL CHECK (kind IN ('bind', 'nfs', 'smb')),
    config           jsonb NOT NULL DEFAULT '{}',
    excludes         text[] NOT NULL DEFAULT '{}',
    workers          int NOT NULL DEFAULT 16 CHECK (workers BETWEEN 1 AND 256),
    max_ops_per_sec  int NOT NULL DEFAULT 0 CHECK (max_ops_per_sec >= 0),
    last_used_basis  text NOT NULL DEFAULT 'atime' CHECK (last_used_basis IN ('atime', 'mtime', 'max')),
    last_scan_id     bigint,
    created_at       timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE scans (
    id             bigserial PRIMARY KEY,
    share_id       bigint NOT NULL REFERENCES shares(id) ON DELETE CASCADE,
    status         text NOT NULL DEFAULT 'running'
                   CHECK (status IN ('running', 'succeeded', 'failed', 'cancelled')),
    mode           text NOT NULL CHECK (mode IN ('full', 'incremental')),
    trigger        text NOT NULL DEFAULT 'manual',
    started_at     timestamptz NOT NULL DEFAULT now(),
    finished_at    timestamptz,
    excludes_hash  text NOT NULL DEFAULT '',
    dirs           bigint NOT NULL DEFAULT 0,
    reused_dirs    bigint NOT NULL DEFAULT 0,
    files          bigint NOT NULL DEFAULT 0,
    bytes          bigint NOT NULL DEFAULT 0,
    alloc_bytes    bigint NOT NULL DEFAULT 0,
    error_count    bigint NOT NULL DEFAULT 0,
    atime_quality  jsonb,
    error          text
);
CREATE INDEX scans_share_started_idx ON scans (share_id, started_at DESC);
-- At most one running scan per share, enforced by the database.
CREATE UNIQUE INDEX scans_one_running_idx ON scans (share_id) WHERE status = 'running';

CREATE TABLE scan_errors (
    id       bigserial PRIMARY KEY,
    scan_id  bigint NOT NULL REFERENCES scans(id) ON DELETE CASCADE,
    path     text NOT NULL,
    kind     text NOT NULL,
    message  text NOT NULL,
    at       timestamptz NOT NULL
);
CREATE INDEX scan_errors_scan_idx ON scan_errors (scan_id);

CREATE SEQUENCE dir_id_seq;
CREATE SEQUENCE file_id_seq;

-- One row per directory of the latest completed scan, with its pre-computed
-- aggregates. *_direct covers files directly inside; *_total the whole subtree.
CREATE TABLE dirs (
    share_id        bigint NOT NULL,
    id              bigint NOT NULL,
    parent_id       bigint,
    name            text NOT NULL,
    depth           int NOT NULL,
    path            ltree NOT NULL,
    mtime           timestamptz NOT NULL,
    entry_count     int NOT NULL,
    last_seen_scan  bigint NOT NULL,

    size_direct     bigint NOT NULL DEFAULT 0,
    alloc_direct    bigint NOT NULL DEFAULT 0,
    files_direct    bigint NOT NULL DEFAULT 0,
    dirs_direct     bigint NOT NULL DEFAULT 0,
    d_oldest_mtime  timestamptz,
    d_newest_mtime  timestamptz,
    d_oldest_atime  timestamptz,
    d_newest_atime  timestamptz,

    size_total      bigint NOT NULL DEFAULT 0,
    alloc_total     bigint NOT NULL DEFAULT 0,
    files_total     bigint NOT NULL DEFAULT 0,
    dirs_total      bigint NOT NULL DEFAULT 0,
    oldest_mtime    timestamptz,
    newest_mtime    timestamptz,
    oldest_atime    timestamptz,
    newest_atime    timestamptz,
    PRIMARY KEY (share_id, id)
) PARTITION BY LIST (share_id);
CREATE INDEX dirs_children_idx ON dirs (share_id, parent_id, size_total DESC);
CREATE INDEX dirs_depth_idx    ON dirs (share_id, depth, parent_id);
CREATE INDEX dirs_path_idx     ON dirs USING gist (path);
CREATE INDEX dirs_top_idx      ON dirs (share_id, size_total DESC);
CREATE INDEX dirs_name_trgm    ON dirs USING gin (name gin_trgm_ops);

CREATE TABLE files (
    share_id     bigint NOT NULL,
    id           bigint NOT NULL DEFAULT nextval('file_id_seq'),
    dir_id       bigint NOT NULL,
    name         text NOT NULL,
    ext          text NOT NULL,
    category_id  smallint NOT NULL,
    size         bigint NOT NULL,
    alloc        bigint NOT NULL,
    mtime        timestamptz NOT NULL,
    atime        timestamptz NOT NULL,
    ctime        timestamptz NOT NULL,
    nlink        int NOT NULL DEFAULT 1,
    dev          bigint,
    ino          bigint,
    -- false for the 2nd..nth hardlink of the same inode: listed, but not summed.
    counted      boolean NOT NULL DEFAULT true,
    PRIMARY KEY (share_id, id)
) PARTITION BY LIST (share_id);
CREATE INDEX files_dir_idx      ON files (share_id, dir_id);
CREATE INDEX files_size_idx     ON files (share_id, size DESC);
CREATE INDEX files_cat_size_idx ON files (share_id, category_id, size DESC);
CREATE INDEX files_ext_size_idx ON files (share_id, ext, size DESC);
CREATE INDEX files_mtime_idx    ON files (share_id, mtime);
CREATE INDEX files_atime_idx    ON files (share_id, atime);
CREATE INDEX files_link_idx     ON files (share_id, dev, ino) WHERE nlink > 1;
CREATE INDEX files_name_trgm    ON files USING gin (name gin_trgm_ops);

-- Recursive and direct totals per directory x category.
CREATE TABLE dir_category_agg (
    share_id     bigint NOT NULL,
    dir_id       bigint NOT NULL,
    category_id  smallint NOT NULL,
    size_direct  bigint NOT NULL DEFAULT 0,
    alloc_direct bigint NOT NULL DEFAULT 0,
    files_direct bigint NOT NULL DEFAULT 0,
    size_total   bigint NOT NULL DEFAULT 0,
    alloc_total  bigint NOT NULL DEFAULT 0,
    files_total  bigint NOT NULL DEFAULT 0,
    PRIMARY KEY (share_id, dir_id, category_id)
) PARTITION BY LIST (share_id);
CREATE INDEX dir_category_agg_cat_idx ON dir_category_agg (share_id, category_id, size_total DESC);

CREATE TABLE share_ext_agg (
    share_id  bigint NOT NULL,
    ext       text NOT NULL,
    size      bigint NOT NULL,
    alloc     bigint NOT NULL,
    files     bigint NOT NULL,
    PRIMARY KEY (share_id, ext)
) PARTITION BY LIST (share_id);

CREATE FUNCTION ensure_share_partitions(p_share bigint) RETURNS void
LANGUAGE plpgsql AS $$
DECLARE t text;
BEGIN
    FOREACH t IN ARRAY ARRAY['dirs', 'files', 'dir_category_agg', 'share_ext_agg'] LOOP
        EXECUTE format('CREATE TABLE IF NOT EXISTS %I PARTITION OF %I FOR VALUES IN (%s)',
                       t || '_s' || p_share, t, p_share);
    END LOOP;
END $$;

CREATE FUNCTION drop_share_partitions(p_share bigint) RETURNS void
LANGUAGE plpgsql AS $$
DECLARE t text;
BEGIN
    FOREACH t IN ARRAY ARRAY['dirs', 'files', 'dir_category_agg', 'share_ext_agg'] LOOP
        EXECUTE format('DROP TABLE IF EXISTS %I', t || '_s' || p_share);
    END LOOP;
END $$;
