// set.go — setter handlers for `hostname` and the simple machine-info
// keys (icon-name, chassis, deployment, location).
//
// Scope rules (systemd hostnamectl parity):
//   - --transient   : sethostname(2) only, no on-disk change
//   - --static      : /etc/hostname only, no kernel touch
//   - --pretty      : /etc/machine-info PRETTY_HOSTNAME only
//   - no flag       : do all three (kernel + /etc/hostname + pretty),
//                     though PRETTY_HOSTNAME is only touched if the
//                     new name differs from the "auto pretty" that
//                     would be inferred otherwise.
//
// The simple fields (icon-name / chassis / deployment / location)
// only touch /etc/machine-info; no scope flags apply.

package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/sys/unix"
)

// runHostname implements `hostnamectl hostname [NAME]`. No args → print
// the effective name (kernel view). With an arg, set it per scope.
func runHostname(out io.Writer, opts options) error {
	if len(opts.args) == 0 {
		var uname unix.Utsname
		if err := unix.Uname(&uname); err != nil {
			return fmt.Errorf("uname: %w", err)
		}
		fmt.Fprintln(out, cstring(uname.Nodename[:]))
		return nil
	}
	name := opts.args[0]

	sc := opts.scope
	if !sc.any() {
		sc = sc.all()
	}

	// Static and transient names must be valid POSIX hostnames.
	if sc.has(scopeStatic) || sc.has(scopeTransient) {
		if err := validateHostname(name); err != nil {
			return err
		}
	}

	if sc.has(scopeTransient) {
		if err := unix.Sethostname([]byte(name)); err != nil {
			return fmt.Errorf("sethostname: %w", err)
		}
	}
	if sc.has(scopeStatic) {
		if err := writeAtomic(hostnamePath, []byte(name+"\n"), 0o644); err != nil {
			return fmt.Errorf("write %s: %w", hostnamePath, err)
		}
	}
	if sc.has(scopePretty) {
		mi, err := loadMachineInfo()
		if err != nil {
			return err
		}
		mi.set("PRETTY_HOSTNAME", name)
		if err := mi.save(); err != nil {
			return err
		}
	}
	return nil
}

// runSimpleField implements the icon-name / chassis / deployment /
// location subcommands: no arg → print, one arg → set (empty string
// clears the field).
func runSimpleField(out io.Writer, key string, opts options) error {
	mi, err := loadMachineInfo()
	if err != nil {
		return err
	}
	if len(opts.args) == 0 {
		v := mi.get(key)
		// Chassis auto-detects when unset.
		if v == "" && key == "CHASSIS" {
			v = detectChassis()
		}
		fmt.Fprintln(out, v)
		return nil
	}
	value := opts.args[0]
	if key == "CHASSIS" && value != "" {
		if !validChassis(value) {
			return fmt.Errorf("invalid chassis %q (want: desktop|laptop|convertible|server|tablet|handset|watch|embedded|vm|container)", value)
		}
	}
	mi.set(key, value)
	return mi.save()
}

// validateHostname enforces the RFC 1123 subset systemd-hostnamed uses:
// 1..64 chars, alnum + '-' + '.', not starting/ending with '-' or '.',
// no consecutive dots. `localhost` and empty are rejected as well.
func validateHostname(name string) error {
	if name == "" {
		return errors.New("hostname is empty")
	}
	if len(name) > 64 {
		return fmt.Errorf("hostname %q exceeds 64 characters", name)
	}
	if name == "localhost" || strings.HasPrefix(name, "localhost.") {
		return fmt.Errorf("refusing to set hostname to %q", name)
	}
	for i, r := range name {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '-' || r == '.':
			if i == 0 || i == len(name)-1 {
				return fmt.Errorf("hostname %q cannot start or end with %q", name, r)
			}
		default:
			return fmt.Errorf("hostname %q contains invalid character %q", name, r)
		}
	}
	if strings.Contains(name, "..") {
		return fmt.Errorf("hostname %q contains consecutive dots", name)
	}
	return nil
}

// validChassis mirrors systemd's accepted set.
func validChassis(v string) bool {
	switch v {
	case "desktop", "laptop", "convertible", "server", "tablet",
		"handset", "watch", "embedded", "vm", "container":
		return true
	}
	return false
}

// writeAtomic writes buf to path via tmp+rename in the same dir so
// concurrent readers never see a partial file.
func writeAtomic(path string, buf []byte, mode os.FileMode) error {
	dir := "."
	if idx := strings.LastIndex(path, "/"); idx > 0 {
		dir = path[:idx]
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".hostnamectl.")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(buf); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
