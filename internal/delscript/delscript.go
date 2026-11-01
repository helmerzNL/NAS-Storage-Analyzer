// Package delscript writes the scripts that delete the files a user has selected. It writes text and nothing else:
// the app never deletes anything, the person who runs the script does.
//
// A script is fixed program text plus a block of data. The data holds the base path and the files, one record per
// line (N count, R root, I line to print, F size|mtime|path), and the program only ever reads that block as text:
// a file name is never part of a command. Everything derived from a file name that is printed is escaped first.
// That is the whole safety argument, so the package has no way to run anything: it does not import os/exec,
// syscall or anything that removes files, and a test keeps it that way.
//
// The package is plain Go without a database, so it compiles and is tested on Windows and macOS too.
package delscript

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// MaxFiles is the most files one script holds. A script of that size cannot be read through by eye, which is why
// every script starts with a summary and asks for a confirmation.
const MaxFiles = 2000

// Target is the kind of script.
type Target string

const (
	TargetCmd        Target = "cmd"
	TargetPowerShell Target = "powershell"
	TargetSh         Target = "sh"
)

// ParseTarget turns the name used in the API into a Target.
func ParseTarget(s string) (Target, bool) {
	switch t := Target(s); t {
	case TargetCmd, TargetPowerShell, TargetSh:
		return t, true
	}
	return "", false
}

// IsWindows is true for the targets that run on Windows and address files with backslashes.
func (t Target) IsWindows() bool { return t == TargetCmd || t == TargetPowerShell }

// Extension is the file extension a script of this kind should have.
func (t Target) Extension() string {
	switch t {
	case TargetCmd:
		return ".cmd"
	case TargetPowerShell:
		return ".ps1"
	}
	return ".sh"
}

// File is one selected file as the index knows it.
type File struct {
	Path  string // below the share root, "/" separators, leading "/": query.File.Path
	Size  int64  // bytes at the time of the scan; the script keeps a file whose size is different
	Mtime int64  // seconds since the epoch; 0 means "do not check the time" (unknown, or clamped by the scanner)
	Nlink int    // link count; above 1 the file has other hard links
}

// Dir is one selected folder as the index knows it. A script deletes the folder and everything in it, so what the index
// says is in it (at the scan) is what the person is shown and what the script checks against when it runs.
type Dir struct {
	Path  string // below the share root, "/" separators, leading "/"; never "/" itself, which is the share
	Files int64  // files below it at any depth, at the scan
	Dirs  int64  // folders below it at any depth, at the scan; not the folder itself
	Bytes int64  // the size of those files at the scan
}

// Skip is a file or folder that is not in the script, and why.
type Skip struct {
	Path   string
	Reason Reason
}

// Params describes the script.
type Params struct {
	Share     string
	Root      string // where the machine that runs the script sees the share; see NormalizeRoot
	Target    Target
	Lang      Lang
	ScanID    int64
	ScanTime  time.Time // when the scan that produced the index finished
	Generated time.Time
	// Skipped are files and folders the caller already left out: ids no longer in the index, and names that differ only in case.
	Skipped []Skip
	// Dirs are the folders to delete with everything in them (never for cmd, see ReasonFolderCmd).
	Dirs []Dir
}

// Result is a generated script and what it contains.
type Result struct {
	Script []byte
	Root   string // the normalised base path
	// Files are the files selected one by one that are in the script; Folders the folders, each with everything in it.
	Files, Folders int
	// FolderFiles and FolderDirs are what the index says is inside those folders, at the scan.
	FolderFiles, FolderDirs int64
	// Items is the number to type to confirm: the files, the folders and everything the index says is in them.
	Items int
	// Covered counts selected files and folders that lie inside a selected folder: they are deleted with it, not twice.
	Covered   int
	Bytes     int64 // the size of the files, and of what is in the folders, at the scan
	Hardlinks int
	Skipped   []Skip // the caller's and the ones found here
}

var (
	// ErrNoFiles is returned when nothing is left to put in a script.
	ErrNoFiles = errors.New("none of the selected files or folders can be put in a script")
	// ErrTooMany is returned for more than MaxFiles files.
	ErrTooMany = errors.New("too many files and folders for one script")
)

type entry struct {
	path  string // as the script addresses it, relative to the root
	rel   string // below the share root, "/" separators
	size  int64
	mtime int64
	nlink int
}

