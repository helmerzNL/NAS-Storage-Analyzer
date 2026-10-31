// Package scanner walks a directory tree read-only and reports per-directory
// metadata to a Sink. It never reads file contents and has no code path that
// modifies the file system.
package scanner

import (
	"context"
	"strings"
	"time"
)

// FileRec is one regular file as seen during the walk.
type FileRec struct {
	Name     string
	Ext      string
	Category int16
	Size     int64 // st_size
	Alloc    int64 // st_blocks * 512
	Mtime    time.Time
	Atime    time.Time
	Ctime    time.Time
	Nlink    uint64
	// Dev and Ino are only set when Nlink > 1; they identify hardlink groups.
	Dev, Ino uint64
}

// DirRec is the outcome of visiting one directory.
type DirRec struct {
	ID       int64
	ParentID int64 // 0 for the scan root
	Name     string
	Depth    int
	LPath    string // ltree path: dot-separated directory ids from the root
	Mtime    time.Time
	// EntryCount is the number of raw directory entries (before excludes). It
	// is compared together with Mtime to decide whether a directory changed.
	EntryCount int
	// Reused: the directory looked unchanged, its files were not stat'ed and
	// the stored file rows stay valid. Files is empty.
	Reused bool
	// Failed: the directory could not be read. Stored data for it and for its
	// subtree is kept as it was. Files is empty.
	Failed bool
	Files  []FileRec
}

// ErrKind classifies a recoverable scan problem.
type ErrKind string

const (
	ErrPermission ErrKind = "permission"
	ErrVanished   ErrKind = "vanished"
	ErrTimeout    ErrKind = "timeout"
	ErrIO         ErrKind = "io"
	ErrOther      ErrKind = "other"
)

// ErrRec is a problem that was logged while the scan continued.
type ErrRec struct {
	Path string // relative to the scan root, "" for the root itself
	Kind ErrKind
	Msg  string
	At   time.Time
}

// Sink receives walk results. Implementations must be safe for concurrent use
// and may block to apply backpressure.
type Sink interface {
	Dir(ctx context.Context, d *DirRec) error
	Error(ctx context.Context, e ErrRec) error
}

// PriorDir is what the previous scan stored for a directory.
type PriorDir struct {
	ID         int64
	Mtime      time.Time
	EntryCount int
	LPath      string
	Depth      int
}

// Prior gives access to the previous scan's directory table. A nil Prior
// means "first scan". Prior ids are always reused so that directory ids stay
// stable, even for full scans.
type Prior interface {
	// Root returns the previous root directory, or nil.
	Root(ctx context.Context) (*PriorDir, error)
	// Children returns the previous subdirectories of dirID keyed by name.
	Children(ctx context.Context, dirID int64) (map[string]PriorDir, error)
}

// IDAllocator hands out new directory ids.
type IDAllocator interface {
	Next(ctx context.Context) (int64, error)
}

// sanitizeName makes a file name safe for PostgreSQL text columns: invalid
// UTF-8 bytes become U+FFFD. The database never uses names to reopen files.
func sanitizeName(s string) string {
	if !strings.ContainsRune(s, 0) {
		return strings.ToValidUTF8(s, "�")
	}
	return strings.ToValidUTF8(strings.ReplaceAll(s, "\x00", ""), "�")
}

// clampTime keeps timestamps inside the range PostgreSQL stores without
// error. Some SMB servers report dates far beyond year 9999.
func clampTime(t time.Time) time.Time {
	const lo, hi = int64(-2208988800), int64(7258118400) // 1900-01-01, 2200-01-01
	if u := t.Unix(); u < lo {
		return time.Unix(lo, 0).UTC()
	} else if u > hi {
		return time.Unix(hi, 0).UTC()
	}
	return t.UTC().Truncate(time.Microsecond)
}
