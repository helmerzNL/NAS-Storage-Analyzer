package scanner

import (
	"time"
)

// EntryKind is the type of a directory entry as reported by getdents.
type EntryKind uint8

const (
	KindFile EntryKind = iota
	KindDir
	KindSymlink
	KindOther
)

// Entry is a directory entry without any stat information.
type Entry struct {
	Name string
	Kind EntryKind
}

// FileStat is the subset of lstat the scanner needs.
type FileStat struct {
	Kind   EntryKind
	Size   int64
	Blocks int64 // 512-byte units
	Mtime  time.Time
	Atime  time.Time
	Ctime  time.Time
	Nlink  uint64
	Dev    uint64
	Ino    uint64
}

// Dir is an open directory. All methods are read-only.
type Dir interface {
	// Stat returns the directory's own mtime.
	Stat() (mtime time.Time, err error)
	ReadEntries() ([]Entry, error)
	// Lstat stats name inside the directory without following symlinks.
	Lstat(name string) (FileStat, error)
	Close() error
}

// FS opens directories below a fixed root. It is an interface so tests can
// inject permission errors, hangs and vanishing files.
type FS interface {
	// Open opens the directory at rel, a slash-separated path relative to the
	// root ("" is the root). Intermediate symlinks are not followed.
	Open(rel string) (Dir, error)
	Close() error
}
