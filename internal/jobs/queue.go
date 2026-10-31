// Package jobs is the persistent job queue shared by the app (which enqueues
// scans, manually or on a schedule) and the workers (which run them).
package jobs

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Kinds and states of a job.
const (
	KindScan         = "scan"
	KindRecategorize = "recategorize"

	StateQueued    = "queued"
	StateRunning   = "running"
	StateDone      = "done"
	StateFailed    = "failed"
	StateCancelled = "cancelled"
)

// ErrAlreadyActive: the share already has a waiting or running scan, or a waiting re-categorisation (a running one does not count: see Enqueue).
var ErrAlreadyActive = errors.New("a job of this kind is already queued or running for this share")

// Job is one queue entry.
type Job struct {
	ID          int64      `json:"id"`
	Kind        string     `json:"kind"`
	ShareID     int64      `json:"share_id"`
	State       string     `json:"state"`
	Mode        string     `json:"mode"`
	Trigger     string     `json:"trigger"`
	RequestedBy string     `json:"requested_by"`
	RequestedAt time.Time  `json:"requested_at"`
	StartedAt   *time.Time `json:"started_at"`
	FinishedAt  *time.Time `json:"finished_at"`
	ScanID      *int64     `json:"scan_id"`
	Error       *string    `json:"error"`
}

const jobCols = `id, kind, share_id, state, mode, trigger, requested_by, requested_at, started_at, finished_at, scan_id, error`

func scanJob(row pgx.Row) (*Job, error) {
	var j Job
	err := row.Scan(&j.ID, &j.Kind, &j.ShareID, &j.State, &j.Mode, &j.Trigger, &j.RequestedBy, &j.RequestedAt, &j.StartedAt,
		&j.FinishedAt, &j.ScanID, &j.Error)
	return &j, err
}

// Queue talks to the jobs table.
type Queue struct{ pool *pgxpool.Pool }

func NewQueue(pool *pgxpool.Pool) *Queue { return &Queue{pool: pool} }

// Enqueue adds a job. mode "full" forces a full scan, "auto" lets the worker decide.
//
// It fails with ErrAlreadyActive when the share already has a waiting or running scan, and when it already has a WAITING re-categorisation.
// A re-categorisation that is RUNNING does not block a new one: it read the file-type map when it started, so an edit made since is not in it,
// and the job that follows is the one that applies it. A waiting one will read the map when it starts, so another would add nothing, which is
// why that refusal is safe to ignore (the API does).
func (q *Queue) Enqueue(ctx context.Context, kind string, shareID int64, mode, trigger, by string) (*Job, error) {
	j, err := scanJob(q.pool.QueryRow(ctx, `INSERT INTO jobs (kind, share_id, mode, trigger, requested_by) VALUES ($1, $2, $3, $4, $5)
		RETURNING `+jobCols, kind, shareID, mode, trigger, by))
	if isUnique(err) {
		return nil, ErrAlreadyActive
	}
	return j, err
}

func isUnique(err error) bool {
	var pg *pgconn.PgError
	return errors.As(err, &pg) && pg.Code == "23505"
}

