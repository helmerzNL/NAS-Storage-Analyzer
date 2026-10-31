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
