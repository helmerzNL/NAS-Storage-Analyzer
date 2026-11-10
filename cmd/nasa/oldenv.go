package main

import (
	"fmt"
	"sort"
	"strings"
)

// The environment variables were called NASA_* until they were called NASSA_* (docs/adr/0012-nassa-environment-variables.md). A process that
// still has one of the old names was configured for the old names: starting it anyway would use the default of every setting it did not
// find, among them the database password and the encryption key, so it refuses to start and says what to rename.
const oldPrefix = "NASA_"

// oldEnvNames returns the variables in environ (as os.Environ lists them, NAME=value) that still have the old prefix, sorted.
func oldEnvNames(environ []string) []string {
	var out []string
	for _, kv := range environ {
		name, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(name, oldPrefix) {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// checkOldEnv is nil when no variable has the old prefix. Otherwise it names every one with the name it has now.
func checkOldEnv(environ []string) error {
	old := oldEnvNames(environ)
	if len(old) == 0 {
		return nil
	}
	pairs := make([]string, len(old))
	for i, name := range old {
		pairs[i] = name + " is now NASSA_" + strings.TrimPrefix(name, oldPrefix)
	}
	return fmt.Errorf("the environment variables were renamed from NASA_* to NASSA_*: %s. Rename them in your .env (sed -i 's/^NASA_/NASSA_/' .env) and start again; nothing was started",
		strings.Join(pairs, ", "))
}

// takesSettings says whether a command reads its settings from the environment. Printing the version or a new key does not.
func takesSettings(command string) bool {
	switch command {
	case "serve", "worker", "migrate", "scan", "recategorize", "healthcheck", "rekey":
		return true
	}
	return false
}
