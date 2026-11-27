package service

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"os/user"
	"runtime"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

// The checks in this file are the "Bucket A1" batch — each maps one
// systemd Condition* directive to a small self-contained probe. They
// intentionally share nothing beyond stdlib + golang.org/x/sys/unix.
// Values follow systemd operator-comparison shape where relevant (see
// parseNumericOp) so an operator can write `>= 4` and get what they'd
// expect from `systemd-analyze condition`.

// -------- architecture ---------------------------------------------------

// predicateGOARCH is the architecture the architecture and cpu-feature
// checks answer for. It is runtime.GOARCH outside tests, which set it
// to exercise another architecture's branch on this one.
var predicateGOARCH = runtime.GOARCH

// checkArchitecture compares the param against systemd's name for the
// architecture slinit runs on (architecture_table in systemd's
// architecture.c), so a ConditionArchitecture= value ported from a
// systemd unit means the same thing here.
func checkArchitecture(param string) (bool, string) {
	if archMatches(param) {
		return true, ""
	}
	return false, fmt.Sprintf("architecture: %q != %q", archAlias(predicateGOARCH), strings.TrimSpace(param))
}

// archLegacyNames are the names slinit reported before it used
// systemd's: "x86_64" for x86-64, and the bare GOARCH for the two the
// old table left out. Configs written against them keep matching.
var archLegacyNames = map[string]string{
	"x86_64":  "x86-64",
	"mipsle":  "mips-le",
	"loong64": "loongarch64",
}

// archMatches reports whether param names the current architecture:
// systemd's name, one of slinit's older names, or "native", which
// systemd takes as the architecture it was built for. Used by both
// architecture and the arch-prefix form of cpu-feature (systemd
// d5186757411), as systemd shares condition_test_architecture_parameter
// between them.
func archMatches(param string) bool {
	p := strings.TrimSpace(param)
	if p == "native" {
		return true
	}
	if canon, ok := archLegacyNames[p]; ok {
		p = canon
	}
	return p == archAlias(predicateGOARCH)
}

// archAlias maps a GOARCH to systemd's architecture name.
func archAlias(goarch string) string {
	switch goarch {
	case "386":
		return "x86"
	case "amd64":
		return "x86-64"
	case "ppc64le":
		return "ppc64-le"
	case "mipsle":
		return "mips-le"
	case "mips64le":
		return "mips64-le"
	case "loong64":
		return "loongarch64"
	}
	// arm, arm64, ppc64, s390x, riscv64, mips, mips64: GOARCH and
	// systemd agree.
	return goarch
}

// -------- cpu-feature ----------------------------------------------------

// cpuFeatureAliases are systemd's spellings of two x86 flags that the
// kernel lists in /proc/cpuinfo under another name (systemd reads them
// from CPUID with names of its own, virt.c leaf1_ecx).
var cpuFeatureAliases = map[string]string{
	"pclmul": "pclmulqdq",
	"fma3":   "fma",
}

