// Package worker runs queued jobs: scans of shares (mounting them first when
// needed) and re-categorisation. It can run in its own container so that the
// web application never needs mount privileges.
package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/helmerznl/nas-storage-analyzer/internal/jobs"
	"github.com/helmerznl/nas-storage-analyzer/internal/mounts"
	"github.com/helmerznl/nas-storage-analyzer/internal/scanner"
	"github.com/helmerznl/nas-storage-analyzer/internal/scanrun"
	"github.com/helmerznl/nas-storage-analyzer/internal/shares"
	"github.com/helmerznl/nas-storage-analyzer/internal/store"
)

// Config tunes the worker.
type Config struct {
	ID         string
	Parallel   int           // jobs at the same time (default 2)
	PollEvery  time.Duration // queue polling interval (default 2 s)
	Beat       time.Duration // heartbeat / progress interval (default 2 s)
	DirTimeout time.Duration // see scanner.Options.DirTimeout (default 10 min)
	Loc        *time.Location
	Now        func() time.Time
}

// Worker executes jobs from the queue.
type Worker struct {
	st     *store.Store
	q      *jobs.Queue
	shares *shares.Service
	mounts *mounts.Manager
	cfg    Config
	log    *slog.Logger

	running   atomic.Int32 // jobs executing right now
	lastClaim atomic.Int64 // unix nanoseconds of the last claim attempt that reached the database
}

func New(st *store.Store, sh *shares.Service, m *mounts.Manager, cfg Config, log *slog.Logger) *Worker {
	if cfg.Parallel < 1 {
		cfg.Parallel = 2
	}
	if cfg.PollEvery <= 0 {
		cfg.PollEvery = 2 * time.Second
	}
	if cfg.Beat <= 0 {
		cfg.Beat = 2 * time.Second
	}
	if cfg.DirTimeout == 0 {
		cfg.DirTimeout = 10 * time.Minute
	}
	if cfg.Loc == nil {
		cfg.Loc = time.UTC
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	w := &Worker{st: st, q: jobs.NewQueue(st.Pool), shares: sh, mounts: m, cfg: cfg, log: log}
	w.lastClaim.Store(cfg.Now().UnixNano())
	return w
}

// Healthy reports whether the worker is doing its job: it reached the queue
// recently, or every slot is busy with a scan (those beat on their own, and
// the claim loop is then rightly waiting for a free slot).
func (w *Worker) Healthy() error {
	if int(w.running.Load()) >= w.cfg.Parallel {
		return nil
	}
	limit := 5 * w.cfg.PollEvery
	if limit < 30*time.Second {
		limit = 30 * time.Second
	}
	if age := w.cfg.Now().Sub(time.Unix(0, w.lastClaim.Load())); age > limit {
		return fmt.Errorf("the queue has not been reachable for %s", age.Round(time.Second))
	}
	return nil
}

// OpsLimit returns the operations-per-second limit that applies at time t:
// the day limit inside the day window, the base limit outside it. A window
// that ends before it starts (22 to 6) wraps over midnight.
func OpsLimit(in shares.Input, t time.Time, loc *time.Location) int {
	if in.MaxOpsDay <= 0 {
		return in.MaxOpsPerSec
	}
	h := t.In(loc).Hour()
	var day bool
	if in.DayStartHour <= in.DayEndHour {
		day = h >= in.DayStartHour && h < in.DayEndHour
	} else {
		day = h >= in.DayStartHour || h < in.DayEndHour
	}
	if day {
		return in.MaxOpsDay
	}
	return in.MaxOpsPerSec
}

// Run polls the queue until ctx ends, then waits for running jobs to stop.
func (w *Worker) Run(ctx context.Context) {
	w.log.Info("worker started", "id", w.cfg.ID, "parallel", w.cfg.Parallel)
	slots := make(chan struct{}, w.cfg.Parallel)
	var wg sync.WaitGroup
	reap := time.NewTicker(30 * time.Second)
	defer reap.Stop()
	for ctx.Err() == nil {
		select {
		case <-reap.C:
			if n, err := w.q.Reap(ctx, 2*time.Minute); err != nil && ctx.Err() == nil {
				w.log.Error("reap failed", "error", err)
			} else if n > 0 {
				w.log.Warn("failed jobs of a worker that stopped", "count", n)
			}
		default:
		}
		select {
		case slots <- struct{}{}:
		case <-ctx.Done():
			continue
		}
		job, err := w.q.Claim(ctx, w.cfg.ID)
		if err == nil {
			w.lastClaim.Store(w.cfg.Now().UnixNano())
		}
		if err != nil || job == nil {
			<-slots
			if err != nil && ctx.Err() == nil {
				w.log.Error("claim failed", "error", err)
			}
			select {
			case <-time.After(w.cfg.PollEvery):
			case <-ctx.Done():
			}
			continue
		}
		wg.Add(1)
		w.running.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-slots }()
			defer w.running.Add(-1)
			w.execute(ctx, job)
		}()
	}
	wg.Wait()
	w.log.Info("worker stopped", "id", w.cfg.ID)
}

