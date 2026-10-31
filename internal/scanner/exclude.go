package scanner

import (
	"fmt"
	"path"
	"strings"
)

// Excluder matches shell-style patterns against entry names. A pattern
// without a slash matches the entry name at any depth (".snapshot", "@eaDir",
// ".Trash-*"); a pattern with a slash matches the path relative to the scan
// root ("media/tmp", "*/cache").
type Excluder struct {
	names []string
	paths []string
}

// NewExcluder validates the patterns up front so a typo fails the scan start
// instead of silently excluding nothing.
func NewExcluder(patterns []string) (*Excluder, error) {
	e := &Excluder{}
	for _, p := range patterns {
		p = strings.TrimSpace(p)
		p = strings.Trim(p, "/")
		if p == "" {
			continue
		}
		if _, err := path.Match(p, ""); err != nil {
			return nil, fmt.Errorf("invalid exclude pattern %q: %w", p, err)
		}
		if strings.Contains(p, "/") {
			e.paths = append(e.paths, p)
		} else {
			e.names = append(e.names, p)
		}
	}
	return e, nil
}

// Match reports whether the entry called name at relative path rel is excluded.
func (e *Excluder) Match(name, rel string) bool {
	if e == nil {
		return false
	}
	for _, p := range e.names {
		if ok, _ := path.Match(p, name); ok {
			return true
		}
	}
	for _, p := range e.paths {
		if ok, _ := path.Match(p, rel); ok {
			return true
		}
	}
	return false
}
