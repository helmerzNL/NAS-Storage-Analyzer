package worker

import (
	"strings"
	"testing"

	"github.com/helmerznl/nas-storage-analyzer/internal/store"
)

func TestScanOutcome(t *testing.T) {
	for name, c := range map[string]struct {
		totals   store.ScanTotals
		errs     int64
		action   string
		wantHint string
	}{
		"files found":                    {store.ScanTotals{Dirs: 3, Files: 10}, 0, "scan.finished", ""},
		"nothing at all":                 {store.ScanTotals{}, 0, "scan.empty", "mounted"},
		"only the root folder":           {store.ScanTotals{Dirs: 1}, 0, "scan.empty", "NASA_SHARES_DIR"},
		"empty and unreadable":           {store.ScanTotals{Dirs: 1}, 4, "scan.empty", "mounted"},
		"folders but no files":           {store.ScanTotals{Dirs: 5}, 0, "scan.finished", ""},
		"files, some folders unreadable": {store.ScanTotals{Dirs: 3, Files: 10}, 2, "scan.errors", "2 folders or files could not be read"},
		"a single file below the root":   {store.ScanTotals{Dirs: 1, Files: 1}, 0, "scan.finished", ""},
	} {
		action, hint := scanOutcome(c.totals, c.errs)
		if action != c.action || !strings.Contains(hint, c.wantHint) || (c.wantHint == "" && hint != "") {
			t.Errorf("%s: got %q %q, want %q containing %q", name, action, hint, c.action, c.wantHint)
		}
	}
}

func TestFailureHint(t *testing.T) {
	for msg, wantHint := range map[string]bool{
		"open /mnt/user/Media/Films: permission denied":                               true,
		"scan failed: Permission Denied":                                              true,
		"the share is empty, but the previous scan found files: is it still mounted?": false,
		"another scan of this share is already running":                               false,
		"": false,
	} {
		got := failureHint(msg)
		if (got != "") != wantHint {
			t.Errorf("%q: hint %q, want a hint: %v", msg, got, wantHint)
		}
		if wantHint && !strings.Contains(got, "NASA_SHARES_GID") {
			t.Errorf("%q: the hint must name the setting: %s", msg, got)
		}
	}
}
