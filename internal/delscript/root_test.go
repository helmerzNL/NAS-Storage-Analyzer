package delscript

import (
	"errors"
	"testing"
)

func TestNormalizeRootWindows(t *testing.T) {
	for in, want := range map[string]string{
		`\\ZBNAS\Media`:           `\\ZBNAS\Media`,
		`\\ZBNAS\Media\`:          `\\ZBNAS\Media`,
		`//ZBNAS/Media/Films`:     `\\ZBNAS\Media\Films`,
		`\\ZBNAS\\Media\\Films\\`: `\\ZBNAS\Media\Films`,
		`  \\zbnas\media  `:       `\\zbnas\media`,
		`Z:\`:                     `Z:\`,
		`z:`:                      `Z:\`,
		`Z:/Media/`:               `Z:\Media`,
		`C:\Users\Me\My Files`:    `C:\Users\Me\My Files`,
		`\\NAS\100 (copy)\x`:      `\\NAS\100 (copy)\x`,
		`\\NAS\naïve 日本語\😀`:       `\\NAS\naïve 日本語\😀`,
	} {
		for _, tg := range []Target{TargetCmd, TargetPowerShell} {
			got, err := NormalizeRoot(tg, in)
			if err != nil || got != want {
				t.Errorf("%s %q: got %q %v, want %q", tg, in, got, err, want)
			}
		}
	}
}

func TestNormalizeRootPosix(t *testing.T) {
	for in, want := range map[string]string{
		"/mnt/user/Media":       "/mnt/user/Media",
		"/mnt/user/Media/":      "/mnt/user/Media",
		"//mnt//user///Media/":  "/mnt/user/Media",
		"/":                     "/",
		"/Volumes/My Disk":      "/Volumes/My Disk",
		"/mnt/it's $HOME `x` %": "/mnt/it's $HOME `x` %",
		"  /srv/x  ":            "/srv/x",
	} {
		got, err := NormalizeRoot(TargetSh, in)
		if err != nil || got != want {
			t.Errorf("%q: got %q %v, want %q", in, got, err, want)
		}
	}
}

// The base path is typed by a person and ends up in a script that deletes files: it is refused as hard as a name.
func TestNormalizeRootRefuses(t *testing.T) {
	bad := map[string][]Target{
		"":                         {TargetCmd, TargetPowerShell, TargetSh},
		"   ":                      {TargetCmd, TargetPowerShell, TargetSh},
		"relative/path":            {TargetCmd, TargetPowerShell, TargetSh},
		"/mnt/a\nrm -rf ~":         {TargetCmd, TargetPowerShell, TargetSh},
		"/mnt/a\rb":                {TargetSh},
		"/mnt/a\x00b":              {TargetSh},
		"/mnt/a\x1bb":              {TargetSh},
		"/mnt/a\u202eb":            {TargetSh},
		"/mnt/a\ufffdb":            {TargetSh},
		`/mnt/a"b`:                 {TargetSh},
		"/mnt/a|b":                 {TargetSh},
		"/mnt/../etc":              {TargetSh},
		"/mnt/./x":                 {TargetSh},
		`\\?\C:\x`:                 {TargetCmd, TargetPowerShell},
		`\\.\PhysicalDrive0`:       {TargetCmd, TargetPowerShell},
		`\\server`:                 {TargetCmd, TargetPowerShell}, // a share is needed
		`C:folder`:                 {TargetCmd, TargetPowerShell}, // relative to the current folder of that drive
		`Z:\a:b`:                   {TargetCmd, TargetPowerShell},
		`\\s\sh\a*b`:               {TargetCmd, TargetPowerShell},
		`\\s\sh\a?b`:               {TargetCmd, TargetPowerShell},
		`\\s\sh\..\x`:              {TargetCmd, TargetPowerShell},
		`\\s\sh\ends.`:             {TargetCmd, TargetPowerShell},
		`\\s\sh\ends \sub`:         {TargetCmd, TargetPowerShell}, // a folder that ends in a space
		`\\s\sh` + "\n" + `& calc`: {TargetCmd, TargetPowerShell},
		`\\s\sh\a"b`:               {TargetCmd, TargetPowerShell},
		`\\s\sh\a|b`:               {TargetCmd, TargetPowerShell},
		`\\s\sh\100%`:              {TargetCmd}, // cmd would read it as the start of a variable
		"/mnt/user/Media":          {TargetCmd, TargetPowerShell},
	}
	for in, targets := range bad {
		for _, tg := range targets {
			got, err := NormalizeRoot(tg, in)
			var re *RootError
			if err == nil || !errors.As(err, &re) || got != "" {
				t.Errorf("%s %q must be refused, got %q %v", tg, in, got, err)
			}
		}
	}
	// A percent sign is fine for the targets that do not parse it.
	if got, err := NormalizeRoot(TargetPowerShell, `\\s\sh\100%`); err != nil || got != `\\s\sh\100%` {
		t.Errorf("powershell with a percent sign: %q %v", got, err)
	}
}
