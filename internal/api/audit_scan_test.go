package api_test

import (
	"fmt"
	"os"
	"strings"
	"testing"

	ts "github.com/helmerznl/nas-storage-analyzer/internal/testsupport"
)

// An error on a scan leads to the entry of the audit log that explains it: GET /audit?scan_id=N is the way from the one to the other. The scans here are
// real ones, made by the worker.
func TestAnErrorOfAScanLeadsToItsAuditEntry(t *testing.T) {
	e := newAuthEnv(t, nil)
	e.startWorker()
	admin, _, _ := e.setupAdmin()
	root := t.TempDir()
	for i := 0; i < 120; i++ { // enough files for the worker to refuse a scan that then finds nothing (an unmounted share looks like that)
		ts.Write(t, fmt.Sprintf("%s/d/f%03d.txt", root, i), 10)
	}
	s, _ := scriptShare(t, e, admin, "media", root)
	scanIDs := func() (last, latest float64) {
		sh := admin.do("GET", s, nil).Body
		last, latest = sh["last_scan"].(map[string]any)["id"].(float64), sh["latest_scan"].(map[string]any)["id"].(float64)
		return
	}
	good, _ := scanIDs()

	if err := os.RemoveAll(root + "/d"); err != nil {
		t.Fatal(err)
	}
	expect(t, admin.do("POST", s+"/scan", map[string]any{"mode": "full"}), 202, "")
	waitFor(t, "the emptied share to be refused", func() bool {
		sh := admin.do("GET", s, nil).Body
		l, _ := sh["latest_scan"].(map[string]any)
		return l != nil && l["status"] == "failed"
	})
	_, bad := scanIDs()
	if bad == good {
		t.Fatal("the refused scan has the id of the good one")
	}

	entry := func(scan float64) map[string]any {
		t.Helper()
		r := admin.do("GET", fmt.Sprintf("/api/v1/audit?scan_id=%d", int64(scan)), nil)
		expect(t, r, 200, "")
		items := r.Body["items"].([]any)
		if r.Body["next_before"] != nil || len(items) > 1 {
			t.Fatalf("a scan has at most one entry and no next page: %s", r.Raw)
		}
		if len(items) == 0 {
			return nil
		}
		return items[0].(map[string]any)
	}

	// A scan that went through: its outcome, and not the entry that says it was asked for (which also names the job).
	if g := entry(good); g == nil || g["action"] != "scan.finished" || g["target"] != "media" || g["detail"].(map[string]any)["scan_id"] != good {
		t.Errorf("the entry of the scan that went through: %+v", g)
	}
	// A scan that failed: the worker puts the scan's id in the entry.
	f := entry(bad)
	if f == nil || f["action"] != "scan.failed" || f["detail"].(map[string]any)["scan_id"] != bad || !strings.Contains(fmt.Sprint(f["detail"]), "still mounted") {
		t.Fatalf("the entry of the scan that failed: %+v", f)
	}
	id := f["id"]

	// An entry from before the worker wrote the scan id is found through its job.
	if _, err := e.st.Pool.Exec(t.Context(), `UPDATE audit_log SET detail = detail - 'scan_id' WHERE action = 'scan.failed'`); err != nil {
		t.Fatal(err)
	}
	if g := entry(bad); g == nil || g["id"] != id {
		t.Errorf("an old entry without scan_id is not found through its job: %+v", g)
	}
	// Only how a scan ended is its entry: a later entry that merely mentions the scan, or the job that started it, is not.
	if _, err := e.st.Pool.Exec(t.Context(), `
		INSERT INTO audit_log (username, action, target, detail)
		SELECT 'someone', 'share.update', 'media', jsonb_build_object('scan_id', $1::bigint)
		UNION ALL SELECT 'someone', 'scan.start', 'media', jsonb_build_object('job', j.id) FROM jobs j WHERE j.scan_id = $1::bigint`, int64(bad)); err != nil {
		t.Fatal(err)
	}
	if g := entry(bad); g == nil || g["id"] != id {
		t.Errorf("an entry that is not an outcome was taken for the entry of the scan: %+v", g)
	}
	// Once the job has been pruned there is nothing left to find it by: no entry, not a wrong one.
	if _, err := e.st.Pool.Exec(t.Context(), `DELETE FROM jobs`); err != nil {
		t.Fatal(err)
	}
	if g := entry(bad); g != nil {
		t.Errorf("an entry without scan_id and without its job is guessed at: %+v", g)
	}
	if g := entry(good); g == nil || g["action"] != "scan.finished" {
		t.Errorf("an entry that has its scan_id does not need the job: %+v", g)
	}
	// Should a scan ever have two, the later one is the one that is shown.
	var later int64
	if err := e.st.Pool.QueryRow(t.Context(), `INSERT INTO audit_log (username, action, target, detail) VALUES ('worker', 'scan.errors', 'media', jsonb_build_object('scan_id', $1::bigint)) RETURNING id`, int64(good)).Scan(&later); err != nil {
		t.Fatal(err)
	}
	if g := entry(good); g == nil || g["id"] != float64(later) {
		t.Errorf("the entry of a scan with two is not the later one: %+v", g)
	}
	// A scan nobody wrote an entry for.
	if g := entry(987654); g != nil {
		t.Errorf("an unknown scan has an entry: %+v", g)
	}

	// The parameter must be the id of a scan.
	for _, bad := range []string{"0", "-3", "abc", "", "1.5"} {
		if r := admin.do("GET", "/api/v1/audit?scan_id="+bad, nil); r.Status != 400 {
			t.Errorf("scan_id=%q: %d %s", bad, r.Status, r.Raw)
		}
	}
	// Only an administrator reads the audit log, this way as well.
	v, _ := e.newUser(admin, "viewer3", "viewer")
	expect(t, v.do("GET", fmt.Sprintf("/api/v1/audit?scan_id=%d", int64(good)), nil), 403, "")
}
