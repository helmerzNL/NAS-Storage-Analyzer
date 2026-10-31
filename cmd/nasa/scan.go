package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/helmerznl/nas-storage-analyzer/internal/categorize"
	"github.com/helmerznl/nas-storage-analyzer/internal/scanner"
	"github.com/helmerznl/nas-storage-analyzer/internal/scanrun"
	"github.com/helmerznl/nas-storage-analyzer/internal/store"
)

type stringList []string

func (l *stringList) String() string     { return strings.Join(*l, ",") }
func (l *stringList) Set(v string) error { *l = append(*l, v); return nil }

func newLogger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(os.Stdout, nil))
}

func dsnFromEnv() string {
	if v := os.Getenv("NASA_DB_DSN"); v != "" {
		return v
	}
	return ""
}

func runMigrate(args []string) error {
	fs := flag.NewFlagSet("migrate", flag.ExitOnError)
	dsn := fs.String("db", dsnFromEnv(), "PostgreSQL connection string (env NASA_DB_DSN)")
	fs.Parse(args)
	if *dsn == "" {
		return errors.New("no database: pass -db or set NASA_DB_DSN")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	st, err := store.Open(ctx, *dsn)
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	defer st.Close()
	if err := st.Migrate(ctx); err != nil {
		return err
	}
	newLogger().Info("migrations applied")
	return nil
}

func runScan(args []string) error {
	fs := flag.NewFlagSet("scan", flag.ExitOnError)
	var excludes stringList
	path := fs.String("path", "", "directory to scan (required); mounted read-only in production")
	name := fs.String("name", "", "share name in the database (default: base name of -path)")
	dsn := fs.String("db", dsnFromEnv(), "PostgreSQL connection string (env NASA_DB_DSN)")
	workers := fs.Int("workers", 16, "parallel directory workers")
	full := fs.Bool("full", false, "full scan: stat every file instead of reusing unchanged directories")
	maxOps := fs.Int("max-ops", 0, "limit metadata operations per second (0 = unlimited)")
	dirTimeout := fs.Duration("dir-timeout", 10*time.Minute, "give up on a directory when one filesystem call blocks this long (0 = never)")
	dryRun := fs.Bool("dry-run", false, "walk and report totals without touching the database")
	every := fs.Duration("progress-every", 2*time.Second, "interval of progress log lines (0 = off)")
	fs.Var(&excludes, "exclude", "exclude pattern, repeatable (e.g. -exclude .snapshot -exclude '@eaDir')")
	fs.Parse(args)

	if *path == "" {
		return errors.New("-path is required")
	}
	abs, err := filepath.Abs(*path)
	if err != nil {
		return err
	}
	if *workers < 1 || *workers > 256 {
		return errors.New("-workers must be between 1 and 256")
	}
	log := newLogger()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	progress := func(p scanrun.Progress) {
		attrs := []any{"dirs", p.Dirs, "reused_dirs", p.ReusedDirs, "files", p.Files, "errors", p.Errors,
			"scanned_gib", round(float64(p.Bytes) / (1 << 30)), "files_per_sec", int(p.FilesPerSec), "current_dir", p.CurrentDir}
		if p.Percent != nil {
			attrs = append(attrs, "percent", round(*p.Percent))
		}
		if p.ETA != nil {
			attrs = append(attrs, "eta_seconds", int(p.ETA.Seconds()))
		}
		log.Info("scan progress", attrs...)
	}
	if *every <= 0 {
		progress = nil
	}

	if *dryRun {
		res, err := scanrun.DryRun(ctx, abs, excludes, *workers, float64(*maxOps), *dirTimeout, progress)
		if err != nil {
			return cancelled(err)
		}
		printDryRun(abs, res)
		return nil
	}

	if *dsn == "" {
		return errors.New("no database: pass -db or set NASA_DB_DSN (or use -dry-run)")
	}
	st, err := store.Open(ctx, *dsn)
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	defer st.Close()
	if err := st.Migrate(ctx); err != nil {
		return err
	}
	shareName := *name
	if shareName == "" {
		shareName = filepath.Base(abs)
	}
	share, err := st.EnsureShare(ctx, shareName, abs, excludes, *workers, *maxOps)
	if err != nil {
		return err
	}
	log.Info("scan starting", "share", share.Name, "path", abs, "workers", *workers, "full", *full, "max_ops_per_sec", *maxOps)
	res, err := scanrun.Run(ctx, st, share, scanrun.Options{
		ForceFull: *full, Trigger: "cli", DirTimeout: *dirTimeout, OnProgress: progress, ProgressEvery: *every,
	})
	if err != nil {
		return cancelled(err)
	}
	log.Info("scan finished", "scan_id", res.ScanID, "mode", res.Mode, "dirs", res.Totals.Dirs, "files", res.Totals.Files,
		"bytes", res.Totals.Bytes, "alloc_bytes", res.Totals.Alloc, "reused_dirs", res.Progress.ReusedDirs,
		"errors", res.Progress.Errors, "atime", res.Atime.Verdict, "seconds", round(res.Duration.Seconds()))
	printScanSummary(share.Name, res)
	return nil
}

// cancelled maps an interrupted scan to the conventional exit status 130.
func cancelled(err error) error {
	if errors.Is(err, context.Canceled) {
		fmt.Fprintln(os.Stderr, "scan cancelled; previously stored data is unchanged")
		return exitCode(130)
	}
	return err
}

func round(f float64) float64 { return float64(int(f*100+0.5)) / 100 }

func printScanSummary(name string, r *scanrun.Result) {
	w := os.Stderr
	fmt.Fprintf(w, "\nScan %d of %q finished in %s (%s scan)\n", r.ScanID, name, r.Duration.Round(time.Millisecond), r.Mode)
	fmt.Fprintf(w, "  directories : %d (%d reused from the previous scan)\n", r.Totals.Dirs, r.Progress.ReusedDirs)
	fmt.Fprintf(w, "  files       : %d\n", r.Totals.Files)
	fmt.Fprintf(w, "  logical size: %s\n", human(r.Totals.Bytes))
	fmt.Fprintf(w, "  allocated   : %s\n", human(r.Totals.Alloc))
	fmt.Fprintf(w, "  errors      : %d (see scan_errors)\n", r.Progress.Errors)
	printAtime(w, r.Atime)
}

func printDryRun(path string, r *scanrun.DryRunResult) {
	w := os.Stderr
	fmt.Fprintf(w, "\nDry run of %s finished in %s (nothing stored)\n", path, r.Duration.Round(time.Millisecond))
	fmt.Fprintf(w, "  directories : %d\n  files       : %d (hardlinked duplicates skipped: %d, symlinks skipped: %d)\n", r.Dirs, r.Files, r.HardlinkDupes, r.Symlinks)
	fmt.Fprintf(w, "  logical size: %s\n  allocated   : %s\n  errors      : %d\n", human(r.Bytes), human(r.Alloc), r.Errors)
	names := map[int16]string{}
	for _, c := range categorize.Categories {
		names[c.ID] = c.Key
	}
	var ids []int
	for id := range r.ByCategory {
		ids = append(ids, int(id))
	}
	sort.Slice(ids, func(i, j int) bool { return r.ByCategory[int16(ids[i])].Bytes > r.ByCategory[int16(ids[j])].Bytes })
	for _, id := range ids {
		c := r.ByCategory[int16(id)]
		fmt.Fprintf(w, "    %-12s %10d files  %s\n", names[int16(id)], c.Files, human(c.Bytes))
	}
	printAtime(w, r.Atime)
}

func printAtime(w *os.File, q scanner.AtimeQuality) {
	fmt.Fprintf(w, "  atime       : %s", q.Verdict)
	if len(q.Reasons) > 0 {
		fmt.Fprintf(w, " (%s)", strings.Join(q.Reasons, ", "))
	}
	fmt.Fprintln(w)
	if q.Verdict != scanner.Reliable {
		fmt.Fprintln(w, "                \"last used\" based on atime may be misleading on this share; consider basing it on mtime")
	}
}

func human(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.2f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}
