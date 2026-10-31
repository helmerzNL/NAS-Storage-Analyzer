package api

import (
	"context"
	"log/slog"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"

	"github.com/helmerznl/nas-storage-analyzer/internal/query"
)

type metrics struct {
	reg      *prometheus.Registry
	dur      *prometheus.HistogramVec
	inflight prometheus.Gauge
}

func newMetrics(q *query.DB, pool *pgxpool.Pool, log *slog.Logger) *metrics {
	m := &metrics{
		reg: prometheus.NewRegistry(),
		dur: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "nasa_http_request_duration_seconds",
			Help:    "HTTP request duration by route pattern, method and status class.",
			Buckets: []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30},
		}, []string{"route", "method", "status_class"}),
		inflight: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "nasa_http_requests_in_flight", Help: "HTTP requests currently being served.",
		}),
	}
	poolGauge := func(name, help string, f func(*pgxpool.Stat) float64) prometheus.Collector {
		return prometheus.NewGaugeFunc(prometheus.GaugeOpts{Name: name, Help: help}, func() float64 { return f(pool.Stat()) })
	}
	m.reg.MustRegister(
		collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
		m.dur, m.inflight,
		poolGauge("nasa_db_pool_acquired_connections", "Connections in use.", func(s *pgxpool.Stat) float64 { return float64(s.AcquiredConns()) }),
		poolGauge("nasa_db_pool_idle_connections", "Idle connections.", func(s *pgxpool.Stat) float64 { return float64(s.IdleConns()) }),
		poolGauge("nasa_db_pool_max_connections", "Configured maximum connections.", func(s *pgxpool.Stat) float64 { return float64(s.MaxConns()) }),
		newShareCollector(q, log),
	)
	return m
}

func (m *metrics) observe(route, method string, status int, d time.Duration) {
	m.dur.WithLabelValues(route, method, strconv.Itoa(status/100)+"xx").Observe(d.Seconds())
}

// shareCollector reads the state of every share at scrape time. The query
// side is a handful of indexed lookups per share.
type shareCollector struct {
	q   *query.DB
	log *slog.Logger

	size, alloc, files, dirs, finished, duration, errors, fsTotal, fsFree, running, success *prometheus.Desc
}

func newShareCollector(q *query.DB, log *slog.Logger) *shareCollector {
	d := func(name, help string) *prometheus.Desc {
		return prometheus.NewDesc("nasa_share_"+name, help, []string{"share"}, nil)
	}
	return &shareCollector{
		q: q, log: log,
		size:     d("size_bytes", "Logical size of the share as of the last completed scan."),
		alloc:    d("allocated_bytes", "Allocated size (st_blocks*512) as of the last completed scan."),
		files:    d("files", "Number of files as of the last completed scan."),
		dirs:     d("directories", "Number of directories as of the last completed scan."),
		finished: d("last_scan_timestamp_seconds", "Unix time the last completed scan finished."),
		duration: d("last_scan_duration_seconds", "Duration of the last completed scan."),
		errors:   d("last_scan_errors", "Errors logged during the last completed scan."),
		fsTotal:  d("filesystem_total_bytes", "Capacity of the file system holding the share (statfs)."),
		fsFree:   d("filesystem_free_bytes", "Free space of the file system holding the share (statfs)."),
		running:  d("scan_running", "1 while a scan of the share is running."),
		success:  d("last_scan_success", "1 when the most recent scan attempt succeeded, 0 when it failed or was cancelled."),
	}
}

func (c *shareCollector) Describe(ch chan<- *prometheus.Desc) {
	for _, d := range []*prometheus.Desc{c.size, c.alloc, c.files, c.dirs, c.finished, c.duration, c.errors, c.fsTotal, c.fsFree, c.running, c.success} {
		ch <- d
	}
}

func (c *shareCollector) Collect(ch chan<- prometheus.Metric) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	shares, err := c.q.Shares(ctx)
	if err != nil {
		c.log.Error("collect share metrics", "error", err)
		return
	}
	g := func(d *prometheus.Desc, v float64, share string) {
		ch <- prometheus.MustNewConstMetric(d, prometheus.GaugeValue, v, share)
	}
	for _, s := range shares {
		if sc := s.LastScan; sc != nil {
			g(c.size, float64(sc.Bytes), s.Name)
			g(c.alloc, float64(sc.AllocBytes), s.Name)
			g(c.files, float64(sc.Files), s.Name)
			g(c.dirs, float64(sc.Dirs), s.Name)
			g(c.errors, float64(sc.ErrorCount), s.Name)
			if sc.FinishedAt != nil {
				g(c.finished, float64(sc.FinishedAt.Unix()), s.Name)
				g(c.duration, sc.FinishedAt.Sub(sc.StartedAt).Seconds(), s.Name)
			}
			if sc.FsTotalBytes != nil {
				g(c.fsTotal, float64(*sc.FsTotalBytes), s.Name)
			}
			if sc.FsFreeBytes != nil {
				g(c.fsFree, float64(*sc.FsFreeBytes), s.Name)
			}
		}
		running, success := 0.0, 0.0
		if l := s.LatestScan; l != nil {
			if l.Status == "running" {
				running = 1
			}
			if l.Status == "succeeded" {
				success = 1
			}
		}
		g(c.running, running, s.Name)
		if s.LatestScan != nil && s.LatestScan.Status != "running" {
			g(c.success, success, s.Name)
		}
	}
}
