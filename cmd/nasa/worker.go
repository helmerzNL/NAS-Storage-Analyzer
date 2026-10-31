package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"syscall"

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
	w.Run(ctx)
	return nil
}

func atoi(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}