// execute runs one job and records how it ended.
func (w *Worker) execute(ctx context.Context, job *jobs.Job) {
	log := w.log.With("job", job.ID, "kind", job.Kind, "share_id", job.ShareID)
	log.Info("job started", "trigger", job.Trigger, "mode", job.Mode)
	var state, msg string
	switch job.Kind {
	case jobs.KindScan:
		state, msg = w.runScan(ctx, job, log)
	case jobs.KindRecategorize:
		state, msg = w.runRecategorize(ctx, job)
	default:
		state, msg = jobs.StateFailed, "unknown job kind"
	}
	if state == jobs.StateFailed {
		w.auditFailure(log, job, msg)
	}
	done, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := w.q.Finish(done, job.ID, state, msg); err != nil {
		log.Error("cannot record the job result", "error", err)
	}
	log.Info("job finished", "state", state, "error", msg)
}

func (w *Worker) runRecategorize(ctx context.Context, job *jobs.Job) (string, string) {
	n, err := w.st.Recategorize(ctx, job.ShareID)
	if err != nil {
		return jobs.StateFailed, err.Error()
	}
	w.log.Info("recategorized", "share_id", job.ShareID, "files_changed", n)
	return jobs.StateDone, ""
}

type progressJSON struct {
	Dirs       int64    `json:"dirs"`
	ReusedDirs int64    `json:"reused_dirs"`
	Files      int64    `json:"files"`
	Errors     int64    `json:"errors"`
	Bytes      int64    `json:"bytes"`
	Alloc      int64    `json:"alloc"`
	CurrentDir string   `json:"current_dir"`
	FilesPerS  float64  `json:"files_per_sec"`
	Percent    *float64 `json:"percent"`
	ETASeconds *float64 `json:"eta_seconds"`
	Elapsed    float64  `json:"elapsed_seconds"`
	OpsLimit   int      `json:"ops_limit"`
}

func (w *Worker) runScan(ctx context.Context, job *jobs.Job, log *slog.Logger) (string, string) {
	f, err := w.shares.Load(ctx, job.ShareID)
	if err != nil {
		return jobs.StateFailed, err.Error()
	}
	path, cleanup, err := w.mounts.Prepare(ctx, f)
	if err != nil {
		return jobs.StateFailed, err.Error()
	}
	defer cleanup()

	force := job.Mode == "full"
	if !force && job.Mode == "auto" {
		if force, err = w.st.ShouldForceFull(ctx, f.ID); err != nil {
			return jobs.StateFailed, err.Error()
		}
	}
	th := scanner.NewThrottle(float64(OpsLimit(f.Input, w.cfg.Now(), w.cfg.Loc)))

	jobCtx, stop := context.WithCancel(ctx)
	defer stop()
	var mu sync.Mutex
	var scanID int64
	cancelled := false
	hbDone := make(chan struct{})
	go func() { // heartbeat: keeps the job alive, notices a cancel request, follows the day/night limit
		defer close(hbDone)
		t := time.NewTicker(w.cfg.Beat)
		defer t.Stop()
		for {
			select {
			case <-jobCtx.Done():
				return
			case <-t.C:
				c, err := w.q.Heartbeat(jobCtx, job.ID)
				if err == nil && c {
					mu.Lock()
					cancelled = true
					mu.Unlock()
					stop()
					return
				}
				th.SetRate(float64(OpsLimit(f.Input, w.cfg.Now(), w.cfg.Loc)))
			}
		}
	}()

	res, err := scanrun.Run(jobCtx, w.st, &store.Share{
		ID: f.ID, Name: f.Input.Name, Kind: f.Input.Kind, Path: path, Excludes: f.Input.Excludes, Workers: f.Input.Workers,
		MaxOpsPerSec: f.Input.MaxOpsPerSec,
	}, scanrun.Options{
		ForceFull: force, Trigger: job.Trigger, DirTimeout: w.cfg.DirTimeout, Throttle: th, ProgressEvery: w.cfg.Beat,
		OnStart: func(id int64) {
			mu.Lock()
			scanID = id
			mu.Unlock()
			_ = w.q.SetScan(ctx, job.ID, id)
		},
		OnProgress: func(p scanrun.Progress) {
			mu.Lock()
			id := scanID
			mu.Unlock()
			if id == 0 {
				return
			}
			pj := progressJSON{Dirs: p.Dirs, ReusedDirs: p.ReusedDirs, Files: p.Files, Errors: p.Errors, Bytes: p.Bytes, Alloc: p.Alloc,
				CurrentDir: p.CurrentDir, FilesPerS: p.FilesPerSec, Percent: p.Percent, Elapsed: p.Elapsed.Seconds(),
				OpsLimit: OpsLimit(f.Input, w.cfg.Now(), w.cfg.Loc)}
			if p.ETA != nil {
				s := p.ETA.Seconds()
				pj.ETASeconds = &s
			}
			b, _ := json.Marshal(pj)
			_, _ = w.st.Pool.Exec(context.Background(), `UPDATE scans SET progress = $2 WHERE id = $1 AND status = 'running'`, id, b)
		},
	})
	stop()
	<-hbDone

	mu.Lock()
	wasCancelled := cancelled
	mu.Unlock()
	switch {
	case err == nil:
		if _, perr := w.st.PruneHistory(context.Background(), f.ID); perr != nil {
			log.Error("pruning history failed", "error", perr)
		}
		_, _ = w.st.Pool.Exec(context.Background(), `UPDATE shares SET last_run_at = now() WHERE id = $1`, f.ID)
		log.Info("scan finished", "scan_id", res.ScanID, "mode", res.Mode, "files", res.Totals.Files, "bytes", res.Totals.Bytes,
			"errors", res.Progress.Errors, "seconds", res.Duration.Seconds())
		action, hint := scanOutcome(res.Totals, res.Progress.Errors)
		detail := jobDetail(job)
		detail["scan_id"], detail["mode"] = res.ScanID, res.Mode
		detail["files"], detail["dirs"], detail["bytes"] = res.Totals.Files, res.Totals.Dirs, res.Totals.Bytes
		detail["errors"], detail["seconds"] = res.Progress.Errors, float64(res.Duration.Round(100*time.Millisecond).Milliseconds())/1000
		if hint != "" {
			detail["hint"] = hint
			log.Warn("scan needs attention", "action", action, "hint", hint)
		}
		w.audit(log, action, f.Input.Name, detail)
		return jobs.StateDone, ""
	case wasCancelled:
		return jobs.StateCancelled, ""
	case errors.Is(err, context.Canceled):
		return jobs.StateFailed, "interrupted: the worker is shutting down"
	case errors.Is(err, store.ErrScanRunning):
		return jobs.StateFailed, "another scan of this share is already running"
	}
	return jobs.StateFailed, fmt.Sprint(err)
}

