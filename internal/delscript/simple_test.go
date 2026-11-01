package delscript

import (
	"bytes"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"testing"
	"unicode"
)

func simpleParams(t Target, l Lang) Params {
	p := paramsFor(t, l)
	p.Style = StyleSimple
	return p
}

func eolOf(t Target) string {
	if t.IsWindows() {
		return "\r\n"
	}
	return "\n"
}

func commentPrefix(t Target) string {
	if t == TargetCmd {
		return "rem "
	}
	return "# "
}

// scriptLines splits a simple script into its comment lines, its commands, and the two lines cmd starts with, and checks the line ends on the way: a Windows
// script has CRLF and nothing else, the sh script has LF and nothing else, and only the PowerShell script starts with a byte order mark.
func scriptLines(t *testing.T, target Target, script []byte) (comments, commands, prologue []string) {
	t.Helper()
	s := script
	if bom := []byte(psBOM); target == TargetPowerShell {
		if !bytes.HasPrefix(s, bom) {
			t.Fatalf("the PowerShell script has no byte order mark: Windows PowerShell 5.1 would read it as the ANSI code page")
		}
		s = s[len(bom):]
	} else if bytes.HasPrefix(s, []byte(psBOM)) {
		t.Fatalf("a byte order mark on a %s script", target)
	}
	eol := eolOf(target)
	text := string(s)
	if !strings.HasSuffix(text, eol) {
		t.Fatalf("the script does not end in a line end")
	}
	if target.IsWindows() {
		if strings.Contains(strings.ReplaceAll(text, "\r\n", ""), "\n") || strings.Contains(strings.ReplaceAll(text, "\r\n", ""), "\r") {
			t.Fatalf("a Windows script must have CRLF line ends only")
		}
	} else if strings.Contains(text, "\r") {
		t.Fatalf("an sh script must have LF line ends only")
	}
	for _, l := range strings.Split(strings.TrimSuffix(text, eol), eol) {
		switch {
		case l == "":
		case strings.HasPrefix(l, commentPrefix(target)):
			comments = append(comments, strings.TrimPrefix(l, commentPrefix(target)))
		case target == TargetCmd && (l == "@echo off" || l == "chcp 65001 >nul"):
			prologue = append(prologue, l)
		default:
			commands = append(commands, l)
		}
	}
	return
}

// A set of things to delete that every target can address: two files, one with an apostrophe, and a folder.
func simpleSet() ([]File, []Dir) {
	return []File{
			{Path: "/Films/it's here.mkv", Size: 700, Mtime: 1761900000, Nlink: 1},
			{Path: "/Films/Heat (1995).mkv", Size: 300, Mtime: 1761900001, Nlink: 1},
		},
		[]Dir{{Path: "/Series/Old Show", Files: 12, Dirs: 3, Bytes: 5000}}
}

func TestSimpleScriptIsOneCommandPerFileAndFolder(t *testing.T) {
	want := map[Target][]string{
		TargetSh: {
			`rm -f -- '/mnt/user/Media/Films/Heat (1995).mkv'`,
			`rm -f -- '/mnt/user/Media/Films/it'\''s here.mkv'`,
			`rm -rf -- '/mnt/user/Media/Series/Old Show'`,
		},
		TargetPowerShell: {
			`Remove-Item -LiteralPath '\\ZBNAS\Media\Films\Heat (1995).mkv' -Force`,
			`Remove-Item -LiteralPath '\\ZBNAS\Media\Films\it''s here.mkv' -Force`,
			`Remove-Item -LiteralPath '\\ZBNAS\Media\Series\Old Show' -Recurse -Force`,
		},
		TargetCmd: {
			`del /f /q "\\ZBNAS\Media\Films\Heat (1995).mkv"`,
			`del /f /q "\\ZBNAS\Media\Films\it's here.mkv"`,
			`rd /s /q "\\ZBNAS\Media\Series\Old Show"`,
		},
	}
	for _, target := range allTargets {
		for _, lang := range allLangs {
			files, dirs := simpleSet()
			p := simpleParams(target, lang)
			p.Dirs = dirs
			r := mustGenerate(t, files, p)
			_, commands, prologue := scriptLines(t, target, r.Script)
			if strings.Join(commands, "\n") != strings.Join(want[target], "\n") {
				t.Errorf("%s/%s: the commands are\n%s\nwant\n%s", target, lang, strings.Join(commands, "\n"), strings.Join(want[target], "\n"))
			}
			if target == TargetCmd && len(prologue) != 2 {
				t.Errorf("cmd must start with @echo off and chcp 65001: %q", prologue)
			}
			if target != TargetCmd && len(prologue) != 0 {
				t.Errorf("%s: unexpected prologue %q", target, prologue)
			}
			if r.Files != 2 || r.Folders != 1 || r.Items != 3 || r.FolderFiles != 12 || r.FolderDirs != 3 || r.Bytes != 300+700+5000 {
				t.Errorf("%s/%s: the result is %+v", target, lang, *r)
			}
		}
	}
}

