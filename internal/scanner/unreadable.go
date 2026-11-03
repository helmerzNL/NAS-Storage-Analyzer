package scanner

import (
	"sort"
	"strings"
	"sync"
)

// UnreadableFolder is a folder a scan could not read completely, and why. It is what the audit log shows: which folders
// to look at (usually a permission to fix), not one line per file.
type UnreadableFolder struct {
	// Path is the folder below the share root, with "/" separators and a leading "/"; "/" is the share's top folder.
	Path string
	// Kind is why: the error that was seen first for this folder, or for the folder itself when it could not be opened.
	Kind ErrKind
	// Self is true when the folder itself could not be opened or listed: nothing in it, and nothing below it, is in the scan.
	Self bool
	// Items is how many of the files in a folder that could be listed could not be examined (stat failed). It does not
	// count what is below a folder with Self set.
	Items int
}

// UnreadableReport is what a scan could not read, by folder.
type UnreadableReport struct {
	// Folders is sorted by path and has at most the number that was asked for.
	Folders []UnreadableFolder
	// Total is the number of folders with a problem, also those that are not in Folders.
	Total int
	// Truncated is true when the scan met more distinct folders than are tracked, so Total is a lower bound.
	Truncated bool
}

// maxTrackedUnreadable bounds the memory a scan spends on this: a share whose every folder is unreadable would otherwise
// hold one entry per folder, and the report only ever shows a few dozen.
const maxTrackedUnreadable = 5000

// unreadableSet collects the folders that had a problem while the scan went on. It is safe for concurrent use: the
// workers that walk the share report into it at the same time.
type unreadableSet struct {
	mu        sync.Mutex
	m         map[string]*UnreadableFolder
	truncated bool
}

// folderOf is the folder an error belongs to: the path itself when the folder could not be read, otherwise the folder the
// item is in. A scan root's own problem is fatal and never reaches here, but "" still maps to "/".
func folderOf(e ErrRec) string {
	rel := e.Path
	if !e.Dir {
		if i := strings.LastIndexByte(rel, '/'); i >= 0 {
			rel = rel[:i]
		} else {
			rel = ""
		}
	}
	return "/" + rel
}

func (s *unreadableSet) add(e ErrRec) {
	key := folderOf(e)
	s.mu.Lock()
	defer s.mu.Unlock()
	f := s.m[key]
	if f == nil {
		if len(s.m) >= maxTrackedUnreadable {
			s.truncated = true
			return
		}
		if s.m == nil {
			s.m = map[string]*UnreadableFolder{}
		}
		f = &UnreadableFolder{Path: key, Kind: e.Kind}
		s.m[key] = f
	}
	if e.Dir {
		// The folder itself is the bigger problem: its own error is the reason, and the items in it no longer matter.
		f.Self, f.Kind, f.Items = true, e.Kind, 0
		return
	}
	if !f.Self {
		f.Items++
	}
}

// Unreadable lists up to max folders that could not be read (all of them for a max below 1). It can be called while the
// scan runs.
func (p *Progress) Unreadable(max int) UnreadableReport {
	s := &p.unreadable
	s.mu.Lock()
	defer s.mu.Unlock()
	r := UnreadableReport{Total: len(s.m), Truncated: s.truncated}
	for _, f := range s.m {
		r.Folders = append(r.Folders, *f)
	}
	sort.Slice(r.Folders, func(i, j int) bool { return r.Folders[i].Path < r.Folders[j].Path })
	if max > 0 && len(r.Folders) > max {
		r.Folders = r.Folders[:max]
	}
	return r
}
