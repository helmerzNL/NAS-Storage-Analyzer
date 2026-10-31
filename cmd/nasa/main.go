// Command nasa is the NAS Storage Analyzer binary. Phase 1 provides the
// "migrate" and "scan" subcommands; "serve" and "worker" follow in later phases.
package main

import (
	"fmt"
	"os"
)

const usage = `nasa - NAS Storage Analyzer

Usage:
  nasa <command> [flags]

Commands:
  serve     run the web interface and REST API
  worker    run queued scans (mounts NFS/SMB shares when needed)
  genkey    print a new encryption key for NASA_ENCRYPTION_KEY
  migrate   apply database migrations and seed default categories
  scan      scan a directory tree (read-only) and store the result
  recategorize  apply the current extension mapping to stored files (no rescan)

Run "nasa <command> -h" for the flags of a command.
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "worker":
		err = runWorker(os.Args[2:])
	case "genkey":
		err = runGenKey(os.Args[2:])
	case "serve":
		err = runServe(os.Args[2:])
	case "migrate":
		err = runMigrate(os.Args[2:])
	case "scan":
		err = runScan(os.Args[2:])
	case "recategorize":
		err = runRecategorize(os.Args[2:])
	case "-h", "--help", "help":
		fmt.Print(usage)
		return
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n%s", os.Args[1], usage)
		os.Exit(2)
	}
	if err != nil {
		if code, ok := err.(exitCode); ok {
			os.Exit(int(code))
		}
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

// exitCode lets a command ask for a specific status without printing an error.
type exitCode int

func (e exitCode) Error() string { return fmt.Sprintf("exit status %d", int(e)) }
