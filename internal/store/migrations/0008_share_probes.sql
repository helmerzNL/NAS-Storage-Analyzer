-- A connection test of a share ("probe"): can the worker reach it and list its top folder? An administrator asks for one from the
-- shares screen, a worker runs it (only a worker can mount), and the interface reads the outcome back. It is not a scan: nothing
-- about the contents of the share is kept, only how many entries the top folder had.
--
-- Its own table and not a kind of job: a test has to answer in seconds, so it must never wait behind a scan that takes hours, and
-- the rules of the scan queue (one scan per share, what "busy" means for a restore or a reset) stay what they were.
CREATE TABLE share_probes (
    id            bigserial PRIMARY KEY,
    share_id      bigint NOT NULL REFERENCES shares(id) ON DELETE CASCADE,
    state         text NOT NULL DEFAULT 'queued' CHECK (state IN ('queued', 'running', 'ok', 'failed')),
    requested_by  text NOT NULL DEFAULT '',
    requested_at  timestamptz NOT NULL DEFAULT now(),
    started_at    timestamptz,
    finished_at   timestamptz,
    locked_by     text,
    error         text,                              -- why it failed, in the worker's words
    entries       int,                               -- entries counted in the top folder, when it worked
    more          boolean NOT NULL DEFAULT false     -- the top folder holds more entries than were counted
);
-- At most one waiting or running test per share: a second request gets the one that is already under way.
CREATE UNIQUE INDEX share_probes_one_active_idx ON share_probes (share_id) WHERE state IN ('queued', 'running');
CREATE INDEX share_probes_queue_idx ON share_probes (id) WHERE state = 'queued';
CREATE INDEX share_probes_share_idx ON share_probes (share_id, id DESC);
