package delscript

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// The package writes text. It must stay unable to touch a file or run a program, because that is what makes the
// statement "the app never deletes anything" true of the code that generates delete scripts. The read-only compose
// mount is the second wall; this is the first.
func TestPackageCannotRunProgramsOrTouchFiles(t *testing.T) {
	forbidden := map[string]bool{
		"os": true, "os/exec": true, "os/signal": true, "syscall": true, "io/ioutil": true, "io/fs": true,
		"net": true, "net/http": true, "plugin": true, "unsafe": true, "runtime/cgo": true, "path/filepath": true,
	}
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	for _, e := range entries {
		n := e.Name()
		if e.IsDir() || !strings.HasSuffix(n, ".go") || strings.HasSuffix(n, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(token.NewFileSet(), filepath.Join(".", n), nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		checked++
		for _, imp := range f.Imports {
			path, _ := strconv.Unquote(imp.Path.Value)
			if forbidden[path] || strings.HasPrefix(path, "golang.org/x/sys") || strings.Contains(path, "/internal/store") ||
				strings.Contains(path, "pgx") {
				t.Errorf("%s imports %q: the script generator must not be able to run programs, touch files or reach the database", n, path)
			}
		}
	}
	if checked < 5 {
		t.Fatalf("only %d source files checked; is the test looking in the right folder?", checked)
	}
}
