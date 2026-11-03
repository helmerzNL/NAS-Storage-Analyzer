package backup

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/helmerznl/nas-storage-analyzer/internal/auth"
	"github.com/helmerznl/nas-storage-analyzer/internal/jobs"
	"github.com/helmerznl/nas-storage-analyzer/internal/shares"
)

// replaceSteps is how many things the swap does, for the progress: the shares, the file types, the preferences, and the commit.
const replaceSteps = 4

// run is the restore, in the background. It reports a phase and a step in the job's row (so the screen can follow it from any page, and pick it
// up again after a reload), checks the file once more, and replaces everything in one transaction. Whatever happens, the job ends as done or
// failed; "failed" always means nothing was changed.
func (s *Service) run(jobID int64, actor auth.Actor, data []byte) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	defer func() {
		if r := recover(); r != nil {
			s.log.Error("restore panicked", "job", jobID, "panic", fmt.Sprint(r))
			s.fail(jobID, actor, errors.New("internal error"))
		}
	}()

	t0 := time.Now()
	s.progress(ctx, jobID, "checking", 0, 1)
	c, err := s.Check(ctx, data) // the file on the server is not trusted to be the one that was checked
	if err != nil {
		s.fail(jobID, actor, err)
		return
	}
	checkMS := int(time.Since(t0).Milliseconds())
	s.progress(ctx, jobID, "replacing", 0, replaceSteps)

	t1 := time.Now()
	ch, ids, err := s.replace(ctx, jobID, c, checkMS, t1)
	if err != nil {
		s.fail(jobID, actor, err)
		return
	}
	// The data has been replaced and the job says so: both happened in the same transaction. What follows is the part that cannot be: the
	// shares whose file types changed are categorised again by the worker, which is a queue of its own.
	n := s.recategorize(jobID, actor, ids)
	s.log.Info("backup restored", "job", jobID, "shares_added", len(ch.SharesAdded), "shares_updated", len(ch.SharesUpdated),
		"shares_removed", len(ch.SharesRemoved), "file_types_changed", ch.FileTypesChanged, "preferences", ch.PreferencesApplied,
		"checking_ms", checkMS, "replacing_ms", int(time.Since(t1).Milliseconds()), "recategorize", n)
	if err := s.audit.Audit(ctx, actor, "backup.restore", "", map[string]any{
		"job": jobID, "shares_added": len(ch.SharesAdded), "shares_updated": len(ch.SharesUpdated), "shares_removed": len(ch.SharesRemoved),
		"file_types_changed": ch.FileTypesChanged, "preferences_applied": ch.PreferencesApplied,
		"passwords_unreadable": len(ch.PasswordsUnreadable), "recategorize_shares": n}); err != nil {
		s.log.Error("audit write failed", "action", "backup.restore", "error", err)
	}
}

// progress records where the restore is. It uses the pool and not the transaction: the screen has to see it while the transaction is open.
func (s *Service) progress(ctx context.Context, id int64, phase string, step, steps int) {
	if _, err := s.pool.Exec(ctx, `UPDATE backup_jobs SET phase = $2, step = $3, steps = $4, updated_at = now() WHERE id = $1 AND status = 'running'`,
		id, phase, step, steps); err != nil {
		s.log.Warn("restore progress not recorded", "job", id, "error", err)
	}
}

