package worker

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/helmerznl/nas-storage-analyzer/internal/scanner"
	"github.com/helmerznl/nas-storage-analyzer/internal/store"
	ts "github.com/helmerznl/nas-storage-analyzer/internal/testsupport"
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

func TestAddUnreadableWritesAnOverviewOfFolders(t *testing.T) {
	d := map[string]any{"errors": int64(3)}
	addUnreadable(d, scanner.UnreadableReport{})
	if _, ok := d["unreadable"]; ok {
		t.Errorf("a scan without problems has no list: %v", d)
	}

	addUnreadable(d, scanner.UnreadableReport{Total: 230, Truncated: true, Folders: []scanner.UnreadableFolder{
		{Path: "/private", Kind: scanner.ErrPermission, Self: true},
		{Path: "/shared/noexec", Kind: scanner.ErrPermission, Items: 12},
	}})
	list, _ := d["unreadable"].([]map[string]any)
	if len(list) != 2 {
		t.Fatalf("list = %v", d["unreadable"])
	}
	// A folder that could not be opened says so and has no count; a folder that could be listed has the count of its files.
	if list[0]["path"] != "/private" || list[0]["kind"] != "permission" || list[0]["self"] != true || list[0]["items"] != nil {
		t.Errorf("first = %v", list[0])
	}
	if list[1]["path"] != "/shared/noexec" || list[1]["items"] != 12 || list[1]["self"] != nil {
		t.Errorf("second = %v", list[1])
	}
	if d["unreadable_folders"] != 230 || d["unreadable_truncated"] != true {
		t.Errorf("the total is the number of folders, not the number listed: %v", d)
	}
	if d["errors"] != int64(3) {
		t.Errorf("the existing keys stay: %v", d)
	}
}

// The paths are names from a share, which can hold anything but "/" and a NUL; a jsonb column refuses U+0000, and one failed
// insert would take the audit entry of the whole scan with it.
func TestUnreadableNamesFromTheShareCanBeStoredInTheAuditLog(t *testing.T) {
	st := ts.NewStore(t)
	w := &Worker{st: st}
	d := map[string]any{}
	addUnreadable(d, scanner.UnreadableReport{Total: 3, Folders: []scanner.UnreadableFolder{
		{Path: "/with\x00nul", Kind: scanner.ErrPermission, Self: true},
		{Path: "/not\xff\xfeutf8", Kind: scanner.ErrIO, Items: 3},
		{Path: "/日本語 and \"quotes\" and \\ and \n newline", Kind: scanner.ErrOther, Self: true},
	}})
	w.audit(slog.New(slog.NewTextHandler(io.Discard, nil)), "scan.errors", "media", d)

	var raw []byte
	if err := st.Pool.QueryRow(context.Background(), `SELECT detail FROM audit_log WHERE action = 'scan.errors'`).Scan(&raw); err != nil {
		t.Fatalf("the entry must have been stored: %v", err)
	}
	var back struct {
		Unreadable []struct{ Path string } `json:"unreadable"`
	}
	if err := json.Unmarshal(raw, &back); err != nil || len(back.Unreadable) != 3 {
		t.Fatalf("detail = %s (%v)", raw, err)
	}
	for _, f := range back.Unreadable {
		if strings.ContainsRune(f.Path, 0) || !utf8.ValidString(f.Path) {
			t.Errorf("path %q must be valid text without NUL", f.Path)
		}
	}
	if back.Unreadable[2].Path != "/日本語 and \"quotes\" and \\ and \n newline" {
		t.Errorf("a name that is already fine must stay as it is: %q", back.Unreadable[2].Path)
	}
}
