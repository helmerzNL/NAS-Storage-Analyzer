package scanrun

import (
	"context"
	"errors"
	"os"
	"reflect"
	"strconv"
	"syscall"
	"testing"

	"github.com/helmerznl/nas-storage-analyzer/internal/query"
	"github.com/helmerznl/nas-storage-analyzer/internal/scanner"
	"github.com/helmerznl/nas-storage-analyzer/internal/store"
	ts "github.com/helmerznl/nas-storage-analyzer/internal/testsupport"
)

func run(t *testing.T, st *store.Store, sh *store.Share, o Options) *Result {
	t.Helper()
	res, err := Run(context.Background(), st, sh, o)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestScanMatchesFilesystem(t *testing.T) {
	// Tiny batches force the multi-batch COPY path.
	defer store.SetBatchSizes(7, 3)()
	st := ts.NewStore(t)
	root := t.TempDir()
	ts.BuildTree(t, root)
	sh := newShare(t, st, "media", root, ts.TestExcludes)

	res := run(t, st, sh, Options{})
	if res.Mode != "full" {
		t.Errorf("first scan mode = %s, want full", res.Mode)
	}
	want := ts.FSExpect(t, root, ts.TestExcludes)
	if res.Totals.Files != want.Files || res.Totals.Bytes != want.Bytes || res.Totals.Alloc != want.Alloc || res.Totals.Dirs != want.Dirs {
		t.Errorf("totals = %+v, want dirs=%d files=%d bytes=%d alloc=%d", res.Totals, want.Dirs, want.Files, want.Bytes, want.Alloc)
	}
	if got := extTotals(t, st, sh.ID); !reflect.DeepEqual(got, want.Ext) {
		t.Errorf("extension totals = %v\nwant %v", got, want.Ext)
	}
	checkConsistency(t, st, sh.ID)

	tot := dirTotalsByPath(t, st, sh.ID)
	if _, ok := tot["movies/@eaDir"]; ok {
		t.Error("excluded directory stored")
	}
	if tot["empty"] != (dirTotals{}) {
		t.Errorf("empty dir totals = %+v", tot["empty"])
	}
	// movies: b.mp4 (5000) + c.MKV (70000); link-to-dir must not add anything.
	if got := tot["movies"].Size; got != 75000 {
		t.Errorf("movies size = %d, want 75000", got)
	}

	// Hardlinks: both rows exist, exactly one is summed.
	var rows, counted int
	err := st.Pool.QueryRow(context.Background(),
		`SELECT count(*), count(*) FILTER (WHERE counted) FROM files WHERE share_id = $1 AND name IN ('h.jpg', 'h-copy.jpg')`,
		sh.ID).Scan(&rows, &counted)
	if err != nil || rows != 2 || counted != 1 {
		t.Errorf("cross-directory hardlink rows=%d counted=%d err=%v, want 2 and 1", rows, counted, err)
	}
	// Same-directory hardlink: a.mkv + a-link.mkv count once in root's direct sum.
	var rootDirect int64
	st.Pool.QueryRow(context.Background(),
		`SELECT files_direct FROM dirs WHERE share_id = $1 AND parent_id IS NULL`, sh.ID).Scan(&rootDirect)
	if rootDirect != 1 {
		t.Errorf("root direct files = %d, want 1 (a.mkv and its hardlink count once)", rootDirect)
	}

	// Categories: "noext" and the empty-extension files are Other (8); .CR2 is Images.
	var video, images int64
	st.Pool.QueryRow(context.Background(), `SELECT coalesce(sum(size_total),0) FROM dir_category_agg
		WHERE share_id = $1 AND category_id = 1 AND dir_id = (SELECT id FROM dirs WHERE share_id = $1 AND parent_id IS NULL)`, sh.ID).Scan(&video)
	st.Pool.QueryRow(context.Background(), `SELECT coalesce(sum(size_total),0) FROM dir_category_agg
		WHERE share_id = $1 AND category_id = 3 AND dir_id = (SELECT id FROM dirs WHERE share_id = $1 AND parent_id IS NULL)`, sh.ID).Scan(&images)
	if video != 1000+5000+70000 || images != 10000+2000 {
		t.Errorf("video=%d images=%d, want 76000 and 12000", video, images)
	}

	// The scan row.
	var status string
	var aq []byte
	st.Pool.QueryRow(context.Background(), `SELECT status, atime_quality FROM scans WHERE id = $1`, res.ScanID).Scan(&status, &aq)
	if status != "succeeded" || len(aq) == 0 {
		t.Errorf("scan row status=%s atime_quality=%s", status, aq)
	}
}

func TestIncrementalEqualsFreshFullScan(t *testing.T) {
	defer store.SetBatchSizes(5, 2)()
	st := ts.NewStore(t)
	root := t.TempDir()
	ts.BuildTree(t, root)
	a := newShare(t, st, "incremental", root, ts.TestExcludes)
	run(t, st, a, Options{})
	ts.Settle()

	// Structural changes: each one alters the mtime/entry count of a directory.
	ts.Write(t, root+"/movies/new.mkv", 123456)
	if err := os.RemoveAll(root + "/music/sub"); err != nil {
		t.Fatal(err)
	}
	ts.Write(t, root+"/new/x/y.zip", 777)
	if err := os.Rename(root+"/photos", root+"/pictures"); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(root + "/docs/f.pdf"); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(root + "/empty"); err != nil {
		t.Fatal(err)
	}
	ts.Settle()

	inc := run(t, st, a, Options{})
	if inc.Mode != "incremental" {
		t.Fatalf("second scan mode = %s", inc.Mode)
	}
	if inc.Progress.ReusedDirs == 0 {
		t.Error("incremental scan reused no directories")
	}
	checkConsistency(t, st, a.ID)

	b := newShare(t, st, "fresh", root, ts.TestExcludes)
	full := run(t, st, b, Options{})
	if inc.Totals != full.Totals {
		t.Errorf("incremental totals %+v != fresh full totals %+v", inc.Totals, full.Totals)
	}
	if got, want := dirTotalsByPath(t, st, a.ID), dirTotalsByPath(t, st, b.ID); !reflect.DeepEqual(got, want) {
		t.Errorf("per-directory totals differ\nincremental: %v\nfull:        %v", got, want)
	}
	if got, want := extTotals(t, st, a.ID), extTotals(t, st, b.ID); !reflect.DeepEqual(got, want) {
		t.Errorf("extension totals differ\nincremental: %v\nfull:        %v", got, want)
	}
	// Directory ids of untouched directories stay stable across scans.
	var moved int
	st.Pool.QueryRow(context.Background(), `SELECT count(*) FROM dirs WHERE share_id = $1 AND last_seen_scan <> $2`, a.ID, inc.ScanID).Scan(&moved)
	if moved != 0 {
		t.Errorf("%d directories were not seen by the latest scan but still exist", moved)
	}
}

func TestInPlaceChangeNeedsFullScan(t *testing.T) {
	st := ts.NewStore(t)
	root := t.TempDir()
	ts.Write(t, root+"/d/log.txt", 100)
	sh := newShare(t, st, "inplace", root, nil)
	run(t, st, sh, Options{})
	ts.Settle()
	if err := os.WriteFile(root+"/d/log.txt", make([]byte, 9000), 0o644); err != nil {
		t.Fatal(err)
	}

	inc := run(t, st, sh, Options{})
	if inc.Mode != "incremental" || inc.Totals.Bytes != 100 {
		t.Fatalf("known limitation changed: mode=%s bytes=%d (want incremental, 100)", inc.Mode, inc.Totals.Bytes)
	}
	full := run(t, st, sh, Options{ForceFull: true})
	if full.Mode != "full" || full.Totals.Bytes != 9000 {
		t.Errorf("full scan: mode=%s bytes=%d, want full and 9000", full.Mode, full.Totals.Bytes)
	}
}

func TestChangedExcludesForceFullScan(t *testing.T) {
	st := ts.NewStore(t)
	root := t.TempDir()
	ts.Write(t, root+"/keep/a.txt", 10)
	ts.Write(t, root+"/skipme/b.txt", 20)
	sh := newShare(t, st, "excl", root, nil)
	if res := run(t, st, sh, Options{}); res.Totals.Bytes != 30 {
		t.Fatalf("bytes = %d", res.Totals.Bytes)
	}
	sh = newShare(t, st, "excl", root, []string{"skipme"})
	res := run(t, st, sh, Options{})
	if res.Mode != "full" {
		t.Errorf("mode = %s; changing excludes must force a full scan", res.Mode)
	}
	if res.Totals.Bytes != 10 {
		t.Errorf("bytes = %d, want 10 after excluding skipme", res.Totals.Bytes)
	}
}

func TestUnreadableDirectoryKeepsPreviousData(t *testing.T) {
	st := ts.NewStore(t)
	root := t.TempDir()
	ts.BuildTree(t, root)
	sh := newShare(t, st, "denied", root, ts.TestExcludes)
	first := run(t, st, sh, Options{})
	before := dirTotalsByPath(t, st, sh.ID)

	real, err := scanner.NewOSFS(root)
	if err != nil {
		t.Fatal(err)
	}
	defer real.Close()
	gfs := &guardFS{FS: real, deny: map[string]error{"music": syscall.EACCES}}
	ts.Settle()
	res := run(t, st, sh, Options{FS: gfs, ForceFull: true})

	if res.Totals != first.Totals {
		t.Errorf("totals changed although the only change was an unreadable directory: %+v -> %+v", first.Totals, res.Totals)
	}
	if after := dirTotalsByPath(t, st, sh.ID); !reflect.DeepEqual(before, after) {
		t.Errorf("per-directory totals changed:\nbefore %v\nafter  %v", before, after)
	}
	if _, ok := dirTotalsByPath(t, st, sh.ID)["music/sub"]; !ok {
		t.Error("subtree of the unreadable directory was dropped")
	}
	var n int
	var kind string
	st.Pool.QueryRow(context.Background(), `SELECT count(*), coalesce(min(kind), '') FROM scan_errors WHERE scan_id = $1`, res.ScanID).Scan(&n, &kind)
	if n != 1 || kind != "permission" {
		t.Errorf("scan_errors: n=%d kind=%q, want 1 permission", n, kind)
	}
	checkConsistency(t, st, sh.ID)
}

func TestCancelledScanLeavesStoredDataIntact(t *testing.T) {
	st := ts.NewStore(t)
	root := t.TempDir()
	ts.BuildTree(t, root)
	sh := newShare(t, st, "cancel", root, ts.TestExcludes)
	first := run(t, st, sh, Options{})
	before := dirTotalsByPath(t, st, sh.ID)
	ts.Write(t, root+"/movies/extra.mkv", 5000) // would change totals if the scan were published
	ts.Settle()

	real, _ := scanner.NewOSFS(root)
	defer real.Close()
	ctx, cancel := context.WithCancel(context.Background())
	opens := 0
	sh.Workers = 1
	gfs := &guardFS{FS: real, onOpen: func(string) {
		if opens++; opens == 4 {
			cancel()
		}
	}}
	_, err := Run(ctx, st, sh, Options{FS: gfs, ForceFull: true})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if after := dirTotalsByPath(t, st, sh.ID); !reflect.DeepEqual(before, after) {
		t.Error("cancelled scan changed the stored data")
	}
	var status string
	st.Pool.QueryRow(context.Background(), `SELECT status FROM scans WHERE share_id = $1 ORDER BY id DESC LIMIT 1`, sh.ID).Scan(&status)
	if status != "cancelled" {
		t.Errorf("status = %s, want cancelled", status)
	}
	var lastScan int64
	st.Pool.QueryRow(context.Background(), `SELECT last_scan_id FROM shares WHERE id = $1`, sh.ID).Scan(&lastScan)
	if lastScan != first.ScanID {
		t.Errorf("last_scan_id = %d, want %d", lastScan, first.ScanID)
	}

	// The share is not stuck: the next scan publishes the new file.
	sh.Workers = 8
	res := run(t, st, sh, Options{ForceFull: true})
	if res.Totals.Bytes != first.Totals.Bytes+5000 {
		t.Errorf("bytes = %d, want %d", res.Totals.Bytes, first.Totals.Bytes+5000)
	}
}

func TestOnlyOneRunningScanPerShare(t *testing.T) {
	st := ts.NewStore(t)
	root := t.TempDir()
	ts.Write(t, root+"/a", 1)
	sh := newShare(t, st, "single", root, nil)
	ctx := context.Background()
	id, err := st.BeginScan(ctx, sh.ID, "full", "manual", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.BeginScan(ctx, sh.ID, "full", "manual", ""); !errors.Is(err, store.ErrScanRunning) {
		t.Errorf("second BeginScan err = %v, want ErrScanRunning", err)
	}
	if _, err := Run(ctx, st, sh, Options{}); !errors.Is(err, store.ErrScanRunning) {
		t.Errorf("Run err = %v, want ErrScanRunning", err)
	}
	_ = st.FailScan(ctx, id, false, errors.New("test"), 0)
	if _, err := st.BeginScan(ctx, sh.ID, "full", "manual", ""); err != nil {
		t.Errorf("BeginScan after finishing: %v", err)
	}
}

func TestSharesAreIsolated(t *testing.T) {
	st := ts.NewStore(t)
	r1, r2 := t.TempDir(), t.TempDir()
	ts.Write(t, r1+"/a.mkv", 100)
	ts.Write(t, r2+"/b.mp3", 50)
	s1, s2 := newShare(t, st, "one", r1, nil), newShare(t, st, "two", r2, nil)
	run(t, st, s1, Options{})
	run(t, st, s2, Options{})
	if got := extTotals(t, st, s1.ID); len(got) != 1 || got["mkv"][0] != 100 {
		t.Errorf("share one ext totals = %v", got)
	}
	if got := extTotals(t, st, s2.ID); len(got) != 1 || got["mp3"][0] != 50 {
		t.Errorf("share two ext totals = %v", got)
	}
}

func TestMigrateIsIdempotentAndKeepsEditedMapping(t *testing.T) {
	st := ts.NewStore(t)
	ctx := context.Background()
	// A user moves "iso" from Disk images to Archives through the UI.
	if _, err := st.Pool.Exec(ctx, `UPDATE extension_map SET category_id = 5 WHERE ext = 'iso'`); err != nil {
		t.Fatal(err)
	}
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	var cat int16
	st.Pool.QueryRow(ctx, `SELECT category_id FROM extension_map WHERE ext = 'iso'`).Scan(&cat)
	if cat != 5 {
		t.Errorf("seed overwrote an edited mapping: iso -> %d", cat)
	}
	m, err := st.LoadMapper(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if m.Category("iso") != 5 || m.Category("mkv") != 1 {
		t.Errorf("mapper iso=%d mkv=%d", m.Category("iso"), m.Category("mkv"))
	}
}

func TestDryRunMatchesFilesystem(t *testing.T) {
	root := t.TempDir()
	ts.BuildTree(t, root)
	res, err := DryRun(context.Background(), root, ts.TestExcludes, 4, 0, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := ts.FSExpect(t, root, ts.TestExcludes)
	if res.Dirs != want.Dirs || res.Files != want.Files || res.Bytes != want.Bytes || res.Alloc != want.Alloc {
		t.Errorf("dry run = dirs %d files %d bytes %d alloc %d, want %d/%d/%d/%d",
			res.Dirs, res.Files, res.Bytes, res.Alloc, want.Dirs, want.Files, want.Bytes, want.Alloc)
	}
	if res.Symlinks != 3 {
		t.Errorf("symlinks = %d, want 3", res.Symlinks)
	}
	if res.HardlinkDupes != 2 {
		t.Errorf("hardlink duplicates = %d, want 2", res.HardlinkDupes)
	}
}

func TestSmallIncrementalSampleKeepsPreviousAtimeVerdict(t *testing.T) {
	st := ts.NewStore(t)
	root := t.TempDir()
	for i := 0; i < 150; i++ {
		ts.Write(t, root+"/bulk/f"+itoa(i)+".txt", 10) // fresh files: atime == mtime
	}
	ts.Write(t, root+"/other/x.txt", 1)
	sh := newShare(t, st, "atime", root, nil)
	first := run(t, st, sh, Options{})
	if first.Atime.Verdict != scanner.Unreliable || first.Atime.CarriedOver {
		t.Fatalf("first verdict = %+v, want unreliable and not carried over", first.Atime)
	}
	ts.Settle()
	ts.Write(t, root+"/other/y.txt", 1) // only /other changes; /bulk is reused
	second := run(t, st, sh, Options{})
	if second.Mode != "incremental" || second.Progress.ReusedDirs == 0 {
		t.Fatalf("mode=%s reused=%d", second.Mode, second.Progress.ReusedDirs)
	}
	if second.Atime.Verdict != scanner.Unreliable || !second.Atime.CarriedOver {
		t.Errorf("second verdict = %+v, want the first scan's unreliable verdict, carried over", second.Atime)
	}
	var stored string
	st.Pool.QueryRow(context.Background(), `SELECT atime_quality->>'verdict' FROM scans WHERE id = $1`, second.ScanID).Scan(&stored)
	if stored != scanner.Unreliable {
		t.Errorf("stored verdict = %q", stored)
	}
}

func itoa(i int) string { return strconv.Itoa(i) }

func TestRecategorizeAppliesNewMappingWithoutRescan(t *testing.T) {
	st := ts.NewStore(t)
	root := t.TempDir()
	ts.BuildTree(t, root)
	sh := newShare(t, st, "recat", root, ts.TestExcludes)
	run(t, st, sh, Options{})
	ctx := context.Background()

	catSize := func(cat int) int64 {
		var n int64
		st.Pool.QueryRow(ctx, `SELECT coalesce(sum(size_total), 0) FROM dir_category_agg
			WHERE share_id = $1 AND category_id = $2 AND dir_id = (SELECT id FROM dirs WHERE share_id = $1 AND parent_id IS NULL)`,
			sh.ID, cat).Scan(&n)
		return n
	}
	videoBefore, docsBefore := catSize(1), catSize(4)
	if videoBefore == 0 {
		t.Fatal("expected video data in the tree")
	}
	// The user decides that "mkv" and "mp4" are documents, and a new extension "noext"-less file stays Other.
	if _, err := st.Pool.Exec(ctx, `UPDATE extension_map SET category_id = 4 WHERE ext IN ('mkv', 'mp4')`); err != nil {
		t.Fatal(err)
	}
	changed, err := st.Recategorize(ctx, sh.ID)
	if err != nil {
		t.Fatal(err)
	}
	// a.mkv + its hardlink, b.mp4, c.MKV
	if changed != 4 {
		t.Errorf("changed = %d, want 4 rows (a.mkv, a-link.mkv, b.mp4, c.MKV)", changed)
	}
	if got := catSize(1); got != 0 {
		t.Errorf("video size after recategorize = %d, want 0 (was %d)", got, videoBefore)
	}
	if got := catSize(4); got != docsBefore+videoBefore {
		t.Errorf("document size = %d, want %d", got, docsBefore+videoBefore)
	}
	checkConsistency(t, st, sh.ID)

	// Idempotent.
	if changed, err = st.Recategorize(ctx, sh.ID); err != nil || changed != 0 {
		t.Errorf("second recategorize changed=%d err=%v", changed, err)
	}
	// Totals did not move.
	want := ts.FSExpect(t, root, ts.TestExcludes)
	if tot := dirTotalsByPath(t, st, sh.ID)[""]; tot.Size != want.Bytes || tot.Files != want.Files {
		t.Errorf("root totals changed by recategorize: %+v", tot)
	}
}

func TestRecomputeAllReproducesScanAggregates(t *testing.T) {
	st := ts.NewStore(t)
	root := t.TempDir()
	ts.BuildTree(t, root)
	sh := newShare(t, st, "recompute", root, ts.TestExcludes)
	run(t, st, sh, Options{})
	before := dirTotalsByPath(t, st, sh.ID)
	beforeExt := extTotals(t, st, sh.ID)
	ctx := context.Background()

	// Corrupt derived data on purpose; RecomputeAll must repair it from files.
	if _, err := st.Pool.Exec(ctx, `UPDATE dirs SET size_total = 1, files_direct = 99, size_direct = 7 WHERE share_id = $1`, sh.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool.Exec(ctx, `DELETE FROM dir_category_agg WHERE share_id = $1`, sh.ID); err != nil {
		t.Fatal(err)
	}
	if err := st.RecomputeAll(ctx, sh.ID); err != nil {
		t.Fatal(err)
	}
	if after := dirTotalsByPath(t, st, sh.ID); !reflect.DeepEqual(before, after) {
		t.Errorf("recompute differs from scan result:\nbefore %v\nafter  %v", before, after)
	}
	if got := extTotals(t, st, sh.ID); !reflect.DeepEqual(beforeExt, got) {
		t.Errorf("extension totals differ: %v vs %v", got, beforeExt)
	}
	checkConsistency(t, st, sh.ID)
	if err := st.RecomputeAll(ctx, sh.ID); err != nil { // idempotent
		t.Fatal(err)
	}
	if after := dirTotalsByPath(t, st, sh.ID); !reflect.DeepEqual(before, after) {
		t.Error("second recompute changed totals")
	}
}

func TestScanStoresFilesystemCapacity(t *testing.T) {
	st := ts.NewStore(t)
	root := t.TempDir()
	ts.Write(t, root+"/a", 1)
	sh := newShare(t, st, "capacity", root, nil)
	res := run(t, st, sh, Options{})
	var total, free *int64
	st.Pool.QueryRow(context.Background(), `SELECT fs_total_bytes, fs_free_bytes FROM scans WHERE id = $1`, res.ScanID).Scan(&total, &free)
	if total == nil || free == nil || *total <= 0 || *free < 0 || *free > *total {
		t.Errorf("capacity total=%v free=%v", total, free)
	}
}

func TestHistorySnapshotsGrowAndAreRetained(t *testing.T) {
	st := ts.NewStore(t)
	root := t.TempDir()
	ts.Write(t, root+"/video/a.mkv", 1000)
	ts.Write(t, root+"/docs/b.pdf", 100)
	sh := newShare(t, st, "hist", root, nil)
	ctx := context.Background()
	var scans []int64
	for i := 1; i <= 4; i++ {
		ts.Settle()
		res := run(t, st, sh, Options{})
		scans = append(scans, res.ScanID)
		ts.Write(t, root+"/video/more"+string(rune('0'+i))+".mkv", 1000*i)
	}
	var n int
	st.Pool.QueryRow(ctx, `SELECT count(*) FROM snapshots WHERE share_id = $1`, sh.ID).Scan(&n)
	if n != 4 {
		t.Fatalf("snapshots = %d, want one per scan", n)
	}
	q := query.New(st.Pool)
	hist, err := q.History(ctx, sh.ID, nil)
	if err != nil || len(hist) != 4 {
		t.Fatalf("history: %d %v", len(hist), err)
	}
	// Sizes after the scans: 1100, 2100, 4100, 7100.
	for i, want := range []int64{1100, 2100, 4100, 7100} {
		if hist[i].Size != want {
			t.Errorf("point %d size = %d, want %d", i, hist[i].Size, want)
		}
	}
	if len(hist[3].Categories) != 2 || hist[3].Categories[0].ID != 1 || hist[3].Categories[0].Size != 7000 {
		t.Errorf("categories of the last point: %+v", hist[3].Categories)
	}
	if hist2, _ := q.History(ctx, sh.ID, &hist[2].At); len(hist2) != 2 {
		t.Errorf("history since the third scan has %d points", len(hist2))
	}
	var videoID int64
	st.Pool.QueryRow(ctx, `SELECT id FROM dirs WHERE share_id = $1 AND name = 'video'`, sh.ID).Scan(&videoID)
	dh, err := q.DirHistory(ctx, sh.ID, videoID)
	if err != nil || len(dh) != 4 || dh[0].Size != 1000 || dh[3].Size != 7000 {
		t.Errorf("folder history: %+v %v", dh, err)
	}
	if rh, _ := q.DirHistory(ctx, sh.ID, 0); len(rh) != 4 || rh[3].Size != 7100 {
		t.Errorf("root history: %+v", rh)
	}

	// Retention keeps the newest two scans.
	st.Pool.Exec(ctx, `UPDATE shares SET retention_scans = 2 WHERE id = $1`, sh.ID)
	removed, err := st.PruneHistory(ctx, sh.ID)
	if err != nil || removed != 2 {
		t.Fatalf("prune removed %d: %v", removed, err)
	}
	if hist, _ = q.History(ctx, sh.ID, nil); len(hist) != 2 || hist[0].ScanID != scans[2] {
		t.Errorf("after pruning: %+v", hist)
	}
	var orphans int
	st.Pool.QueryRow(ctx, `SELECT count(*) FROM snapshot_category WHERE scan_id = $1`, scans[0]).Scan(&orphans)
	if orphans != 0 {
		t.Error("snapshot detail rows survived their snapshot")
	}
	// The published scan is never pruned, however strict the retention.
	st.Pool.Exec(ctx, `UPDATE shares SET retention_days = 1, retention_scans = 0 WHERE id = $1`, sh.ID)
	st.Pool.Exec(ctx, `UPDATE scans SET started_at = now() - interval '40 days' WHERE share_id = $1`, sh.ID)
	st.PruneHistory(ctx, sh.ID)
	if hist, _ = q.History(ctx, sh.ID, nil); len(hist) != 1 || hist[0].ScanID != scans[3] {
		t.Errorf("age-based pruning must keep the published scan: %+v", hist)
	}
	if _, err := q.Dir(ctx, sh.ID, 0, query.Filter{}); err != nil {
		t.Errorf("share unusable after pruning: %v", err)
	}
}

func TestEveryNthAutomaticScanIsFull(t *testing.T) {
	st := ts.NewStore(t)
	root := t.TempDir()
	ts.Write(t, root+"/a", 1)
	sh := newShare(t, st, "cadence", root, nil)
	ctx := context.Background()
	st.Pool.Exec(ctx, `UPDATE shares SET full_scan_every = 3 WHERE id = $1`, sh.ID)
	// With full_scan_every = 3 the scans 1, 4, 7 are full: what ShouldForceFull
	// answers before scans 1..6 (the answer before scan 1 is not used).
	want := []bool{false, false, false, true, false, false}
	for i, w := range want {
		force, err := st.ShouldForceFull(ctx, sh.ID)
		if err != nil {
			t.Fatal(err)
		}
		// With no scan yet the first scan is full anyway; ShouldForceFull only counts scans since the last full one.
		if i > 0 && force != w {
			t.Errorf("before scan %d: force=%v, want %v", i+1, force, w)
		}
		res := run(t, st, sh, Options{ForceFull: force})
		if i == 0 && res.Mode != "full" {
			t.Errorf("first scan mode = %s", res.Mode)
		}
		if i > 0 && force && res.Mode != "full" {
			t.Errorf("scan %d should be full", i+1)
		}
		if i > 0 && !force && res.Mode != "incremental" {
			t.Errorf("scan %d should be incremental", i+1)
		}
	}
	st.Pool.Exec(ctx, `UPDATE shares SET full_scan_every = 0 WHERE id = $1`, sh.ID)
	if force, _ := st.ShouldForceFull(ctx, sh.ID); force {
		t.Error("full_scan_every=0 must never force")
	}
}
