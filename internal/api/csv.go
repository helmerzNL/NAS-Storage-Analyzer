package api

import (
	"encoding/csv"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// textCell neutralises spreadsheet formula injection. File names come from the
// NAS and can start with = + - @ (or a tab / CR), which Excel and LibreOffice
// would execute as a formula when the CSV is opened.
func textCell(s string) string {
	if s != "" && strings.ContainsRune("=+-@\t\r", rune(s[0])) {
		return "'" + s
	}
	return s
}

func i64(n int64) string { return strconv.FormatInt(n, 10) }

func f64(f float64) string { return strconv.FormatFloat(f, 'f', 2, 64) }

func ts(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

func tv(t time.Time) string { return t.UTC().Format(time.RFC3339) }

// csvStream writes a CSV response row by row. It aborts the connection if an
// error happens after output started, so a client never mistakes a truncated
// export for a complete one.
type csvStream struct {
	w       *csv.Writer
	rw      http.ResponseWriter
	rc      *http.ResponseController
	rows    int
	started bool
}

func newCSVStream(rw http.ResponseWriter, filename string, header []string, maxRows int) *csvStream {
	h := rw.Header()
	h.Set("Content-Type", "text/csv; charset=utf-8")
	h.Set("Content-Disposition", `attachment; filename="`+filename+`"`)
	h.Set("X-Max-Rows", strconv.Itoa(maxRows))
	c := &csvStream{w: csv.NewWriter(rw), rw: rw, rc: http.NewResponseController(rw)}
	c.w.Write(header)
	return c
}

func (c *csvStream) row(cells ...string) error {
	c.rows++
	if err := c.w.Write(cells); err != nil {
		return err
	}
	if c.rows%1000 == 0 {
		return c.flush()
	}
	return nil
}

func (c *csvStream) flush() error {
	c.w.Flush()
	if err := c.w.Error(); err != nil {
		return err
	}
	c.started = true
	// Long exports must not be cut by the server's write deadline.
	_ = c.rc.SetWriteDeadline(time.Now().Add(2 * time.Minute))
	return c.rc.Flush()
}

func (c *csvStream) done() error { return c.flush() }
