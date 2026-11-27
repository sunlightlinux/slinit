package hostname

import (
	"encoding/hex"
	"errors"
	"os"
	"path"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/sys/unix"
)

// More DMI fields, beside the ones the status view reads.
var (
	DMIProductSKUPath      = "/sys/class/dmi/id/product_sku"
	DMIProductVersionPath  = "/sys/class/dmi/id/product_version"
	DMIChassisAssetTagPath = "/sys/class/dmi/id/chassis_asset_tag"
	DMIProductUUIDPath     = "/sys/class/dmi/id/product_uuid"
	DMIProductSerialPath   = "/sys/class/dmi/id/product_serial"
	DMIBoardSerialPath     = "/sys/class/dmi/id/board_serial"
	VSockDevicePath        = "/dev/vsock"
)

// Valid is systemd's hostname_is_valid with no flags: letters, digits,
// '-' and '.', at most 64 bytes, no empty label, no label that starts or
// ends with '-', and no trailing dot. The CLI keeps its own stricter rule
// (it also refuses "localhost"); the D-Bus service answers as systemd
// does.
func Valid(s string) bool {
	if s == "" || len(s) > 64 {
		return false
	}
	dot, hyphen := true, true
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '.':
			if dot || hyphen {
				return false
			}
			dot, hyphen = true, false
		case c == '-':
			if dot {
				return false
			}
			dot, hyphen = false, true
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
			dot, hyphen = false, false
		default:
			return false
		}
	}
	return !dot && !hyphen
}

// SafeString is systemd's string_is_safe for the machine-info values: not
// empty, valid UTF-8, no control characters or newline. With strict, as
// for an icon name, backslashes, quotes, glob characters and '/' are
// refused too, and "." and "..", since the name may end up as a file name.
func SafeString(s string, strict bool) bool {
	if s == "" || !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if r < ' ' || r == 0x7f {
			return false
		}
		if strict && strings.ContainsRune("\\\"'*?[/", r) {
			return false
		}
	}
	return !strict || (s != "." && s != "..")
}

// ValidDeployment accepts letters, digits, '-', '.' and ':', as systemd.
func ValidDeployment(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '.' || r == ':') {
			return false
		}
	}
	return true
}

// ReadStatic returns the static hostname: the first line of /etc/hostname
// that is neither empty nor a comment, normalised by Cleanup. As in
// systemd's read_etc_hostname, a name that is still not Valid reads as
// none, so it is never handed to the kernel.
func ReadStatic() string {
	if l := Cleanup(ReadStaticRaw()); Valid(l) {
		return l
	}
	return ""
}

// ReadStaticRaw returns the first line of /etc/hostname that is neither
// empty nor a comment, as written, before Cleanup.
func ReadStaticRaw() string {
	b, err := os.ReadFile(HostnamePath)
	if err != nil {
		return ""
	}
	for _, l := range strings.Split(string(b), "\n") {
		l = strings.TrimSpace(l)
		if l != "" && !strings.HasPrefix(l, "#") {
			return l
		}
	}
	return ""
}

// Cleanup is systemd's hostname_cleanup: it keeps letters, digits, '-'
// and '.', drops a '.' or '-' that would start a label or follow a dot,
// strips trailing dots and hyphens (so "host.example.com." becomes
// "host.example.com") and cuts the name at 64 bytes. '?' and '$' are kept
// as systemd keeps them for its wildcards, which slinit does not
// substitute, so Valid then refuses the name.
func Cleanup(s string) string {
	d := make([]byte, 0, len(s))
	dot, hyphen := true, true
	for i := 0; i < len(s) && len(d) < 64; i++ {
		c := s[i]
		switch {
		case c == '.':
			if dot || hyphen {
				continue
			}
			d = append(d, c)
			dot, hyphen = true, false
		case c == '-':
			if dot {
				continue
			}
			d = append(d, c)
			dot, hyphen = false, true
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '?', c == '$':
			d = append(d, c)
			dot, hyphen = false, false
		}
	}
	for len(d) > 0 && (d[len(d)-1] == '-' || d[len(d)-1] == '.') {
		d = d[:len(d)-1]
	}
	return string(d)
}

