package main

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

func TestOldVariableNamesAreRefusedWithTheNewName(t *testing.T) {
	err := checkOldEnv([]string{"PATH=/usr/bin", "NASA_TAG=beta", "NASSA_TAG=beta", "NASA_DB_PASSWORD=x", "HOME=/root"})
	if err == nil {
		t.Fatal("old names were accepted")
	}
	for _, want := range []string{"NASA_DB_PASSWORD is now NASSA_DB_PASSWORD", "NASA_TAG is now NASSA_TAG", "sed -i 's/^NASA_/NASSA_/' .env", "nothing was started"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the message lacks %q: %v", want, err)
		}
	}
	// Sorted, so the message is the same however the environment happened to be ordered.
	if strings.Index(err.Error(), "NASA_DB_PASSWORD is now") > strings.Index(err.Error(), "NASA_TAG is now") {
		t.Errorf("the names are not sorted: %v", err)
	}
	if strings.Contains(err.Error(), "NASSA_TAG=beta") || strings.Contains(err.Error(), "x,") {
		t.Errorf("a value is in the message: %v", err)
	}
}

func TestTheNewNamesAndOtherVariablesAreLeftAlone(t *testing.T) {
	// Only the prefix counts: not a name that merely contains it, not the new prefix, not a value.
	for _, environ := range [][]string{nil, {}, {"NASSA_TAG=beta", "NASSA_DB_PASSWORD=x"}, {"MYNASA_X=1", "X_NASA_Y=2", "NASAX=1", "nasa_tag=1", "SOMETHING=NASA_TAG"}} {
		if err := checkOldEnv(environ); err != nil {
			t.Errorf("%v: %v", environ, err)
		}
	}
}

// Every variable the example file documents has an old name that is refused, so a new variable cannot be added without being covered.
func TestEveryDocumentedVariableHasItsOldNameRefused(t *testing.T) {
	raw, err := os.ReadFile("../../.env.example")
	if err != nil {
		t.Fatal(err)
	}
	names := regexp.MustCompile(`(?m)^#?(NASSA_[A-Z0-9_]+)=`).FindAllStringSubmatch(string(raw), -1)
	if len(names) < 20 {
		t.Fatalf("only %d variables found in .env.example", len(names))
	}
	for _, m := range names {
		old := "NASA_" + strings.TrimPrefix(m[1], "NASSA_")
		err := checkOldEnv([]string{old + "=1"})
		if err == nil || !strings.Contains(err.Error(), old+" is now "+m[1]) {
			t.Errorf("%s is not refused with %s: %v", old, m[1], err)
		}
	}
}

func TestOnlyTheCommandsThatReadSettingsAreChecked(t *testing.T) {
	for cmd, want := range map[string]bool{"serve": true, "worker": true, "migrate": true, "scan": true, "recategorize": true, "healthcheck": true,
		"version": false, "genkey": false, "help": false, "-h": false, "": false, "bogus": false} {
		if got := takesSettings(cmd); got != want {
			t.Errorf("takesSettings(%q) = %v, want %v", cmd, got, want)
		}
	}
}
