-- Capacity and free space of the file system a share lives on, taken with
-- statfs at the end of each successful scan (e.g. for a free-space sensor).
ALTER TABLE scans
    ADD COLUMN fs_total_bytes bigint,
    ADD COLUMN fs_free_bytes  bigint;