// scanOutcome says how a published scan went, as the audit-log action it is recorded under, and for the
// ones that need attention what to check. A scan that finds nothing is not an error for the scanner (a new,
// empty share is legitimate), so without this it would pass without a trace: a share folder that is not
// mounted, or a NASA_SHARES_DIR that points at the wrong folder, looks exactly like that.
func scanOutcome(t store.ScanTotals, readErrors int64) (action, hint string) {
	switch {
	case t.Files == 0 && t.Dirs <= 1:
		return "scan.empty", "The scan found no files. Is the share mounted on the host, and does the path point at the right " +
			"folder (NASA_SHARES_DIR, NASA_SHARES_MOUNT)? Use Browse in the share form to see what the container sees."
	case readErrors > 0:
		return "scan.errors", fmt.Sprintf("%d folders or files could not be read (permission denied, or a network share that stopped "+
			"answering). The scan is published without them; the dashboard lists them per scan.", readErrors)
	}
	return "scan.finished", ""
}

func jobDetail(job *jobs.Job) map[string]any {
	d := map[string]any{"job": job.ID, "trigger": job.Trigger}
	if job.RequestedBy != "" {
		d["requested_by"] = job.RequestedBy
	}
	return d
}

// auditFailure records why a job failed. The job's own error is kept in the jobs table and shown on the
// dashboard, but only the audit log is a place to look back through, and it is the one admins read.
func (w *Worker) auditFailure(log *slog.Logger, job *jobs.Job, msg string) {
	action := "scan.failed"
	if job.Kind != jobs.KindScan {
		action = "share.recategorize_failed"
	}
	target := fmt.Sprintf("share #%d", job.ShareID)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var name string
	if err := w.st.Pool.QueryRow(ctx, `SELECT name FROM shares WHERE id = $1`, job.ShareID).Scan(&name); err == nil {
		target = name
	}
	detail := jobDetail(job)
	detail["error"] = msg
	w.audit(log, action, target, detail)
}

// audit appends a job outcome to the audit log, as "worker" (nobody signed in did this). Writing it must
// never change what happened to the job, so a failure is only logged.
func (w *Worker) audit(log *slog.Logger, action, target string, detail map[string]any) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := w.st.Pool.Exec(ctx, `INSERT INTO audit_log (user_id, username, ip, action, target, detail) VALUES (NULL, 'worker', '', $1, $2, $3)`,
		action, target, detail); err != nil {
		log.Error("cannot write the audit log", "action", action, "error", err)
	}
}
