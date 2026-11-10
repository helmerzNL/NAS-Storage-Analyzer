package jobs

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// States of a connection test.
const (
	ProbeQueued  = "queued"
	ProbeRunning = "running"
	ProbeOK      = "ok"
	ProbeFailed  = "failed"
)

// How long a test may wait for a worker, and how long a worker may take, before the test is closed as failed. A test that nobody
// picks up means no worker is running, which is exactly what the person who pressed the button needs to hear.
const (
	ProbeWaitLimit = 60 * time.Second
	ProbeRunLimit  = 3 * time.Minute
)

// What a test that timed out says. The interface shows the worker's words as they are.
const (
	ProbeNoWorker   = "no worker picked the test up within 60 seconds: is the worker running? (docker compose ps; in the all-in-one image, see its log)"
	ProbeWorkerGone = "the worker stopped responding during the test"
)

// Probe is one connection test of a share: can the worker reach it and list its top folder?
type Probe struct {
	ID          int64      `json:"id"`
	ShareID     int64      `json:"share_id"`
	State       string     `json:"state"`
	RequestedBy string     `json:"requested_by"`
	RequestedAt time.Time  `json:"requested_at"`
	StartedAt   *time.Time `json:"started_at"`
	FinishedAt  *time.Time `json:"finished_at"`
	Error       *string    `json:"error"`
	Entries     *int       `json:"entries"`
	More        bool       `json:"more"`
}

const probeCols = `id, share_id, state, requested_by, requested_at, started_at, finished_at, error, entries, more`

func scanProbe(row pgx.Row) (*Probe, error) {
	var p Probe
	err := row.Scan(&p.ID, &p.ShareID, &p.State, &p.RequestedBy, &p.RequestedAt, &p.StartedAt, &p.FinishedAt, &p.Error, &p.Entries, &p.More)
	return &p, err
}

// Probes talks to the share_probes table: the app asks for a test and reads its outcome, a worker claims and closes it.
type Probes struct{ pool *pgxpool.Pool }

func NewProbes(pool *pgxpool.Pool) *Probes { return &Probes{pool: pool} }

// expire closes the tests that can no longer end by themselves: one that waited too long (no worker took it) and one whose worker
// went away. Called by whoever asks or reads, so it works when no worker is running at all.
func (p *Probes) expire(ctx context.Context) error {
	_, err := p.pool.Exec(ctx, `
		UPDATE share_probes SET state = 'failed', finished_at = now(), error = CASE WHEN state = 'queued' THEN $1 ELSE $2 END
		WHERE (state = 'queued' AND requested_at < now() - make_interval(secs => $3))
		   OR (state = 'running' AND started_at < now() - make_interval(secs => $4))`,
		ProbeNoWorker, ProbeWorkerGone, ProbeWaitLimit.Seconds(), ProbeRunLimit.Seconds())
	return err
}

// Request asks for a test of a share and returns it. When one is already waiting or running, that one is returned: pressing the
// button twice is one test. Outcomes older than a week are removed on the way.
func (p *Probes) Request(ctx context.Context, shareID int64, by string) (*Probe, error) {
	if err := p.expire(ctx); err != nil {
		return nil, err
	}
	if _, err := p.pool.Exec(ctx, `DELETE FROM share_probes WHERE state IN ('ok', 'failed') AND finished_at < now() - interval '7 days'`); err != nil {
		return nil, err
	}
	probe, err := scanProbe(p.pool.QueryRow(ctx, `INSERT INTO share_probes (share_id, requested_by) VALUES ($1, $2) RETURNING `+probeCols, shareID, by))
	if isUnique(err) {
		return scanProbe(p.pool.QueryRow(ctx, `SELECT `+probeCols+` FROM share_probes WHERE share_id = $1 AND state IN ('queued', 'running')`, shareID))
	}
	return probe, err
}

// Latest returns the most recent test of a share, or nil when it was never tested.
func (p *Probes) Latest(ctx context.Context, shareID int64) (*Probe, error) {
	if err := p.expire(ctx); err != nil {
		return nil, err
	}
	probe, err := scanProbe(p.pool.QueryRow(ctx, `SELECT `+probeCols+` FROM share_probes WHERE share_id = $1 ORDER BY id DESC LIMIT 1`, shareID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return probe, err
}

// Claim takes the oldest waiting test for a worker, or returns nil.
func (p *Probes) Claim(ctx context.Context, workerID string) (*Probe, error) {
	probe, err := scanProbe(p.pool.QueryRow(ctx, `
		UPDATE share_probes SET state = 'running', started_at = now(), locked_by = $1
		WHERE id = (SELECT id FROM share_probes WHERE state = 'queued' ORDER BY id FOR UPDATE SKIP LOCKED LIMIT 1)
		RETURNING `+probeCols, workerID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return probe, err
}

// Finish records how a test ended. A test that was already closed (it ran past its limit) keeps the outcome it was given.
func (p *Probes) Finish(ctx context.Context, id int64, entries int, more bool, errMsg string) error {
	if errMsg != "" {
		_, err := p.pool.Exec(ctx, `UPDATE share_probes SET state = 'failed', finished_at = now(), error = $2 WHERE id = $1 AND state = 'running'`, id, errMsg)
		return err
	}
	_, err := p.pool.Exec(ctx, `UPDATE share_probes SET state = 'ok', finished_at = now(), entries = $2, more = $3 WHERE id = $1 AND state = 'running'`, id, entries, more)
	return err
}
