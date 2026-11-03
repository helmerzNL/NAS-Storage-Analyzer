// Package scanrun orchestrates one scan of one share: planning, walking,
// publishing and bookkeeping. The CLI and the worker both call Run.
package scanrun

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/unix"

	"github.com/helmerznl/nas-storage-analyzer/internal/scanner"
	"github.com/helmerznl/nas-storage-analyzer/internal/store"
)

// Options tunes a single run. Zero values pick sensible defaults.
type Options struct {
	ForceFull  bool
	Trigger    string        // "manual", "schedule", "cli"
	DirTimeout time.Duration // see scanner.Options.DirTimeout
	// OnProgress receives a snapshot every ProgressEvery (default 2s).
	OnProgress    func(Progress)
	ProgressEvery time.Duration
	// FS replaces the real file system (tests).
	FS scanner.FS
	// AllowEmpty publishes a scan that found nothing even though the previous
	// one found files; without it that is refused (see ErrEmptyShare).
	AllowEmpty bool
	// OnStart is called with the new scan's id as soon as it exists.
	OnStart func(scanID int64)
	// Throttle replaces the one built from the share's max_ops_per_sec, so the
	// caller can change the rate while the scan runs.
	Throttle *scanner.Throttle
}

// emptyGuardFiles is how many files the previous scan must have seen before an
// empty result is treated as a missing mount rather than a cleared share.
const emptyGuardFiles = 100

// ErrEmptyShare is returned when a share that held files now shows an empty
// root. The usual cause is a network share that is no longer mounted, which
// leaves an empty mount point; publishing that would erase the history.
var ErrEmptyShare = errors.New("the share is empty, but the previous scan found files: is it still mounted? " +
	"Nothing was changed. If it was emptied on purpose, scan once with `nasa scan -allow-empty`")

// Progress is a live view of a running scan.
type Progress struct {
	Dirs, ReusedDirs, Files, Errors int64
	Bytes, Alloc                    int64
	CurrentDir                      string
	Elapsed                         time.Duration
	FilesPerSec                     float64
	// Percent and ETA are nil when unknown (first scan of a share has no
	// previous totals to compare with).
	Percent *float64
	ETA     *time.Duration
}

// Result summarises a finished scan.
type Result struct {
	ScanID   int64
	Mode     string
	Totals   store.ScanTotals
	Progress Progress
	Atime    scanner.AtimeQuality
	Duration time.Duration
	// Unreadable is the folders the scan could not read completely: the first MaxUnreadableListed of them, and how many there are.
	Unreadable scanner.UnreadableReport
}

// MaxUnreadableListed is how many unreadable folders a finished scan names. The audit log shows an overview to act on, not
// a file per line (the dashboard lists every error of a scan); the total says how many there are.
const MaxUnreadableListed = 50

