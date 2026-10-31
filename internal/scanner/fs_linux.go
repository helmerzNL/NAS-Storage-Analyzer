package scanner

import (
	"fmt"
	"os"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

// chunkLimit bounds how many path bytes one openat call receives. PATH_MAX is
// 4096; walking in chunks lets the scanner enter trees whose absolute path is
// longer than that.
const chunkLimit = 3000

type osFS struct {
	rootFD int
}

// NewOSFS opens root and returns an FS rooted there. root itself may be a
// symlink (a bind-mounted share path); nothing below it is followed.
func NewOSFS(root string) (FS, error) {
	fd, err := unix.Open(root, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: root, Err: err}
	}
	return &osFS{rootFD: fd}, nil
}

func (f *osFS) Close() error { return unix.Close(f.rootFD) }

func (f *osFS) Open(rel string) (Dir, error) {
	rel = strings.Trim(rel, "/")
	fd, err := unix.Dup(f.rootFD)
	if err != nil {
		return nil, err
	}
	if rel != "" {
		parts := strings.Split(rel, "/")
		for len(parts) > 0 {
			n, size := 0, 0
			for n < len(parts) && (n == 0 || size+len(parts[n])+1 <= chunkLimit) {
				size += len(parts[n]) + 1
				n++
			}
			next, err := unix.Openat(fd, strings.Join(parts[:n], "/"),
				unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
			unix.Close(fd)
			if err != nil {
				return nil, err
			}
			fd = next
			parts = parts[n:]
		}
	}
	// os.File gives buffered getdents with d_type. Naming it after /proc/self/fd
	// makes the DT_UNKNOWN lstat fallback work for any path length.
	file := os.NewFile(uintptr(fd), fmt.Sprintf("/proc/self/fd/%d", fd))
	if file == nil {
		unix.Close(fd)
		return nil, fmt.Errorf("invalid fd %d", fd)
	}
	return &osDir{f: file, fd: fd}, nil
}

type osDir struct {
	f  *os.File
	fd int
}

func (d *osDir) Close() error { return d.f.Close() }

func (d *osDir) Stat() (time.Time, error) {
	var st unix.Stat_t
	if err := unix.Fstat(d.fd, &st); err != nil {
		return time.Time{}, err
	}
	return time.Unix(int64(st.Mtim.Sec), int64(st.Mtim.Nsec)), nil
}

func (d *osDir) ReadEntries() ([]Entry, error) {
	des, err := d.f.ReadDir(-1)
	out := make([]Entry, 0, len(des))
	for _, de := range des {
		k := KindOther
		switch t := de.Type(); {
		case t.IsRegular():
			k = KindFile
		case t.IsDir():
			k = KindDir
		case t&os.ModeSymlink != 0:
			k = KindSymlink
		}
		out = append(out, Entry{Name: de.Name(), Kind: k})
	}
	return out, err
}

func (d *osDir) Lstat(name string) (FileStat, error) {
	var st unix.Stat_t
	if err := unix.Fstatat(d.fd, name, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return FileStat{}, err
	}
	k := KindOther
	switch st.Mode & unix.S_IFMT {
	case unix.S_IFREG:
		k = KindFile
	case unix.S_IFDIR:
		k = KindDir
	case unix.S_IFLNK:
		k = KindSymlink
	}
	return FileStat{
		Kind:   k,
		Size:   st.Size,
		Blocks: st.Blocks,
		Mtime:  time.Unix(int64(st.Mtim.Sec), int64(st.Mtim.Nsec)),
		Atime:  time.Unix(int64(st.Atim.Sec), int64(st.Atim.Nsec)),
		Ctime:  time.Unix(int64(st.Ctim.Sec), int64(st.Ctim.Nsec)),
		Nlink:  uint64(st.Nlink),
		Dev:    uint64(st.Dev),
		Ino:    uint64(st.Ino),
	}, nil
}
