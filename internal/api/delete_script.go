package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/helmerznl/nas-storage-analyzer/internal/delscript"
)

// scriptRequest is the body of POST /shares/{share}/delete-script.
type scriptRequest struct {
	IDs        []int64 `json:"ids"`
	Script     string  `json:"script"`      // cmd | powershell | sh
	ClientRoot string  `json:"client_root"` // empty: the share's own base path for this kind of script
	Lang       string  `json:"lang"`        // en (default) | nl
}

// deleteScript writes a script that deletes the files the administrator selected, for them to run. It writes text and
// nothing else: the application never deletes a file (docs/adr/0006-delete-scripts.md). What the script says about
// the files is fixed program text plus data; see internal/delscript.
func (s *Server) deleteScript(w http.ResponseWriter, r *http.Request) error {
	id, err := shareID(r)
	if err != nil {
		return err
	}
	var req scriptRequest
	if err := readJSON(r, &req); err != nil {
		return err
	}
	target, ok := delscript.ParseTarget(req.Script)
	if !ok {
		return s.fieldProblem(w, r, "script", "must be cmd, powershell or sh")
	}
	lang := delscript.Lang(req.Lang)
	switch lang {
	case "":
		lang = delscript.LangEN
	case delscript.LangEN, delscript.LangNL:
	default:
		return s.fieldProblem(w, r, "lang", "must be en or nl")
	}
	switch n := len(req.IDs); {
	case n == 0:
		return s.fieldProblem(w, r, "ids", "select at least one file")
	case n > delscript.MaxFiles:
		return s.fieldProblem(w, r, "ids", fmt.Sprintf("at most %d files fit in one script", delscript.MaxFiles))
	}

	info, err := s.q.ShareByID(r.Context(), id)
	if err != nil {
		return err
	}
	root := strings.TrimSpace(req.ClientRoot)
	if root == "" {
		key := "client_posix"
		if target.IsWindows() {
			key = "client_windows"
		}
		if root = info.Config[key]; root == "" {
			return s.fieldProblem(w, r, "client_root",
				"this share has no base path for this kind of script: enter where the machine that runs it sees the share, or set it on the share")
		}
	}

	sel, err := s.q.FilesForScript(r.Context(), id, req.IDs)
	if err != nil {
		return err
	}
	files := make([]delscript.File, 0, len(sel.Files))
	var skipped []delscript.Skip
	for _, missing := range sel.Missing {
		// A file that is gone has no name to give; the id is what the person selected.
		skipped = append(skipped, delscript.Skip{Path: "id " + strconv.FormatInt(missing, 10), Reason: delscript.ReasonGone})
	}
	for _, f := range sel.Files {
		if f.Twin {
			skipped = append(skipped, delscript.Skip{Path: f.Path, Reason: delscript.ReasonAmbiguous})
			continue
		}
		files = append(files, delscript.File{Path: f.Path, Size: f.Size, Mtime: scriptMtime(f.Mtime), Nlink: f.Nlink})
	}

	now := s.now()
	res, err := delscript.Generate(files, delscript.Params{
		Share: info.Name, Root: root, Target: target, Lang: lang,
		ScanID: sel.ScanID, ScanTime: sel.ScanTime, Generated: now, Skipped: skipped,
	})
	var rootErr *delscript.RootError
	switch {
	case errors.As(err, &rootErr):
		return s.fieldProblem(w, r, "client_root", rootErr.Msg)
	case errors.Is(err, delscript.ErrNoFiles):
		return s.fieldProblem(w, r, "ids", err.Error()+" ("+skipSummary(res.Skipped)+")")
	case err != nil:
		return err
	}

	sum := sha256.Sum256(res.Script)
	// What was handed out, never which files: the log is read by people who may not see the share.
	s.audit(r, "script.generate", info.Name, map[string]any{
		"script": string(target), "files": res.Files, "skipped": len(res.Skipped), "bytes": res.Bytes,
		"scan_id": sel.ScanID, "root": res.Root, "sha256": hex.EncodeToString(sum[:]),
	})

	h := w.Header()
	h.Set("Content-Type", "text/plain; charset=utf-8")
	h.Set("Content-Disposition", attachment("delete-"+info.Name+"-"+now.UTC().Format("20060102-1504")+target.Extension()))
	h.Set("Cache-Control", "no-store")
	h.Set("X-Files", strconv.Itoa(res.Files))
	h.Set("X-Skipped", strconv.Itoa(len(res.Skipped)))
	h.Set("X-Bytes", strconv.FormatInt(res.Bytes, 10))
	_, err = w.Write(res.Script)
	return err
}

// fieldProblem answers 400 and names the field of the request that is wrong. It returns the write error, if any.
func (s *Server) fieldProblem(w http.ResponseWriter, r *http.Request, field, detail string) error {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(http.StatusBadRequest)
	return json.NewEncoder(w).Encode(problemBody{Type: "about:blank", Title: "invalid request", Status: http.StatusBadRequest,
		Detail: detail, Code: "invalid_request", Field: field, RequestID: requestID(r)})
}

// scriptMtime is the modification time as the script compares it, in seconds. Zero means "do not compare": the scanner
// clamps times outside 1900-2200 to the limits (some SMB servers report dates far beyond year 9999), so a value on a
// limit is not the real time of the file.
func scriptMtime(t time.Time) int64 {
	const lo, hi = int64(-2208988800), int64(7258118400)
	u := t.Unix()
	if u <= lo || u >= hi {
		return 0
	}
	return u
}

// skipSummary counts the files left out per reason: "windows-name 2, gone 1".
func skipSummary(skipped []delscript.Skip) string {
	count := map[delscript.Reason]int{}
	for _, s := range skipped {
		count[s.Reason]++
	}
	parts := make([]string, 0, len(count))
	for reason, n := range count {
		parts = append(parts, fmt.Sprintf("%s %d", reason, n))
	}
	sort.Strings(parts)
	if len(parts) == 0 {
		return "nothing was selected"
	}
	return strings.Join(parts, ", ")
}

// attachment builds a Content-Disposition for a download with the standard library's RFC 6266 / RFC 2231 encoding, which
// puts a name with letters outside ASCII in filename*. Spaces become hyphens: a script is typed on a command line.
func attachment(name string) string {
	return mime.FormatMediaType("attachment", map[string]string{"filename": strings.ReplaceAll(name, " ", "-")})
}
