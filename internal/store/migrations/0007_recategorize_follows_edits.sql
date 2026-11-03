-- A re-categorisation reads the file-type map when it runs. An edit made while one is RUNNING is therefore not in it, and the single
-- "one waiting or running job per share and kind" index refused the job that should have followed, so the later edit was never applied
-- (and nobody was told: the refusal is, rightly, ignored when a job is only waiting, because a waiting job will read the newer map).
--
-- A scan keeps the old rule: at most one waiting or running per share, and a second "scan now" is refused.
-- A re-categorisation may have one RUNNING and one WAITING behind it, never two of either. The waiting one covers every edit made until it
-- starts; Claim does not start it while the running one is still at work (Queue.Claim).
DROP INDEX jobs_one_active_idx;
CREATE UNIQUE INDEX jobs_one_active_scan_idx ON jobs (share_id) WHERE kind = 'scan' AND state IN ('queued', 'running');
CREATE UNIQUE INDEX jobs_one_waiting_recategorize_idx ON jobs (share_id) WHERE kind = 'recategorize' AND state = 'queued';
CREATE UNIQUE INDEX jobs_one_running_recategorize_idx ON jobs (share_id) WHERE kind = 'recategorize' AND state = 'running';
