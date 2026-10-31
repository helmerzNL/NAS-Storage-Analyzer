// Command nasabench seeds a synthetic share of millions of files straight into
// the database and measures API latency on it. It is a developer tool and is
// not part of the production image.
package main

import (
	"fmt"
	"os"
)

const usage = `nasabench - seed and measure

  nasabench seed    -db DSN [-share bench] [-files 10000000] [-dirs 200000]
  nasabench queries -db DSN [-share bench] [-runs 30] [-out report.md]
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "seed":
		err = runSeed(os.Args[2:])
	case "queries":
		err = runQueries(os.Args[2:])
	default:
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
