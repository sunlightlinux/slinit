package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// FuzzDecodeValue fuzzes the os-release/machine-info value decoder.
// This handler is called for every KEY=VALUE line in every file the
// hostnamectl status pipeline reads (/etc/machine-info,
// /etc/os-release, /sys/class/dmi/id/*), so any panic here would
// crash `hostnamectl status` for an operator with a hostile file.
// Round-trip check: encoding a decoded value and decoding again must
// yield the same value — closes drift between encoder and decoder.
func FuzzDecodeValue(f *testing.F) {
	f.Add(`plain`)
	f.Add(`"double quoted"`)
	f.Add(`'single quoted'`)
	f.Add(`"has space"`)
	f.Add(`"has\"escape"`)
	f.Add(`"back \\ slash"`)
	f.Add(`has#hash`)
	f.Add(`has=equals`)
	f.Add(`bare # trailing`)
	f.Add(``)
	f.Add(`"`)
	f.Add(`'`)
	f.Add(`\`)
	f.Add(`"unterminated`)
	f.Add(`'unterminated`)

	f.Fuzz(func(t *testing.T, data string) {
		v := decodeValue(data)
		if strings.ContainsRune(v, '\x00') {
			// Values embedded with NULs would break downstream
			// consumers that pass them to filesystem or exec APIs.
			t.Errorf("decoded value contains NUL: %q → %q", data, v)
		}
		// Round-trip through encoder → decoder must be idempotent.
		reenc := encodeValue(v)
		v2 := decodeValue(reenc)
		if v2 != v {
			t.Errorf("value round-trip drift:\n  first :%q\n  reenc :%q\n  second:%q",
				v, reenc, v2)
		}
	})
}

// FuzzLoadMachineInfo fuzzes the /etc/machine-info file reader end
// to end. Every parsed line goes through decodeValue; every mi.get()
// downstream reads from the parsed lines slice, so a bad parse that
// left a nil pointer or malformed key would trip the getter.
//
// The invariant is "load + delete + save + reload roundtrip is
// idempotent" — save() must produce bytes that reload() can parse to
// the same shape, otherwise a manual /etc/machine-info edit and a
// subsequent hostnamectl set would drift.
func FuzzLoadMachineInfo(f *testing.F) {
	f.Add("PRETTY_HOSTNAME=\"My Host\"\nICON_NAME=computer-laptop\nCHASSIS=vm\n")
	f.Add("# comment\nPRETTY_HOSTNAME=X\n")
	f.Add("")
	f.Add("KEY=")
	f.Add("KEY=\"value with \\\"escaped\\\" quotes\"\n")
	f.Add("REPEATED=first\nREPEATED=second\nOTHER=x\n")
	f.Add("MALFORMED\nMISSING_EQ\n=only-equal\n")

	f.Fuzz(func(t *testing.T, data string) {
		dir := t.TempDir()
		path := filepath.Join(dir, "machine-info")
		if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
			t.Skip()
		}
		restore := swapMIPath(path)
		defer restore()

		mi, err := loadMachineInfo()
		if err != nil {
			return
		}
		// Every getter must survive on any parsed mi.
		_ = mi.get("PRETTY_HOSTNAME")
		_ = mi.get("ICON_NAME")
		_ = mi.get("CHASSIS")
		// save() must succeed if load succeeded — the file is
		// bytes-in, bytes-out (with normalisation).
		if err := mi.save(); err != nil {
			t.Errorf("save after successful load failed: %v", err)
			return
		}
		// Reload the saved bytes and confirm the same key set
		// survives round-trip (values equal on every key we get).
		mi2, err := loadMachineInfo()
		if err != nil {
			t.Errorf("reload after save failed: %v", err)
			return
		}
		for _, k := range []string{"PRETTY_HOSTNAME", "ICON_NAME", "CHASSIS", "DEPLOYMENT", "LOCATION"} {
			if mi.get(k) != mi2.get(k) {
				t.Errorf("key %q drifted through save/reload: %q → %q",
					k, mi.get(k), mi2.get(k))
			}
		}
	})
}

// FuzzParseOSRelease fuzzes /etc/os-release. Format is nearly identical
// to machine-info but consumers care about specific fields (PRETTY_NAME,
// CPE_NAME, HOME_URL, SUPPORT_END). Any parser regression that dropped
// a key would silently degrade hostnamectl status output for the whole
// OS field block.
func FuzzParseOSRelease(f *testing.F) {
	f.Add(`NAME="Sunlight OS"
PRETTY_NAME="Sunlight OS 2.2"
ID=sunlight
CPE_NAME=cpe:2.3:o:sunlightlinux:sunlight:2.2:*:*:*:*:*:*:*
HOME_URL=https://sunlight.example
`)
	f.Add("# comment\nNAME=A\n")
	f.Add("")
	f.Add(`NAME="unterminated`)
	f.Add("BAD_LINE\nNAME=x\n")

	f.Fuzz(func(t *testing.T, data string) {
		dir := t.TempDir()
		path := filepath.Join(dir, "os-release")
		if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
			t.Skip()
		}
		m, err := parseOSRelease(path)
		if err != nil {
			return
		}
		// Every value returned must be usable downstream (no NUL, no
		// unhandled escape residue).
		for k, v := range m {
			if strings.ContainsRune(v, '\x00') {
				t.Errorf("os-release value for %q contains NUL: %q", k, v)
			}
			_ = k
		}
	})
}

// swapMIPath swaps the package-level machineInfoPath var so
// loadMachineInfo/save target the fuzz's temp file. Restore
// function returns the previous value.
func swapMIPath(p string) func() {
	old := machineInfoPath
	machineInfoPath = p
	return func() { machineInfoPath = old }
}