// Claim takes the oldest waiting job for a worker, or returns nil. A waiting re-categorisation is passed over while another one of the same
// share is running: they would rewrite the same rows, and the waiting one exists exactly to run after the running one. The unique index on running
// re-categorisations is the backstop; the guard keeps the blocked job from being chosen again and again ahead of the work behind it.
func (q *Queue) Claim(ctx context.Context, workerID string) (*Job, error) {
	j, err := scanJob(q.pool.QueryRow(ctx, `
		UPDATE jobs SET state = 'running', started_at = now(), heartbeat_at = now(), locked_by = $1
		WHERE id = (SELECT w.id FROM jobs w WHERE w.state = 'queued'
			AND NOT (w.kind = 'recategorize' AND EXISTS (SELECT 1 FROM jobs r WHERE r.share_id = w.share_id AND r.kind = 'recategorize' AND r.state = 'running'))
			ORDER BY w.id FOR UPDATE SKIP LOCKED LIMIT 1)
		RETURNING `+jobCols, workerID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return j, err
}

// Heartbeat tells the queue the worker is alive and returns whether somebody
// asked to cancel the job.
func (q *Queue) Heartbeat(ctx context.Context, id int64) (cancel bool, err error) {
	err = q.pool.QueryRow(ctx, `UPDATE jobs SET heartbeat_at = now() WHERE id = $1 AND state = 'running' RETURNING cancel_requested`, id).Scan(&cancel)
	if errors.Is(err, pgx.ErrNoRows) {
		return true, nil // the job is gone or was reaped: stop
	}
	return cancel, err
}

// SetScan links the job to the scan it started.
func (q *Queue) SetScan(ctx context.Context, id, scanID int64) error {
	_, err := q.pool.Exec(ctx, `UPDATE jobs SET scan_id = $2 WHERE id = $1`, id, scanID)
	return err
}

// Finish closes a job.
func (q *Queue) Finish(ctx context.Context, id int64, state, errMsg string) error {
	var e *string
	if errMsg != "" {
		e = &errMsg
	}
	_, err := q.pool.Exec(ctx, `UPDATE jobs SET state = $2, finished_at = now(), error = $3 WHERE id = $1`, id, state, e)
	return err
}

// Cancel stops the active job of a share: a waiting one is cancelled at once,
// a running one is asked to stop. It reports whether there was anything to cancel.
func (q *Queue) Cancel(ctx context.Context, shareID int64, kind string) (bool, error) {
	tag, err := q.pool.Exec(ctx, `
		UPDATE jobs SET state = CASE WHEN state = 'queued' THEN 'cancelled' ELSE state END,
			finished_at = CASE WHEN state = 'queued' THEN now() ELSE finished_at END,
			cancel_requested = true
		WHERE share_id = $1 AND kind = $2 AND state IN ('queued', 'running')`, shareID, kind)
	return tag.RowsAffected() > 0, err
}

// Reap fails jobs whose worker stopped sending heartbeats (a crash or a lost
// container) and closes the scans they left running. It returns how many.
func (q *Queue) Reap(ctx context.Context, staleAfter time.Duration) (int, error) {
	rows, err := q.pool.Query(ctx, `
		UPDATE jobs SET state = 'failed', finished_at = now(), error = 'worker stopped responding'
		WHERE state = 'running' AND heartbeat_at < now() - make_interval(secs => $1) RETURNING share_id, scan_id`, staleAfter.Seconds())
	if err != nil {
		return 0, err
	}
	type pair struct {
		share int64
		scan  *int64
	}
	var reaped []pair
	for rows.Next() {
		var p pair
		if err := rows.Scan(&p.share, &p.scan); err != nil {
			return 0, err
		}
		reaped = append(reaped, p)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	for _, p := range reaped {
		if _, err := q.pool.Exec(ctx, `UPDATE scans SET status = 'failed', finished_at = now(), error = 'interrupted', progress = NULL
			WHERE share_id = $1 AND status = 'running'`, p.share); err != nil {
			return 0, err
		}
	}
	return len(reaped), nil
}

// Recent returns the latest jobs of a share, newest first.
func (q *Queue) Recent(ctx context.Context, shareID int64, limit int) ([]Job, error) {
	rows, err := q.pool.Query(ctx, `SELECT `+jobCols+` FROM jobs WHERE share_id = $1 ORDER BY id DESC LIMIT $2`, shareID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Job{}
	for rows.Next() {
		j, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *j)
	}
	return out, rows.Err()
}

// Prune deletes finished jobs older than a month.
func (q *Queue) Prune(ctx context.Context) error {
	_, err := q.pool.Exec(ctx, `DELETE FROM jobs WHERE state IN ('done', 'failed', 'cancelled') AND finished_at < now() - interval '30 days'`)
	return err
}
