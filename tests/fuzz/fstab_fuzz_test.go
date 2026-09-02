package fuzz

import (
	"strings"
	"testing"

	"github.com/sunlightlinux/slinit/pkg/fstab"
)

// FuzzFstabParse fuzzes the /etc/fstab parser (pkg/fstab). The parser
// splits on whitespace and applies util-linux-style octal escape
// decoding on the spec (device) and mountpoint fields — the escape
// path is the same shape that broke dinit/systemd historically (see
// systemd eb5ee83c1f fstab-filter escape round-trip).
//
// Invariants:
//   1. Parser must not panic on any input.
//   2. Every parsed Entry must expose accessor methods (Options())
//      without panicking, including on synthesised weird inputs like
//      empty options, all-comma sequences, and embedded escapes.
//   3. FindByFile lookups must be safe on any parse result.
func FuzzFstabParse(f *testing.F) {
	// Realistic fstab seeds.
	f.Add("/dev/sda1 / ext4 defaults 0 1\n")
	f.Add("UUID=deadbeef /home ext4 rw,noatime 0 2\n")
	f.Add("LABEL=root / btrfs subvol=@,compress=zstd:1 0 0\n")
	f.Add("tmpfs /tmp tmpfs nodev,nosuid,size=2G 0 0\n")
	f.Add("proc /proc proc defaults 0 0\n")
	// util-linux octal escapes in mountpoint.
	f.Add("/dev/sdb1 /mnt/my\\040folder ext4 defaults 0 0\n")
	f.Add("/dev/sdc1 /path\\011tab ext4 defaults 0 0\n")
	// Comments + blank lines.
	f.Add("# header\n\n/dev/sda1 / ext4 defaults 0 1\n\n# trailing\n")
	// Four/five/six fields (default freq/passno).
	f.Add("/dev/sda1 / ext4 defaults\n")
	f.Add("/dev/sda1 / ext4 defaults 0\n")
	// Complex real-world mount option strings.
	f.Add("//srv/share /mnt/smb cifs credentials=/etc/creds,uid=1000,gid=1000,iocharset=utf8 0 0\n")
	f.Add("nfs.example:/export /mnt/nfs nfs4 rw,noatime,rsize=1048576,wsize=1048576 0 0\n")
	// Adversarial: many comma-separated options.
	f.Add("/dev/sda1 / ext4 " + strings.Repeat("a,", 500) + "last 0 1\n")
	// Empty.
	f.Add("")
	// Only comments.
	f.Add("# only comments\n# more\n")

	f.Fuzz(func(t *testing.T, data string) {
		entries, err := fstab.Parse(strings.NewReader(data))
		if err != nil {
			return
		}
		// Every parsed entry must survive Options() traversal + lookup.
		for i := range entries {
			opts := entries[i].Options()
			_ = opts
			// FindByFile is a common downstream call; must never panic
			// on any parsed entry set.
			_ = fstab.FindByFile(entries, entries[i].File)
		}
	})
}
