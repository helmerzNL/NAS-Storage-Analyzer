package shares_test

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/helmerznl/nas-storage-analyzer/internal/shares"
)

// tree builds a shares root with a few folders, a file, and links.
func tree(t *testing.T) (root, outside string) {
	t.Helper()
	base := t.TempDir()
	root = filepath.Join(base, "shares")
	outside = filepath.Join(base, "outside")
	for _, d := range []string{"media/Movies", "media/music", "Backups", "zeta", "empty"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(outside, "secret"), 0o755); err != nil {
		t.Fatal(err)
	}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.WriteFile(filepath.Join(root, "notes.txt"), []byte("x"), 0o644))
	must(os.Symlink("media", filepath.Join(root, "link-inside")))                              // relative link to a folder in the root
	must(os.Symlink(outside, filepath.Join(root, "link-outside")))                             // leaves the root
	must(os.Symlink("notes.txt", filepath.Join(root, "link-to-file")))                         // not a folder
	must(os.Symlink("does-not-exist", filepath.Join(root, "link-broken")))                     // dangling
	must(os.Symlink(filepath.Join(outside, "secret"), filepath.Join(root, "media", "escape"))) // below the root
	return root, outside
}

func names(l *shares.Listing) []string {
	out := make([]string, len(l.Dirs))
	for i, d := range l.Dirs {
		out[i] = d.Name
	}
	return out
}

func TestBrowseListsOnlyFoldersSorted(t *testing.T) {
	root, _ := tree(t)
	l, err := shares.Browse(root, "")
	if err != nil {
		t.Fatal(err)
	}
	// Case-insensitive order; the file, the link to a file, the broken link and the link
	// that leaves the root are not offered. The link to a folder inside the root is.
	if got, want := strings.Join(names(l), ","), "Backups,empty,link-inside,media,zeta"; got != want {
		t.Errorf("folders = %s, want %s", got, want)
	}
	if l.Root != root || l.Path != root || l.Parent != "" || l.Truncated {
		t.Errorf("listing header = %+v", l)
	}
	for _, d := range l.Dirs {
		if d.Path != root+"/"+d.Name {
			t.Errorf("path of %s = %s", d.Name, d.Path)
		}
		if d.Symlink != (d.Name == "link-inside") {
			t.Errorf("symlink flag of %s = %v", d.Name, d.Symlink)
		}
	}
}

func TestBrowseSubfolderHasParent(t *testing.T) {
	root, _ := tree(t)
	l, err := shares.Browse(root, root+"/media/")
	if err != nil {
		t.Fatal(err)
	}
	if l.Path != root+"/media" || l.Parent != root {
		t.Errorf("path/parent = %s / %s", l.Path, l.Parent)
	}
	if got, want := strings.Join(names(l), ","), "Movies,music"; got != want { // "escape" leaves the root
		t.Errorf("folders = %s, want %s", got, want)
	}
	if l, err = shares.Browse(root, root+"/empty"); err != nil || len(l.Dirs) != 0 || l.Dirs == nil {
		t.Errorf("an empty folder must list as an empty array: %+v %v", l, err)
	}
}

func TestBrowseFollowsALinkInsideTheRoot(t *testing.T) {
	root, _ := tree(t)
	l, err := shares.Browse(root, root+"/link-inside")
	if err != nil {
		t.Fatal(err)
	}
	if l.Path != root+"/link-inside" || l.Parent != root {
		t.Errorf("path/parent = %s / %s", l.Path, l.Parent)
	}
	if got, want := strings.Join(names(l), ","), "Movies,music"; got != want {
		t.Errorf("folders = %s, want %s", got, want)
	}
	if len(l.Dirs) > 0 && l.Dirs[0].Path != root+"/link-inside/Movies" {
		t.Errorf("entries keep the path the caller used: %s", l.Dirs[0].Path)
	}
}

func TestBrowseNeverLeavesTheRoot(t *testing.T) {
	root, outside := tree(t)
	for name, p := range map[string]string{
		"absolute elsewhere":       "/etc",
		"outside folder":           outside,
		"dot-dot":                  root + "/../outside",
		"dot-dot at the end":       root + "/media/../..",
		"sibling with same stem":   root + "x",
		"link that leaves":         root + "/link-outside",
		"below a link that leaves": root + "/link-outside/secret",
		"link below the root":      root + "/media/escape",
		"relative":                 "media",
		"nul byte":                 root + "/media\x00/..",
	} {
		_, err := shares.Browse(root, p)
		var ve *shares.ValidationError
		if !errors.As(err, &ve) || ve.Field != "path" {
			t.Errorf("%s (%q): want a path validation error, got %v", name, p, err)
		}
	}
}

func TestBrowseErrors(t *testing.T) {
	root, _ := tree(t)
	if _, err := shares.Browse(root, root+"/nope"); !errors.Is(err, shares.ErrNotFound) {
		t.Errorf("a missing folder: %v", err)
	}
	if _, err := shares.Browse(root, root+"/link-broken"); !errors.Is(err, shares.ErrNotFound) {
		t.Errorf("a dangling link: %v", err)
	}
	var ve *shares.ValidationError
	if _, err := shares.Browse(root, root+"/notes.txt"); !errors.As(err, &ve) || ve.Field != "path" {
		t.Errorf("a file: %v", err)
	}
	if _, err := shares.Browse("", ""); !errors.Is(err, shares.ErrNoSharesRoot) {
		t.Errorf("no root configured: %v", err)
	}
	if _, err := shares.Browse(filepath.Join(root, "gone"), ""); !errors.As(err, &ve) || !strings.Contains(ve.Msg, "NASA_SHARES_DIR") {
		t.Errorf("a root that does not exist must point at the configuration: %v", err)
	}
}

// A bind-mounted share path may itself be a symlink; the scanner accepts that, so the picker must too.
func TestBrowseRootThatIsALink(t *testing.T) {
	root, _ := tree(t)
	link := filepath.Join(t.TempDir(), "mnt-shares")
	if err := os.Symlink(root, link); err != nil {
		t.Fatal(err)
	}
	l, err := shares.Browse(link, "")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.Join(names(l), ","), "Backups,empty,link-inside,media,zeta"; got != want {
		t.Errorf("folders = %s, want %s", got, want)
	}
	if l.Dirs[0].Path != link+"/Backups" {
		t.Errorf("paths stay below the configured root: %s", l.Dirs[0].Path)
	}
}

func TestBrowseTruncatesHugeFolders(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < shares.BrowseMaxDirs+5; i++ {
		if err := os.Mkdir(filepath.Join(root, fmt.Sprintf("d%04d", i)), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	l, err := shares.Browse(root, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(l.Dirs) != shares.BrowseMaxDirs || !l.Truncated {
		t.Errorf("got %d folders, truncated=%v", len(l.Dirs), l.Truncated)
	}
}