// checkCPUFeature succeeds when the CPU has the requested feature
// (case-insensitive), as systemd's ConditionCPUFeature=. Common
// examples: avx2, sha_ni, aes.
//
// systemd's has_cpu_with_flag asks the hardware on x86 (CPUID) and on
// arm64 (the AT_HWCAP words) and answers false on every other
// architecture. The kernel names those same bits in /proc/cpuinfo: the
// "flags" line on x86 and the "Features" line on arm64, the latter from
// the same hwcap names systemd's table copies. Reading only "flags"
// made every feature false on arm64.
//
// Also accepts an optional architecture prefix so a service can spell
// "arm64.bti" instead of bare "bti" on mixed-arch fleets where CPU
// feature names can collide across architectures. On a matching arch
// the prefix is stripped and the remaining feature is looked up as
// usual; on any other arch the condition returns false without
// touching /proc/cpuinfo. Mirrors systemd d5186757411 (2026-07-10).
func checkCPUFeature(param string) (bool, string) {
	param = strings.TrimSpace(param)
	if arch, feature, ok := strings.Cut(param, "."); ok {
		if !archMatches(arch) {
			return false, fmt.Sprintf("cpu-feature: arch prefix %q does not match current %q", arch, archAlias(predicateGOARCH))
		}
		param = feature
	}
	want := strings.ToLower(param)
	if want == "" {
		return false, "cpu-feature: empty flag name"
	}
	var key string
	switch predicateGOARCH {
	case "amd64", "386":
		key = "flags"
		if alias, ok := cpuFeatureAliases[want]; ok {
			want = alias
		}
	case "arm64":
		key = "Features"
	default:
		return false, fmt.Sprintf("cpu-feature: no CPU feature list on %s", archAlias(predicateGOARCH))
	}
	data, err := os.ReadFile(hostPath("/proc/cpuinfo"))
	if err != nil {
		return false, fmt.Sprintf("cpu-feature: /proc/cpuinfo: %v", err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		name, val, ok := strings.Cut(line, ":")
		if !ok || strings.TrimSpace(name) != key {
			continue
		}
		for _, f := range strings.Fields(val) {
			if strings.ToLower(f) == want {
				return true, ""
			}
		}
		return false, fmt.Sprintf("cpu-feature: %q not in %s", want, key)
	}
	return false, fmt.Sprintf("cpu-feature: no %s line in /proc/cpuinfo", key)
}

// -------- cpus, memory (numeric OP-value) --------------------------------

// checkCPUs implements ConditionCPUs= — the number of usable CPUs (as
// runtime.NumCPU reports) compared against the operator-prefixed value
// the operator declared (>=, <=, >, <, ==, or none = ==).
func checkCPUs(param string) (bool, string) {
	op, want, err := parseNumericOp(param)
	if err != nil {
		return false, fmt.Sprintf("cpus: %v", err)
	}
	got := int64(runtime.NumCPU())
	if evalNumericOp(got, op, want) {
		return true, ""
	}
	return false, fmt.Sprintf("cpus: got %d, want %s %d", got, op, want)
}

// checkMemory implements ConditionMemory= — total physical memory in
// bytes from /proc/meminfo MemTotal (in kB, converted here). Accepts
// K/M/G/T suffixes on the RHS so an operator can write "2G" rather
// than "2147483648".
func checkMemory(param string) (bool, string) {
	spec := strings.TrimSpace(param)
	// Split leading op if present, then parse RHS with size suffix.
	op, rest := splitOp(spec)
	want, err := parseSize(strings.TrimSpace(rest))
	if err != nil {
		return false, fmt.Sprintf("memory: %v", err)
	}
	got, err := readMemTotalBytes()
	if err != nil {
		return false, fmt.Sprintf("memory: %v", err)
	}
	if evalNumericOp(got, op, want) {
		return true, ""
	}
	return false, fmt.Sprintf("memory: got %d, want %s %d", got, op, want)
}

func readMemTotalBytes() (int64, error) {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if !strings.HasPrefix(sc.Text(), "MemTotal:") {
			continue
		}
		fields := strings.Fields(sc.Text())
		if len(fields) < 2 {
			continue
		}
		n, err := strconv.ParseInt(fields[1], 10, 64)
		if err != nil {
			return 0, fmt.Errorf("MemTotal parse: %w", err)
		}
		return n * 1024, nil // /proc/meminfo reports kB
	}
	return 0, fmt.Errorf("no MemTotal in /proc/meminfo")
}

// -------- kernel-version -------------------------------------------------

// checkKernelVersion implements ConditionKernelVersion= — the running
// kernel's version string (uname -r) compared against a version spec.
// Systemd uses fnmatch on the raw string; we implement string equality
// with an optional leading operator (>=, <=, >, <, ==) that compares
// dotted-decimal components lexicographically-then-numerically.
func checkKernelVersion(param string) (bool, string) {
	spec := strings.TrimSpace(param)
	op, rest := splitOp(spec)
	want := strings.TrimSpace(rest)
	var uts unix.Utsname
	if err := unix.Uname(&uts); err != nil {
		return false, fmt.Sprintf("kernel-version: uname: %v", err)
	}
	got := nulString(uts.Release[:])
	cmp := compareVersion(got, want)
	if evalNumericOp(int64(cmp), op, 0) {
		return true, ""
	}
	return false, fmt.Sprintf("kernel-version: got %q, want %s %q", got, op, want)
}

// -------- kernel-module-loaded ------------------------------------------

// checkKernelModuleLoaded reports whether the named module is loaded,
// as systemd's condition_test_kernel_module_loaded decides it: the
// module has a /sys/module/<name> directory and, when that directory
// has an initstate file, it reads "live". A module built into the
// kernel has the directory (when it has parameters or a version) but no
// initstate, and counts as loaded; /proc/modules, read before, lists
// only modules loaded at runtime, so a built-in one read as missing,
// and it listed a module still running its init ("Loading") as loaded.
// '-' and '_' name the same module, as modprobe has it, and the kernel
// names the directory with '_'.
func checkKernelModuleLoaded(param string) (bool, string) {
	want := strings.TrimSpace(param)
	if want == "" {
		return false, "kernel-module-loaded: empty module name"
	}
	name := strings.ReplaceAll(want, "-", "_")
	// filename_is_valid: no '/', not "." or "..", at most NAME_MAX.
	if strings.ContainsRune(name, '/') || name == "." || name == ".." || len(name) > 255 {
		return false, fmt.Sprintf("kernel-module-loaded: %q is not a module name", want)
	}
	dir := hostPath("/sys/module/" + name)
	st, err := os.Stat(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return false, fmt.Sprintf("kernel-module-loaded: %q not loaded (no /sys/module/%s)", want, name)
		}
		return false, fmt.Sprintf("kernel-module-loaded: %v", err)
	}
	if !st.IsDir() {
		return false, fmt.Sprintf("kernel-module-loaded: /sys/module/%s is not a directory", name)
	}
	data, err := os.ReadFile(dir + "/initstate")
	if os.IsNotExist(err) {
		return true, "" // built in
	}
	if err != nil {
		return false, fmt.Sprintf("kernel-module-loaded: /sys/module/%s/initstate: %v", name, err)
	}
	if state := strings.TrimSpace(string(data)); state != "live" {
		return false, fmt.Sprintf("kernel-module-loaded: %q is %s, not live", want, state)
	}
	return true, ""
}

