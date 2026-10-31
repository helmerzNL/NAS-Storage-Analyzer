package buildinfo

import "testing"

func TestChannelOf(t *testing.T) {
	for tag, want := range map[string]string{
		"":               Stable, // NASA_TAG unset
		"latest":         Stable,
		"0.1.0":          Stable, // pinned to a version
		"v0.1.0":         Stable,
		"beta":           Beta,
		"Beta":           Beta,
		"beta-5d74b70":   Beta, // the tag of one beta build
		"0.2.0-beta.3":   Beta, // a beta cut for testers
		" latest ":       Stable,
		"somethingelse":  Stable,
		"BETA-ABCDEF123": Beta,
	} {
		if got := ChannelOf(tag); got != want {
			t.Errorf("ChannelOf(%q) = %q, want %q", tag, got, want)
		}
	}
}

func TestNew(t *testing.T) {
	if got := New("", "", ""); got != (Info{Version: "dev", Channel: Stable}) {
		t.Errorf("a build from a checkout = %+v", got)
	}
	if got := New("0.1.0", "abc1234", "beta"); got != (Info{Version: "0.1.0", Commit: "abc1234", Channel: Beta}) {
		t.Errorf("a beta build = %+v", got)
	}
	// The same binary reports a different channel for a different NASA_TAG: the channel is not baked in.
	if New("0.1.0", "abc1234", "beta").Channel == New("0.1.0", "abc1234", "").Channel {
		t.Error("beta and unset NASA_TAG report the same channel")
	}
}
