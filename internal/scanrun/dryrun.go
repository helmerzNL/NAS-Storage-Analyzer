package scanrun

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/helmerznl/nas-storage-analyzer/internal/categorize"
	"github.com/helmerznl/nas-storage-analyzer/internal/scanner"
	"github.com/helmerznl/nas-storage-analyzer/internal/store"
)

// DryRunResult holds the numbers of a scan that touched no database.
type DryRunResult struct {
	Dirs, Files   int64 // Files counts hardlinked inodes once
	Bytes, Alloc  int64
	Errors        int64
	Symlinks      int64
	ByCategory    map[int16]CategoryTotal
	Atime         scanner.AtimeQuality
	Duration      time.Duration
	HardlinkDupes int64
}

type CategoryTotal struct{ Files, Bytes int64 }

type dryRunSink struct {
	mu     sync.Mutex
	seen   map[[2]uint64]bool
	res    *DryRunResult
	errors atomic.Int64
}

func (s *dryRunSink) Dir(_ context.Context, d *scanner.DirRec) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.res.Dirs++
	for i := range d.Files {
		f := &d.Files[i]
		if f.Nlink > 1 {
			k := [2]uint64{f.Dev, f.Ino}
			if s.seen[k] {
				s.res.HardlinkDupes++
				continue
			}
			s.seen[k] = true
		}
		s.res.Files++
		s.res.Bytes += f.Size
		s.res.Alloc += f.Alloc
		c := s.res.ByCategory[f.Category]
		c.Files++
		c.Bytes += f.Size
		s.res.ByCategory[f.Category] = c
	}
	return nil
}

func (s *dryRunSink) Error(context.Context, scanner.ErrRec) error { return nil }

// DryRun walks path and returns totals without any database. It uses the
// built-in extension mapping.
func DryRun(ctx context.Context, path string, excludes []string, workers int, maxOps float64, dirTimeout time.Duration, onProgress func(Progress)) (*DryRunResult, error) {
	started := time.Now()
	excl, err := scanner.NewExcluder(excludes)
	if err != nil {
		return nil, err
	}
	fsys, err := scanner.NewOSFS(path)
	if err != nil {
		return nil, err
	}
	defer fsys.Close()

	res := &DryRunResult{ByCategory: map[int16]CategoryTotal{}}
	sink := &dryRunSink{seen: map[[2]uint64]bool{}, res: res}
	prog := &scanner.Progress{}
	ids := &counterIDs{}
	stop := startReporter(Options{OnProgress: onProgress}, func() Progress { return snap(prog, started, false, store.ScanTotals{}) })
	err = scanner.Walk(ctx, scanner.Options{
		Workers: workers, Excludes: excl, Throttle: scanner.NewThrottle(maxOps), DirTimeout: dirTimeout,
		Mapper: categorize.DefaultMapper(), FS: fsys, IDs: ids, Sink: sink, Progress: prog,
	})
	stop()
	res.Errors = prog.Errors.Load()
	res.Symlinks = prog.Symlinks.Load()
	res.Atime = scanner.AssessAtime(prog.Atime(), mountOf(path))
	res.Duration = time.Since(started)
	return res, err
}

type counterIDs struct{ n atomic.Int64 }

func (c *counterIDs) Next(context.Context) (int64, error) { return c.n.Add(1), nil }
