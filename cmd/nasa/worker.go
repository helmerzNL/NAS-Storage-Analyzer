package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/helmerznl/nas-storage-analyzer/internal/mounts"
	"github.com/helmerznl/nas-storage-analyzer/internal/secrets"
	"github.com/helmerznl/nas-storage-analyzer/internal/shares"
	"github.com/helmerznl/nas-storage-analyzer/internal/store"
	"github.com/helmerznl/nas-storage-analyzer/internal/worker"
)

func runWorker(args []string) error {
	fs := flag.NewFlagSet("worker", flag.ExitOnError)
	dsn := fs.String("db", dsnFromEnv(), "PostgreSQL connection string (env NASA_DB_DSN, or NASA_DB_HOST/PORT/NAME/USER/PASSWORD)")
	parallel := fs.Int("parallel", atoi(env("NASA_WORKER_PARALLEL", "2")), "scans that may run at the same time")
	fs.Parse(args)
	if *dsn == "" {
		return errors.New("no database: set NASA_DB_DSN or NASA_DB_HOST/NASA_DB_PASSWORD")
	}
	box, err := secrets.FromEnv()
	if err != nil {
		return err
	}
	loc, err := location()
	if err != nil {
		return err
	}
	log := newLogger()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	st, err := store.Open(ctx, *dsn)
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	defer st.Close()
	if err := st.Migrate(ctx); err != nil { // safe next to the app: serialised by an advisory lock
		return err
	}
	id := env("NASA_WORKER_ID", "")
	if id == "" {
		host, _ := os.Hostname()
		id = host + "-" + strconv.Itoa(os.Getpid())
	}
	w := worker.New(st,
		shares.NewService(st.Pool, box, shares.Options{SharesRoot: env("NASA_SHARES_ROOT", "/mnt/shares")}, loc),
		mounts.New(env("NASA_MOUNT_DIR", "/mnt/managed")),
		worker.Config{ID: id, Parallel: *parallel, Loc: loc}, log)
	if addr := env("NASA_WORKER_HEALTH_LISTEN", "127.0.0.1:8081"); addr != "" {
		go serveHealth(ctx, addr, w.Healthy, log)
	}
	w.Run(ctx)
	return nil
}

// serveHealth answers /healthz for the container health check: 200 while the
// worker can reach its queue, 503 with the reason otherwise.
func serveHealth(ctx context.Context, addr string, healthy func() error, log *slog.Logger) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		if err := healthy(); err != nil {
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
			return
		}
		w.Write([]byte("ok\n"))
	})
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		log.Error("worker health listener failed", "addr", addr, "error", err)
		return
	}
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() { <-ctx.Done(); srv.Close() }()
	srv.Serve(ln)
}

func atoi(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}