// Run scans share and publishes the result. On failure or cancellation the
// previously published data of the share is left untouched.
func Run(ctx context.Context, st *store.Store, share *store.Share, o Options) (*Result, error) {
	started := time.Now()
	if o.Trigger == "" {
		o.Trigger = "manual"
	}
	excl, err := scanner.NewExcluder(share.Excludes)
	if err != nil {
		return nil, err
	}
	hash := store.ExcludesHash(share.Excludes)
	plan, err := st.PlanScan(ctx, share.ID, o.ForceFull, hash)
	if err != nil {
		return nil, err
	}
	mapper, err := st.LoadMapper(ctx)
	if err != nil {
		return nil, err
	}
	fsys := o.FS
	if fsys == nil {
		if fsys, err = scanner.NewOSFS(share.Path); err != nil {
			return nil, fmt.Errorf("open share path: %w", err)
		}
		defer fsys.Close()
	}
	scanID, err := st.BeginScan(ctx, share.ID, plan.Mode, o.Trigger, hash)
	if err != nil {
		return nil, err
	}
	if o.OnStart != nil {
		o.OnStart(scanID)
	}
	fail := func(cause error, errCount int64) error {
		// The caller's context may be the reason for failing; record on a fresh one.
		bg, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = st.FailScan(bg, scanID, errors.Is(cause, context.Canceled), cause, errCount)
		return cause
	}

	w, err := st.NewScanWriter(ctx, share.ID, scanID)
	if err != nil {
		return nil, fail(err, 0)
	}
	defer w.Close()

	var prior scanner.Prior
	if plan.HasPrior {
		prior = st.NewPrior(share.ID)
	}
	prev, hasPrev, err := st.PreviousTotals(ctx, share.ID)
	if err != nil {
		return nil, fail(err, 0)
	}

	prog := &scanner.Progress{}
	snapshot := func() Progress { return snap(prog, started, hasPrev, prev) }
	stopReport := startReporter(o, snapshot)

	werr := scanner.Walk(ctx, scanner.Options{
		Workers:     share.Workers,
		Excludes:    excl,
		Incremental: plan.Mode == "incremental",
		Throttle:    throttleFor(o, share),
		DirTimeout:  o.DirTimeout,
		Mapper:      mapper,
		FS:          fsys,
		Prior:       prior,
		IDs:         st.NewDirIDs(),
		Sink:        w,
		Progress:    prog,
	})
	stopReport()
	if werr != nil {
		w.Abort(context.Background())
		return nil, fail(werr, prog.Errors.Load())
	}

	if hasPrev && prev.Files >= emptyGuardFiles && !o.AllowEmpty && prog.Files.Load() == 0 && prog.Dirs.Load() <= 1 {
		w.Abort(context.Background())
		return nil, fail(ErrEmptyShare, prog.Errors.Load())
	}

	quality := scanner.AssessAtime(prog.Atime(), mountOf(share.Path))
	if sample := prog.Atime().Files; sample < scanner.MinAtimeSample && plan.HasPrior {
		// An incremental scan that reused nearly everything saw too few files to
		// judge atime; keep the verdict of an earlier scan rather than degrade it.
		if raw, ok, err := st.PreviousAtimeQuality(ctx, share.ID); err == nil && ok {
			var prevQ scanner.AtimeQuality
			if json.Unmarshal(raw, &prevQ) == nil {
				prevQ.CarriedOver = true
				quality = prevQ
			}
		}
	}
	fsTotal, fsFree := fsCapacity(share.Path)
	tot, err := w.Commit(ctx, store.Summary{
		ReusedDirs: prog.ReusedDirs.Load(), ErrorCount: prog.Errors.Load(), AtimeQuality: quality,
		FsTotal: fsTotal, FsFree: fsFree,
	})
	if err != nil {
		return nil, fail(fmt.Errorf("publish scan: %w", err), prog.Errors.Load())
	}
	return &Result{ScanID: scanID, Mode: plan.Mode, Totals: tot, Progress: snapshot(), Atime: quality,
		Duration: time.Since(started), Unreadable: prog.Unreadable(MaxUnreadableListed)}, nil
}

func startReporter(o Options, snapshot func() Progress) (stop func()) {
	if o.OnProgress == nil {
		return func() {}
	}
	every := o.ProgressEvery
	if every <= 0 {
		every = 2 * time.Second
	}
	done, finished := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(finished)
		t := time.NewTicker(every)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				o.OnProgress(snapshot())
			case <-done:
				return
			}
		}
	}()
	return func() { close(done); <-finished }
}

func snap(p *scanner.Progress, started time.Time, hasPrev bool, prev store.ScanTotals) Progress {
	el := time.Since(started)
	s := Progress{
		Dirs: p.Dirs.Load(), ReusedDirs: p.ReusedDirs.Load(), Files: p.Files.Load(), Errors: p.Errors.Load(),
		Bytes: p.Bytes.Load(), Alloc: p.Alloc.Load(), CurrentDir: p.Current(), Elapsed: el,
	}
	if el > 0 {
		s.FilesPerSec = float64(s.Files) / el.Seconds()
	}
	if hasPrev && prev.Dirs > 0 {
		f := float64(s.Dirs) / float64(prev.Dirs)
		if f > 1 {
			f = 1
		}
		pct := f * 100
		s.Percent = &pct
		if f >= 0.02 && f < 1 {
			eta := time.Duration(float64(el) * (1 - f) / f)
			s.ETA = &eta
		}
	}
	return s
}

// mountOf finds the mount a share path lives on; nil if unknown.
func mountOf(path string) *scanner.MountInfo {
	abs, err := filepath.EvalSymlinks(path)
	if err != nil {
		return nil
	}
	f, err := os.Open("/proc/self/mountinfo")
	if err != nil {
		return nil
	}
	defer f.Close()
	mi, err := scanner.FindMount(abs, f)
	if err != nil {
		return nil
	}
	return mi
}

// fsCapacity returns the size and free space (available to unprivileged
// users) of the file system holding path, or nils when statfs fails.
func fsCapacity(path string) (total, free *int64) {
	var st unix.Statfs_t
	if err := unix.Statfs(path, &st); err != nil {
		return nil, nil
	}
	t, f := int64(st.Blocks)*int64(st.Bsize), int64(st.Bavail)*int64(st.Bsize)
	return &t, &f
}

func throttleFor(o Options, share *store.Share) *scanner.Throttle {
	if o.Throttle != nil {
		return o.Throttle
	}
	return scanner.NewThrottle(float64(share.MaxOpsPerSec))
}
