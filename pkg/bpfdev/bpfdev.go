// Package bpfdev builds and attaches a cgroup device filter: an eBPF
// program of type BPF_PROG_TYPE_CGROUP_DEVICE that decides, for every
// open and mknod of a device node by a process in the cgroup, whether
// it is allowed. It is slinit's device-policy / device-allow (systemd's
// DevicePolicy= / DeviceAllow=), and the program has the shape of
// systemd's (src/core/bpf-devices.c), so a unit means the same in both.
//
// The kernel calls the program with the device's type, the access
// asked for (read, write, mknod), its major and minor; it returns 1 to
// allow, 0 to deny. Every rule is a run of compares that jumps to the
// final "allow" when all of them match; falling off the last rule
// denies.
package bpfdev

import (
	"bufio"
	"fmt"
	"io"
	"path"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

// Access bits, as the kernel defines them (BPF_DEVCG_ACC_*).
const (
	AccMknod = unix.BPF_DEVCG_ACC_MKNOD
	AccRead  = unix.BPF_DEVCG_ACC_READ
	AccWrite = unix.BPF_DEVCG_ACC_WRITE
	accAll   = AccMknod | AccRead | AccWrite
)

// Policy is device-policy: auto (no filter unless something is
// allowed, then the pseudo devices too), closed (the pseudo devices
// and what is allowed) or strict (only what is allowed).
type Policy string

const (
	PolicyAuto   Policy = "auto"
	PolicyClosed Policy = "closed"
	PolicyStrict Policy = "strict"
)

// ParsePolicy checks a device-policy value.
func ParsePolicy(s string) (Policy, error) {
	switch p := Policy(strings.TrimSpace(s)); p {
	case PolicyAuto, PolicyClosed, PolicyStrict:
		return p, nil
	}
	return "", fmt.Errorf("device-policy %q: want auto, closed or strict", s)
}

// Allow is one device-allow entry: a device node path (/dev/...,
// /dev/char/M:N, /dev/block/M:N), or char-NAME / block-NAME for every
// device of a driver in /proc/devices (NAME a glob, a major number or
// "*"), and the access granted.
type Allow struct {
	Spec   string
	Access uint32
}

// ParseAllow parses "SPEC [rwm]"; the access defaults to rwm.
func ParseAllow(s string) (Allow, error) {
	f := strings.Fields(s)
	if len(f) == 0 || len(f) > 2 {
		return Allow{}, fmt.Errorf("device-allow %q: want a device and an optional access (r, w, m)", s)
	}
	a := Allow{Spec: f[0], Access: accAll}
	switch {
	case strings.HasPrefix(a.Spec, "/dev/"), strings.HasPrefix(a.Spec, "/run/systemd/inaccessible/"):
		if path.Clean(a.Spec) != a.Spec {
			return Allow{}, fmt.Errorf("device-allow %q: path is not clean", a.Spec)
		}
	case strings.HasPrefix(a.Spec, "char-") && len(a.Spec) > len("char-"),
		strings.HasPrefix(a.Spec, "block-") && len(a.Spec) > len("block-"):
	default:
		return Allow{}, fmt.Errorf("device-allow %q: want a /dev path, char-NAME or block-NAME", a.Spec)
	}
	if len(f) == 2 {
		a.Access = 0
		for _, c := range f[1] {
			switch c {
			case 'r':
				a.Access |= AccRead
			case 'w':
				a.Access |= AccWrite
			case 'm':
				a.Access |= AccMknod
			default:
				return Allow{}, fmt.Errorf("device-allow %q: access %q: want r, w and m only", s, f[1])
			}
		}
	}
	return a, nil
}

// Rule allows one device, a driver's devices or a whole class. Major
// or Minor -1 matches any.
type Rule struct {
	Type   byte // 'c' or 'b'
	Major  int64
	Minor  int64
	Access uint32
}

// Env is what resolving touches outside the process: stat of a device
// node and /proc/devices. Tests give their own.
type Env struct {
	// Stat returns whether the node is a char device, and its major
	// and minor; ok false when it does not exist.
	Stat func(path string) (char bool, major, minor uint32, ok bool, err error)
	// ProcDevices opens /proc/devices.
	ProcDevices func() (io.ReadCloser, error)
}

// pseudo is what auto (with allows) and closed let through besides,
// as systemd's bpf_devices_allow_list_static.
var pseudo = []string{"/dev/null", "/dev/zero", "/dev/full", "/dev/random", "/dev/urandom", "/dev/tty", "/dev/ptmx"}

// Resolve turns the policy and entries into rules, at start time: a
// node's numbers are read when the service starts, as a driver loaded
// since boot gets its major then. filter false means no program at all
// (auto with nothing allowed). An entry naming nothing (a missing node,
// a driver not loaded) is skipped, as systemd skips it, with a note.
func Resolve(policy Policy, allows []Allow, env Env) (rules []Rule, filter bool, notes []string, err error) {
	if policy == "" {
		policy = PolicyAuto
	}
	if policy == PolicyAuto && len(allows) == 0 {
		return nil, false, nil, nil
	}
	if policy == PolicyClosed || policy == PolicyAuto {
		for _, p := range pseudo {
			r, ok, err := nodeRule(p, accAll, env)
			if err != nil {
				return nil, false, nil, err
			}
			if ok {
				rules = append(rules, r)
			}
		}
		// A pty may be read and written, not made.
		rs, err := majorRules('c', "pts", AccRead|AccWrite, env)
		if err != nil {
			return nil, false, nil, err
		}
		rules = append(rules, rs...)
	}
	for _, a := range allows {
		if a.Access == 0 {
			continue
		}
		var rs []Rule
		switch {
		case strings.HasPrefix(a.Spec, "char-"):
			rs, err = majorRules('c', strings.TrimPrefix(a.Spec, "char-"), a.Access, env)
		case strings.HasPrefix(a.Spec, "block-"):
			rs, err = majorRules('b', strings.TrimPrefix(a.Spec, "block-"), a.Access, env)
		default:
			var r Rule
			var ok bool
			r, ok, err = nodeRule(a.Spec, a.Access, env)
			if ok {
				rs = []Rule{r}
			}
		}
		if err != nil {
			return nil, false, nil, err
		}
		if len(rs) == 0 {
			notes = append(notes, fmt.Sprintf("device-allow %s matches no device; skipped", a.Spec))
		}
		rules = append(rules, rs...)
	}
	return rules, true, notes, nil
}

func nodeRule(p string, acc uint32, env Env) (Rule, bool, error) {
	switch p {
	case "/run/systemd/inaccessible/chr":
		return Rule{'c', 0, 0, acc}, true, nil
	case "/run/systemd/inaccessible/blk":
		return Rule{'b', 0, 0, acc}, true, nil
	}
	for _, d := range []struct {
		prefix string
		t      byte
	}{{"/dev/char/", 'c'}, {"/dev/block/", 'b'}} {
		if rest, ok := strings.CutPrefix(p, d.prefix); ok {
			maj, mnr, found := strings.Cut(rest, ":")
			a, err1 := strconv.ParseUint(maj, 10, 32)
			b, err2 := strconv.ParseUint(mnr, 10, 32)
			if found && err1 == nil && err2 == nil {
				return Rule{d.t, int64(a), int64(b), acc}, true, nil
			}
		}
	}
	char, maj, mnr, ok, err := env.Stat(p)
	if err != nil {
		return Rule{}, false, fmt.Errorf("device-allow %s: %w", p, err)
	}
	if !ok {
		return Rule{}, false, nil
	}
	t := byte('b')
	if char {
		t = 'c'
	}
	return Rule{t, int64(maj), int64(mnr), acc}, true, nil
}

// majorRules resolves char-NAME / block-NAME: "*" is the whole class, a
// number that major, anything else a glob over /proc/devices' names.
func majorRules(t byte, name string, acc uint32, env Env) ([]Rule, error) {
	if name == "*" {
		return []Rule{{t, -1, -1, acc}}, nil
	}
	if n, err := strconv.ParseUint(name, 10, 32); err == nil && n > 0 {
		return []Rule{{t, int64(n), -1, acc}}, nil
	}
	f, err := env.ProcDevices()
	if err != nil {
		return nil, fmt.Errorf("device-allow %c %s: /proc/devices: %w", t, name, err)
	}
	defer f.Close()
	header := "Character devices:"
	if t == 'b' {
		header = "Block devices:"
	}
	var rules []Rule
	good := false
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		switch {
		case line == header:
			good = true
			continue
		case line == "" || strings.HasSuffix(line, "devices:"):
			good = false
			continue
		case !good:
			continue
		}
		num, drv, ok := strings.Cut(line, " ")
		n, err := strconv.ParseUint(num, 10, 32)
		if !ok || err != nil || n == 0 {
			continue
		}
		if m, _ := path.Match(name, strings.TrimSpace(drv)); m {
			rules = append(rules, Rule{t, int64(n), -1, acc})
		}
	}
	return rules, sc.Err()
}
