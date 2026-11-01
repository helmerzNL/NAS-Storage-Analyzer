package delscript

import (
	"fmt"
	"sort"
	"strings"
	"unicode"
)

// The simple script (ADR-0011) is the other end of the advanced one. It is a text file with one ordinary command per file or folder, made to be read,
// cut down to the lines that are wanted and pasted into a terminal: from Windows, over SSH, on the NAS. It asks nothing, checks nothing and keeps
// no data block, so what a person reads is what runs. That is the opposite of what ADR-0006 chose for the advanced script, and it is only
// acceptable because of what this style is for and what it is not allowed to do:
//
//   - A path is quoted in the way its language needs, and a name that cannot be quoted for certain is left out and reported (ClassifyPath, classifySimple),
//     the same way the advanced script leaves out a name it cannot address.
//   - It is for a small number (MaxSimple). A paste of hundreds of lines runs before anyone can look.
//   - The lines at the top, which are text for a person, hold nothing a shell could read as a command: see commentSafe and the test over the messages.
//
// The commands are the ones of the language, not a pretend "rm" in all three: Remove-Item for PowerShell, and del and rd for cmd.

// classifySimple is what a simple script refuses on top of ClassifyPath. The commands are typed or pasted into cmd.exe, which reads a percent sign as the
// start of a variable and, where delayed expansion is on, an exclamation mark as well, even between double quotes. A name with one of them cannot
// be quoted for certain, so it is left out. The other languages quote everything.
func classifySimple(t Target, rel string) Reason {
	if t == TargetCmd && strings.ContainsAny(rel, "%!") {
		return ReasonCmdChars
	}
	return ""
}

// classifyDirSimple is ClassifyDir for a simple script: the share itself is never deleted, and cmd does take a folder here, because rd needs no
// check of what is in it. That is the whole difference with ClassifyDir.
func classifyDirSimple(t Target, rel string) Reason {
	if rel == "" || rel == "/" {
		return ReasonRoot
	}
	if r := ClassifyPath(t, rel); r != "" {
		return r
	}
	return classifySimple(t, rel)
}

// commentSafe makes a name fit to be printed in a comment line: letters, digits, space and . _ - / stay, and anything else becomes "?". A comment line
// is only a comment because the shell says so (zsh does not, by default), and a name is anything the person who made the folder typed, so
// the only names that may appear in one are names that cannot end the line, open a quote or start a substitution whatever the shell makes of them.
func commentSafe(s string) string {
	const longest = 120
	var b strings.Builder
	n := 0
	for _, r := range s {
		if n == longest {
			b.WriteString("...")
			break
		}
		n++
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r), r == ' ', r == '.', r == '_', r == '-', r == '/':
			b.WriteRune(r)
		default:
			b.WriteByte('?')
		}
	}
	return b.String()
}

// simpleLine is one command of a simple script, kept with the path it is for so the commands can be put in the order of the paths.
type simpleLine struct{ rel, text string }

// renderSimple writes a simple script: a few comment lines for the person, then one command per file or folder, sorted by path.
// Windows scripts have CRLF line ends, and the PowerShell one starts with a byte order mark: the file is UTF-8 and Windows PowerShell 5.1 reads
// a file as the ANSI code page without it. The cmd script says "chcp 65001" first, which is what lets cmd read a name that is not ASCII.
func renderSimple(t *messages, p Params, joined string, keep []entry, folders []dirEntry, r *Result) string {
	cmd := p.Target == TargetCmd
	prefix := "# "
	if cmd {
		prefix = "rem "
	}
	var out []string
	if cmd {
		out = append(out, "@echo off", "chcp 65001 >nul")
	}
	c := func(s string) { out = append(out, prefix+s) }

	c(t.simpleTitle)
	c(fmt.Sprintf(t.simpleCount, r.Files+r.Folders, r.Files, r.Folders, p.ScanTime.UTC().Format("2006-01-02 15:04 UTC"), p.ScanID))
	for _, n := range t.simpleNote {
		c(n)
	}
	if r.Covered > 0 {
		c(fmt.Sprintf(t.covered, r.Covered))
	}
	if len(r.Skipped) > 0 {
		c(fmt.Sprintf(t.skippedHead, len(r.Skipped)))
		const showSkipped = 40
		for i, s := range r.Skipped {
			if i == showSkipped {
				c(fmt.Sprintf(t.skippedMore, len(r.Skipped)-showSkipped))
				break
			}
			c(fmt.Sprintf(t.skippedLine, commentSafe(s.Path), t.reason[s.Reason]))
		}
	}
	out = append(out, "")

	lines := make([]simpleLine, 0, len(keep)+len(folders))
	for _, e := range keep {
		lines = append(lines, simpleLine{e.rel, simpleCommand(p.Target, joined+e.path, false)})
	}
	for _, d := range folders {
		lines = append(lines, simpleLine{d.rel, simpleCommand(p.Target, joined+d.path, true)})
	}
	sort.Slice(lines, func(i, j int) bool { return lines[i].rel < lines[j].rel })
	for _, l := range lines {
		out = append(out, l.text)
	}

	eol := "\n"
	if p.Target.IsWindows() {
		eol = "\r\n"
	}
	s := strings.Join(out, eol) + eol
	if p.Target == TargetPowerShell {
		s = psBOM + s
	}
	return s
}

// simpleCommand is the command that deletes the file at full, or the folder with everything in it.
//
//	sh          rm -f -- '/mnt/user/Media/film.mkv'        rm -rf -- '/mnt/user/Media/film'
//	PowerShell  Remove-Item -LiteralPath '…' -Force        Remove-Item -LiteralPath '…' -Recurse -Force
//	cmd         del /f /q "…"                              rd /s /q "…"
//
// Force (-f, /f) is there because a terminal that is pasted into asks a question about a write-protected file, and the next pasted line would be taken
// as the answer. -LiteralPath is there because PowerShell would otherwise read [ ] * ? in a name as a pattern.
func simpleCommand(t Target, full string, folder bool) string {
	switch t {
	case TargetSh:
		if folder {
			return "rm -rf -- " + shQuote(full)
		}
		return "rm -f -- " + shQuote(full)
	case TargetPowerShell:
		if folder {
			return "Remove-Item -LiteralPath " + psQuote(full) + " -Recurse -Force"
		}
		return "Remove-Item -LiteralPath " + psQuote(full) + " -Force"
	default:
		if folder {
			return `rd /s /q "` + full + `"`
		}
		return `del /f /q "` + full + `"`
	}
}
