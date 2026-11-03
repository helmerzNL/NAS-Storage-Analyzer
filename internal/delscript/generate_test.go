package delscript

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"unicode/utf8"
)

var update = flag.Bool("update", false, "rewrite the golden scripts in testdata/golden")

// The whole safety argument: a file name can only ever land in the data block. Two different selections give a
// script whose code is the same byte for byte, so nothing a name contains can have changed what the code does.
func TestCodeDoesNotDependOnTheFiles(t *testing.T) {
	hostile := []File{
		{Path: "/ZZMARK/it's `id` $(id) ${HOME} 100% %PATH% !x! ^& ( ) | < > \" ;.txt", Size: 5, Mtime: 1},
		{Path: "/ZZMARK/日本語 😀 \u2019q\u2019.txt", Size: 6, Mtime: 2},
		{Path: "/ZZMARK/bad\nrm -rf ~", Size: 7},
		{Path: "/ZZMARK/\u202eevil.txt", Size: 8},
		{Path: "/ZZMARK/NUL", Size: 9},
	}
	for _, tg := range allTargets {
		for _, l := range allLangs {
			p := paramsFor(tg, l)
			a := mustGenerate(t, fileSet(3, "ONE"), p)
			p2 := p
			p2.Share = "Other share"
			p2.ScanID = 99
			p2.Skipped = []Skip{{Path: "/ZZMARK/x\ny", Reason: ReasonGone}}
			b := mustGenerate(t, append(fileSet(40, "TWO"), hostile...), p2)
			if codeRegion(a.Script) != codeRegion(b.Script) {
				t.Errorf("%s/%s: the code region differs between two selections", tg, l)
			}
			if strings.Contains(codeRegion(b.Script), "ZZMARK") {
				t.Errorf("%s/%s: a file name reached the code region", tg, l)
			}
			// And nothing derived from a name starts a line of code: every such line is a record.
			for _, line := range strings.Split(strings.ReplaceAll(string(b.Script), "\r\n", "\n"), "\n") {
				if strings.Contains(line, "ZZMARK") && !regexp.MustCompile(`^[IF]\|`).MatchString(line) {
					t.Errorf("%s/%s: a name appears outside a record: %q", tg, l, line)
				}
			}
		}
	}
}

func TestCodeRegionIsPlainAsciiWithoutDataLookalikes(t *testing.T) {
	for _, tg := range allTargets {
		for _, l := range allLangs {
			code := codeRegion(mustGenerate(t, fileSet(2, "A"), paramsFor(tg, l)).Script)
			code = strings.TrimPrefix(code, "\xef\xbb\xbf") // the BOM of the PowerShell script
			for i, r := range code {
				if r >= utf8.RuneSelf {
					t.Errorf("%s/%s: non-ASCII character %q in the code region at byte %d", tg, l, r, i)
					break
				}
			}
		}
	}
}

// cmd skips its own code with skip=N when it reads the data back; N must be exactly the number of lines of code.
func TestCmdSkipCountIsExact(t *testing.T) {
	for _, l := range allLangs {
		script := string(mustGenerate(t, fileSet(3, "A"), paramsFor(TargetCmd, l)).Script)
		lines := strings.Split(strings.TrimSuffix(script, "\r\n"), "\r\n")
		first := -1
		for i, line := range lines {
			if strings.HasPrefix(line, "N|") {
				first = i
				break
			}
		}
		if first < 0 {
			t.Fatal("no data block")
		}
		for i, line := range lines[:first] {
			if line == "" || strings.HasPrefix(line, ";") {
				t.Errorf("line %d of the code is %q: for /f would treat it differently from the other lines", i+1, line)
			}
		}
		skips := regexp.MustCompile(`skip=(\d+) `).FindAllStringSubmatch(script, -1)
		if len(skips) != 5 {
			t.Fatalf("%d loops with skip, want 5", len(skips))
		}
		for _, m := range skips {
			if m[1] != fmt.Sprint(first) {
				t.Errorf("skip=%s but there are %d lines of code", m[1], first)
			}
		}
		if strings.Contains(script, "SKIPN") {
			t.Errorf("a placeholder was left in the script")
		}
	}
}