// replace is the swap: one transaction, the job marked done inside it. Nothing the file said has been written if it returns an error.
func (s *Service) replace(ctx context.Context, jobID int64, c *Checked, checkMS int, started time.Time) (*Changes, []int64, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, nil, err
	}
	defer tx.Rollback(context.Background())

	// A statement that waits for a lock (a long export holds the tables) fails after a while instead of stalling the whole application behind
	// it. Nothing has been changed then, and the message says to try again.
	if _, err := tx.Exec(ctx, `SET LOCAL lock_timeout = '15s'`); err != nil {
		return nil, nil, err
	}
	// Shares are what the restore rewrites. Holding this lock keeps a scan from being queued (a job takes a share-row lock) and the scheduler from
	// touching a share while the swap is open; both go on afterwards, seeing the new shares.
	if _, err := tx.Exec(ctx, `LOCK TABLE shares IN EXCLUSIVE MODE`); err != nil {
		return nil, nil, err
	}
	busy, err := s.scansActive(ctx, tx)
	if err != nil {
		return nil, nil, err
	}
	if busy {
		return nil, nil, ErrBusy
	}

	ch := &Changes{SharesAdded: []string{}, SharesUpdated: []string{}, SharesRemoved: []string{}, PreferencesSkipped: []string{},
		PasswordsUnreadable: nonNil(c.Unreadable)}

	if c.File.Shares != nil {
		p, err := s.shares.ReplaceTx(ctx, tx, c.File.Shares)
		if err != nil {
			return nil, nil, err
		}
		ch.SharesAdded, ch.SharesUpdated, ch.SharesRemoved = p.Added, p.Updated, p.Removed
	}
	if err := s.step(ctx, jobID, "shares", 1); err != nil {
		return nil, nil, err
	}

	if c.File.FileTypes != nil {
		n, err := s.shares.ReplaceFileTypesTx(ctx, tx, c.File.FileTypes)
		if err != nil {
			return nil, nil, err
		}
		ch.FileTypesChanged = n
	}
	if err := s.step(ctx, jobID, "file_types", 2); err != nil {
		return nil, nil, err
	}

	if c.File.Preferences != nil {
		for _, p := range c.Prefs {
			exists, changed, err := auth.ReplacePrefsTx(ctx, tx, p.Username, p.Prefs)
			if err != nil {
				return nil, nil, err
			}
			switch {
			case !exists:
				ch.PreferencesSkipped = append(ch.PreferencesSkipped, p.Username)
			case changed:
				ch.PreferencesApplied++
			}
		}
	}
	if err := s.step(ctx, jobID, "preferences", 3); err != nil {
		return nil, nil, err
	}

	var ids []int64
	if ch.FileTypesChanged > 0 {
		if ids, err = shares.SharesWithData(ctx, tx); err != nil {
			return nil, nil, err
		}
		ch.RecategorizeShares = len(ids)
	}

	// Done is part of the swap: "the data was replaced" and "the job says done" cannot disagree.
	if _, err := tx.Exec(ctx, `UPDATE backup_jobs SET status = 'done', phase = 'replacing', step = $2, steps = $2, summary = $3, checking_ms = $4,
		replacing_ms = $5, finished_at = now(), updated_at = now() WHERE id = $1 AND status = 'running'`,
		jobID, replaceSteps, ch, checkMS, int(time.Since(started).Milliseconds())); err != nil {
		return nil, nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, nil, err
	}
	return ch, ids, nil
}

// step is called between the steps of the swap: it records the progress and lets a test make the swap fail halfway.
func (s *Service) step(ctx context.Context, jobID int64, name string, n int) error {
	s.progress(ctx, jobID, "replacing", n, replaceSteps)
	if s.hook != nil {
		return s.hook(name)
	}
	return nil
}

// recategorize queues the re-categorisation of the shares that have data, after a changed file-type map. It returns how many were queued.
func (s *Service) recategorize(jobID int64, actor auth.Actor, ids []int64) int {
	if len(ids) == 0 || s.queue == nil {
		return 0
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	n := 0
	for _, id := range ids {
		if _, err := s.queue.Enqueue(ctx, jobs.KindRecategorize, id, "auto", "backup", actor.Username); err == nil {
			n++
		} else if !errors.Is(err, jobs.ErrAlreadyActive) {
			s.log.Warn("could not queue a re-categorisation", "job", jobID, "share", id, "error", err)
		}
	}
	return n
}

// fail ends the job as failed. The cause is stored as a key and parameters, worded when read in the reader's language; an unexpected error is
// logged in full and shown as a generic sentence, never as an exception text.
func (s *Service) fail(jobID int64, actor auth.Actor, err error) {
	key, params := failure(err)
	if key == "failed" {
		s.log.Error("restore failed", "job", jobID, "error", err)
	} else {
		s.log.Warn("restore refused", "job", jobID, "reason", key, "error", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, e := s.pool.Exec(ctx, `UPDATE backup_jobs SET status = 'failed', error_key = $2, error_params = $3, finished_at = now(), updated_at = now()
		WHERE id = $1 AND status = 'running'`, jobID, key, params); e != nil {
		s.log.Error("could not record the failure of a restore", "job", jobID, "error", e)
	}
	if e := s.audit.Audit(ctx, actor, "backup.restore.failed", "", map[string]any{"job": jobID, "reason": key}); e != nil {
		s.log.Error("audit write failed", "action", "backup.restore.failed", "error", e)
	}
}

// failure maps an error to the key the screen words.
func failure(err error) (string, map[string]string) {
	var inv *InvalidError
	var pg *pgconn.PgError
	switch {
	case errors.As(err, &inv):
		p := map[string]string{"field": inv.Field, "detail": inv.Detail}
		for k, v := range inv.Params {
			p[k] = v
		}
		return "invalid", p
	case errors.Is(err, ErrBusy):
		return "busy", map[string]string{}
	case errors.As(err, &pg) && pg.Code == "55P03": // lock_not_available: the lock timeout of the swap
		return "locked", map[string]string{}
	default:
		return "failed", map[string]string{}
	}
}
