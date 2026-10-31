// Package shares validates and stores share definitions (what to scan and
// how). The scan itself lives in scanrun and the mounting in mounts.
package shares

import (
	"errors"
	"fmt"
	"path"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/helmerznl/nas-storage-analyzer/internal/jobs"
	"github.com/helmerznl/nas-storage-analyzer/internal/scanner"
)

// Kinds of share.
const (
	KindBind = "bind" // a path already mounted on the host and bind-mounted into the container (recommended)
	KindNFS  = "nfs"  // mounted by the worker (needs CAP_SYS_ADMIN)
	KindSMB  = "smb"  // mounted by the worker (needs CAP_SYS_ADMIN)
)

// Input is everything an administrator can set on a share. The same shape is
// used for the API body; kind-specific fields are stored in the config column.
type Input struct {
	Name string `json:"name"`
	Kind string `json:"kind"`

	Path string `json:"path,omitempty"` // bind: path inside the container, below the shares root

	Server       string  `json:"server,omitempty"`        // nfs, smb
	Export       string  `json:"export,omitempty"`        // nfs: exported path
	Share        string  `json:"share,omitempty"`         // smb: share name
	Username     string  `json:"username,omitempty"`      // smb; empty = guest
	Domain       string  `json:"domain,omitempty"`        // smb
	Password     *string `json:"password,omitempty"`      // smb; write-only, stored encrypted
	Version      string  `json:"version,omitempty"`       // nfs: 3, 4, 4.1, 4.2; smb: 2.0 ... 3.1.1
	MountOptions string  `json:"mount_options,omitempty"` // extra options from an allow-list; "ro" is always added

	Excludes       []string `json:"excludes"`
	Workers        int      `json:"workers"`
	MaxOpsPerSec   int      `json:"max_ops_per_sec"` // 0 = unlimited
	MaxOpsDay      int      `json:"max_ops_day"`     // limit during the day window; 0 = same as MaxOpsPerSec
	DayStartHour   int      `json:"day_start_hour"`  // 0-23
	DayEndHour     int      `json:"day_end_hour"`    // 0-24
	Cron           string   `json:"cron"`            // 5 fields; empty = no schedule
	LastUsedBasis  string   `json:"last_used_basis"` // atime | mtime | max
	FullScanEvery  int      `json:"full_scan_every"` // every Nth scan is full; 0 = never automatically
	RetentionScans int      `json:"retention_scans"` // history kept by number of scans; 0 = unlimited
	RetentionDays  int      `json:"retention_days"`  // ... and by age; 0 = unlimited
	SnapshotDepth  int      `json:"snapshot_depth"`  // folder depth kept in history, 1-12
}

// Defaults returns the settings a new share starts with.
func Defaults() Input {
	return Input{Kind: KindBind, Workers: 16, DayStartHour: 8, DayEndHour: 22, LastUsedBasis: "atime", FullScanEvery: 7,
		RetentionScans: 104, SnapshotDepth: 4, Excludes: []string{".snapshot", "@eaDir", "#recycle", ".Trash-*"}}
}

// ValidationError is a problem with the submitted settings (HTTP 400).
type ValidationError struct{ Field, Msg string }

func (e *ValidationError) Error() string { return e.Field + ": " + e.Msg }

func bad(field, format string, a ...any) error {
	return &ValidationError{Field: field, Msg: fmt.Sprintf(format, a...)}
}

var (
	nameRE     = regexp.MustCompile(`^[\p{L}\p{N}][\p{L}\p{N} ._-]{0,63}$`)
	hostRE     = regexp.MustCompile(`^([A-Za-z0-9]([A-Za-z0-9.-]{0,251}[A-Za-z0-9])?|[0-9A-Fa-f:]{2,45})$`)
	exportRE   = regexp.MustCompile(`^/[A-Za-z0-9._/@+ -]*$`)
	smbShareRE = regexp.MustCompile(`^[^\\/:*?"<>|,\x00-\x1f]{1,80}$`)
	userRE     = regexp.MustCompile(`^[A-Za-z0-9._@\\ -]{0,64}$`)
	domainRE   = regexp.MustCompile(`^[A-Za-z0-9._-]{0,64}$`)
)

