package main

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/helmerznl/nas-storage-analyzer/internal/api"
	"github.com/helmerznl/nas-storage-analyzer/internal/auth"
	"github.com/helmerznl/nas-storage-analyzer/internal/jobs"
	"github.com/helmerznl/nas-storage-analyzer/internal/secrets"
	"github.com/helmerznl/nas-storage-analyzer/internal/shares"
	"github.com/helmerznl/nas-storage-analyzer/internal/store"
	"github.com/helmerznl/nas-storage-analyzer/internal/webui"
)

// env returns the first non-empty variable; a VAR_FILE variant (Docker
// secrets) wins over VAR.
func env(name, def string) string {
	if f := os.Getenv(name + "_FILE"); f != "" {
		if b, err := os.ReadFile(f); err == nil {
			return strings.TrimSpace(string(b))
		}
	}
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}

func envBool(name string) bool {
	v := strings.ToLower(env(name, ""))
	return v == "1" || v == "true" || v == "yes"
}

// location returns the time zone for cron schedules and the day/night limit
// (NASA_TZ, else TZ, else UTC).
func location() (*time.Location, error) {
	name := env("NASA_TZ", os.Getenv("TZ"))
	if name == "" {
		return time.UTC, nil
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return nil, fmt.Errorf("unknown time zone %q (NASA_TZ): %w", name, err)
	}
	return loc, nil
}

func runGenKey(args []string) error {
	fmt.Println(secrets.GenerateKey())
	return nil
}

func randomToken() string {
	b := make([]byte, 15)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b)
}

func runServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	listen := fs.String("listen", env("NASA_LISTEN", ":8080"), "address to listen on (env NASA_LISTEN)")
	dsn := fs.String("db", dsnFromEnv(), "PostgreSQL connection string (env NASA_DB_DSN, or NASA_DB_HOST/PORT/NAME/USER/PASSWORD)")
	migrate := fs.Bool("migrate", true, "apply database migrations before serving")
	stmtTimeout := fs.Duration("statement-timeout", 10*time.Second, "abort any database statement that runs longer")
	maxConns := fs.Int("max-conns", 20, "maximum database connections for queries")
	csvRows := fs.Int("csv-max-rows", 1_000_000, "maximum rows of one CSV export")
	fs.Parse(args)

	if *dsn == "" {
		return errors.New("no database: set NASA_DB_DSN or NASA_DB_HOST/NASA_DB_PASSWORD")
	}
	box, err := secrets.FromEnv()
	if err != nil {
		return err
	}
	proxies, err := api.ParseTrustedProxies(env("NASA_TRUSTED_PROXIES", ""))
	if err != nil {
		return err
	}
	log := newLogger()
	if w := dataPathWarning(os.Getenv("DATA_PATH")); w != "" {
		log.Warn(w)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	rw, err := store.Open(ctx, *dsn)
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	defer rw.Close()
	if *migrate {
		if err := rw.Migrate(ctx); err != nil {
			return err
		}
	}
	ro, err := store.OpenReadOnly(ctx, *dsn, int32(*maxConns), *stmtTimeout)
	if err != nil {
		return fmt.Errorf("connect (read-only): %w", err)
	}
	defer ro.Close()

	loc, err := location()
	if err != nil {
		return err
	}
	sharesRoot := env("NASA_SHARES_ROOT", "/mnt/shares")
	queue := jobs.NewQueue(rw.Pool)
	svc := auth.NewService(rw.Pool, box, auth.DefaultConfig())
	opts := api.Options{
		Auth:           svc,
		Shares:         shares.NewService(rw.Pool, box, shares.Options{SharesRoot: sharesRoot}, loc),
		Queue:          queue,
		SharesRoot:     sharesRoot,
		Static:         webui.Handler(),
		TrustedProxies: proxies,
		SetupToken:     env("NASA_SETUP_TOKEN", ""),
		MetricsToken:   env("NASA_METRICS_TOKEN", ""),
		RequireHTTPS:   envBool("NASA_REQUIRE_HTTPS"),
	}
	if id := env("NASA_RP_ID", ""); id != "" {
		opts.RP = auth.RelyingParty{ID: id, Name: env("NASA_RP_NAME", "NAS Storage Analyzer")}
		for _, o := range strings.Split(env("NASA_RP_ORIGINS", ""), ",") {
			if o = strings.TrimSpace(o); o != "" {
				opts.RP.Origins = append(opts.RP.Origins, o)
			}
		}
		if len(opts.RP.Origins) == 0 {
			return errors.New("NASA_RP_ID is set but NASA_RP_ORIGINS is empty (for example https://nas.example.com)")
		}
	} else {
		log.Warn("NASA_RP_ID is not set: passkeys are bound to the address used in the browser. Set NASA_RP_ID and NASA_RP_ORIGINS in production.")
	}
	if n, err := svc.UserCount(ctx); err != nil {
		return err
	} else if n == 0 {
		if opts.SetupToken == "" {
			opts.SetupToken = randomToken()
		}
		log.Warn("no account exists yet: open the web interface and create the first administrator",
			"setup_token", opts.SetupToken)
	}

	go jobs.NewScheduler(rw.Pool, queue, loc, log).Run(ctx)
	go func() {
		t := time.NewTicker(10 * time.Minute)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				if err := svc.Maintain(ctx); err != nil && ctx.Err() == nil {
					log.Error("maintenance failed", "error", err)
				}
				if err := queue.Prune(ctx); err != nil && ctx.Err() == nil {
					log.Error("pruning jobs failed", "error", err)
				}
			case <-ctx.Done():
				return
			}
		}
	}()

	srv := &http.Server{
		Addr:              *listen,
		Handler:           api.New(ro, log, api.Config{CSVMaxRows: *csvRows, Build: buildInfo()}, opts),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		IdleTimeout:       2 * time.Minute,
		// No WriteTimeout: CSV exports stream for as long as they need; handlers set their own deadlines.
	}
	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	log.Info("listening", "version", version, "commit", commit, "channel", buildInfo().Channel, "addr", *listen, "trusted_proxies", len(proxies), "require_https", opts.RequireHTTPS)
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