func TestSimpleScriptIsSortedByPath(t *testing.T) {
	for _, target := range allTargets {
		files := []File{{Path: "/b/z.mkv"}, {Path: "/b/a.mkv"}, {Path: "/a/y.mkv"}}
		p := simpleParams(target, LangEN)
		p.Dirs = []Dir{{Path: "/b/m"}, {Path: "/a/b"}}
		r := mustGenerate(t, files, p)
		_, commands, _ := scriptLines(t, target, r.Script)
		var got []string
		for _, c := range commands {
			got = append(got, c[strings.IndexAny(c, `'"`):])
		}
		sep := "/"
		if target.IsWindows() {
			sep = `\`
		}
		root := strings.TrimRight(rootFor(target), sep)
		q := func(rel string) string {
			full := root + strings.ReplaceAll(rel, "/", sep)
			if target == TargetCmd {
				return `"` + full + `"`
			}
			return "'" + full + "'"
		}
		var want []string
		for _, rel := range []string{"/a/b", "/a/y.mkv", "/b/a.mkv", "/b/m", "/b/z.mkv"} {
			want = append(want, q(rel))
		}
		for i := range want {
			// cmd quotes with " and the others with ', and the flags come first: only the path is compared
			if i >= len(got) || !strings.HasPrefix(got[i], want[i]) {
				t.Fatalf("%s: the commands are not in the order of their paths:\n%s", target, strings.Join(commands, "\n"))
			}
		}
	}
}

func TestSimpleScriptHeaderIsComments(t *testing.T) {
	for _, target := range allTargets {
		files, dirs := simpleSet()
		for lang, count := range map[Lang]string{
			LangEN: "Commands: 3 (files: 2, folders: 1). Index of scan: 2026-10-31 12:30 UTC (scan 7).",
			LangNL: "Opdrachten: 3 (bestanden: 2, mappen: 1). Index van scan: 2026-10-31 12:30 UTC (scan 7).",
		} {
			p := simpleParams(target, lang)
			p.Dirs = dirs
			r := mustGenerate(t, files, p)
			comments, _, _ := scriptLines(t, target, r.Script)
			if len(comments) != 6 {
				t.Errorf("%s/%s: want a title, the count and four notes, got %d comment lines:\n%s", target, lang, len(comments), strings.Join(comments, "\n"))
				continue
			}
			if comments[1] != count {
				t.Errorf("%s/%s: the count line is %q, want %q", target, lang, comments[1], count)
			}
			// What the person is told before pasting: nothing is checked, everything runs at once.
			joined := strings.Join(comments, "\n")
			for _, word := range map[Lang][]string{LangEN: {"Nothing is asked", "every one of them at once"}, LangNL: {"niets gevraagd", "allemaal meteen"}}[lang] {
				if !strings.Contains(joined, word) {
					t.Errorf("%s/%s: the notes do not say %q:\n%s", target, lang, word, joined)
				}
			}
			// A blank line sits between the notes and the commands, so a person sees where the commands start.
			sep := eolOf(target) + eolOf(target)
			if !strings.Contains(string(r.Script), sep) {
				t.Errorf("%s/%s: no blank line before the commands", target, lang)
			}
		}
	}
}

func TestSimpleScriptHasNoSummaryNoConfirmationAndNoData(t *testing.T) {
	record := regexp.MustCompile(`(?m)^[NRITFD]\|`)
	for _, target := range allTargets {
		files, dirs := simpleSet()
		p := simpleParams(target, LangEN)
		p.Dirs = dirs
		s := string(mustGenerate(t, files, p).Script)
		if record.MatchString(s) {
			t.Errorf("%s: the simple script holds a data record", target)
		}
		for _, bad := range []string{"DELETE", "$data", "read ", "set /p", "Read-Host", "stat ", "FOUND"} {
			if strings.Contains(s, bad) {
				t.Errorf("%s: the simple script holds %q: it asks and checks nothing", target, bad)
			}
		}
	}
}

func TestAdvancedScriptIsUntouchedByTheStyle(t *testing.T) {
	for _, target := range allTargets {
		files, dirs := simpleSet()
		if target == TargetCmd {
			dirs = nil // an advanced cmd script takes no folders
		}
		var out [][]byte
		for _, style := range []Style{"", StyleAdvanced} {
			p := paramsFor(target, LangEN)
			p.Style = style
			p.Dirs = dirs
			out = append(out, mustGenerate(t, files, p).Script)
		}
		if !bytes.Equal(out[0], out[1]) {
			t.Errorf("%s: no style and the advanced style give different scripts", target)
		}
		if !strings.Contains(string(out[0]), "DELETE") {
			t.Errorf("%s: the advanced script lost its confirmation", target)
		}
	}
}

func TestParseStyleAndExtension(t *testing.T) {
	for in, want := range map[string]Style{"": StyleAdvanced, "advanced": StyleAdvanced, "simple": StyleSimple} {
		if got, ok := ParseStyle(in); !ok || got != want {
			t.Errorf("ParseStyle(%q) = %q, %v", in, got, ok)
		}
	}
	for _, bad := range []string{"Simple", "easy", "simple ", "ADVANCED", "txt"} {
		if _, ok := ParseStyle(bad); ok {
			t.Errorf("ParseStyle(%q) is accepted", bad)
		}
	}
	for _, target := range allTargets {
		if got := target.FileExtension(StyleSimple); got != ".txt" {
			t.Errorf("%s simple: %q, want .txt", target, got)
		}
		for _, style := range []Style{"", StyleAdvanced} {
			if got := target.FileExtension(style); got != target.Extension() {
				t.Errorf("%s advanced: %q, want %q", target, got, target.Extension())
			}
		}
	}
}

func TestSimpleScriptIsForASmallNumber(t *testing.T) {
	for _, target := range allTargets {
		gen := func(files, dirs int, style Style) error {
			fs := fileSet(files, "f")
			p := paramsFor(target, LangEN)
			p.Style = style
			for i := 0; i < dirs; i++ {
				p.Dirs = append(p.Dirs, Dir{Path: fmt.Sprintf("/d/d%03d", i)})
			}
			_, err := Generate(fs, p)
			return err
		}
		if err := gen(MaxSimple, 0, StyleSimple); err != nil {
			t.Errorf("%s: %d files: %v", target, MaxSimple, err)
		}
		if err := gen(MaxSimple-1, 1, StyleSimple); err != nil {
			t.Errorf("%s: %d items with a folder: %v", target, MaxSimple, err)
		}
		for _, c := range [][2]int{{MaxSimple + 1, 0}, {MaxSimple, 1}, {0, MaxSimple + 1}, {100, 101}} {
			if err := gen(c[0], c[1], StyleSimple); !errors.Is(err, ErrTooManySimple) {
				t.Errorf("%s: %d files and %d folders: %v, want ErrTooManySimple", target, c[0], c[1], err)
			}
		}
		// The advanced script takes what the simple one refuses: the limit belongs to the style.
		if err := gen(MaxSimple+1, 0, StyleAdvanced); err != nil {
			t.Errorf("%s: the advanced script refuses %d files: %v", target, MaxSimple+1, err)
		}
		if err := gen(MaxFiles+1, 0, StyleSimple); !errors.Is(err, ErrTooManySimple) {
			t.Errorf("%s: beyond both limits the simple limit speaks first: %v", target, err)
		}
	}
}

func TestSimpleScriptDeletesAFolderOnce(t *testing.T) {
	for _, target := range allTargets {
		files := []File{{Path: "/a/in.mkv"}, {Path: "/a-b/out.mkv"}, {Path: "/a/sub/deep.mkv"}}
		p := simpleParams(target, LangEN)
		p.Dirs = []Dir{{Path: "/a"}, {Path: "/a/sub"}}
		r := mustGenerate(t, files, p)
		_, commands, _ := scriptLines(t, target, r.Script)
		if len(commands) != 2 || !strings.Contains(commands[0], "a-b") && !strings.Contains(commands[1], "a-b") {
			t.Fatalf("%s: want the folder /a and the file /a-b/out.mkv, got\n%s", target, strings.Join(commands, "\n"))
		}
		for _, c := range commands {
			if strings.Contains(c, "in.mkv") || strings.Contains(c, "deep.mkv") || strings.Contains(c, "sub") {
				t.Errorf("%s: %q is inside a selected folder and goes with it", target, c)
			}
		}
		if r.Files != 1 || r.Folders != 1 || r.Covered != 3 {
			t.Errorf("%s: files %d, folders %d, covered %d", target, r.Files, r.Folders, r.Covered)
		}
	}
}

func TestSimpleScriptNeverDeletesTheShare(t *testing.T) {
	for _, target := range allTargets {
		for _, root := range []string{"/", ""} {
			p := simpleParams(target, LangEN)
			p.Dirs = []Dir{{Path: root, Files: 5}}
			r, err := Generate([]File{{Path: "/x.mkv"}}, p)
			if err != nil {
				t.Fatal(err)
			}
			if len(r.Skipped) != 1 || r.Skipped[0].Reason != ReasonRoot {
				t.Errorf("%s: folder %q: skipped %s", target, root, names(r.Skipped))
			}
			_, commands, _ := scriptLines(t, target, r.Script)
			if len(commands) != 1 {
				t.Errorf("%s: folder %q: %d commands, want only the file", target, root, len(commands))
			}
		}
		p := simpleParams(target, LangEN)
		p.Dirs = []Dir{{Path: "/"}}
		if _, err := Generate(nil, p); !errors.Is(err, ErrNoFiles) {
			t.Errorf("%s: only the share: %v, want ErrNoFiles", target, err)
		}
	}
}

func TestSimpleCmdScriptTakesWhatTheAdvancedOneCannot(t *testing.T) {
	// Folders: the advanced cmd script cannot check what is in one; rd needs no check.
	// Empty files: the advanced cmd script cannot tell one from a folder; del takes it.
	p := simpleParams(TargetCmd, LangEN)
	p.Dirs = []Dir{{Path: "/Old"}}
	r := mustGenerate(t, []File{{Path: "/empty.txt", Size: 0}}, p)
	if len(r.Skipped) != 0 || r.Files != 1 || r.Folders != 1 {
		t.Errorf("the simple cmd script leaves out %s", names(r.Skipped))
	}
	adv := paramsFor(TargetCmd, LangEN)
	adv.Dirs = []Dir{{Path: "/Old"}}
	ra, err := Generate([]File{{Path: "/empty.txt", Size: 0}}, adv)
	if !errors.Is(err, ErrNoFiles) || len(ra.Skipped) != 2 {
		t.Fatalf("the advanced cmd script must still leave both out: %v %s", err, names(ra.Skipped))
	}
}

func TestSimpleCmdScriptLeavesOutWhatItCannotQuote(t *testing.T) {
	files := []File{{Path: "/a/100% pure.mkv"}, {Path: "/a/wow!.mkv"}, {Path: "/a/plain.mkv"}, {Path: "/a/it's & (x) ^ y.mkv"}}
	for _, target := range allTargets {
		p := simpleParams(target, LangEN)
		p.Dirs = []Dir{{Path: "/b/100%"}, {Path: "/b/fine"}}
		r := mustGenerate(t, files, p)
		_, commands, _ := scriptLines(t, target, r.Script)
		if target == TargetCmd {
			// A percent sign is read as a variable even between double quotes, and an exclamation mark where delayed expansion is on.
			if len(commands) != 3 || len(r.Skipped) != 3 {
				t.Fatalf("cmd: %d commands, skipped %s", len(commands), names(r.Skipped))
			}
			for _, s := range r.Skipped {
				if s.Reason != ReasonCmdChars {
					t.Errorf("cmd: %s left out for %s", s.Path, s.Reason)
				}
			}
			for _, c := range commands {
				if strings.ContainsAny(c, "%!") {
					t.Errorf("cmd: %q has a character cmd may read as a variable", c)
				}
			}
			continue
		}
		// sh and PowerShell quote everything: these names go in, whole.
		if len(commands) != 6 || len(r.Skipped) != 0 {
			t.Errorf("%s: %d commands, skipped %s", target, len(commands), names(r.Skipped))
		}
	}
	p := simpleParams(TargetCmd, LangEN)
	p.Root = `\\ZBNAS\Media!`
	var re *RootError
	if _, err := Generate(files, p); !errors.As(err, &re) {
		t.Errorf("a cmd root with an exclamation mark is accepted: %v", err)
	}
	p.Target, p.Root = TargetPowerShell, `\\ZBNAS\Media!`
	if _, err := Generate(files, p); err != nil {
		t.Errorf("PowerShell quotes the exclamation mark: %v", err)
	}
}

func TestSimpleScriptKeepsTheRulesAboutNames(t *testing.T) {
	// Whatever the advanced script cannot address, the simple one leaves out too: the lines are typed into a terminal, where a control character
	// ends the line and a name Windows reserves does something else than delete the file.
	files := []File{{Path: "/a/line\nbreak.mkv"}, {Path: "/a/bad\xffutf.mkv"}, {Path: "/a/CON.mkv"}, {Path: "/a/q?.mkv"}, {Path: "/a/ok.mkv"}, {Path: "/a/../up.mkv"}}
	for _, target := range allTargets {
		r := mustGenerate(t, files, simpleParams(target, LangEN))
		_, commands, _ := scriptLines(t, target, r.Script)
		want := 1
		if target == TargetSh {
			want = 3 // CON and q? are fine on a Linux file system
		}
		if len(commands) != want {
			t.Errorf("%s: %d commands, want %d:\n%s", target, len(commands), want, strings.Join(commands, "\n"))
		}
		for _, c := range commands {
			if strings.ContainsAny(c, "\r\xff") || strings.Contains(c, "..") {
				t.Errorf("%s: %q should have been left out", target, c)
			}
		}
	}
	// A Windows path beyond what cmd and Windows PowerShell 5.1 can open is left out as well.
	long := "/" + strings.Repeat("d", 130) + "/" + strings.Repeat("f", 130) + ".mkv"
	for _, target := range []Target{TargetPowerShell, TargetCmd} {
		r := mustGenerate(t, []File{{Path: long}, {Path: "/short.mkv"}}, simpleParams(target, LangEN))
		if r.Files != 1 || len(r.Skipped) != 1 || r.Skipped[0].Reason != ReasonTooLong {
			t.Errorf("%s: %+v skipped %s", target, *r, names(r.Skipped))
		}
	}
}

// hostileSkipped are names a person may have given a file, as the list of what was left out shows them.
var hostileSkipped = []string{
	"/x; rm -rf ~ #.mkv", "/a/$(id).mkv", "/a/`id`.mkv", "/a/' ; id ; '.mkv", `/a/" & del *.* & ".mkv`, "/a/100%PATH%.mkv", "/a/!x!.mkv",
	"/a/^&|<>.mkv", "/a/line\nrm -rf ~\n.mkv", "/a/cr\rx.mkv", "/a/\u2028x.mkv", "/a/\u202ertl.mkv", "/a/ünï-日本語.mkv", "/a/\\x.mkv",
	"/a/(paren).mkv", "/a/{brace}.mkv", "/a/*.mkv", "/a/~.mkv", "/a/bad\xffutf.mkv", "/a/tab\tx.mkv", "/a/emoji-\U0001F600.mkv", "/a/\x1b[31mred.mkv",
}

// commandStart is how every command of a simple script begins, and the quote that follows the path begins with.
var commandStart = map[Target]*regexp.Regexp{
	TargetSh:         regexp.MustCompile(`^rm -(rf|f) -- '`),
	TargetPowerShell: regexp.MustCompile(`^Remove-Item -LiteralPath '`),
	TargetCmd:        regexp.MustCompile(`^(del /f /q|rd /s /q) "`),
}

// wholeLineOK are the characters a comment line may hold beside letters and digits: nothing in it ends a line, quotes, substitutes or redirects.
func wholeLineOK(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r) || strings.ContainsRune(" .,:_/()-?", r)
}

func TestSimpleScriptTextForAPersonCannotBeReadAsACommand(t *testing.T) {
	for _, target := range allTargets {
		for _, lang := range allLangs {
			p := simpleParams(target, lang)
			for _, path := range hostileSkipped {
				p.Skipped = append(p.Skipped, Skip{Path: path, Reason: ReasonGone})
			}
			// Files that are left out here because of their name get the same treatment as the ones the caller left out.
			var files []File
			for _, path := range hostileSkipped {
				files = append(files, File{Path: path})
			}
			files = append(files, File{Path: "/keep/this.mkv"})
			p.Dirs = []Dir{{Path: "/keep/folder"}}
			r := mustGenerate(t, files, p)
			comments, commands, _ := scriptLines(t, target, r.Script)
			// A line that is not a comment is a command for a file or folder that is in the script, and nothing else: a name with a line break in it
			// is left out, not split over two lines, so the script has as many lines as it has commands.
			if len(commands) != r.Files+r.Folders {
				t.Fatalf("%s/%s: %d lines that are not comments for %d files and folders:\n%s", target, lang, len(commands), r.Files+r.Folders, strings.Join(commands, "\n"))
			}
			for _, c := range commands {
				if !commandStart[target].MatchString(c) {
					t.Fatalf("%s/%s: %q is not one of the commands", target, lang, c)
				}
			}
			for _, c := range comments {
				for _, ch := range c {
					if !wholeLineOK(ch) {
						t.Fatalf("%s/%s: the comment %q holds %q, which a shell could read as more than a comment", target, lang, c, ch)
					}
				}
			}
			// The names are still there to be read, with a ? where a character could not be shown.
			joined := strings.Join(comments, "\n")
			for _, want := range []string{"x? rm -rf ? ?.mkv", "ünï-日本語.mkv", "?id?.mkv"} {
				if !strings.Contains(joined, want) {
					t.Errorf("%s/%s: %q is not shown in\n%s", target, lang, want, joined)
				}
			}
		}
	}
}

func TestCommentSafe(t *testing.T) {
	for in, want := range map[string]string{
		"/a/b c.mkv":       "/a/b c.mkv",
		"x; rm -rf ~ #":    "x? rm -rf ? ?",
		"$(id) `id` 'a'":   "??id? ?id? ?a?",
		"line\nbreak":      "line?break",
		"日本語_é-ü.1":        "日本語_é-ü.1",
		"%PATH% !x! ^ & |": "?PATH? ?x? ? ? ?",
		"\u202e\u200b\x00": "???",
	} {
		if got := commentSafe(in); got != want {
			t.Errorf("commentSafe(%q) = %q, want %q", in, got, want)
		}
	}
	long := commentSafe(strings.Repeat("a", 500))
	if len(long) != 123 || !strings.HasSuffix(long, "...") {
		t.Errorf("a long name is cut to %d characters: %q", len(long), long)
	}
}

func TestSimpleScriptSaysWhatItLeftOut(t *testing.T) {
	for _, target := range allTargets {
		files := []File{{Path: "/a/gone.mkv"}, {Path: "/a/here.mkv"}}
		p := simpleParams(target, LangEN)
		p.Skipped = []Skip{{Path: "/a/gone.mkv", Reason: ReasonGone}}
		p.Dirs = []Dir{{Path: "/"}}
		r := mustGenerate(t, files[1:], p)
		comments, _, _ := scriptLines(t, target, r.Script)
		joined := strings.Join(comments, "\n")
		if !strings.Contains(joined, "NOT in this script (2)") || !strings.Contains(joined, "/a/gone.mkv: no longer in the index") ||
			!strings.Contains(joined, "this is the share itself") {
			t.Errorf("%s: what was left out is not listed:\n%s", target, joined)
		}
	}
	// A long list is cut short, and says so.
	p := simpleParams(TargetSh, LangEN)
	for i := 0; i < 55; i++ {
		p.Skipped = append(p.Skipped, Skip{Path: fmt.Sprintf("/gone/%02d.mkv", i), Reason: ReasonGone})
	}
	r := mustGenerate(t, []File{{Path: "/keep.mkv"}}, p)
	comments, _, _ := scriptLines(t, TargetSh, r.Script)
	joined := strings.Join(comments, "\n")
	if !strings.Contains(joined, "/gone/39.mkv") || strings.Contains(joined, "/gone/40.mkv") || !strings.Contains(joined, "... and 15 more") {
		t.Errorf("the list is not cut at 40:\n%s", joined)
	}
}

func TestSimpleScriptSaysWhatWentWithAFolder(t *testing.T) {
	files := []File{{Path: "/a/x.mkv"}, {Path: "/a/sub/y.mkv"}, {Path: "/keep.mkv"}}
	p := simpleParams(TargetSh, LangNL)
	p.Dirs = []Dir{{Path: "/a"}}
	r := mustGenerate(t, files, p)
	comments, _, _ := scriptLines(t, TargetSh, r.Script)
	if !strings.Contains(strings.Join(comments, "\n"), "2 gekozen bestanden of mappen liggen in een gekozen map") {
		t.Errorf("the notes do not say what went with the folder:\n%s", strings.Join(comments, "\n"))
	}
}
