package delscript

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"
)

// Reason says why a file is not in a script. The codes are stable: the server returns them and the interface may
// show them.
type Reason string

const (
	// ReasonGone and ReasonAmbiguous are decided by the caller, who has the index: the id is no longer in it (a scan
	// ran since the file was selected), or a sibling differs from the name only in case or normalisation.
	ReasonGone      Reason = "gone"
	ReasonAmbiguous Reason = "ambiguous"
	// The rest are decided here, from the name alone.
	ReasonEncoding    Reason = "encoding"       // U+FFFD: the scanner replaced bytes that were not text
	ReasonControl     Reason = "control"        // control, line-break, invisible or bidirectional characters
	ReasonWindowsName Reason = "windows-name"   // a character or ending Windows cannot address
	ReasonReserved    Reason = "reserved-name"  // CON, NUL, COM1, ...
	ReasonTooLong     Reason = "too-long"       // beyond what cmd and Windows PowerShell 5.1 can open
	ReasonEmptyCmd    Reason = "empty-file-cmd" // cmd cannot tell an empty file from a folder
	// Only for a folder that is to be deleted with everything in it.
	ReasonRoot      Reason = "root"       // the share itself: its own folder is never deleted
	ReasonFolderCmd Reason = "folder-cmd" // cmd cannot check what is in a folder, so it takes none
	// Only for a simple cmd script, whose lines are typed or pasted into cmd.exe.
	ReasonCmdChars Reason = "cmd-chars" // cmd may read a percent sign or an exclamation mark in the name as a variable
)

// maxWindowsPath is MAX_PATH less the terminating NUL. Windows PowerShell 5.1 and cmd cannot open longer paths
// whatever LongPathsEnabled says, so such a file is left out instead of being reported as "already gone".
const maxWindowsPath = 259

// controlRune reports the characters that must never reach a script or a terminal as they are: they can start
// a new line, hide part of a name, or reorder how it is shown. It is deliberately wider than unicode.IsControl.
func controlRune(r rune) bool {
	switch {
	case unicode.IsControl(r): // C0, DEL and C1 (includes U+0085)
		return true
	case r == 0x2028 || r == 0x2029: // line and paragraph separator
		return true
	case r == 0x200b || r == 0x200e || r == 0x200f || r == 0x061c || r == 0xfeff: // zero width space, direction marks, BOM
		return true
	case r >= 0x202a && r <= 0x202e: // bidirectional embedding and override
		return true
	case r >= 0x2066 && r <= 0x2069: // bidirectional isolates
		return true
	}
	return false
}

// windowsReserved are the device names Windows will not let a file have, with or without an extension.
var windowsReserved = map[string]bool{
	"CON": true, "PRN": true, "AUX": true, "NUL": true, "CONIN$": true, "CONOUT$": true,
	"COM1": true, "COM2": true, "COM3": true, "COM4": true, "COM5": true, "COM6": true, "COM7": true, "COM8": true, "COM9": true,
	"LPT1": true, "LPT2": true, "LPT3": true, "LPT4": true, "LPT5": true, "LPT6": true, "LPT7": true, "LPT8": true, "LPT9": true,
}

// classifyComponent judges one path component (a folder or the file name).
func classifyComponent(t Target, c string) Reason {
	if c == "" || c == "." || c == ".." {
		return ReasonControl
	}
	for _, r := range c {
		switch {
		case r == utf8.RuneError || r == 0xfffd:
			return ReasonEncoding
		case controlRune(r):
			return ReasonControl
		}
	}
	if !t.IsWindows() {
		return ""
	}
	if strings.ContainsAny(c, `<>:"|?*\`) {
		return ReasonWindowsName
	}
	if strings.HasSuffix(c, " ") || strings.HasSuffix(c, ".") {
		return ReasonWindowsName
	}
	stem := c
	if i := strings.IndexByte(stem, '.'); i >= 0 {
		stem = stem[:i]
	}
	if windowsReserved[strings.ToUpper(strings.TrimRight(stem, " "))] {
		return ReasonReserved
	}
	return ""
}

// ClassifyPath judges a path below the share root ("/folder/name", "/" separators) for a target. It returns "" when
// every component can be addressed unambiguously, otherwise the reason. Every folder in the path counts, not
// only the file name: a folder that cannot be addressed makes the file unreachable too.
func ClassifyPath(t Target, rel string) Reason {
	if !strings.HasPrefix(rel, "/") || strings.Contains(rel, "//") {
		return ReasonControl
	}
	for _, c := range strings.Split(rel[1:], "/") {
		if r := classifyComponent(t, c); r != "" {
			return r
		}
	}
	return ""
}

// ClassifyDir judges the path of a folder that is to be deleted with everything in it: ClassifyPath, and two things only a
// folder has. The share itself (the path "/") is never deleted, whatever is asked: it is the one folder that would take
// everything with it. And cmd takes no folders at all, because it cannot check what is in one (see renderCmd).
func ClassifyDir(t Target, rel string) Reason {
	if rel == "" || rel == "/" {
		return ReasonRoot
	}
	if t == TargetCmd {
		return ReasonFolderCmd
	}
	return ClassifyPath(t, rel)
}

// utf16Len is how many UTF-16 units Windows counts for s.
func utf16Len(s string) int {
	n := 0
	for _, r := range s {
		n += len(utf16.Encode([]rune{r}))
	}
	return n
}

// displayEscape makes any text safe to print: the characters that classify as control, and U+FFFD, are shown
// as \xNN or \uNNNN. It is used for everything derived from a file name that is printed (never executed).
func displayEscape(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == utf8.RuneError || r == 0xfffd:
			b.WriteString(`�`)
		case controlRune(r) && r < 0x100:
			fmt.Fprintf(&b, `\x%02X`, r)
		case controlRune(r):
			fmt.Fprintf(&b, `\u%04X`, r)
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}
