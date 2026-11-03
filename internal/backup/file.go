// Package backup takes everything the owner entered as one file and puts it back, from the settings screen, following App-Guidance's
// backup-restore.md. The four rules of that page are the shape of this package:
//
//  1. The file is the owner's, so it never unlocks the app: no password, passkey, authenticator secret, recovery code, session or invitation,
//     and no account or role at all. The one exception is the SMB password of a share, and only as the ciphertext the database already holds,
//     sealed with a key that lives in the environment and never in the file.
//  2. A restore is all or nothing and the file is not trusted: it is held to the same validators as typing the data in, and everything is
//     swapped in one transaction.
//  3. Nothing slow is invisible: the job reports a phase and a step.
//  4. Once the file is up, the restore belongs to the server: the browser starts it and watches it.
//
// What is in the file, and what is not and why, is listed in sections (and guarded by a test that fails when a table is added without being
// classified), so a new table or column cannot silently be left out.
package backup

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/helmerznl/nas-storage-analyzer/internal/auth"
	"github.com/helmerznl/nas-storage-analyzer/internal/shares"
)

const (
	// Format names the kind of file; FormatVersion is its version. A file of an older version still restores (a section that did not exist yet is
	// left as it is); a file of a newer version is refused with the version that wrote it.
	Format        = "nas-storage-analyzer-backup"
	FormatVersion = 1

	// MaxFileBytes is the largest file the stage route accepts. A backup is a few tens of kilobytes (a share is about a kilobyte, a file type thirty
	// bytes), so this is generous; whatever runs in front of the application has to allow a body this large (docs/deploy.md).
	MaxFileBytes = 8 << 20
	// The most entries a file may hold in a section. They are far above anything real and keep a hostile file from asking for work without end.
	MaxShares      = 200
	MaxFileTypes   = 5000
	MaxPreferences = 500

	// StageTTL is how long an uploaded, checked file waits for the owner's confirmation.
	StageTTL = time.Hour
	// JobStale is how long a running job may go without moving before it is given up as interrupted.
	JobStale = 15 * time.Minute
)

// File is the backup file. A section that is absent (nil) is left as it is by a restore; one that is present but empty replaces what is there
// with nothing, which is what the confirmation shows.
type File struct {
	Format        string    `json:"format"`
	FormatVersion int       `json:"format_version"`
	AppVersion    string    `json:"app_version"`
	CreatedAt     time.Time `json:"created_at"`
	// IncludesFiles is false: this application has no uploaded files, so the backup is one JSON file and never an archive.
	IncludesFiles bool `json:"includes_files"`

	Shares      []shares.Portable `json:"shares"`
	FileTypes   []shares.FileType `json:"file_types"`
	Preferences []auth.UserPrefs  `json:"preferences"`
}

// InvalidError is a file that is refused. Key names what is wrong in a way the screen words in the reader's language; Detail is the English
// technical reason (for support) and Field says where in the file.
type InvalidError struct {
	Key    string // not_backup, newer, too_big, too_many, invalid
	Field  string
	Detail string
	Params map[string]string
}

func (e *InvalidError) Error() string {
	if e.Field != "" {
		return e.Field + ": " + e.Detail
	}
	return e.Detail
}

func invalid(key, field, format string, a ...any) *InvalidError {
	return &InvalidError{Key: key, Field: field, Detail: fmt.Sprintf(format, a...)}
}

// Checked is a file that passed every check, with the values as they will be stored.
type Checked struct {
	File File
	// Unreadable names the shares whose SMB password could not be opened with this installation's key; they are restored without one.
	Unreadable []string
	// Prefs are the preferences as they will be stored (through auth.CleanPrefs).
	Prefs []auth.UserPrefs
}

// header is the part of a file every version has. It is read first and loosely, so a file of a newer version is reported as that and not as a
// file with fields this version does not know.
type header struct {
	Format        string `json:"format"`
	FormatVersion int    `json:"format_version"`
	AppVersion    string `json:"app_version"`
}

