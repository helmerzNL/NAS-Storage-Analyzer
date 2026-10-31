package main

import (
	"strings"
	"testing"
)

func TestDataPathWarningOnlyForARelativePath(t *testing.T) {
	for _, rel := range []string{"./data", "data", "../nas", "~/nas", "appdata/nasa"} {
		if w := dataPathWarning(rel); !strings.Contains(w, "relative path") || !strings.Contains(w, rel) {
			t.Errorf("DATA_PATH=%q: warning = %q", rel, w)
		}
	}
	for _, ok := range []string{"/srv/nasa", "/mnt/user/appdata/nas-storage-analyzer", "/", ""} {
		if w := dataPathWarning(ok); w != "" {
			t.Errorf("DATA_PATH=%q: unexpected warning %q", ok, w)
		}
	}
}

// A beta build says so: the same binary reports beta for NASA_TAG=beta and stable when it is unset.
func TestBuildInfoFollowsNASA_TAG(t *testing.T) {
	oldVersion, oldCommit := version, commit
	t.Cleanup(func() { version, commit = oldVersion, oldCommit })
	version, commit = "0.1.0", "abc1234"

	t.Setenv("NASA_TAG", "beta")
	if got := buildInfo().Channel; got != "beta" {
		t.Errorf("NASA_TAG=beta: channel = %q", got)
	}
	t.Setenv("NASA_TAG", "")
	if got := buildInfo().Channel; got != "stable" {
		t.Errorf("NASA_TAG unset: channel = %q", got)
	}
	t.Setenv("NASA_TAG", "latest")
	if got := buildInfo().Channel; got != "stable" {
		t.Errorf("NASA_TAG=latest: channel = %q", got)
	}
	if got := versionLine(); got != "nasa 0.1.0 (abc1234)" {
		t.Errorf("versionLine = %q", got)
	}
	commit = ""
	if got := versionLine(); got != "nasa 0.1.0" {
		t.Errorf("versionLine without a commit = %q", got)
	}
}
