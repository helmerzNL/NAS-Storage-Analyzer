package scanner

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"strconv"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/helmerznl/nas-storage-analyzer/internal/categorize"
)

// Options configures one Walk.
type Options struct {
	Workers int
	// Excludes may be nil.
	Excludes *Excluder
	// Incremental allows a directory whose mtime and entry count match the
	// previous scan to keep its stored file rows. When false every directory
	// is read in full, but directory ids are still taken from Prior.
	Incremental bool
	// Throttle may be nil.
	Throttle *Throttle
	// DirTimeout aborts a directory when a single FS call blocks this long
	// (a dead NFS server). Zero disables the watchdog.
	DirTimeout time.Duration
	Mapper     *categorize.Mapper
	FS         FS
	// Prior may be nil (first scan).
	Prior    Prior
	IDs      IDAllocator
	Sink     Sink
	Progress *Progress
}

// Progress holds live counters. All methods are safe for concurrent use.
type Progress struct {
	Dirs       atomic.Int64
	ReusedDirs atomic.Int64
	FailedDirs atomic.Int64
	Files      atomic.Int64
	Bytes      atomic.Int64 // logical size
	Alloc      atomic.Int64 // allocated size
	Errors     atomic.Int64
	Symlinks   atomic.Int64
	OtherTypes atomic.Int64
	Excluded   atomic.Int64
	AtimeFiles atomic.Int64
	AtimeEq    atomic.Int64
	AtimePre   atomic.Int64
	current    atomic.Pointer[string]
	// unreadable is the folders that had a problem, for the audit log (see Unreadable).
	unreadable unreadableSet
}

// Current returns a recently visited directory, relative to the root.
func (p *Progress) Current() string {
	if s := p.current.Load(); s != nil {
		return *s
	}
	return ""
}

// Atime returns the atime/mtime relationship counters.
func (p *Progress) Atime() AtimeStats {
	return AtimeStats{Files: p.AtimeFiles.Load(), EqualMtime: p.AtimeEq.Load(), BeforeMtime: p.AtimePre.Load()}
}

const statBatch = 256

type task struct {
	rel         string
	name        string
	depth       int
	parentID    int64
	parentLPath string
	prior       *PriorDir
}

type queue struct {
	mu     sync.Mutex
	cond   *sync.Cond
	items  []task
	active int
	closed bool
}

func newQueue() *queue {
	q := &queue{}
	q.cond = sync.NewCond(&q.mu)
	return q
}

// pop returns the next task (LIFO keeps memory proportional to depth x
// breadth rather than tree size). It returns false when the walk is done.
func (q *queue) pop() (task, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for len(q.items) == 0 && q.active > 0 && !q.closed {
		q.cond.Wait()
	}
	if q.closed || len(q.items) == 0 {
		q.cond.Broadcast()
		return task{}, false
	}
	t := q.items[len(q.items)-1]
	q.items = q.items[:len(q.items)-1]
	q.active++
	return t, true
}

// finish marks a task done and enqueues its subdirectories.
func (q *queue) finish(sub []task) {
	q.mu.Lock()
	q.items = append(q.items, sub...)
	q.active--
	q.cond.Broadcast()
	q.mu.Unlock()
}

func (q *queue) close() {
	q.mu.Lock()
	q.closed = true
	q.cond.Broadcast()
	q.mu.Unlock()
}

type walker struct {
	o      Options
	cancel context.CancelCauseFunc
}

// Walk scans the tree below o.FS's root and reports to o.Sink. Recoverable
// problems (permission denied, vanished entries, timeouts) are reported to
// the Sink and the walk continues. Walk returns an error only for fatal
// conditions: an unreadable root, a failing Sink or a cancelled context.
func Walk(parent context.Context, o Options) error {
	if o.FS == nil || o.Sink == nil || o.IDs == nil || o.Mapper == nil {
		return errors.New("scanner: FS, Sink, IDs and Mapper are required")
	}
	if o.Workers < 1 {
		o.Workers = 1
	}
	if o.Progress == nil {
		o.Progress = &Progress{}
	}
	ctx, cancel := context.WithCancelCause(parent)
	defer cancel(nil)
	w := &walker{o: o, cancel: cancel}

	var rootPrior *PriorDir
	if o.Prior != nil {
		var err error
		if rootPrior, err = o.Prior.Root(ctx); err != nil {
			return fmt.Errorf("load previous root: %w", err)
		}
	}
	q := newQueue()
	q.items = []task{{prior: rootPrior}}

	go func() {
		<-ctx.Done()
		q.close()
	}()

	var wg sync.WaitGroup
	for i := 0; i < o.Workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w.worker(ctx, q)
		}()
	}
	wg.Wait()
	return context.Cause(ctx)
}

func (w *walker) worker(ctx context.Context, q *queue) {
	for {
		t, ok := q.pop()
		if !ok {
			return
		}
		sub := w.visitSafe(ctx, t)
		q.finish(sub)
	}
}