// Check holds a whole file to the rules, changing nothing. It is what the stage route runs, and what the job runs again before it replaces
// anything: the file on the server is not trusted to be the one that was checked an hour ago.
func (s *Service) Check(ctx context.Context, data []byte) (*Checked, error) {
	if len(data) > MaxFileBytes {
		return nil, &InvalidError{Key: "too_big", Detail: fmt.Sprintf("the file is larger than %d MB", MaxFileBytes>>20), Params: map[string]string{"max_mb": fmt.Sprint(MaxFileBytes >> 20)}}
	}
	var h header
	if err := json.Unmarshal(data, &h); err != nil || h.Format != Format {
		return nil, invalid("not_backup", "", "this is not a NAS Storage Analyzer backup file")
	}
	if h.FormatVersion < 1 {
		return nil, invalid("not_backup", "format_version", "the file has no usable format_version")
	}
	if h.FormatVersion > FormatVersion {
		e := invalid("newer", "format_version", "the file was written by a newer version (%s, format %d); update first, then restore", h.AppVersion, h.FormatVersion)
		e.Params = map[string]string{"app_version": h.AppVersion, "format": fmt.Sprint(h.FormatVersion)}
		return nil, e
	}
	var f File
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&f); err != nil {
		return nil, invalid("invalid", "", "%s", jsonProblem(err))
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, invalid("invalid", "", "there is more after the end of the backup")
	}
	if f.IncludesFiles {
		return nil, invalid("invalid", "includes_files", "this application has no uploaded files, so a backup with files is not one of its own")
	}
	switch {
	case len(f.Shares) > MaxShares:
		return nil, tooMany("shares", len(f.Shares), MaxShares)
	case len(f.FileTypes) > MaxFileTypes:
		return nil, tooMany("file_types", len(f.FileTypes), MaxFileTypes)
	case len(f.Preferences) > MaxPreferences:
		return nil, tooMany("preferences", len(f.Preferences), MaxPreferences)
	}

	out := &Checked{File: f}
	if f.Shares != nil {
		ready, unreadable, err := s.shares.Prepare(f.Shares)
		if err != nil {
			return nil, asInvalid(err)
		}
		out.File.Shares, out.Unreadable = ready, unreadable
	}
	if f.FileTypes != nil {
		cats, err := s.shares.Categories(ctx)
		if err != nil {
			return nil, err
		}
		if err := shares.PrepareFileTypes(f.FileTypes, cats); err != nil {
			return nil, asInvalid(err)
		}
	}
	if f.Preferences != nil {
		seen := map[string]int{}
		out.Prefs = make([]auth.UserPrefs, 0, len(f.Preferences))
		for i, p := range f.Preferences {
			field := fmt.Sprintf("preferences[%d]", i)
			name := strings.ToLower(strings.TrimSpace(p.Username))
			if name == "" || len(name) > 64 {
				return nil, invalid("invalid", field+".username", "a user name is 1-64 characters")
			}
			if j, dup := seen[name]; dup {
				return nil, invalid("invalid", field+".username", "the user %q is also in preferences[%d]", p.Username, j)
			}
			seen[name] = i
			clean, err := auth.CleanPrefs(p.Prefs)
			if err != nil {
				return nil, invalid("invalid", field+".prefs", "%s", err.Error())
			}
			out.Prefs = append(out.Prefs, auth.UserPrefs{Username: strings.TrimSpace(p.Username), Prefs: clean})
		}
	}
	return out, nil
}

func tooMany(section string, n, max int) *InvalidError {
	e := invalid("too_many", section, "%d entries; at most %d are accepted", n, max)
	e.Params = map[string]string{"section": section, "max": fmt.Sprint(max)}
	return e
}

// asInvalid turns what the share and file-type validators report into a refused file.
func asInvalid(err error) error {
	var ve *shares.ValidationError
	if errors.As(err, &ve) {
		return invalid("invalid", ve.Field, "%s", ve.Msg)
	}
	return err
}

// jsonProblem words a decoding error without echoing the file's content back more than the error already does.
func jsonProblem(err error) string {
	var syn *json.SyntaxError
	var typ *json.UnmarshalTypeError
	switch {
	case errors.As(err, &syn):
		return fmt.Sprintf("the file is not valid JSON (at byte %d)", syn.Offset)
	case errors.As(err, &typ):
		return fmt.Sprintf("%s has the wrong type (expected %s)", typ.Field, typ.Type)
	default:
		return strings.TrimPrefix(err.Error(), "json: ")
	}
}
