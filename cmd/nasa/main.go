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
  migrate   apply database migrations and seed default categories
  scan      scan a directory tree (read-only) and store the result

Run "nasa <command> -h" for the flags of a command.
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "migrate":
		err = runMigrate(os.Args[2:])
	case "scan":
		err = runScan(os.Args[2:])
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