// Generate writes a script for files and, in p.Dirs, folders with everything in them. It never fails on something it cannot
// address: that is left out and reported in Result.Skipped, and the script says so too.
func Generate(files []File, p Params) (*Result, error) {
	if _, ok := ParseTarget(string(p.Target)); !ok {
		return nil, fmt.Errorf("unknown script type %q", p.Target)
	}
	if len(files)+len(p.Dirs) > MaxFiles {
		return nil, ErrTooMany
	}
	root, err := NormalizeRoot(p.Target, p.Root)
	if err != nil {
		return nil, err
	}
	txt := textFor(p.Lang)

	joined := strings.TrimRight(root, `\/`) // what a path below the root is appended to
	skipped := append([]Skip(nil), p.Skipped...)

	// Folders first: they decide which of the files are already covered.
	var folders []dirEntry
	seenDir := map[string]bool{}
	for _, d := range p.Dirs {
		if seenDir[d.Path] {
			continue
		}
		seenDir[d.Path] = true
		reason := ClassifyDir(p.Target, d.Path)
		addressed := d.Path
		if p.Target.IsWindows() {
			addressed = strings.ReplaceAll(d.Path, "/", `\`)
			if reason == "" && utf16Len(joined)+utf16Len(addressed) > maxWindowsPath {
				reason = ReasonTooLong
			}
		}
		if reason != "" {
			skipped = append(skipped, Skip{Path: d.Path, Reason: reason})
			continue
		}
		folders = append(folders, dirEntry{path: addressed, rel: d.Path, files: max(d.Files, 0), dirs: max(d.Dirs, 0), bytes: max(d.Bytes, 0)})
	}
	// A folder inside another selected folder goes with it: it is deleted once, and the script says it once. The outer folder
	// is found by looking at the folders above a path, so the order the paths sort in does not matter ("/a-b" sorts before "/a/b").
	sort.Slice(folders, func(i, j int) bool {
		if di, dj := strings.Count(folders[i].rel, "/"), strings.Count(folders[j].rel, "/"); di != dj {
			return di < dj
		}
		return folders[i].rel < folders[j].rel
	})
	top := map[string]bool{}
	inside := func(rel string) bool {
		for i := strings.LastIndexByte(rel, '/'); i > 0; i = strings.LastIndexByte(rel[:i], '/') {
			if top[rel[:i]] {
				return true
			}
		}
		return false
	}
	covered := 0
	var kept []dirEntry
	for _, f := range folders { // shallowest first, so every folder is judged after the folders above it
		if inside(f.rel) {
			covered++
			continue
		}
		top[f.rel] = true
		kept = append(kept, f)
	}
	folders = kept
	sort.Slice(folders, func(i, j int) bool { return folders[i].rel < folders[j].rel })

	var keep []entry
	seen := map[string]bool{}
	var bytes int64
	hard := 0
	for _, f := range files {
		if seen[f.Path] {
			continue
		}
		seen[f.Path] = true
		if inside(f.Path) {
			covered++
			continue
		}
		reason := ClassifyPath(p.Target, f.Path)
		addressed := f.Path
		if p.Target.IsWindows() {
			addressed = strings.ReplaceAll(f.Path, "/", `\`)
			if reason == "" && utf16Len(joined)+utf16Len(addressed) > maxWindowsPath {
				reason = ReasonTooLong
			}
		}
		if reason == "" && p.Target == TargetCmd && f.Size <= 0 {
			reason = ReasonEmptyCmd
		}
		if reason != "" {
			skipped = append(skipped, Skip{Path: f.Path, Reason: reason})
			continue
		}
		if p.Target == TargetCmd {
			addressed = strings.TrimPrefix(addressed, `\`) // cmd works relative to the base path
		}
		size := f.Size
		if size < 0 {
			size = 0
		}
		mtime := f.Mtime
		if mtime < 0 {
			mtime = 0
		}
		keep = append(keep, entry{path: addressed, rel: f.Path, size: size, mtime: mtime, nlink: f.Nlink})
		bytes += size
		if f.Nlink > 1 {
			hard++
		}
	}
	if len(keep) == 0 && len(folders) == 0 {
		return &Result{Root: root, Skipped: skipped, Covered: covered}, ErrNoFiles
	}

	var folderFiles, folderDirs int64
	for _, f := range folders {
		folderFiles += f.files
		folderDirs += f.dirs
		bytes += f.bytes
	}
	// The number to type: what the person is told is going, counted the same way in the summary and in the script's report.
	items := len(keep) + len(folders) + int(folderFiles) + int(folderDirs)

	data := []string{fmt.Sprintf("N|%d", len(keep)+len(folders))}
	if p.Target != TargetCmd { // cmd takes no folders, so for it the number to type is the number of records
		data = append(data, fmt.Sprintf("T|%d", items))
	}
	data = append(data, "R|"+root)
	r := &Result{Root: root, Files: len(keep), Folders: len(folders), FolderFiles: folderFiles, FolderDirs: folderDirs, Items: items,
		Covered: covered, Bytes: bytes, Hardlinks: hard, Skipped: skipped}
	for _, l := range summary(txt, p, root, keep, folders, r) {
		data = append(data, "I|"+l)
	}
	for _, e := range keep {
		data = append(data, fmt.Sprintf("F|%d|%d|%s", e.size, e.mtime, e.path))
	}
	for _, d := range folders {
		data = append(data, fmt.Sprintf("D|%d|%d|%s", d.files, d.dirs, d.path))
	}

	var script string
	switch p.Target {
	case TargetSh:
		script = renderSh(txt, data)
	case TargetPowerShell:
		script = renderPowerShell(txt, data)
	default:
		script = renderCmd(txt, data)
	}
	r.Script = []byte(script)
	return r, nil
}

type dirEntry struct {
	path  string // as the script addresses it, relative to the root
	rel   string // below the share root, "/" separators
	files int64
	dirs  int64
	bytes int64
}

// summary is what the script prints before it asks for the confirmation. Every line is data.
func summary(txt *messages, p Params, root string, keep []entry, folders []dirEntry, r *Result) []string {
	out := []string{
		txt.title,
		fmt.Sprintf(txt.share, displayEscape(p.Share)),
		fmt.Sprintf(txt.root, displayEscape(root)),
		fmt.Sprintf(txt.scan, p.ScanTime.UTC().Format("2006-01-02 15:04 UTC"), p.ScanID),
		fmt.Sprintf(txt.generated, p.Generated.UTC().Format("2006-01-02 15:04 UTC")),
	}
	if len(folders) == 0 {
		out = append(out, fmt.Sprintf(txt.files, len(keep)))
	} else {
		// With folders the number to type is not the number of files that were ticked, so what it is made of is said.
		out = append(out, fmt.Sprintf(txt.items, r.Items))
		if len(keep) > 0 {
			out = append(out, fmt.Sprintf(txt.selFiles, len(keep)))
		}
		out = append(out, fmt.Sprintf(txt.selFolders, len(folders), r.FolderFiles, r.FolderDirs))
	}
	out = append(out, fmt.Sprintf(txt.size, humanSize(r.Bytes, p.Lang)))
	if r.Hardlinks > 0 {
		out = append(out, fmt.Sprintf(txt.hardlinks, r.Hardlinks))
	}
	if len(folders) > 0 {
		out = append(out, txt.folderRule)
	}
	if r.Covered > 0 {
		out = append(out, fmt.Sprintf(txt.covered, r.Covered))
	}
	if len(folders) > 0 {
		sorted := append([]dirEntry(nil), folders...)
		sort.Slice(sorted, func(i, j int) bool {
			if sorted[i].files != sorted[j].files {
				return sorted[i].files > sorted[j].files
			}
			return sorted[i].rel < sorted[j].rel
		})
		out = append(out, txt.folderHead)
		const showWhole = 25
		for i, f := range sorted {
			if i == showWhole {
				out = append(out, fmt.Sprintf(txt.skippedMore, len(sorted)-showWhole))
				break
			}
			out = append(out, fmt.Sprintf(txt.folderLine, f.files, f.dirs, humanSize(f.bytes, p.Lang), displayEscape(f.rel)))
		}
	}
	if len(keep) > 0 {
		out = append(out, txt.folders)
		type folder struct {
			path  string
			count int
			bytes int64
		}
		byDir := map[string]*folder{}
		for _, e := range keep {
			d := e.rel[:strings.LastIndex(e.rel, "/")+1]
			if d == "" {
				d = "/"
			}
			f := byDir[d]
			if f == nil {
				f = &folder{path: d}
				byDir[d] = f
			}
			f.count++
			f.bytes += e.size
		}
		list := make([]*folder, 0, len(byDir))
		for _, f := range byDir {
			list = append(list, f)
		}
		sort.Slice(list, func(i, j int) bool {
			if list[i].count != list[j].count {
				return list[i].count > list[j].count
			}
			return list[i].path < list[j].path
		})
		const showFolders = 12
		for i, f := range list {
			if i == showFolders {
				out = append(out, fmt.Sprintf(txt.skippedMore, len(list)-showFolders))
				break
			}
			out = append(out, fmt.Sprintf("  %5d  %10s  %s", f.count, humanSize(f.bytes, p.Lang), displayEscape(f.path)))
		}
	}
	if len(r.Skipped) > 0 {
		out = append(out, fmt.Sprintf(txt.skippedHead, len(r.Skipped)))
		const showSkipped = 40
		for i, s := range r.Skipped {
			if i == showSkipped {
				out = append(out, fmt.Sprintf(txt.skippedMore, len(r.Skipped)-showSkipped))
				break
			}
			out = append(out, fmt.Sprintf(txt.skippedLine, displayEscape(s.Path), txt.reason[s.Reason]))
		}
	}
	out = append(out, "--------")
	return out
}

func humanSize(n int64, l Lang) string {
	const unit = 1024
	var s string
	if n < unit {
		s = fmt.Sprintf("%d B", n)
	} else {
		div, exp := int64(unit), 0
		for m := n / unit; m >= unit && exp < 4; m /= unit {
			div *= unit
			exp++
		}
		s = fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTP"[exp])
	}
	if l == LangNL {
		s = strings.Replace(s, ".", ",", 1)
	}
	return s
}
