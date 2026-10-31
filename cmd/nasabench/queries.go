package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/helmerznl/nas-storage-analyzer/internal/api"
	"github.com/helmerznl/nas-storage-analyzer/internal/store"
)

type scenario struct {
	group, name, path string
	// worst marks free filter combinations over the whole share, for which the
	// agreed budget is 2 s instead of 500 ms.
	worst bool
}

type result struct {
	scenario
	status         int
	bytes          int64
	tier           string
	first          time.Duration
	p50, p95, max_ time.Duration
	err            string
}

func runQueries(args []string) error {
	fs := flag.NewFlagSet("queries", flag.ExitOnError)
	dsn := fs.String("db", os.Getenv("NASA_DB_DSN"), "PostgreSQL connection string")
	name := fs.String("share", "bench", "share to measure")
	runs := fs.Int("runs", 30, "warm runs per scenario")
	out := fs.String("out", "", "also write the markdown report to this file")
	only := fs.String("only", "", "run only scenarios whose group or name contains this text")
	csvRows := fs.Int("csv-rows", 100_000, "row cap for the CSV scenarios")
	fs.Parse(args)
	if *dsn == "" {
		return errors.New("pass -db or set NASA_DB_DSN")
	}
	ctx := context.Background()

	pool, err := store.OpenReadOnly(ctx, *dsn, 20, 60*time.Second)
	if err != nil {
		return err
	}
	defer pool.Close()
	var share, root, deep, mid int64
	var files, dirs int64
	if err := pool.QueryRow(ctx, `SELECT s.id, d.id, d.files_total, 1 + d.dirs_total FROM shares s
		JOIN dirs d ON d.share_id = s.id AND d.parent_id IS NULL WHERE s.name = $1`, *name).Scan(&share, &root, &files, &dirs); err != nil {
		return fmt.Errorf("share %q: %w", *name, err)
	}
	pick := func(depth int) (id int64) {
		pool.QueryRow(ctx, `SELECT id FROM dirs WHERE share_id = $1 AND depth = $2 ORDER BY size_total DESC LIMIT 1`, share, depth).Scan(&id)
		return
	}
	deep, mid = pick(4), pick(2)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	srv := &http.Server{Handler: api.New(pool, slog.New(slog.NewTextHandler(io.Discard, nil)), api.Config{CSVMaxRows: *csvRows, QueryTimeout: 60 * time.Second})}
	go srv.Serve(ln)
	defer srv.Close()
	base := fmt.Sprintf("http://%s/api/v1/shares/%d", ln.Addr(), share)

	GB := int64(1 << 30)
	sc := []scenario{
		{"dashboard", "share list with last scan", "/../../shares", false},
		{"dashboard", "category breakdown of the share", "/categories", false},
		{"dashboard", "top 20 extensions", "/extensions?limit=20", false},

		{"folder browser", "root children, size desc (no filter)", fmt.Sprintf("/dirs/%d/children?limit=100", root), false},
		{"folder browser", "root children by name", fmt.Sprintf("/dirs/%d/children?sort=name&limit=100", root), false},
		{"folder browser", "root children by file count", fmt.Sprintf("/dirs/%d/children?sort=files&limit=100", root), false},
		{"folder browser", "root children by last used", fmt.Sprintf("/dirs/%d/children?sort=used&limit=100", root), false},
		{"folder browser", "mid-depth folder children", fmt.Sprintf("/dirs/%d/children?limit=100", mid), false},
		{"folder browser", "deep folder children", fmt.Sprintf("/dirs/%d/children?limit=100", deep), false},
		{"folder browser", "folder detail with breadcrumb", fmt.Sprintf("/dirs/%d", deep), false},
		{"folder browser", "treemap root, depth 2", fmt.Sprintf("/dirs/%d/treemap?depth=2", root), false},
		{"folder browser", "treemap root, depth 3, category video", fmt.Sprintf("/dirs/%d/treemap?depth=3&category=video", root), false},

		{"filter: category", "root children, category video", fmt.Sprintf("/dirs/%d/children?category=video", root), false},
		{"filter: category", "root children, video+audio+images", fmt.Sprintf("/dirs/%d/children?category=video,audio,images", root), false},
		{"filter: category", "mid folder children, category documents", fmt.Sprintf("/dirs/%d/children?category=documents", mid), false},

		{"filter: free combination", "root children, ext=mkv", fmt.Sprintf("/dirs/%d/children?ext=mkv", root), true},
		{"filter: free combination", "root children, ext=mkv, min 1 GiB", fmt.Sprintf("/dirs/%d/children?ext=mkv&min_size=%d", root, GB), true},
		{"filter: free combination", "root children, ext=mkv, min 1 GiB, unused 1 y", fmt.Sprintf("/dirs/%d/children?ext=mkv&min_size=%d&unused_days=365", root, GB), true},
		{"filter: free combination", "root children, min 100 MiB (millions match)", fmt.Sprintf("/dirs/%d/children?min_size=%d", root, 100<<20), true},
		{"filter: free combination", "root children, unused 2 y (millions match)", fmt.Sprintf("/dirs/%d/children?unused_days=730", root), true},
		{"filter: free combination", "root children, modified before 2022", fmt.Sprintf("/dirs/%d/children?modified_before=2022-01-01", root), true},
		{"filter: free combination", "root children, jpg 1-10 MiB", fmt.Sprintf("/dirs/%d/children?ext=jpg&min_size=%d&max_size=%d", root, 1<<20, 10<<20), true},
		{"filter: free combination", "mid folder children, ext=pdf, unused 1 y", fmt.Sprintf("/dirs/%d/children?ext=pdf&unused_days=365", mid), true},
		{"filter: free combination", "deep folder children, ext=mp3, min 5 MiB", fmt.Sprintf("/dirs/%d/children?ext=mp3&min_size=%d", deep, 5<<20), true},
		{"filter: free combination", "folder detail root, ext=mkv matching totals", fmt.Sprintf("/dirs/%d?ext=mkv&min_size=%d", root, GB), true},

		{"file lists", "top 100 files", "/top/files?n=100", false},
		{"file lists", "top 100 files, category video", "/top/files?n=100&category=video", false},
		{"file lists", "top 100 files, ext=iso", "/top/files?n=100&ext=iso", false},
		{"file lists", "top 100 files in a mid folder", fmt.Sprintf("/top/files?n=100&dir=%d", mid), false},
		{"file lists", "cold files (unused 1 y), top 100", "/cold/files?n=100", false},
		{"file lists", "cold files, ext=mkv, unused 3 y", "/cold/files?n=100&ext=mkv&unused_days=1095", true},
		{"file lists", "files in a folder by name", fmt.Sprintf("/files?dir=%d&sort=name&limit=100", deep), false},
		{"file lists", "files recursive in mid folder, min 1 GiB", fmt.Sprintf("/files?dir=%d&recursive=true&min_size=%d&limit=100", mid, GB), false},
		{"file lists", "files share-wide, jpg + modified before 2021", "/files?ext=jpg&modified_before=2021-01-01&limit=100", true},
		{"file lists", "files share-wide by last modified", "/files?sort=mtime&limit=100", false},

		{"folder lists", "top 50 folders by total", "/top/dirs?n=50", false},
		{"folder lists", "top 50 folders by direct size", "/top/dirs?n=50&by=direct", false},
		{"folder lists", "cold folders (unused 1 y)", "/cold/dirs?n=50", false},
		{"folder lists", "cold folders by direct size, unused 3 y", "/cold/dirs?n=50&by=direct&unused_days=1095", false},

		{"search", "name search, selective (f777)", "/../../search?q=f77777&share=" + fmt.Sprint(share), false},
		{"search", "name search, common (jpg)", "/../../search?q=.jpg&share=" + fmt.Sprint(share), false},
		{"search", "name search, 3 characters (dir1)", "/../../search?q=dir1&share=" + fmt.Sprint(share), false},

		{"export", fmt.Sprintf("CSV top files, %d rows", *csvRows), "/top/files?format=csv", false},
		{"export", fmt.Sprintf("CSV folders, root children (%d dirs)", 6), fmt.Sprintf("/dirs/%d/children?format=csv", root), false},
	}

	client := &http.Client{Timeout: 5 * time.Minute}
	var results []result
	failed := 0
	for _, s := range sc {
		if *only != "" && !strings.Contains(s.group+" "+s.name, *only) {
			continue
		}
		u := base + s.path
		if strings.HasPrefix(s.path, "/../../") {
			u = strings.TrimSuffix(base, fmt.Sprintf("/shares/%d", share)) + strings.TrimPrefix(s.path, "/../..")
		} else if strings.HasPrefix(s.path, "/../") {
			u = strings.TrimSuffix(base, fmt.Sprintf("/%d", share)) + strings.TrimPrefix(s.path, "/..")
		}
		r := result{scenario: s}
		var times []time.Duration
		n := *runs
		if strings.Contains(s.path, "format=csv") {
			n = min(n, 3)
		}
		for i := 0; i <= n; i++ {
			t0 := time.Now()
			resp, err := client.Get(u)
			if err != nil {
				r.err = err.Error()
				break
			}
			nb, _ := io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			d := time.Since(t0)
			r.status, r.bytes = resp.StatusCode, nb
			r.tier = resp.Header.Get("X-Query-Tier")
			if resp.StatusCode != 200 {
				r.err = fmt.Sprintf("HTTP %d", resp.StatusCode)
				break
			}
			if i == 0 {
				r.first = d
			} else {
				times = append(times, d)
			}
		}
		if len(times) > 0 {
			sort.Slice(times, func(i, j int) bool { return times[i] < times[j] })
			r.p50, r.p95, r.max_ = times[len(times)/2], times[min(len(times)-1, len(times)*95/100)], times[len(times)-1]
		}
		if r.err != "" {
			failed++
		}
		results = append(results, r)
		fmt.Printf("%-26s %-48s tier %-2s first %6.0f ms  p50 %6.0f  p95 %6.0f  %s\n", s.group, s.name, r.tier,
			ms(r.first), ms(r.p50), ms(r.p95), r.err)
	}

	report := render(ctx, pool, *name, files, dirs, *runs, results)
	if *out != "" {
		if err := os.WriteFile(*out, []byte(report), 0o644); err != nil {
			return err
		}
	}
	if failed > 0 {
		return fmt.Errorf("%d scenario(s) failed", failed)
	}
	return nil
}

