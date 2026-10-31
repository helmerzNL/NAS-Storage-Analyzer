package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"github.com/jackc/pgx/v5"

	"github.com/helmerznl/nas-storage-analyzer/internal/scanner"
)

// Batch thresholds are variables so tests can force many small batches.
var (
	flushFiles = 50_000
	flushDirs  = 20_000
)

const maxStoredErrs = 10_000

type stageFile struct {
	dirID int64
	scanner.FileRec
}

type batch struct {
	dirs  []*scanner.DirRec
	files []stageFile
}

// ScanWriter implements scanner.Sink. Rows are COPY'd in batches into
// connection-local staging tables; nothing becomes visible to readers until
// Commit publishes the scan in one transaction. A scan that is aborted,
// cancelled or fails leaves the previously published data untouched.
type ScanWriter struct {
	s       *Store
	conn    *pgx.Conn
	shareID int64
	scanID  int64

	mu    sync.Mutex
	dirs  []*scanner.DirRec
	files []stageFile

	batches chan batch
	wdone   chan struct{}
	closed  bool // guarded by mu

	// The COPY goroutine reports failures through its own lock: Dir holds mu
	// while it waits for the goroutine to make room in batches.
	errMu sync.Mutex
	werr  error

	emu       sync.Mutex
	errBuf    []scanner.ErrRec
	errStored int
}

// NewScanWriter opens a dedicated connection and creates the staging tables.
func (s *Store) NewScanWriter(ctx context.Context, shareID, scanID int64) (*ScanWriter, error) {
	conn, err := pgx.ConnectConfig(ctx, s.Pool.Config().ConnConfig)
	if err != nil {
		return nil, err
	}
	_, err = conn.Exec(ctx, `
		CREATE TEMP TABLE stage_dirs (
			id bigint, parent_id bigint, name text, depth int, path text,
			mtime timestamptz, entry_count int, reused boolean, failed boolean);
		CREATE TEMP TABLE stage_files (
			dir_id bigint, name text, ext text, category_id smallint, size bigint, alloc bigint,
			mtime timestamptz, atime timestamptz, ctime timestamptz, nlink int, dev bigint, ino bigint)`)
	if err != nil {
		conn.Close(ctx)
		return nil, err
	}
	w := &ScanWriter{s: s, conn: conn, shareID: shareID, scanID: scanID,
		batches: make(chan batch, 3), wdone: make(chan struct{})}
	go w.run()
	return w, nil
}

var dirCols = []string{"id", "parent_id", "name", "depth", "path", "mtime", "entry_count", "reused", "failed"}
var fileCols = []string{"dir_id", "name", "ext", "category_id", "size", "alloc", "mtime", "atime", "ctime", "nlink", "dev", "ino"}

func (w *ScanWriter) run() {
	defer close(w.wdone)
	for b := range w.batches {
		if w.loadErr() != nil {
			continue // drain
		}
		ctx := context.Background()
		_, err := w.conn.CopyFrom(ctx, pgx.Identifier{"stage_dirs"}, dirCols,
			pgx.CopyFromSlice(len(b.dirs), func(i int) ([]any, error) {
				d := b.dirs[i]
				var parent any
				if d.ParentID != 0 {
					parent = d.ParentID
				}
				return []any{d.ID, parent, d.Name, int32(d.Depth), d.LPath, d.Mtime, int32(d.EntryCount), d.Reused, d.Failed}, nil
			}))
		if err == nil {
			_, err = w.conn.CopyFrom(ctx, pgx.Identifier{"stage_files"}, fileCols,
				pgx.CopyFromSlice(len(b.files), func(i int) ([]any, error) {
					f := &b.files[i]
					var dev, ino any
					if f.Nlink > 1 {
						dev, ino = int64(f.Dev), int64(f.Ino) // bit pattern; only compared for equality
					}
					return []any{f.dirID, f.Name, f.Ext, f.Category, f.Size, f.Alloc, f.Mtime, f.Atime, f.Ctime,
						int32(min(f.Nlink, 1<<30)), dev, ino}, nil
				}))
		}
		if err != nil {
			w.setErr(fmt.Errorf("copy batch: %w", err))
		}
	}
}

func (w *ScanWriter) loadErr() error { w.errMu.Lock(); defer w.errMu.Unlock(); return w.werr }
func (w *ScanWriter) setErr(err error) {
	w.errMu.Lock()
	if w.werr == nil {
		w.werr = err
	}
	w.errMu.Unlock()
}

// Dir implements scanner.Sink.
func (w *ScanWriter) Dir(ctx context.Context, d *scanner.DirRec) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := w.loadErr(); err != nil {
		return err
	}
	if w.closed {
		return errors.New("scan writer closed")
	}
	w.dirs = append(w.dirs, d)
	for _, f := range d.Files {
		w.files = append(w.files, stageFile{dirID: d.ID, FileRec: f})
	}
	if len(w.files) >= flushFiles || len(w.dirs) >= flushDirs {
		return w.dispatchLocked(ctx)
	}
	return nil
}

// dispatchLocked hands the buffer to the COPY goroutine. It blocks when the
// goroutine is behind, which throttles the scanner to the database's speed.
func (w *ScanWriter) dispatchLocked(ctx context.Context) error {
	if len(w.dirs) == 0 && len(w.files) == 0 {
		return nil
	}
	b := batch{dirs: w.dirs, files: w.files}
	w.dirs, w.files = nil, nil
	select {
	case w.batches <- b:
		return nil
	case <-ctx.Done():
		return context.Cause(ctx)
	}
}