func (w *walker) visitSafe(ctx context.Context, t task) (sub []task) {
	defer func() {
		if r := recover(); r != nil {
			w.cancel(fmt.Errorf("scanner panic in %q: %v", t.rel, r))
			sub = nil
		}
	}()
	sub, err := w.visit(ctx, t)
	if err != nil {
		w.cancel(err)
		return nil
	}
	return sub
}

// guard tracks whether a worker is currently inside an FS call, so the
// watchdog can tell a hung syscall from throttle waits and slow batching.
type guard struct{ busySince atomic.Int64 }

func (g *guard) begin() { g.busySince.Store(time.Now().UnixNano()) }
func (g *guard) end()   { g.busySince.Store(0) }

type dirOutcome struct {
	mtime      time.Time
	entryCount int
	reused     bool
	files      []FileRec
	subdirs    []string
	errs       []ErrRec
	err        error
}

var errDirTimeout = errors.New("filesystem call timed out")

func (w *walker) visit(ctx context.Context, t task) ([]task, error) {
	id, lpath, depth := int64(0), "", t.depth
	if t.prior != nil {
		id, lpath, depth = t.prior.ID, t.prior.LPath, t.prior.Depth
	} else {
		var err error
		if id, err = w.o.IDs.Next(ctx); err != nil {
			return nil, fmt.Errorf("allocate directory id: %w", err)
		}
		lpath = strconv.FormatInt(id, 10)
		if t.parentLPath != "" {
			lpath = t.parentLPath + "." + lpath
		}
	}
	rel := t.rel
	w.o.Progress.current.Store(&rel)

	out := w.readDir(ctx, t)
	if ctx.Err() != nil {
		return nil, context.Cause(ctx)
	}
	for _, e := range out.errs {
		if err := w.report(ctx, e); err != nil {
			return nil, err
		}
	}

	rec := &DirRec{ID: id, ParentID: t.parentID, Name: sanitizeName(t.name), Depth: depth, LPath: lpath}
	if out.err != nil {
		if rel == "" {
			return nil, fmt.Errorf("scan root unreadable: %w", out.err)
		}
		rec.Failed = true
		if t.prior != nil {
			rec.Mtime, rec.EntryCount = t.prior.Mtime, t.prior.EntryCount
		} else {
			rec.Mtime = time.Unix(0, 0).UTC()
		}
		w.o.Progress.FailedDirs.Add(1)
		e := classify(rel, out.err)
		e.Dir = true // the folder itself could not be read, not one file in it
		if err := w.report(ctx, e); err != nil {
			return nil, err
		}
	} else {
		rec.Mtime = clampTime(out.mtime)
		rec.EntryCount = out.entryCount
		rec.Reused = out.reused
		rec.Files = out.files
		if out.reused {
			w.o.Progress.ReusedDirs.Add(1)
		}
		w.accountFiles(out.files)
	}
	w.o.Progress.Dirs.Add(1)
	if err := w.o.Sink.Dir(ctx, rec); err != nil {
		return nil, fmt.Errorf("sink: %w", err)
	}
	if rec.Failed {
		return nil, nil
	}

	var children map[string]PriorDir
	if t.prior != nil && w.o.Prior != nil && len(out.subdirs) > 0 {
		var err error
		if children, err = w.o.Prior.Children(ctx, id); err != nil {
			return nil, fmt.Errorf("load previous children of %q: %w", rel, err)
		}
	}
	claimed := map[int64]bool{}
	sub := make([]task, 0, len(out.subdirs))
	for _, name := range out.subdirs {
		ct := task{rel: join(rel, name), name: name, depth: depth + 1, parentID: id, parentLPath: lpath}
		if c, ok := children[sanitizeName(name)]; ok && !claimed[c.ID] {
			claimed[c.ID] = true
			c := c
			ct.prior = &c
		}
		sub = append(sub, ct)
	}
	return sub, nil
}

func (w *walker) accountFiles(files []FileRec) {
	p := w.o.Progress
	var bytes, alloc, eq, pre int64
	for i := range files {
		f := &files[i]
		bytes += f.Size
		alloc += f.Alloc
		switch {
		case f.Atime.Unix() == f.Mtime.Unix():
			eq++
		case f.Atime.Unix() < f.Mtime.Unix():
			pre++
		}
	}
	p.Files.Add(int64(len(files)))
	p.Bytes.Add(bytes)
	p.Alloc.Add(alloc)
	p.AtimeFiles.Add(int64(len(files)))
	p.AtimeEq.Add(eq)
	p.AtimePre.Add(pre)
}

func (w *walker) report(ctx context.Context, e ErrRec) error {
	w.o.Progress.Errors.Add(1)
	w.o.Progress.unreadable.add(e)
	if e.At.IsZero() {
		e.At = time.Now()
	}
	return w.o.Sink.Error(ctx, e)
}

