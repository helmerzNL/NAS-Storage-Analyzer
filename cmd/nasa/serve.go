package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/helmerznl/nas-storage-analyzer/internal/api"
	"github.com/helmerznl/nas-storage-analyzer/internal/store"
)

func runServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	listen := fs.String("listen", "127.0.0.1:8080", "address to listen on")
	dsn := fs.String("db", dsnFromEnv(), "PostgreSQL connection string (env NASA_DB_DSN)")
	remote := fs.Bool("allow-unauthenticated-remote", false, "allow binding to a non-loopback address although the API has no authentication yet")
	migrate := fs.Bool("migrate", true, "apply database migrations before serving")
	stmtTimeout := fs.Duration("statement-timeout", 10*time.Second, "abort any database statement that runs longer")
	maxConns := fs.Int("max-conns", 20, "maximum database connections")
	csvRows := fs.Int("csv-max-rows", 1_000_000, "maximum rows of one CSV export")
	fs.Parse(args)

	if *dsn == "" {
		return errors.New("no database: pass -db or set NASA_DB_DSN")
	}
	if err := api.CheckListenAddr(*listen, *remote); err != nil {
		return err
	}
	log := newLogger()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if *migrate {
		st, err := store.Open(ctx, *dsn)
		if err != nil {
			return fmt.Errorf("connect: %w", err)
		}
		err = st.Migrate(ctx)
		st.Close()
		if err != nil {
			return err
		}
	}
	pool, err := store.OpenReadOnly(ctx, *dsn, int32(*maxConns), *stmtTimeout)
	if err != nil {
		return fmt.Errorf("connect (read-only): %w", err)
	}
	defer pool.Close()

	srv := &http.Server{
		Addr:              *listen,
		Handler:           api.New(pool, log, api.Config{CSVMaxRows: *csvRows}),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		IdleTimeout:       2 * time.Minute,
		// No WriteTimeout: CSV exports stream for as long as they need; handlers set their own deadlines.
	}
	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	log.Info("api listening", "addr", *listen, "unauthenticated", true)
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	log.Info("shutting down")
	return srv.Shutdown(shutdown)
}
