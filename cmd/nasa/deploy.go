package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/helmerznl/nas-storage-analyzer/internal/buildinfo"
)

// buildInfo is what this process reports as its version and release channel. The channel comes from
// NASA_TAG, the variable docker-compose.yml uses to pick the image and passes into the container.
func buildInfo() buildinfo.Info {
	return buildinfo.New(version, commit, os.Getenv("NASA_TAG"))
}

// versionLine is the output of `nasa version`.
func versionLine() string {
	b := buildInfo()
	line := "nasa " + b.Version
	if b.Commit != "" {
		line += " (" + b.Commit + ")"
	}
	return line
}

// dataPathWarning returns the warning to log for the value of DATA_PATH, or "" when there is nothing to say.
//
// DATA_PATH is the host folder that holds everything that must not be lost. Compose refuses to start
// without it, but a relative value is not resolved by the app: it is resolved by whatever tool starts the
// stack, and a deploy tool that makes a fresh checkout (or Dockhand on Unraid, which resolved ./data to a
// folder in the RAM disk) leaves a database that looks fine until the next reboot. Nothing errors on the
// way, so the app says it. A tool that injects variables itself bypasses the .env.example, which is why this
// is checked here and not only in the template.
func dataPathWarning(dataPath string) string {
	if dataPath == "" || strings.HasPrefix(dataPath, "/") {
		return ""
	}
	return fmt.Sprintf("DATA_PATH is %q, a relative path. The tool that starts the stack decides where that is "+
		"(Dockhand on Unraid: a folder in the RAM disk), so the database may be somewhere you do not expect or be lost "+
		"at the next reboot. Set an absolute folder of the host in .env, then run scripts/migrate_data.sh on the host "+
		"(docs/deploy.md, section 'Waar de data staat / Where the data lives').", dataPath)
}

// updateCheckEnabled reads NASA_UPDATE_CHECK: on unless it says no. "false", "0", "no" and "off" (any case) switch it off;
// anything else, including empty, leaves the default, which is on.
func updateCheckEnabled(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "false", "0", "no", "off":
		return false
	}
	return true
}
