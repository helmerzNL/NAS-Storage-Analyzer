package delscript

import (
	"fmt"
	"regexp"
	"strings"
	"testing"
)

func record(script []byte, kind string) []string {
	re := regexp.MustCompile(`(?m)^` + kind + `\|(.*?)\r?$`)
	var out []string
	for _, m := range re.FindAllStringSubmatch(string(script), -1) {
		out = append(out, m[1])
	}
	return out
}

func dirParams(target Target, dirs ...Dir) Params {
	p := paramsFor(target, LangEN)
	p.Dirs = dirs
	return p
}

func TestFolderRecordsAndTheNumberToType(t *testing.T) {
	files := fileSet(3, "loose")
	dirs := []Dir{{Path: "/Photos/2019", Files: 4800, Dirs: 84, Bytes: 1 << 30}, {Path: "/Old stuff", Files: 2, Dirs: 0, Bytes: 10}}
	for _, target := range []Target{TargetSh, TargetPowerShell} {
		res := mustGenerate(t, files, dirParams(target, dirs...))
		sep := "/"
		if target.IsWindows() {
			sep = `\`
		}
		if got, want := record(res.Script, "D"), []string{
			"2|0|" + sep + "Old stuff", "4800|84|" + strings.ReplaceAll("/Photos/2019", "/", sep),
		}; fmt.Sprint(got) != fmt.Sprint(want) {
			t.Errorf("%s: folder records = %q, want %q", target, got, want)
		}
		// N is the number of records (a truncated script is noticed by it); T is what is typed: everything the scan knew of them.
		wantItems := 3 + 2 + (4800 + 84) + (2 + 0)
		if res.Items != wantItems || res.Files != 3 || res.Folders != 2 || res.FolderFiles != 4802 || res.FolderDirs != 84 {
			t.Errorf("%s: %+v, want %d items", target, res, wantItems)
		}
		if n, tt := record(res.Script, "N"), record(res.Script, "T"); fmt.Sprint(n) != "[5]" || fmt.Sprint(tt) != fmt.Sprintf("[%d]", wantItems) {
			t.Errorf("%s: N = %v, T = %v", target, n, tt)
		}
		if res.Bytes != (100+101+102)+(1<<30)+10 {
			t.Errorf("%s: bytes %d count the folders too", target, res.Bytes)
		}
		sum := strings.Join(record(res.Script, "I"), "\n")
		for _, want := range []string{fmt.Sprintf("Items to delete: %d", wantItems), "Files you selected: 3", "Folders you selected: 2, with 4802 files and 84 folders",
			"EVERYTHING", "/Photos/2019", "/Old stuff"} {
			if !strings.Contains(strings.ReplaceAll(sum, `\`, "/"), want) {
				t.Errorf("%s: the summary lacks %q:\n%s", target, want, sum)
			}
		}
	}
}

// A script with files only is what it was: the number to type is the number of files, and the folder lines are not there.
func TestAScriptWithoutFoldersIsAsItWas(t *testing.T) {
	for _, target := range allTargets {
		res := mustGenerate(t, fileSet(4, "x"), paramsFor(target, LangEN))
		if res.Items != 4 || res.Folders != 0 {
			t.Errorf("%s: items %d folders %d", target, res.Items, res.Folders)
		}
		if target != TargetCmd {
			if n, tt := record(res.Script, "N"), record(res.Script, "T"); fmt.Sprint(n) != "[4]" || fmt.Sprint(tt) != "[4]" {
				t.Errorf("%s: N = %v, T = %v", target, n, tt)
			}
		}
		sum := strings.Join(record(res.Script, "I"), "\n")
		if !strings.Contains(sum, "Files to delete: 4") || strings.Contains(sum, "EVERYTHING") || strings.Contains(sum, "Items to delete") {
			t.Errorf("%s: a script without folders must read as before:\n%s", target, sum)
		}
		if len(record(res.Script, "D")) != 0 {
			t.Errorf("%s: no folder records", target)
		}
	}
}

// The share itself is the one folder that would take everything with it: it is never put in a script, whatever the request holds.
func TestTheShareItselfIsNeverDeleted(t *testing.T) {
	for _, target := range allTargets {
		for _, root := range []string{"/", ""} {
			_, err := Generate(nil, dirParams(target, Dir{Path: root, Files: 1000000, Dirs: 5000}))
			if err != ErrNoFiles {
				t.Errorf("%s %q: err = %v, want ErrNoFiles", target, root, err)
			}
			// With something else in the request the rest goes on, and the root is reported.
			res := mustGenerate(t, fileSet(2, "a"), dirParams(target, Dir{Path: root, Files: 9}))
			if res.Folders != 0 || len(record(res.Script, "D")) != 0 || !strings.Contains(names(res.Skipped), "=root") {
				t.Errorf("%s %q: folders %d, skipped %s", target, root, res.Folders, names(res.Skipped))
			}
		}
	}
}

// Covered folders and files are found by looking at the folders above a path, not at the order paths sort in: "/a-b" sorts before "/a/b".
func TestAFolderInsideAFolderIsCoveredWhateverTheSortOrder(t *testing.T) {
	dirs := []Dir{
		{Path: "/a/b/c", Files: 1}, {Path: "/a-b", Files: 1}, {Path: "/a", Files: 5, Dirs: 2}, {Path: "/a/b", Files: 3, Dirs: 1}, {Path: "/z/y", Files: 1}, {Path: "/zy", Files: 1},
	}
	files := []File{
		{Path: "/a/x.txt", Size: 1, Nlink: 1}, {Path: "/a/b/c/deep.txt", Size: 1, Nlink: 1}, {Path: "/a-b/y.txt", Size: 1, Nlink: 1},
		{Path: "/ab/z.txt", Size: 1, Nlink: 1}, {Path: "/z/other.txt", Size: 1, Nlink: 1},
	}
	res := mustGenerate(t, files, dirParams(TargetSh, dirs...))
	got := record(res.Script, "D")
	want := []string{"5|2|/a", "1|0|/a-b", "1|0|/z/y", "1|0|/zy"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("folders = %q, want %q", got, want)
	}
	// Covered: /a/b/c and /a/b (two folders), /a/x.txt and /a/b/c/deep.txt (two files). /a-b/y.txt is inside /a-b, which is a selected folder
	// too, so it is covered as well; /ab/z.txt and /z/other.txt are not inside any selected folder.
	if res.Covered != 5 {
		t.Errorf("covered = %d, want 5", res.Covered)
	}
	if loose := record(res.Script, "F"); len(loose) != 2 || !strings.HasSuffix(loose[0], "/ab/z.txt") && !strings.HasSuffix(loose[1], "/ab/z.txt") {
		t.Errorf("loose files = %q: /ab/z.txt and /z/other.txt stay", loose)
	}
	if n := fmt.Sprint(record(res.Script, "N")); n != "[6]" {
		t.Errorf("N = %s: four folders and two files", n)
	}
}

func TestAFolderNameIsJudgedLikeAFileName(t *testing.T) {
	for _, c := range []struct {
		path   string
		target Target
		reason Reason
	}{
		{"/a\nb", TargetSh, ReasonControl},
		{"/a\u202eb", TargetSh, ReasonControl},
		{"/bad\ufffdname", TargetSh, ReasonEncoding},
		{"/ok/with space/$HOME", TargetSh, ""},
		{"/a//b", TargetSh, ReasonControl},
		{"/trailing/", TargetSh, ReasonControl},
		{"/rel/../up", TargetSh, ReasonControl},
		{"/CON", TargetPowerShell, ReasonReserved},
		{"/a:b", TargetPowerShell, ReasonWindowsName},
		{"/ends.", TargetPowerShell, ReasonWindowsName},
		{"/anything", TargetCmd, ReasonFolderCmd},
	} {
		if got := ClassifyDir(c.target, c.path); got != c.reason {
			t.Errorf("ClassifyDir(%s, %q) = %q, want %q", c.target, c.path, got, c.reason)
		}
	}
}

func TestAFolderOnWindowsMustBeAddressable(t *testing.T) {
	long := "/" + strings.Repeat("x", 300)
	res, err := Generate(nil, dirParams(TargetPowerShell, Dir{Path: long, Files: 1}))
	if err != ErrNoFiles || !strings.Contains(names(res.Skipped), "too-long") {
		t.Errorf("err = %v, skipped = %s", err, names(res.Skipped))
	}
	// The same folder on a POSIX shell is fine.
	if res := mustGenerate(t, nil, dirParams(TargetSh, Dir{Path: long, Files: 1})); res.Folders != 1 {
		t.Errorf("sh must take it: %+v", res)
	}
}

func TestFoldersCountTowardTheLimitAndNothingGoesNegative(t *testing.T) {
	dirs := make([]Dir, 0, MaxFiles)
	for i := 0; i < MaxFiles; i++ {
		dirs = append(dirs, Dir{Path: fmt.Sprintf("/d%04d", i), Files: 1})
	}
	if _, err := Generate(fileSet(1, "x"), dirParams(TargetSh, dirs...)); err != ErrTooMany {
		t.Errorf("err = %v, want ErrTooMany: files and folders share the limit", err)
	}
	res := mustGenerate(t, nil, dirParams(TargetSh, Dir{Path: "/neg", Files: -5, Dirs: -1, Bytes: -9}))
	if res.Items != 1 || res.Bytes != 0 || record(res.Script, "D")[0] != "0|0|/neg" {
		t.Errorf("negative counts must be zero: %+v %q", res, record(res.Script, "D"))
	}
	// The same folder twice is one folder.
	res = mustGenerate(t, nil, dirParams(TargetSh, Dir{Path: "/same", Files: 1}, Dir{Path: "/same", Files: 1}))
	if res.Folders != 1 {
		t.Errorf("folders = %d", res.Folders)
	}
}

// A folder name is data in the script, like a file name: the code is the same whatever the folders are.
func TestFolderCodeDoesNotDependOnTheFolders(t *testing.T) {
	for _, target := range []Target{TargetSh, TargetPowerShell} {
		a := mustGenerate(t, fileSet(2, "x"), dirParams(target, Dir{Path: "/one", Files: 1}))
		b := mustGenerate(t, fileSet(5, "y"), dirParams(target, Dir{Path: "/two $(id) 'q' `x`", Files: 99, Dirs: 9}, Dir{Path: "/three"}))
		if codeRegion(a.Script) != codeRegion(b.Script) {
			t.Errorf("%s: the code of two scripts differs: only the data may", target)
		}
	}
}