// Options configure validation.
type Options struct {
	// SharesRoot is the directory below which bind shares must lie
	// (NASA_SHARES_MOUNT, "/mnt/shares" by default, in the container).
	// Empty disables the check (tests).
	SharesRoot string
}

// Validate checks and normalises an Input in place.
func Validate(in *Input, opt Options) error {
	in.Name = strings.TrimSpace(in.Name)
	if !nameRE.MatchString(in.Name) {
		return bad("name", "use 1-64 letters, digits, spaces or . _ -")
	}
	switch in.Kind {
	case KindBind:
		if err := validateBindPath(in, opt); err != nil {
			return err
		}
	case KindNFS:
		if in.Path != "" || in.Share != "" || in.Username != "" || in.Domain != "" || in.Password != nil {
			return bad("kind", "an NFS share has a server and an exported path only")
		}
		if !hostRE.MatchString(in.Server) {
			return bad("server", "enter a host name or IP address")
		}
		if !exportRE.MatchString(in.Export) || strings.Contains(in.Export, "..") {
			return bad("export", "enter the exported path, for example /volume1/media")
		}
		if in.Version == "" {
			in.Version = "3"
		}
	case KindSMB:
		if in.Path != "" || in.Export != "" {
			return bad("kind", "an SMB share has a server and a share name, not a path")
		}
		if !hostRE.MatchString(in.Server) {
			return bad("server", "enter a host name or IP address")
		}
		if !smbShareRE.MatchString(in.Share) {
			return bad("share", "enter the share name")
		}
		if !userRE.MatchString(in.Username) {
			return bad("username", "contains characters that are not allowed")
		}
		if !domainRE.MatchString(in.Domain) {
			return bad("domain", "contains characters that are not allowed")
		}
		if in.Version == "" {
			in.Version = "3.0"
		}
	default:
		return bad("kind", "must be bind, nfs or smb")
	}
	if in.Kind != KindBind {
		if _, err := mountOptions(in.Kind, in.Version, in.MountOptions); err != nil {
			return err
		}
	} else if in.MountOptions != "" || in.Server != "" || in.Export != "" || in.Share != "" {
		return bad("kind", "a bind share has only a path")
	}

	if len(in.Excludes) > 100 {
		return bad("excludes", "at most 100 patterns")
	}
	cleaned := make([]string, 0, len(in.Excludes))
	for _, p := range in.Excludes {
		if p = strings.TrimSpace(p); p != "" {
			if utf8.RuneCountInString(p) > 200 {
				return bad("excludes", "a pattern is longer than 200 characters")
			}
			cleaned = append(cleaned, p)
		}
	}
	in.Excludes = cleaned
	if _, err := scanner.NewExcluder(in.Excludes); err != nil {
		return bad("excludes", "%s", strings.TrimPrefix(err.Error(), "invalid exclude pattern "))
	}
	for _, c := range []struct {
		field     string
		v, lo, hi int
	}{
		{"workers", in.Workers, 1, 256}, {"max_ops_per_sec", in.MaxOpsPerSec, 0, 1_000_000}, {"max_ops_day", in.MaxOpsDay, 0, 1_000_000},
		{"day_start_hour", in.DayStartHour, 0, 23}, {"day_end_hour", in.DayEndHour, 0, 24},
		{"full_scan_every", in.FullScanEvery, 0, 1000}, {"retention_scans", in.RetentionScans, 0, 100_000},
		{"retention_days", in.RetentionDays, 0, 36_500}, {"snapshot_depth", in.SnapshotDepth, 1, 12},
	} {
		if c.v < c.lo || c.v > c.hi {
			return bad(c.field, "must be between %d and %d", c.lo, c.hi)
		}
	}
	switch in.LastUsedBasis {
	case "atime", "mtime", "max":
	default:
		return bad("last_used_basis", "must be atime, mtime or max")
	}
	in.Cron = strings.TrimSpace(in.Cron)
	if err := jobs.ValidateCron(in.Cron); err != nil {
		return bad("cron", "%s", err.Error())
	}
	return nil
}