// readDir runs readDirSync under the hung-call watchdog. A call that never
// returns leaks its goroutine (the kernel will not let it go), but the scan
// moves on.
func (w *walker) readDir(ctx context.Context, t task) dirOutcome {
	g := &guard{}
	if w.o.DirTimeout <= 0 {
		return w.readDirSync(ctx, t, g)
	}
	ch := make(chan dirOutcome, 1)
	go func() { ch <- w.readDirSync(ctx, t, g) }()
	tick := time.NewTicker(maxDuration(w.o.DirTimeout/4, 10*time.Millisecond))
	defer tick.Stop()
	for {
		select {
		case r := <-ch:
			return r
		case <-ctx.Done():
			return dirOutcome{err: context.Cause(ctx)}
		case <-tick.C:
			if s := g.busySince.Load(); s != 0 && time.Since(time.Unix(0, s)) > w.o.DirTimeout {
				return dirOutcome{err: errDirTimeout}
			}
		}
	}
}

func (w *walker) readDirSync(ctx context.Context, t task, g *guard) (out dirOutcome) {
	if err := w.o.Throttle.Wait(ctx, 1); err != nil {
		return dirOutcome{err: err}
	}
	g.begin()
	d, err := w.o.FS.Open(t.rel)
	g.end()
	if err != nil {
		return dirOutcome{err: err}
	}
	defer d.Close()

	g.begin()
	mtime, err := d.Stat()
	var entries []Entry
	if err == nil {
		entries, err = d.ReadEntries()
	}
	g.end()
	if err != nil {
		return dirOutcome{err: err}
	}

	out.mtime = mtime
	out.entryCount = len(entries)
	out.reused = w.o.Incremental && t.prior != nil &&
		t.prior.EntryCount == len(entries) && t.prior.Mtime.Equal(clampTime(mtime))

	var fileNames []string
	for _, e := range entries {
		child := join(t.rel, e.Name)
		if w.o.Excludes.Match(e.Name, child) {
			w.o.Progress.Excluded.Add(1)
			continue
		}
		switch e.Kind {
		case KindDir:
			out.subdirs = append(out.subdirs, e.Name)
		case KindFile:
			if !out.reused {
				fileNames = append(fileNames, e.Name)
			}
		case KindSymlink:
			w.o.Progress.Symlinks.Add(1)
		default:
			w.o.Progress.OtherTypes.Add(1)
		}
	}
	if len(fileNames) == 0 {
		return out
	}
	out.files = make([]FileRec, 0, len(fileNames))
	for start := 0; start < len(fileNames); start += statBatch {
		end := min(start+statBatch, len(fileNames))
		if err := w.o.Throttle.Wait(ctx, end-start); err != nil {
			return dirOutcome{err: err}
		}
		for _, name := range fileNames[start:end] {
			g.begin()
			st, err := d.Lstat(name)
			g.end()
			if err != nil {
				out.errs = append(out.errs, classify(join(t.rel, name), err))
				continue
			}
			if st.Kind != KindFile {
				// Replaced between getdents and lstat; not a regular file now.
				w.o.Progress.OtherTypes.Add(1)
				continue
			}
			out.files = append(out.files, w.fileRec(name, st))
		}
	}
	return out
}

func (w *walker) fileRec(name string, st FileStat) FileRec {
	clean := sanitizeName(name)
	ext := categorize.Ext(clean)
	f := FileRec{
		Name:     clean,
		Ext:      ext,
		Category: w.o.Mapper.Category(ext),
		Size:     st.Size,
		Alloc:    st.Blocks * 512,
		Mtime:    st.Mtime,
		Atime:    st.Atime,
		Ctime:    st.Ctime,
		Nlink:    st.Nlink,
	}
	if st.Nlink > 1 {
		f.Dev, f.Ino = st.Dev, st.Ino
	}
	// Clamping keeps PostgreSQL from rejecting absurd timestamps; it does not
	// change the ordering atime/mtime that accountFiles compares.
	f.Mtime, f.Atime, f.Ctime = clampTime(f.Mtime), clampTime(f.Atime), clampTime(f.Ctime)
	return f
}

func join(rel, name string) string {
	if rel == "" {
		return name
	}
	return rel + "/" + name
}

func maxDuration(a, b time.Duration) time.Duration {
	if a > b {
		return a
	}
	return b
}

func classify(rel string, err error) ErrRec {
	e := ErrRec{Path: rel, Msg: err.Error(), Kind: ErrOther}
	switch {
	case errors.Is(err, errDirTimeout):
		e.Kind = ErrTimeout
	case errors.Is(err, fs.ErrPermission), errors.Is(err, syscall.EACCES), errors.Is(err, syscall.EPERM):
		e.Kind = ErrPermission
	case errors.Is(err, fs.ErrNotExist), errors.Is(err, syscall.ENOENT), errors.Is(err, syscall.ENOTDIR):
		e.Kind = ErrVanished
	case errors.Is(err, syscall.ESTALE), errors.Is(err, syscall.EIO), errors.Is(err, syscall.ETIMEDOUT):
		e.Kind = ErrIO
	}
	return e
}