// bash 3.2 is the sh of every Mac. It ends a $( ) at the first ) it sees, also the one that closes a case pattern, so a
// case inside a command substitution is a syntax error there. The tests run in the shells of the CI machine, and macOS
// is the only place that has bash 3.2, so this keeps the rule where it can be seen everywhere.
func TestShScriptHasNoCaseInsideACommandSubstitution(t *testing.T) {
	script := string(mustGenerate(t, fileSet(2, "A"), paramsFor(TargetSh, LangEN)).Script)
	for i, line := range strings.Split(script, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue // a comment may talk about it
		}
		if strings.Contains(line, "$(") && !strings.Contains(line, "$((") && regexp.MustCompile(`\bcase\b`).MatchString(line) {
			t.Errorf("line %d has a case inside $( ): %s", i+1, line)
		}
	}
	// And the case statements that are there sit in functions or groups, never on a line with a substitution.
	if !strings.Contains(script, "found() {") {
		t.Errorf("the count of found files must be a function")
	}
}

func TestLineEndingsAndEncodingPerTarget(t *testing.T) {
	files := fileSet(2, "Ünïcode")
	for tg, want := range map[Target]struct {
		crlf, bom bool
	}{TargetSh: {false, false}, TargetPowerShell: {true, true}, TargetCmd: {true, false}} {
		s := string(mustGenerate(t, files, paramsFor(tg, LangEN)).Script)
		if hasCRLF := strings.Contains(s, "\r\n"); hasCRLF != want.crlf {
			t.Errorf("%s: CRLF = %v, want %v", tg, hasCRLF, want.crlf)
		}
		if !want.crlf && strings.Contains(s, "\r") {
			t.Errorf("%s: a carriage return in a script that must be LF only", tg)
		}
		if want.crlf && strings.Count(s, "\n") != strings.Count(s, "\r\n") {
			t.Errorf("%s: a bare LF in a script that must be CRLF", tg)
		}
		if hasBOM := strings.HasPrefix(s, "\xef\xbb\xbf"); hasBOM != want.bom {
			t.Errorf("%s: BOM = %v, want %v", tg, hasBOM, want.bom)
		}
		if !utf8.ValidString(s) {
			t.Errorf("%s: not valid UTF-8", tg)
		}
		if !strings.Contains(s, "Ünïcode") {
			t.Errorf("%s: the non-ASCII folder name is missing from the data", tg)
		}
	}
	if s := string(mustGenerate(t, files, paramsFor(TargetSh, LangEN)).Script); !strings.HasPrefix(s, "#!/bin/sh\n") {
		t.Errorf("the sh script must start with a shebang: %.30q", s)
	}
	if s := string(mustGenerate(t, files, paramsFor(TargetCmd, LangEN)).Script); !strings.HasPrefix(s, "@echo off\r\n") {
		t.Errorf("the cmd script must start with @echo off: %.30q", s)
	}
}

func TestRecordsAndCounts(t *testing.T) {
	files := []File{
		{Path: "/A/one.mkv", Size: 1000, Mtime: 1700000000, Nlink: 1},
		{Path: "/A/two.mkv", Size: 2000, Mtime: 1700000001, Nlink: 3},
		{Path: "/A/two.mkv", Size: 2000, Mtime: 1700000001, Nlink: 3}, // the same file twice
		{Path: "/B/three.mkv", Size: 3000, Mtime: 0},
		{Path: "/B/bad\nname", Size: 1},
	}
	r := mustGenerate(t, files, paramsFor(TargetSh, LangEN))
	if r.Files != 3 || r.Bytes != 6000 || r.Hardlinks != 1 || len(r.Skipped) != 1 || r.Skipped[0].Reason != ReasonControl {
		t.Fatalf("result = %+v", r)
	}
	script := string(r.Script)
	for _, want := range []string{
		"\nN|3\n", "\nR|/mnt/user/Media\n",
		"\nF|1000|1700000000|/A/one.mkv\n", "\nF|2000|1700000001|/A/two.mkv\n", "\nF|3000|0|/B/three.mkv\n",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("missing record %q", want)
		}
	}
	if strings.Count(script, "\nF|") != 3 {
		t.Errorf("F records: %d, want 3", strings.Count(script, "\nF|"))
	}
	if !strings.Contains(script, `bad\x0Aname`) {
		t.Errorf("the skipped file must be listed with its control character escaped")
	}
	if strings.Contains(script, "bad\nname") {
		t.Errorf("a raw newline from a file name reached the script")
	}
}

