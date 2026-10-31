package scanner

import (
	"context"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/helmerznl/nas-storage-analyzer/internal/categorize"
)

// memSink collects everything a walk reports.
type memSink struct {
	mu   sync.Mutex
	dirs []*DirRec
	errs []ErrRec
}

func (s *memSink) Dir(_ context.Context, d *DirRec) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.dirs = append(s.dirs, d)
	return nil
}

func (s *memSink) Error(_ context.Context, e ErrRec) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.errs = append(s.errs, e)
	return nil
}

// byRel indexes directory records by their path below the root, rebuilt from
// the parent chain.
func (s *memSink) byRel() map[string]*DirRec {
	byID := map[int64]*DirRec{}
	for _, d := range s.dirs {
		byID[d.ID] = d
	}
	out := map[string]*DirRec{}
	for _, d := range s.dirs {
		rel := ""
		for x := d; x.ParentID != 0; x = byID[x.ParentID] {
			if rel == "" {
				rel = x.Name
			} else {
				rel = x.Name + "/" + rel
			}
		}
		out[rel] = d
	}
	return out
}

func (s *memSink) fileNames() []string {
	var names []string
	for _, d := range s.dirs {
		for _, f := range d.Files {
			names = append(names, f.Name)
		}
	}
	sort.Strings(names)
	return names
}

func (s *memSink) errKinds() map[ErrKind]int {
	m := map[ErrKind]int{}
	for _, e := range s.errs {
		m[e.Kind]++
	}
	return m
}

type seqIDs struct{ n atomic.Int64 }

func (s *seqIDs) Next(context.Context) (int64, error) { return s.n.Add(1), nil }

// memPrior replays the records of an earlier walk.
type memPrior struct {
	root     *PriorDir
	children map[int64]map[string]PriorDir
}

func priorFrom(s *memSink) *memPrior {
	p := &memPrior{children: map[int64]map[string]PriorDir{}}
	for _, d := range s.dirs {
		pd := PriorDir{ID: d.ID, Mtime: d.Mtime, EntryCount: d.EntryCount, LPath: d.LPath, Depth: d.Depth}
		if d.ParentID == 0 {
			c := pd
			p.root = &c
			continue
		}
		if p.children[d.ParentID] == nil {
			p.children[d.ParentID] = map[string]PriorDir{}
		}
		p.children[d.ParentID][d.Name] = pd
	}
	return p
}

func (p *memPrior) Root(context.Context) (*PriorDir, error) { return p.root, nil }
func (p *memPrior) Children(_ context.Context, id int64) (map[string]PriorDir, error) {
	return p.children[id], nil
}

// faultFS wraps an FS and injects failures keyed by relative path.
type faultFS struct {
	FS
	openErr  map[string]error         // rel dir -> error from Open
	lstatErr map[string]error         // rel file -> error from Lstat
	hang     map[string]chan struct{} // rel dir -> Open blocks until closed
	lstats   atomic.Int64
	reads    atomic.Int64
	onOpen   func(rel string)
}

func (f *faultFS) Open(rel string) (Dir, error) {
	if f.onOpen != nil {
		f.onOpen(rel)
	}
	if ch, ok := f.hang[rel]; ok {
		<-ch
	}
	if err := f.openErr[rel]; err != nil {
		return nil, err
	}
	d, err := f.FS.Open(rel)
	if err != nil {
		return nil, err
	}
	f.reads.Add(1)
	return &faultDir{Dir: d, rel: rel, fs: f}, nil
}

type faultDir struct {
	Dir
	rel string
	fs  *faultFS
}

func (d *faultDir) Lstat(name string) (FileStat, error) {
	d.fs.lstats.Add(1)
	if err := d.fs.lstatErr[join(d.rel, name)]; err != nil {
		return FileStat{}, err
	}
	return d.Dir.Lstat(name)
}

func walkOpts(t *testing.T, fsys FS, sink Sink) Options {
	t.Helper()
	return Options{
		Workers: 4,
		FS:      fsys,
		Mapper:  categorize.DefaultMapper(),
		IDs:     &seqIDs{},
		Sink:    sink,
	}
}

func mustOSFS(t *testing.T, root string) FS {
	t.Helper()
	fsys, err := NewOSFS(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { fsys.Close() })
	return fsys
}

func within(t *testing.T, d time.Duration, f func()) {
	t.Helper()
	done := make(chan struct{})
	go func() { f(); close(done) }()
	select {
	case <-done:
	case <-time.After(d):
		t.Fatalf("did not finish within %v", d)
	}
}