// -------- os-release ----------------------------------------------------

// checkOSRelease implements ConditionOSRelease=KEY=VALUE — the field
// KEY must be present in /etc/os-release and match VALUE exactly. This
// is the minimal shape; systemd also accepts version-compare syntax on
// VERSION_ID which we don't bother with (an operator can use
// kernel-version + OSRelease=ID= together to gate a service on a
// specific major-release string).
func checkOSRelease(param string) (bool, string) {
	spec := strings.TrimSpace(param)
	key, wantVal, ok := strings.Cut(spec, "=")
	if !ok {
		return false, fmt.Sprintf("os-release: expected KEY=VALUE, got %q", spec)
	}
	key = strings.TrimSpace(key)
	wantVal = strings.TrimSpace(wantVal)
	got, err := readOSReleaseField(key)
	if err != nil {
		return false, fmt.Sprintf("os-release: %v", err)
	}
	if got == wantVal {
		return true, ""
	}
	return false, fmt.Sprintf("os-release: %s=%q, want %q", key, got, wantVal)
}

func readOSReleaseField(key string) (string, error) {
	for _, p := range []string{"/etc/os-release", "/usr/lib/os-release"} {
		v, err := readOSReleaseFile(p, key)
		if err == nil {
			return v, nil
		}
	}
	return "", fmt.Errorf("no %s in /etc/os-release or /usr/lib/os-release", key)
}

func readOSReleaseFile(path, key string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	prefix := key + "="
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, prefix) {
			continue
		}
		v := strings.TrimPrefix(line, prefix)
		// Strip matching quotes, preserving embedded characters.
		if len(v) >= 2 {
			if (v[0] == '"' && v[len(v)-1] == '"') || (v[0] == '\'' && v[len(v)-1] == '\'') {
				v = v[1 : len(v)-1]
			}
		}
		return v, nil
	}
	return "", fmt.Errorf("key %s not present", key)
}

// -------- user, group ---------------------------------------------------

// checkUser accepts either a numeric uid ("uid:1000"), a plain uid
// number ("1000"), or a username ("root"). Compared against the daemon's
// current euid. "@system" matches the system-uid range (uid < 1000)
// per systemd's shorthand.
func checkUser(param string) (bool, string) {
	spec := strings.TrimSpace(param)
	if spec == "@system" {
		if os.Geteuid() < 1000 {
			return true, ""
		}
		return false, fmt.Sprintf("user: euid %d is not a system uid", os.Geteuid())
	}
	if strings.HasPrefix(spec, "uid:") {
		spec = strings.TrimPrefix(spec, "uid:")
	}
	if n, err := strconv.Atoi(spec); err == nil {
		if os.Geteuid() == n {
			return true, ""
		}
		return false, fmt.Sprintf("user: euid %d != %d", os.Geteuid(), n)
	}
	u, err := user.Lookup(spec)
	if err != nil {
		return false, fmt.Sprintf("user: lookup %q: %v", spec, err)
	}
	n, err := strconv.Atoi(u.Uid)
	if err != nil {
		return false, fmt.Sprintf("user: bad uid in /etc/passwd for %q: %v", spec, err)
	}
	if os.Geteuid() == n {
		return true, ""
	}
	return false, fmt.Sprintf("user: euid %d != %d (%s)", os.Geteuid(), n, spec)
}

// checkGroup accepts a numeric gid or a group name. Matches when the
// daemon's egid or any of its supplementary groups equals the target.
// "@system" matches the system-gid range (gid < 1000).
func checkGroup(param string) (bool, string) {
	spec := strings.TrimSpace(param)
	if spec == "@system" {
		if os.Getegid() < 1000 {
			return true, ""
		}
		return false, fmt.Sprintf("group: egid %d is not a system gid", os.Getegid())
	}
	if strings.HasPrefix(spec, "gid:") {
		spec = strings.TrimPrefix(spec, "gid:")
	}
	var want int
	if n, err := strconv.Atoi(spec); err == nil {
		want = n
	} else {
		g, err := user.LookupGroup(spec)
		if err != nil {
			return false, fmt.Sprintf("group: lookup %q: %v", spec, err)
		}
		n, err := strconv.Atoi(g.Gid)
		if err != nil {
			return false, fmt.Sprintf("group: bad gid for %q: %v", spec, err)
		}
		want = n
	}
	if os.Getegid() == want {
		return true, ""
	}
	supp, err := os.Getgroups()
	if err == nil {
		for _, g := range supp {
			if g == want {
				return true, ""
			}
		}
	}
	return false, fmt.Sprintf("group: %d not in egid/supp of daemon", want)
}