func TestSkipsAndErrors(t *testing.T) {
	p := paramsFor(TargetCmd, LangEN)
	files := []File{
		{Path: "/ok/a.txt", Size: 10},
		{Path: "/empty/zero.txt", Size: 0}, // cmd cannot tell an empty file from a folder
		{Path: "/win/a:b.txt", Size: 10},
		{Path: "/win/NUL.txt", Size: 10},
		{Path: "/win/" + strings.Repeat("x", 300) + ".txt", Size: 10},
	}
	r := mustGenerate(t, files, p)
	want := map[string]Reason{"/empty/zero.txt": ReasonEmptyCmd, "/win/a:b.txt": ReasonWindowsName, "/win/NUL.txt": ReasonReserved,
		"/win/" + strings.Repeat("x", 300) + ".txt": ReasonTooLong}
	got := map[string]Reason{}
	for _, s := range r.Skipped {
		got[s.Path] = s.Reason
	}
	if r.Files != 1 || fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("files=%d skipped=%s", r.Files, names(r.Skipped))
	}
	// An empty file is fine for PowerShell and sh.
	for _, tg := range []Target{TargetPowerShell, TargetSh} {
		if r := mustGenerate(t, []File{{Path: "/empty/zero.txt", Size: 0}}, paramsFor(tg, LangEN)); r.Files != 1 {
			t.Errorf("%s: an empty file must be accepted", tg)
		}
	}
	// The caller's skips are kept and listed.
	p = paramsFor(TargetSh, LangEN)
	p.Skipped = []Skip{{Path: "/gone.txt", Reason: ReasonGone}, {Path: "/Case.txt", Reason: ReasonAmbiguous}}
	r = mustGenerate(t, fileSet(1, "x"), p)
	if len(r.Skipped) != 2 || !strings.Contains(string(r.Script), "no longer in the index") {
		t.Errorf("skipped = %s", names(r.Skipped))
	}

	if _, err := Generate(nil, paramsFor(TargetSh, LangEN)); !errors.Is(err, ErrNoFiles) {
		t.Errorf("no files: %v", err)
	}
	if _, err := Generate([]File{{Path: "/a\nb", Size: 1}}, paramsFor(TargetSh, LangEN)); !errors.Is(err, ErrNoFiles) {
		t.Errorf("only unusable files: %v", err)
	}
	if _, err := Generate(fileSet(MaxFiles+1, "x"), paramsFor(TargetSh, LangEN)); !errors.Is(err, ErrTooMany) {
		t.Errorf("too many files: %v", err)
	}
	if r, err := Generate(fileSet(MaxFiles, "x"), paramsFor(TargetSh, LangEN)); err != nil || r.Files != MaxFiles {
		t.Errorf("exactly MaxFiles must work: %v", err)
	}
	bad := paramsFor(TargetSh, LangEN)
	bad.Root = "not absolute"
	var re *RootError
	if _, err := Generate(fileSet(1, "x"), bad); !errors.As(err, &re) {
		t.Errorf("a bad root: %v", err)
	}
	bad = paramsFor(TargetSh, LangEN)
	bad.Target = "bash"
	if _, err := Generate(fileSet(1, "x"), bad); err == nil {
		t.Errorf("an unknown target must be refused")
	}
}

