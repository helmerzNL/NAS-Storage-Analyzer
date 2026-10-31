package jobs

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/robfig/cron/v3"
)

// NextRun returns the first time after "after" that the 5-field cron
// expression fires, evaluated in loc. An empty expression has no next run.
func NextRun(expr string, after time.Time, loc *time.Location) (*time.Time, error) {
	if expr == "" {
		return nil, nil
	}
	sched, err := cron.ParseStandard(expr)
	if err != nil {
		return nil, fmt.Errorf("invalid cron expression %q: %w", expr, err)
	}
	next := sched.Next(after.In(loc))
	if next.IsZero() {
		return nil, fmt.Errorf("cron expression %q never fires", expr)
	}
	return &next, nil
}

// ValidateCron checks an expression without computing anything.
func ValidateCron(expr string) error {
	_, err := NextRun(expr, time.Now(), time.UTC)
	return err
}

// Scheduler enqueues scheduled scans. Every app instance runs one, but only
// the holder of a database advisory lock acts, so schedules fire once however
// many instances exist. Its state is the shares table (next_run_at), so it
// survives restarts; a run missed while the app was down happens once on return.
type Scheduler struct {
	pool *pgxpool.Pool
	q    *Queue
	loc  *time.Location
	log  *slog.Logger
	// Every is the polling interval (default 15 s).
	Every time.Duration
}

const schedulerLockKey int64 = 7003

func NewScheduler(pool *pgxpool.Pool, q *Queue, loc *time.Location, log *slog.Logger) *Scheduler {
	return &Scheduler{pool: pool, q: q, loc: loc, log: log, Every: 15 * time.Second}
}

// Run blocks until ctx ends.
func (s *Scheduler) Run(ctx context.Context) {
	for ctx.Err() == nil {
		s.lead(ctx)
		select {
		case <-ctx.Done():
		case <-time.After(s.Every):
		}
	}
}

// lead tries to become the leader and works until it loses the connection holding the lock.
func (s *Scheduler) lead(ctx context.Context) {
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return
	}
	defer conn.Release()
	var got bool
	if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock($1)`, schedulerLockKey).Scan(&got); err != nil || !got {
		return
	}
	defer conn.Exec(context.Background(), `SELECT pg_advisory_unlock($1)`, schedulerLockKey)
	s.log.Info("scheduler is leader")
	t := time.NewTicker(s.Every)
	defer t.Stop()
	for {
		if err := s.Tick(ctx); err != nil && ctx.Err() == nil {
			s.log.Error("scheduler tick failed", "error", err)
		}
		if err := conn.Conn().Ping(ctx); err != nil {
			return // lost the connection, so the lock is gone
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Tick enqueues every share whose time has come and plans its next run. Exported for tests.
func (s *Scheduler) Tick(ctx context.Context) error {
	if _, err := s.q.Reap(ctx, 2*time.Minute); err != nil {
		return err
	}
	rows, err := s.pool.Query(ctx, `SELECT id, cron FROM shares WHERE cron <> '' AND next_run_at IS NOT NULL AND next_run_at <= now()`)
	if err != nil {
		return err
	}
	type due struct {
		id   int64
		cron string
	}
	var list []due
	for rows.Next() {
		var d due
		if err := rows.Scan(&d.id, &d.cron); err != nil {
			rows.Close()
			return err
		}
		list = append(list, d)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, d := range list {
		if _, err := s.q.Enqueue(ctx, KindScan, d.id, "auto", "schedule", ""); err != nil && err != ErrAlreadyActive {
			return err
		}
		next, err := NextRun(d.cron, time.Now(), s.loc)
		if err != nil {
			s.log.Error("invalid schedule, disabling it", "share_id", d.id, "error", err)
		}
		if _, err := s.pool.Exec(ctx, `UPDATE shares SET next_run_at = $2, last_run_at = now() WHERE id = $1`, d.id, next); err != nil {
			return err
		}
	}
	return nil
}