// -------- environment ---------------------------------------------------

// checkEnvironment implements ConditionEnvironment=KEY=VALUE against the
// running slinit daemon's env dict (os.Environ()). Also accepts the
// simpler KEY form which matches whenever KEY is set to any value.
func checkEnvironment(param string) (bool, string) {
	spec := strings.TrimSpace(param)
	key, wantVal, hasVal := strings.Cut(spec, "=")
	key = strings.TrimSpace(key)
	if key == "" {
		return false, "environment: empty KEY"
	}
	got, present := os.LookupEnv(key)
	if !present {
		return false, fmt.Sprintf("environment: %s unset", key)
	}
	if !hasVal {
		return true, "" // key alone: any value suffices
	}
	if got == strings.TrimSpace(wantVal) {
		return true, ""
	}
	return false, fmt.Sprintf("environment: %s=%q, want %q", key, got, wantVal)
}

// -------- shared helpers ------------------------------------------------

// splitOp separates a leading comparison operator (>=, <=, ==, >, <, =)
// from the value. The operator defaults to == when absent.
func splitOp(v string) (string, string) {
	v = strings.TrimSpace(v)
	for _, op := range []string{">=", "<=", "==", "=", ">", "<"} {
		if strings.HasPrefix(v, op) {
			return normOp(op), strings.TrimSpace(v[len(op):])
		}
	}
	return "==", v
}

func normOp(op string) string {
	if op == "=" {
		return "=="
	}
	return op
}

// parseNumericOp splits leading op from a bare integer RHS.
func parseNumericOp(v string) (string, int64, error) {
	op, rest := splitOp(v)
	n, err := strconv.ParseInt(strings.TrimSpace(rest), 10, 64)
	if err != nil {
		return "", 0, fmt.Errorf("numeric parse %q: %w", rest, err)
	}
	return op, n, nil
}

func evalNumericOp(got int64, op string, want int64) bool {
	switch op {
	case ">":
		return got > want
	case ">=":
		return got >= want
	case "<":
		return got < want
	case "<=":
		return got <= want
	default: // "==" and anything else defaults to equality
		return got == want
	}
}

// parseSize accepts an integer + optional K/M/G/T suffix (base 1024).
// Empty suffix = bytes.
func parseSize(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty size")
	}
	mult := int64(1)
	last := s[len(s)-1]
	switch last {
	case 'K', 'k':
		mult = 1024
		s = s[:len(s)-1]
	case 'M', 'm':
		mult = 1024 * 1024
		s = s[:len(s)-1]
	case 'G', 'g':
		mult = 1024 * 1024 * 1024
		s = s[:len(s)-1]
	case 'T', 't':
		mult = 1024 * 1024 * 1024 * 1024
		s = s[:len(s)-1]
	}
	n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("size parse: %w", err)
	}
	return n * mult, nil
}

// compareVersion returns -1, 0, +1 for a < b, a == b, a > b when the
// strings are dotted-decimal (kernel-version style: "6.12.82"). Non-
// numeric suffixes are ignored beyond the first dot chain.
func compareVersion(a, b string) int {
	aa := versionParts(a)
	bb := versionParts(b)
	n := len(aa)
	if len(bb) > n {
		n = len(bb)
	}
	for i := 0; i < n; i++ {
		x, y := 0, 0
		if i < len(aa) {
			x = aa[i]
		}
		if i < len(bb) {
			y = bb[i]
		}
		if x != y {
			if x < y {
				return -1
			}
			return 1
		}
	}
	return 0
}

func versionParts(s string) []int {
	// Strip any trailing "-suffix" so "6.12.82-lowlatency-sunlight1"
	// compares the same as "6.12.82".
	if i := strings.IndexAny(s, "-+"); i >= 0 {
		s = s[:i]
	}
	var out []int
	for _, p := range strings.Split(s, ".") {
		n, err := strconv.Atoi(p)
		if err != nil {
			break
		}
		out = append(out, n)
	}
	return out
}

// nulString slices a NUL-terminated byte array (unix.Utsname fields) up
// to the first zero byte and returns it as a Go string.
func nulString(b []byte) string {
	if i := bytes.IndexByte(b, 0); i >= 0 {
		return string(b[:i])
	}
	return string(b)
}