// The path a Windows script addresses is the root plus the path, with backslashes, and for cmd relative to the root.
func TestWindowsPathsAreConverted(t *testing.T) {
	files := []File{{Path: "/Movies/Action/a.mkv", Size: 5}}
	ps := string(mustGenerate(t, files, paramsFor(TargetPowerShell, LangEN)).Script)
	if !strings.Contains(ps, "\r\nF|5|0|\\Movies\\Action\\a.mkv\r\n") || !strings.Contains(ps, "\r\nR|\\\\ZBNAS\\Media\r\n") {
		t.Errorf("powershell records:\n%s", ps)
	}
	cmd := string(mustGenerate(t, files, paramsFor(TargetCmd, LangEN)).Script)
	if !strings.Contains(cmd, "\r\nF|5|0|Movies\\Action\\a.mkv\r\n") {
		t.Errorf("cmd records:\n%s", cmd)
	}
	// A drive root: the join must not double the backslash.
	p := paramsFor(TargetPowerShell, LangEN)
	p.Root = `Z:\`
	if s := string(mustGenerate(t, files, p).Script); !strings.Contains(s, "\r\nR|Z:\\\r\n") {
		t.Errorf("drive root record:\n%s", s)
	}
}

// The summary is data too, so it must be readable and honest: where, how many, how big, and what is left out.
func TestSummaryTellsTheTruth(t *testing.T) {
	p := paramsFor(TargetSh, LangEN)
	p.Skipped = []Skip{{Path: "/x/y", Reason: ReasonAmbiguous}}
	r := mustGenerate(t, []File{{Path: "/A/a.mkv", Size: 3 << 30, Nlink: 2}, {Path: "/A/b.mkv", Size: 1 << 20}}, p)
	s := string(r.Script)
	for _, want := range []string{
		"I|Base path:      /mnt/user/Media", "I|Files to delete: 2", "I|Size (upper bound): 3.0 GiB",
		"1 of these have other hard links", "I|NOT in this script (1)", "2023-11-14", // placeholder replaced below
	} {
		if want == "2023-11-14" {
			want = "2026-10-31 12:30 UTC (scan 7)"
		}
		if !strings.Contains(s, want) {
			t.Errorf("summary lacks %q", want)
		}
	}
	nl := string(mustGenerate(t, []File{{Path: "/A/a.mkv", Size: 1536}}, paramsFor(TargetSh, LangNL)).Script)
	if !strings.Contains(nl, "Padbasis:") || !strings.Contains(nl, "1,5 KiB") {
		t.Errorf("the Dutch summary:\n%s", nl)
	}
}

// Every message exists in both languages, every reason has a text, and the strings that end up in code contain none
// of the characters that mean something to cmd, PowerShell or sh inside a quoted string.
func TestMessagesAreCompleteAndSafe(t *testing.T) {
	reasons := []Reason{ReasonGone, ReasonAmbiguous, ReasonEncoding, ReasonControl, ReasonWindowsName, ReasonReserved, ReasonTooLong, ReasonEmptyCmd,
		ReasonRoot, ReasonFolderCmd, ReasonCmdChars}
	for _, m := range []*messages{&textEN, &textNL} {
		for _, r := range reasons {
			if m.reason[r] == "" {
				t.Errorf("no text for reason %q", r)
			}
		}
		code := []string{m.cancelled, m.notOpen, m.noStat, m.wrongRoot, m.deleted, m.gone, m.changed, m.notRegular, m.failed, m.notFolder, m.hasLinks, m.grown}
		code = append(code, m.confirm[:]...)
		code = append(code, m.incomplete[:]...)
		code = append(code, m.done[:]...)
		code = append(code, m.found[:]...)
		for _, s := range code {
			if s == "" {
				t.Errorf("an empty code message")
			}
			if strings.ContainsAny(s, "%&|<>^()\"'$`\\!\n\r") {
				t.Errorf("code message %q contains a character with a meaning in a shell", s)
			}
			for _, r := range s {
				if r >= utf8.RuneSelf {
					t.Errorf("code message %q is not ASCII", s)
				}
			}
		}
	}
	// What the simple script says in its comment lines is read by a person and pasted by a person into a shell, where a # line is only a comment when
	// the shell says so (zsh does not by default): nothing in it may be able to end the line or start a command, whatever shell it lands in.
	for _, m := range []*messages{&textEN, &textNL} {
		var comment []string
		comment = append(comment, m.simpleTitle, m.simpleCount, m.covered, m.skippedHead, m.skippedLine)
		comment = append(comment, m.simpleNote[:]...)
		for _, r := range reasons {
			comment = append(comment, m.reason[r])
		}
		for _, s := range comment {
			s = strings.NewReplacer("%d", "1", "%s", "x").Replace(s)
			if s == "" || strings.ContainsAny(s, ";&|<>$`'\"\\!%^\n\r") {
				t.Errorf("comment text %q is empty or holds a character that could end a line or start a command", s)
			}
			for _, r := range s {
				if r >= utf8.RuneSelf {
					t.Errorf("comment text %q is not ASCII", s)
				}
			}
		}
	}
	// The two languages have the same shape.
	if len(textEN.reason) != len(textNL.reason) {
		t.Errorf("reasons differ: %d and %d", len(textEN.reason), len(textNL.reason))
	}
}