func validateBindPath(in *Input, opt Options) error {
	if in.Path == "" || !path.IsAbs(in.Path) || strings.ContainsRune(in.Path, 0) {
		example := "/mnt/shares"
		if opt.SharesRoot != "" {
			example = opt.SharesRoot
		}
		return bad("path", "enter an absolute path inside the container, for example %s", path.Join(example, "media"))
	}
	in.Path = path.Clean(in.Path)
	if opt.SharesRoot != "" {
		root := path.Clean(opt.SharesRoot)
		if !inside(root, in.Path) {
			return bad("path", "must be inside %s (mount your share there, see the README)", root)
		}
	}
	return nil
}

// ---- mount options --------------------------------------------------------------

var (
	nfsVersions = map[string]bool{"3": true, "4": true, "4.0": true, "4.1": true, "4.2": true}
	smbVersions = map[string]bool{"2.0": true, "2.1": true, "3.0": true, "3.02": true, "3.1.1": true}

	nfsOptRE = regexp.MustCompile(`^(soft|hard|intr|nolock|noatime|nodiratime|tcp|udp|nordirplus|noresvport|nconnect=([1-9]|1[0-6])|` +
		`(timeo|retrans|rsize|wsize|actimeo|port|mountport)=[0-9]{1,7}|lookupcache=(all|none|pos|positive)|proto=(tcp|udp)|sec=(sys|krb5|krb5i|krb5p))$`)
	smbOptRE = regexp.MustCompile(`^(soft|hard|noserverino|nounix|nobrl|noperm|seal|nosharesock|noatime|nodiratime|` +
		`cache=(none|strict|loose)|iocharset=[A-Za-z0-9-]{1,20}|(rsize|wsize|actimeo|echo_interval|port)=[0-9]{1,7}|sec=(ntlmssp|ntlmsspi|ntlmv2|ntlmv2i|krb5|krb5i|none))$`)
)

// mountOptions builds the -o string for mount(8): read-only first, then the
// protocol version, then the administrator's extra options. Only options from
// an allow-list pass, so a share definition cannot smuggle in "rw" or a
// different file system type.
func mountOptions(kind, version, extra string) (string, error) {
	var re *regexp.Regexp
	var ver string
	switch kind {
	case KindNFS:
		if !nfsVersions[version] {
			return "", bad("version", "NFS version must be one of 3, 4, 4.1, 4.2")
		}
		re, ver = nfsOptRE, "vers="+version
	case KindSMB:
		if !smbVersions[version] {
			return "", bad("version", "SMB version must be one of 2.0, 2.1, 3.0, 3.02, 3.1.1")
		}
		re, ver = smbOptRE, "vers="+version
	default:
		return "", errors.New("not a mounted share")
	}
	opts := []string{"ro", ver}
	seen := map[string]bool{}
	for _, o := range strings.Split(extra, ",") {
		if o = strings.TrimSpace(o); o == "" {
			continue
		}
		if !re.MatchString(o) {
			return "", bad("mount_options", "option %q is not allowed", o)
		}
		if k := strings.SplitN(o, "=", 2)[0]; !seen[k] {
			seen[k] = true
			opts = append(opts, o)
		}
	}
	return strings.Join(opts, ","), nil
}

// MountOptions is the exported form of mountOptions for the mount manager.
func MountOptions(kind, version, extra string) (string, error) {
	return mountOptions(kind, version, extra)
}
