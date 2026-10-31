package shares

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"
)

// Limits of one folder listing. A folder can hold millions of entries; reading
// all of them to fill a picker would be slow and memory-hungry, so the listing
// stops early and says so.
const (
	// BrowseMaxScanned is how many directory entries one listing reads at most.
	BrowseMaxScanned = 20000
	// BrowseMaxDirs is how many sub-folders one listing returns at most.
	BrowseMaxDirs = 1000
)

// ErrNoSharesRoot is returned when no shares root is configured.
var ErrNoSharesRoot = errors.New("no shares root is configured")

// BrowseEntry is a sub-folder in a listing.
type BrowseEntry struct {
	Name string `json:"name"`
	// Path is the full path inside the container, ready to use as the path of a share.
	Path string `json:"path"`
	// Symlink marks a link to a folder (the scanner opens the path it is given, so a
	// link as the root of a share works; links below the root are never followed).
	Symlink bool `json:"symlink,omitempty"`
}

// Listing is the content of one folder below the shares root.
type Listing struct {
	Root string `json:"root"`
	Path string `json:"path"`
	// Parent is empty at the root: the picker never leaves the shares root.
	Parent string        `json:"parent,omitempty"`
	Dirs   []BrowseEntry `json:"dirs"`
	// Truncated says the folder has more entries than were read or returned.
	Truncated bool `json:"truncated"`
}

// inside reports whether p is root or lies below it. Both must be clean and absolute.
func inside(root, p string) bool {
	if root == "/" {
		return path.IsAbs(p)
	}
	return p == root || strings.HasPrefix(p, root+"/")
}

// Browse lists the sub-folders of p, which must be root or lie below it ("" means
// root). It is read-only and only returns folders: files are never listed, opened
// or stat'ed. Symbolic links are resolved and shown only when they point to a
// folder that is itself inside the root, so a link cannot lead out of it.
func Browse(root, p string) (*Listing, error) {
	if root == "" {
		return nil, ErrNoSharesRoot
	}
	root = path.Clean(root)
	if p == "" {
		p = root
	}
	if !path.IsAbs(p) || strings.ContainsRune(p, 0) {
		return nil, bad("path", "enter an absolute path inside the container, for example %s", path.Join(root, "media"))
	}
	p = path.Clean(p)
	if !inside(root, p) {
		return nil, bad("path", "must be inside %s", root)
	}

	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, bad("path", "the shares root %s does not exist in the container; check NASA_SHARES_DIR and NASA_SHARES_MOUNT", root)
		}
		return nil, err
	}
	resolved, err := filepath.EvalSymlinks(p)
	if err != nil {
		return nil, mapFSError(err)
	}
	if !inside(realRoot, resolved) {
		return nil, bad("path", "must be inside %s", root)
	}

	f, err := os.Open(resolved)
	if err != nil {
		return nil, mapFSError(err)
	}
	defer f.Close()
	if st, err := f.Stat(); err != nil {
		return nil, mapFSError(err)
	} else if !st.IsDir() {
		return nil, bad("path", "%s is not a folder", p)
	}

	out := &Listing{Root: root, Path: p, Dirs: []BrowseEntry{}}
	if p != root {
		out.Parent = path.Dir(p)
	}
	scanned := 0
	for {
		ents, err := f.ReadDir(512)
		for _, e := range ents {
			scanned++
			name := e.Name()
			if !utf8.ValidString(name) || name == "" {
				continue // would not survive the round trip through JSON
			}
			entry := BrowseEntry{Name: name, Path: path.Join(p, name)}
			switch {
			case e.IsDir():
			case e.Type()&fs.ModeSymlink != 0:
				target, err := filepath.EvalSymlinks(filepath.Join(resolved, name))
				if err != nil || !inside(realRoot, filepath.ToSlash(target)) {
					continue
				}
				if st, err := os.Stat(target); err != nil || !st.IsDir() {
					continue
				}
				entry.Symlink = true
			default:
				continue
			}
			if len(out.Dirs) >= BrowseMaxDirs {
				out.Truncated = true
				continue
			}
			out.Dirs = append(out.Dirs, entry)
		}
		if err != nil {
			if !errors.Is(err, io.EOF) {
				return nil, mapFSError(err)
			}
			break
		}
		if scanned >= BrowseMaxScanned {
			out.Truncated = true
			break
		}
	}
	sort.Slice(out.Dirs, func(i, j int) bool {
		a, b := strings.ToLower(out.Dirs[i].Name), strings.ToLower(out.Dirs[j].Name)
		if a != b {
			return a < b
		}
		return out.Dirs[i].Name < out.Dirs[j].Name
	})
	return out, nil
}

// mapFSError turns the errors a listing can hit into ones the API can show.
func mapFSError(err error) error {
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return ErrNotFound
	case errors.Is(err, fs.ErrPermission):
		return bad("path", "this folder cannot be read (permission denied). %s", PermissionHint())
	}
	return err
}

// PermissionHint says who the app runs as and what to do about a folder it cannot read. The usual cause is a share
// that only its owner and one group may read (Unraid: nobody:users, mode 770) while the app runs as a user that is
// neither; NASA_SHARES_GID adds the group.
func PermissionHint() string {
	return fmt.Sprintf("The app runs as uid %d, gid %d. Give that user read access, or set NASA_SHARES_GID to the numeric "+
		"group that owns the share (Unraid: 100) and recreate the containers; see docs/deploy.md, Leesrechten.",
		os.Getuid(), os.Getgid())
}