// Scripts go through the reader the shell uses (a line at a time), so a name must never be able to end its line.
func TestNoRecordCanEndEarlyOrStartAnother(t *testing.T) {
	for _, tg := range allTargets {
		files := []File{
			{Path: "/a/x\u2028F|1|1|/etc/passwd", Size: 1},
			{Path: "/a/x\u0085N|9", Size: 1},
			{Path: "/a/ok|with|bars.txt", Size: 1},
		}
		if tg.IsWindows() {
			files[2].Path = "/a/ok;with;semicolons.txt" // a bar is not a character Windows allows in a name
		}
		r := mustGenerate(t, files, paramsFor(tg, LangEN))
		s := strings.ReplaceAll(string(r.Script), "\r\n", "\n")
		if strings.Count(s, "\nF|") != 1 || r.Files != 1 {
			t.Errorf("%s: a name must not be able to add a record; files=%d skipped=%s", tg, r.Files, names(r.Skipped))
		}
		if want := files[2].Path[len("/a/"):]; !strings.Contains(s, want) {
			t.Errorf("%s: %q must survive in the data", tg, want)
		}
	}
}

func FuzzGenerate(f *testing.F) {
	for _, s := range []string{"a.txt", "it's", "100%", "a\nb", "\u202e", "$(x)", "`x`", "a|b", "-n", "日本語", "\xff", "CON", "a:b", ""} {
		f.Add(s, int64(5), int64(7))
	}
	f.Fuzz(func(t *testing.T, name string, size, mtime int64) {
		files := []File{{Path: "/d/" + name, Size: size, Mtime: mtime}, {Path: "/d/plain.txt", Size: 3}}
		var ref map[Target]string
		for _, tg := range allTargets {
			r, err := Generate(files, paramsFor(tg, LangEN))
			if err != nil {
				t.Fatalf("%s: %v", tg, err)
			}
			base, err := Generate([]File{{Path: "/d/plain.txt", Size: 3}}, paramsFor(tg, LangEN))
			if err != nil {
				t.Fatal(err)
			}
			if codeRegion(r.Script) != codeRegion(base.Script) {
				t.Fatalf("%s: the code depends on the name %q", tg, name)
			}
			// Every record is one line; a name never splits or adds a line.
			lines := strings.Split(strings.ReplaceAll(string(r.Script), "\r\n", "\n"), "\n")
			f := 0
			for _, l := range lines {
				if strings.HasPrefix(l, "F|") {
					f++
				}
			}
			if f != r.Files {
				t.Fatalf("%s: %d F records for %d files, name %q", tg, f, r.Files, name)
			}
			_ = ref
		}
	})
}

// golden pins the exact text of one script per target and language, so a change to the code is a visible diff.
func TestGolden(t *testing.T) {
	files := []File{
		{Path: "/Movies/Action/a b.mkv", Size: 1500000000, Mtime: 1761900000, Nlink: 1},
		{Path: "/Movies/it's 100%.mkv", Size: 42, Mtime: 1761900001, Nlink: 2},
		{Path: "/Music/\u65e5\u672c\u8a9e.flac", Size: 7, Mtime: 0, Nlink: 1},
	}
	for _, tg := range allTargets {
		for _, l := range allLangs {
			p := paramsFor(tg, l)
			p.Skipped = []Skip{{Path: "/Movies/gone.mkv", Reason: ReasonGone}}
			got := mustGenerate(t, files, p).Script
			name := filepath.Join("testdata", "golden", fmt.Sprintf("%s.%s%s", tg, l, tg.Extension()))
			if *update {
				if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(name, got, 0o644); err != nil {
					t.Fatal(err)
				}
				continue
			}
			want, err := os.ReadFile(name)
			if err != nil {
				t.Fatalf("%v (run go test -update to create it)", err)
			}
			if !bytes.Equal(got, want) {
				t.Errorf("%s differs from the golden file; review the change and run go test -update", name)
			}
		}
	}
}