// WriteStatic writes /etc/hostname, or removes it for an empty name.
func WriteStatic(name string) error {
	if name == "" {
		if err := os.Remove(HostnamePath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	return WriteAtomic(HostnamePath, []byte(name+"\n"), 0o644)
}

// KernelHostname is uname's nodename. A variable, like SetKernelHostname,
// so tests can stand in for the kernel.
var KernelHostname = func() string {
	var u unix.Utsname
	if unix.Uname(&u) != nil {
		return ""
	}
	return cstr(u.Nodename[:])
}

// Uname returns the kernel name, release and version.
func Uname() (name, release, version string) {
	var u unix.Utsname
	if unix.Uname(&u) != nil {
		return "", "", ""
	}
	return cstr(u.Sysname[:]), cstr(u.Release[:]), cstr(u.Version[:])
}

func cstr(b []byte) string {
	for i, c := range b {
		if c == 0 {
			return string(b[:i])
		}
	}
	return string(b)
}

// SetKernelHostname sets the kernel's hostname, unless it already is name.
// It reports whether it changed anything.
var SetKernelHostname = func(name string) (bool, error) {
	if KernelHostname() == name {
		return false, nil
	}
	return true, unix.Sethostname([]byte(name))
}

// FallbackHostname is used when os-release names no DEFAULT_HOSTNAME.
var FallbackHostname = "localhost"

// DefaultHostname is os-release's DEFAULT_HOSTNAME when that is a valid
// hostname, else FallbackHostname, as systemd's
// get_default_hostname_or_fallback.
func DefaultHostname() string {
	if m, err := ParseOSRelease(OSReleasePath); err == nil {
		if v := m["DEFAULT_HOSTNAME"]; Valid(v) {
			return v
		}
	}
	return FallbackHostname
}

// DMI reads one DMI field, trimmed; "" when it is missing.
func DMI(path string) string { return ReadTrimmed(path) }

// FirmwareDate parses DMI's bios_date (MM/DD/YYYY) to microseconds since
// the epoch, or 0.
func FirmwareDate() uint64 {
	t, err := time.Parse("01/02/2006", DMI(DMIBiosDatePath))
	if err != nil {
		return 0
	}
	return uint64(t.UnixMicro())
}

// ErrNotAvailable is returned when the firmware does not report a value.
var ErrNotAvailable = errors.New("not available")

// ProductUUID reads the firmware's product UUID as 16 bytes. The file is
// readable by root only.
func ProductUUID() ([]byte, error) {
	s := strings.ReplaceAll(DMI(DMIProductUUIDPath), "-", "")
	b, err := hex.DecodeString(s)
	if err != nil || len(b) != 16 {
		return nil, ErrNotAvailable
	}
	return b, nil
}

// HardwareSerial reads the product serial, or the board serial.
func HardwareSerial() (string, error) {
	for _, p := range []string{DMIProductSerialPath, DMIBoardSerialPath} {
		if v := DMI(p); v != "" {
			return v, nil
		}
	}
	return "", ErrNotAvailable
}

// IDBytes reads a 128-bit id stored as hex (machine-id, boot_id), as the
// 16 bytes systemd's MachineID and BootID properties carry.
func IDBytes(path string) []byte {
	s := strings.ReplaceAll(ReadTrimmed(path), "-", "")
	b, err := hex.DecodeString(s)
	if err != nil || len(b) != 16 {
		return []byte{}
	}
	return b
}

// VSockCID is the machine's AF_VSOCK address, or 0xffffffff
// (VMADDR_CID_ANY) when there is none.
func VSockCID() uint32 {
	f, err := os.Open(VSockDevicePath)
	if err != nil {
		return 0xffffffff
	}
	defer f.Close()
	cid, err := unix.IoctlGetUint32(int(f.Fd()), unix.IOCTL_VM_SOCKETS_GET_LOCAL_CID)
	if err != nil {
		return 0xffffffff
	}
	return cid
}

// MaxTags is systemd's MACHINE_TAGS_MAX.
const MaxTags = 1024

// ValidTag is systemd's machine_tag_is_valid: 1..255 bytes, safe
// characters, no '-', '.' or '=' first, and no '-' or '.' last (before the
// '=' of a key=value tag).
func ValidTag(s string) bool {
	if len(s) == 0 || len(s) >= 256 || strings.ContainsAny(s[:1], "-.=") {
		return false
	}
	key := s
	if i := strings.IndexByte(s, '='); i > 0 {
		key = s[:i]
	}
	if strings.ContainsAny(key[len(key)-1:], "-.") {
		return false
	}
	for _, r := range s {
		if r <= ' ' || r == 0x7f || r == ':' || r == '"' || r == '\'' || r == '\\' {
			return false
		}
	}
	return true
}

func tagKey(t string) string {
	if i := strings.IndexByte(t, '='); i > 0 {
		return t[:i]
	}
	return t
}

// NormalizeTags deduplicates tags by key, the last value winning, and
// sorts them. It fails on an invalid tag or too many.
func NormalizeTags(tags []string) ([]string, error) {
	byKey := map[string]string{}
	for _, t := range tags {
		if !ValidTag(t) {
			return nil, errors.New("invalid tag " + t)
		}
		byKey[tagKey(t)] = t
	}
	if len(byKey) > MaxTags {
		return nil, errors.New("too many tags")
	}
	out := make([]string, 0, len(byKey))
	for _, t := range byKey {
		out = append(out, t)
	}
	sort.Strings(out)
	return out, nil
}

// ParseTags reads TAGS= (colon-separated), dropping invalid tags.
func ParseTags(s string) []string {
	var keep []string
	for _, t := range strings.Split(s, ":") {
		if ValidTag(t) {
			keep = append(keep, t)
		}
	}
	out, _ := NormalizeTags(keep)
	if out == nil {
		return []string{}
	}
	return out
}

// RemoveTags drops the tags whose key matches one of the glob patterns.
func RemoveTags(tags, patterns []string) []string {
	var out []string
	for _, t := range tags {
		drop := false
		for _, p := range patterns {
			if ok, _ := path.Match(p, tagKey(t)); ok {
				drop = true
				break
			}
		}
		if !drop {
			out = append(out, t)
		}
	}
	return out
}