// Error implements scanner.Sink. At most maxStoredErrs rows are kept per scan;
// the scan's error_count still reflects every error.
func (w *ScanWriter) Error(ctx context.Context, e scanner.ErrRec) error {
	w.emu.Lock()
	defer w.emu.Unlock()
	if w.errStored+len(w.errBuf) >= maxStoredErrs {
		return nil
	}
	w.errBuf = append(w.errBuf, e)
	if len(w.errBuf) >= 500 {
		return w.flushErrorsLocked(ctx)
	}
	return nil
}

func (w *ScanWriter) flushErrorsLocked(ctx context.Context) error {
	if len(w.errBuf) == 0 {
		return nil
	}
	buf := w.errBuf
	w.errBuf = nil
	_, err := w.s.Pool.CopyFrom(ctx, pgx.Identifier{"scan_errors"}, []string{"scan_id", "path", "kind", "message", "at"},
		pgx.CopyFromSlice(len(buf), func(i int) ([]any, error) {
			e := buf[i]
			return []any{w.scanID, sanitizeText(e.Path), string(e.Kind), sanitizeText(e.Msg), e.At}, nil
		}))
	w.errStored += len(buf)
	return err
}

func sanitizeText(s string) string {
	b := make([]rune, 0, len(s))
	for _, r := range s {
		if r != 0 {
			b = append(b, r)
		}
	}
	return string(b)
}

// Summary carries run statistics into the scans row at commit time.
type Summary struct {
	ReusedDirs   int64
	ErrorCount   int64
	AtimeQuality any // JSON-serialisable
	// FsTotal and FsFree are the capacity and free space of the file system
	// holding the share (statfs); nil when unknown.
	FsTotal, FsFree *int64
}

// drain flushes buffers and waits for the COPY goroutine.
func (w *ScanWriter) drain(ctx context.Context) error {
	w.mu.Lock()
	err := w.dispatchLocked(ctx)
	if !w.closed {
		w.closed = true
		close(w.batches)
	}
	w.mu.Unlock()
	<-w.wdone
	if err != nil {
		return err
	}
	return w.loadErr()
}

// Commit publishes the scan: staged rows replace the stored state of the
// visited directories, vanished directories are removed, and all aggregates
// are recomputed, in a single transaction.
func (w *ScanWriter) Commit(ctx context.Context, sum Summary) (ScanTotals, error) {
	var tot ScanTotals
	if err := w.drain(ctx); err != nil {
		return tot, err
	}
	w.emu.Lock()
	err := w.flushErrorsLocked(ctx)
	w.emu.Unlock()
	if err != nil {
		return tot, fmt.Errorf("store scan errors: %w", err)
	}
	aq, _ := json.Marshal(sum.AtimeQuality)

	tx, err := w.conn.Begin(ctx)
	if err != nil {
		return tot, err
	}
	defer tx.Rollback(context.Background())
	if err := mergeScan(ctx, tx, w.shareID, w.scanID); err != nil {
		return tot, err
	}
	if err := aggregate(ctx, tx, w.shareID, false); err != nil {
		return tot, err
	}
	err = tx.QueryRow(ctx, `
		SELECT 1 + dirs_total, files_total, size_total, alloc_total FROM dirs
		WHERE share_id = $1 AND parent_id IS NULL`, w.shareID).Scan(&tot.Dirs, &tot.Files, &tot.Bytes, &tot.Alloc)
	if err != nil {
		return tot, fmt.Errorf("read root totals: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE scans SET status = 'succeeded', finished_at = now(), dirs = $2, reused_dirs = $3, files = $4,
			bytes = $5, alloc_bytes = $6, error_count = $7, atime_quality = $8,
			fs_total_bytes = $9, fs_free_bytes = $10
		WHERE id = $1`, w.scanID, tot.Dirs, sum.ReusedDirs, tot.Files, tot.Bytes, tot.Alloc, sum.ErrorCount, aq,
		sum.FsTotal, sum.FsFree); err != nil {
		return tot, err
	}
	if _, err := tx.Exec(ctx, `UPDATE shares SET last_scan_id = $2 WHERE id = $1`, w.shareID, w.scanID); err != nil {
		return tot, err
	}
	return tot, tx.Commit(ctx)
}

// Abort discards everything staged. Errors collected so far stay in
// scan_errors so the failed scan can be diagnosed.
func (w *ScanWriter) Abort(ctx context.Context) {
	_ = w.drain(ctx)
	w.emu.Lock()
	_ = w.flushErrorsLocked(context.Background())
	w.emu.Unlock()
	w.Close()
}

// Close releases the dedicated connection (and with it the staging tables).
func (w *ScanWriter) Close() {
	w.mu.Lock()
	if !w.closed {
		w.closed = true
		close(w.batches)
	}
	w.mu.Unlock()
	<-w.wdone
	w.conn.Close(context.Background())
}

// SetBatchSizes overrides the COPY batch thresholds and returns a function
// that restores them. It exists so tests can exercise multi-batch paths with
// small trees.
func SetBatchSizes(files, dirs int) (restore func()) {
	of, od := flushFiles, flushDirs
	flushFiles, flushDirs = files, dirs
	return func() { flushFiles, flushDirs = of, od }
}
