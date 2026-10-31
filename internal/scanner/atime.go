package scanner

import (
	"bufio"
	"io"
	"strconv"
	"strings"
)

// AtimeStats counts how atime relates to mtime across scanned files.
type AtimeStats struct {
	Files       int64
	EqualMtime  int64 // atime == mtime
	BeforeMtime int64 // atime < mtime: never read since written, or mtime preserved by a copy
}

// MountInfo describes the mount a scan root lives on.
type MountInfo struct {
	MountPoint string   `json:"mount_point"`
	FSType     string   `json:"fs_type"`
	Options    []string `json:"options"`
}

// AtimeQuality is the verdict on whether "last used" can be based on atime.
type AtimeQuality struct {
	Verdict        string     `json:"verdict"` // reliable | doubtful | unreliable
	Reasons        []string   `json:"reasons"` // machine keys, translated in the UI
	SampleFiles    int64      `json:"sample_files"`
	PctEqualMtime  float64    `json:"pct_equal_mtime"`
	PctBeforeMtime float64    `json:"pct_before_mtime"`
	Mount          *MountInfo `json:"mount,omitempty"`
	// CarriedOver: this scan stat'ed too few files (an incremental scan that
	// reused almost everything), so the verdict of the last full-enough scan
	// is shown instead.
	CarriedOver bool `json:"carried_over,omitempty"`
}

const (
	Reliable   = "reliable"
	Doubtful   = "doubtful"
	Unreliable = "unreliable"

	// MinAtimeSample is the number of stat'ed files below which a verdict from
	// statistics alone is not trustworthy.
	MinAtimeSample = 100
)

var networkFS = map[string]bool{"nfs": true, "nfs4": true, "cifs": true, "smb3": true, "smbfs": true}

// AssessAtime combines mount options and the statistics of scanned files.
// mi may be nil when the mount could not be determined. Statistics only cover
// files that were stat'ed in this run, so an incremental scan has a smaller
// sample than a full scan.
func AssessAtime(s AtimeStats, mi *MountInfo) AtimeQuality {
	q := AtimeQuality{Verdict: Reliable, SampleFiles: s.Files, Mount: mi}
	worsen := func(v, reason string) {
		q.Reasons = append(q.Reasons, reason)
		if v == Unreliable || (v == Doubtful && q.Verdict == Reliable) {
			q.Verdict = v
		}
	}
	if s.Files > 0 {
		q.PctEqualMtime = float64(s.EqualMtime) / float64(s.Files)
		q.PctBeforeMtime = float64(s.BeforeMtime) / float64(s.Files)
	}
	if mi != nil {
		for _, o := range mi.Options {
			if o == "noatime" {
				worsen(Unreliable, "mount_noatime")
			}
		}
		if networkFS[mi.FSType] {
			worsen(Doubtful, "network_fs")
		}
	}
	if s.Files < MinAtimeSample {
		worsen(Doubtful, "small_sample")
		return q
	}
	switch {
	case q.PctEqualMtime >= 0.9:
		worsen(Unreliable, "atime_equals_mtime_majority")
	case q.PctEqualMtime >= 0.5:
		worsen(Doubtful, "atime_equals_mtime_many")
	}
	if q.PctBeforeMtime >= 0.5 {
		worsen(Doubtful, "atime_before_mtime_majority")
	}
	return q
}

// FindMount returns the mount containing absPath using the format of
// /proc/self/mountinfo. The longest matching mount point wins.
func FindMount(absPath string, mountinfo io.Reader) (*MountInfo, error) {
	var best *MountInfo
	sc := bufio.NewScanner(mountinfo)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		// id parent maj:min root mountpoint options [optional...] - fstype source superopts
		sep := -1
		for i, x := range f {
			if x == "-" {
				sep = i
				break
			}
		}
		if sep < 6 || sep+1 >= len(f) {
			continue
		}
		mp := unescapeMountinfo(f[4])
		if !underPath(absPath, mp) {
			continue
		}
		if best == nil || len(mp) >= len(best.MountPoint) {
			opts := strings.Split(f[5], ",")
			if sep+3 < len(f) {
				opts = append(opts, strings.Split(f[sep+3], ",")...)
			}
			best = &MountInfo{MountPoint: mp, FSType: f[sep+1], Options: opts}
		}
	}
	return best, sc.Err()
}

func underPath(p, mp string) bool {
	if mp == "/" {
		return true
	}
	return p == mp || strings.HasPrefix(p, mp+"/")
}

// unescapeMountinfo decodes the octal escapes (\040 for space) used in
// mountinfo fields.
func unescapeMountinfo(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+4 <= len(s) {
			if n, err := strconv.ParseUint(s[i+1:i+4], 8, 8); err == nil {
				b.WriteByte(byte(n))
				i += 3
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}
