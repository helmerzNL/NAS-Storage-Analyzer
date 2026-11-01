package delscript

import (
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"
)

var (
	scanTime  = time.Date(2026, 10, 31, 12, 30, 0, 0, time.UTC)
	generated = time.Date(2026, 10, 31, 13, 0, 0, 0, time.UTC)
)

// rootFor is a valid base path for each target.
func rootFor(t Target) string {
	if t.IsWindows() {
		return `\\ZBNAS\Media`
	}
	return "/mnt/user/Media"
}

func paramsFor(t Target, l Lang) Params {
	return Params{Share: "Media", Root: rootFor(t), Target: t, Lang: l, ScanID: 7, ScanTime: scanTime, Generated: generated}
}

var allTargets = []Target{TargetSh, TargetPowerShell, TargetCmd}
var allLangs = []Lang{LangEN, LangNL}

// dataLine matches a record of the data block. No line of code starts like this.
var dataLine = regexp.MustCompile(`(?m)^[NRITFD]\|.*(\r?\n|$)`)

// codeRegion is the script without its data block.
func codeRegion(script []byte) string { return dataLine.ReplaceAllString(string(script), "") }

// fileSet makes n plain files with distinct names.
func fileSet(n int, prefix string) []File {
	out := make([]File, n)
	for i := range out {
		out[i] = File{Path: fmt.Sprintf("/%s/f%03d.mkv", prefix, i), Size: int64(100 + i), Mtime: 1761900000 + int64(i), Nlink: 1}
	}
	return out
}

func mustGenerate(t *testing.T, files []File, p Params) *Result {
	t.Helper()
	r, err := Generate(files, p)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	return r
}

func names(sk []Skip) string {
	var parts []string
	for _, s := range sk {
		parts = append(parts, fmt.Sprintf("%s=%s", s.Path, s.Reason))
	}
	return strings.Join(parts, ", ")
}
