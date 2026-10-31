package scanner

import (
	"slices"
	"strings"
	"testing"
)

const sampleMountinfo = `22 28 0:21 / /sys rw,nosuid,nodev,noexec,relatime shared:7 - sysfs sysfs rw
28 1 8:1 / / rw,relatime shared:1 - ext4 /dev/sda1 rw
41 28 0:35 / /mnt/nas rw,noatime shared:20 - nfs4 nas:/export rw,vers=4.2,hard
42 41 0:36 / /mnt/nas/with\040space rw,relatime - cifs //nas/s rw,vers=3.1.1
`

func TestFindMount(t *testing.T) {
	cases := []struct {
		path, mp, fs string
		opt          string
	}{
		{"/home/user/x", "/", "ext4", "relatime"},
		{"/mnt/nas/media", "/mnt/nas", "nfs4", "noatime"},
		{"/mnt/nas", "/mnt/nas", "nfs4", "noatime"},
		{"/mnt/nasty", "/", "ext4", "relatime"}, // prefix must respect path boundaries
		{"/mnt/nas/with space/a", "/mnt/nas/with space", "cifs", "relatime"},
	}
	for _, c := range cases {
		mi, err := FindMount(c.path, strings.NewReader(sampleMountinfo))
		if err != nil || mi == nil {
			t.Fatalf("%s: %v %v", c.path, mi, err)
		}
		if mi.MountPoint != c.mp || mi.FSType != c.fs || !slices.Contains(mi.Options, c.opt) {
			t.Errorf("%s -> %+v, want %s/%s/%s", c.path, *mi, c.mp, c.fs, c.opt)
		}
	}
}

func TestAssessAtime(t *testing.T) {
	ext4 := &MountInfo{FSType: "ext4", Options: []string{"rw", "relatime"}}
	cases := []struct {
		name    string
		s       AtimeStats
		mi      *MountInfo
		verdict string
		reason  string
	}{
		{"healthy", AtimeStats{Files: 1000, EqualMtime: 100, BeforeMtime: 50}, ext4, Reliable, ""},
		{"noatime mount", AtimeStats{Files: 1000}, &MountInfo{FSType: "ext4", Options: []string{"noatime"}}, Unreliable, "mount_noatime"},
		{"atime==mtime almost everywhere", AtimeStats{Files: 1000, EqualMtime: 950}, ext4, Unreliable, "atime_equals_mtime_majority"},
		{"atime==mtime for many", AtimeStats{Files: 1000, EqualMtime: 600}, ext4, Doubtful, "atime_equals_mtime_many"},
		{"atime older than mtime", AtimeStats{Files: 1000, BeforeMtime: 700}, ext4, Doubtful, "atime_before_mtime_majority"},
		{"network fs", AtimeStats{Files: 1000}, &MountInfo{FSType: "nfs4"}, Doubtful, "network_fs"},
		{"tiny sample", AtimeStats{Files: 10}, ext4, Doubtful, "small_sample"},
		{"unknown mount, healthy stats", AtimeStats{Files: 1000, EqualMtime: 10}, nil, Reliable, ""},
	}
	for _, c := range cases {
		q := AssessAtime(c.s, c.mi)
		if q.Verdict != c.verdict {
			t.Errorf("%s: verdict %s, want %s (%v)", c.name, q.Verdict, c.verdict, q.Reasons)
		}
		if c.reason != "" && !slices.Contains(q.Reasons, c.reason) {
			t.Errorf("%s: reasons %v lack %s", c.name, q.Reasons, c.reason)
		}
	}
	// A worse reason must never be hidden by a later milder one.
	q := AssessAtime(AtimeStats{Files: 1000, BeforeMtime: 900},
		&MountInfo{FSType: "ext4", Options: []string{"noatime"}})
	if q.Verdict != Unreliable {
		t.Errorf("verdict downgraded to %s", q.Verdict)
	}
}
