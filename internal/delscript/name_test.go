package delscript

import (
	"strings"
	"testing"
)

func TestClassifyPathWindows(t *testing.T) {
	long := strings.Repeat("x", 100)
	for path, want := range map[string]Reason{
		"/Movies/a.mkv":                      "",
		"/Movies/100% pure!.mkv":             "",
		"/Movies/it's [1] (2) {3} ^ & ;.mkv": "",
		"/Movies/日本語 😀.mkv":                  "",
		"/Movies/" + long + ".mkv":           "",
		"/a/b:c.txt":                         ReasonWindowsName,
		"/a/b*c.txt":                         ReasonWindowsName,
		"/a/b?c.txt":                         ReasonWindowsName,
		`/a/b"c.txt`:                         ReasonWindowsName,
		"/a/b<c>.txt":                        ReasonWindowsName,
		"/a/b|c.txt":                         ReasonWindowsName,
		`/a/b\c.txt`:                         ReasonWindowsName,
		"/a/ends-in-dot.":                    ReasonWindowsName,
		"/a/ends-in-space ":                  ReasonWindowsName,
		"/a/CON":                             ReasonReserved,
		"/a/con.txt":                         ReasonReserved,
		"/a/NUL.tar.gz":                      ReasonReserved,
		"/a/Com1.log":                        ReasonReserved,
		"/a/lpt9":                            ReasonReserved,
		"/a/COM10.txt":                       "",
		"/a/console.txt":                     "",
		"/folder:with:colons/a.txt":          ReasonWindowsName, // a folder counts as much as the file
		"/CON/a.txt":                         ReasonReserved,
		"/a/bad\nname":                       ReasonControl,
		"/a/bad\rname":                       ReasonControl,
		"/a/bad\x1bname":                     ReasonControl,
		"/a/bad\x7fname":                     ReasonControl,
		"/a/bad\u0085name":                   ReasonControl, // next line
		"/a/bad\u2028name":                   ReasonControl,
		"/a/bad\u202ename":                   ReasonControl, // right-to-left override: hides how a name ends
		"/a/bad\u2066name":                   ReasonControl,
		"/a/bad\u200bname":                   ReasonControl, // zero width space
		"/a/bad\ufeffname":                   ReasonControl,
		"/a/bad�name":                        ReasonEncoding, // the scanner replaced bytes that were not text
		"/a/\xffbad":                         ReasonEncoding,
		"relative/path":                      ReasonControl,
		"/a//b":                              ReasonControl,
	} {
		for _, tg := range []Target{TargetCmd, TargetPowerShell} {
			if got := ClassifyPath(tg, path); got != want {
				t.Errorf("%s %q: got %q, want %q", tg, path, got, want)
			}
		}
	}
}

func TestClassifyPathSh(t *testing.T) {
	// sh can address everything a Linux file system holds except what cannot be told apart or shown honestly.
	for path, want := range map[string]Reason{
		"/Movies/a.mkv":               "",
		`/a/b"c*d?e|f\g<h>i:j.txt`:    "",
		"/a/ends-in-dot.":             "",
		"/a/ends-in-space ":           "",
		"/a/CON":                      "",
		"/a/-rf":                      "",
		"/a/$(id) `id` ${x} 'q' %s":   "",
		"/a/bad\nname":                ReasonControl,
		"/a/bad\u202ename":            ReasonControl,
		"/a/bad�name":                 ReasonEncoding,
		"/folder with\nnewline/a.txt": ReasonControl,
		"/a/emoji-\U0001F600-\u200d-joiner-is-ok.": "", // a zero width joiner is how family emoji are written
	} {
		if got := ClassifyPath(TargetSh, path); got != want {
			t.Errorf("sh %q: got %q, want %q", path, got, want)
		}
	}
}

func TestDisplayEscape(t *testing.T) {
	for in, want := range map[string]string{
		"plain":              "plain",
		"a\nb":               `a\x0Ab`,
		"a\x1b[31mred":       `a\x1B[31mred`,
		"a\u202eb":           `a\u202Eb`,
		"a�b":                `a�b`,
		`back\slash and 日本語`: `back\slash and 日本語`,
	} {
		if got := displayEscape(in); got != want {
			t.Errorf("%q: got %q, want %q", in, got, want)
		}
	}
	// Whatever goes in, nothing a terminal or a line reader would act on comes out.
	if out := displayEscape("x\r\n\x00\x1b\u2028\u0085y"); strings.ContainsAny(out, "\r\n\x00\x1b\u2028\u0085") {
		t.Errorf("control characters survived: %q", out)
	}
}
