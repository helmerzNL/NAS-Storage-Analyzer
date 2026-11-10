package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/helmerznl/nas-storage-analyzer/internal/rekey"
	"github.com/helmerznl/nas-storage-analyzer/internal/secrets"
	"github.com/helmerznl/nas-storage-analyzer/internal/store"
)

const rekeyUsage = `Usage: nasa rekey [-check] [-db ...]

Replaces the key that encrypts what the database stores encrypted: the SMB passwords of shares, the authenticator secrets
of users and the addresses of notification targets. Use it when the key may have been seen by somebody else, or as routine.

  The current key   NASSA_ENCRYPTION_KEY       or the file named by NASSA_ENCRYPTION_KEY_FILE
  The new key       NASSA_NEW_ENCRYPTION_KEY   or the file named by NASSA_NEW_ENCRYPTION_KEY_FILE   (make one: nasa genkey)

Everything is re-encrypted in one transaction: afterwards all of it is under the new key, or nothing has changed. Stop the
app and the worker first, run this, put the new key in NASSA_ENCRYPTION_KEY and start them again. A backup file made in the
app before this holds SMB passwords under the old key: make a new one.

  -check   change nothing: report whether every stored secret opens with the current key

`

func runRekey(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("rekey", flag.ExitOnError)
	fs.Usage = func() { fmt.Fprint(fs.Output(), rekeyUsage); fs.PrintDefaults() }
	dsn := fs.String("db", dsnFromEnv(), "PostgreSQL connection string (env NASSA_DB_DSN, or NASSA_DB_HOST/PORT/NAME/USER/PASSWORD)")
	check := fs.Bool("check", false, "change nothing: report whether every stored secret opens with the current key")
	fs.Parse(args)
	if *dsn == "" {
		return errors.New("no database: pass -db or set NASSA_DB_DSN")
	}
	current, err := secrets.FromEnv()
	if err != nil {
		return err
	}
	var next *secrets.Box
	if !*check {
		if next, err = secrets.NewKeyFromEnv(); err != nil {
			return err
		}
		if next.SameKey(current) {
			return errors.New("the new key is the current key: nothing to do (make a new one with: nasa genkey)")
		}
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	st, err := store.Open(ctx, *dsn)
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	defer st.Close()

	if *check {
		res, problems, err := rekey.Check(ctx, st.Pool, current)
		if err != nil {
			return err
		}
		if len(problems) > 0 {
			return fmt.Errorf("%d of %d stored secrets do not open with NASSA_ENCRYPTION_KEY: %s", len(problems), res.Total(), strings.Join(problems, "; "))
		}
		fmt.Fprintf(out, "All stored secrets open with NASSA_ENCRYPTION_KEY (%s).\n", res)
		return nil
	}

	res, already, err := rekey.Run(ctx, st.Pool, current, next, "nasa rekey")
	if err != nil {
		return err
	}
	if already {
		fmt.Fprintf(out, "The stored secrets are already under the new key (%s): nothing was changed.\n", res)
	} else {
		fmt.Fprintf(out, "Re-encrypted with the new key: %s.\n", res)
	}
	fmt.Fprint(out, "Now set NASSA_ENCRYPTION_KEY to the new key and start the app and the worker; with the old key they can open none of this.\n"+
		"A backup file made in the app before this holds SMB passwords under the old key: make a new one.\n")
	return nil
}