func ms(d time.Duration) float64 { return float64(d) / float64(time.Millisecond) }

func verdict(r result) string {
	if r.err != "" {
		return "FOUT"
	}
	if r.group == "export" {
		return "ℹ️ stroom"
	}
	budget := 500 * time.Millisecond
	if r.worst {
		budget = 2 * time.Second
	}
	switch {
	case r.p95 <= 500*time.Millisecond:
		return "✅ ≤ 500 ms"
	case r.p95 <= budget:
		return "🟡 ≤ 2 s (toegestaan)"
	}
	return "❌ te traag"
}

func render(ctx context.Context, pool *pgxpool.Pool, share string, files, dirs int64, runs int, rs []result) string {
	var b strings.Builder
	setting := func(name string) string {
		var v string
		pool.QueryRow(ctx, "SHOW "+name).Scan(&v)
		return v
	}
	var version string
	pool.QueryRow(ctx, `SELECT split_part(version(), ' on ', 1)`).Scan(&version)
	var id int64
	pool.QueryRow(ctx, `SELECT id FROM shares WHERE name = $1`, share).Scan(&id)
	size := func(t string) string {
		var v string
		pool.QueryRow(ctx, `SELECT pg_size_pretty(pg_total_relation_size($1::regclass))`, fmt.Sprintf("%s_s%d", t, id)).Scan(&v)
		return v
	}

	fmt.Fprintf(&b, "# Meetrapport fase 2 – API-latentie\n\n")
	fmt.Fprintf(&b, "Gemeten op %s met `nasabench queries`.\n\n", time.Now().UTC().Format("2006-01-02 15:04 UTC"))
	fmt.Fprintf(&b, "## Dataset en omgeving\n\n")
	fmt.Fprintf(&b, "| | |\n|---|---|\n")
	fmt.Fprintf(&b, "| Bestanden | %s |\n| Mappen | %s |\n", sep(files), sep(dirs))
	fmt.Fprintf(&b, "| Grootte `files` (incl. indexen) | %s |\n| Grootte `dirs` | %s |\n| Grootte `dir_category_agg` | %s |\n",
		size("files"), size("dirs"), size("dir_category_agg"))
	fmt.Fprintf(&b, "| Database | %s |\n", version)
	fmt.Fprintf(&b, "| `shared_buffers` / `effective_cache_size` / `work_mem` | %s / %s / %s |\n",
		setting("shared_buffers"), setting("effective_cache_size"), setting("work_mem"))
	fmt.Fprintf(&b, "| `random_page_cost` / `max_parallel_workers_per_gather` | %s / %s |\n",
		setting("random_page_cost"), setting("max_parallel_workers_per_gather"))
	fmt.Fprintf(&b, "| Meetmethode | HTTP via loopback, `net/http` server in hetzelfde proces als de meting; per scenario 1 eerste aanroep (\"eerste\") en %d herhalingen waarvan p50/p95/max |\n\n", runs)
	fmt.Fprintf(&b, "Budget: gangbare weergaven ≤ 500 ms, vrije filtercombinaties op share-niveau ≤ 2 s. Het oordeel gaat op p95.\n\n")

	counts := map[string]int{}
	group := ""
	for _, r := range rs {
		if r.group != group {
			group = r.group
			fmt.Fprintf(&b, "### %s\n\n| Scenario | Niveau | Antwoord | Eerste | p50 | p95 | max | Oordeel |\n|---|---|---|---|---|---|---|---|\n", group)
		}
		v := verdict(r)
		counts[strings.Fields(v)[0]]++
		tier := r.tier
		if tier == "" {
			tier = "–"
		}
		fmt.Fprintf(&b, "| %s | %s | %s | %.0f ms | %.0f ms | %.0f ms | %.0f ms | %s |\n",
			r.name, tier, humanBytes(r.bytes), ms(r.first), ms(r.p50), ms(r.p95), ms(r.max_), v)
		if next := nextGroup(rs, r); next {
			b.WriteString("\n")
		}
	}
	fmt.Fprintf(&b, "\n**Samenvatting:** %d × ✅, %d × 🟡, %d × ❌, %d × fout.\n", counts["✅"], counts["🟡"], counts["❌"], counts["FOUT"])
	return b.String()
}

func nextGroup(rs []result, cur result) bool {
	for i, r := range rs {
		if r.name == cur.name && r.group == cur.group {
			return i+1 < len(rs) && rs[i+1].group != cur.group
		}
	}
	return false
}

func sep(n int64) string {
	s := fmt.Sprint(n)
	var out []byte
	for i, c := range []byte(s) {
		if i > 0 && (len(s)-i)%3 == 0 {
			out = append(out, '.')
		}
		out = append(out, c)
	}
	return string(out)
}

func humanBytes(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MiB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KiB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}
