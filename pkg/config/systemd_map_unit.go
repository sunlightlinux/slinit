package config

import (
	"errors"
	"fmt"
	"math"
	"math/bits"
	"regexp"
	"runtime"
	"strconv"
	"strings"
)

// The [Unit] directives other than the dependencies: the Condition*= and
// Assert*= families, the start rate limit, the emergency actions and the
// job timeout.
//
// Conditions are the bulk. slinit evaluates nearly every systemd
// condition natively (condition-<kind> / assert-<kind>, see
// pkg/service/predicate*.go), but its evaluators were written from the
// documentation's one-line summaries, and several read the value
// differently: ConditionCPUs=4 means ">= 4" to systemd and "== 4" to
// slinit, ConditionMemoryPressure=20% holds when pressure is BELOW 20%
// for systemd and above it for slinit. (Several were brought in line
// with systemd's src/shared/condition.c in 4.1: architecture names,
// cpu-feature on arm64, security=audit and measured-os, virtualization,
// kernel-module-loaded, directory-not-empty, needs-update=DIR and
// path-is-mount-point.) So every kind goes through a conv
// that passes on only the values whose evaluation is the same, rewrites
// the ones it can (an implied operator, a size suffix, an architecture
// name), and reports the rest.
//
// What becomes of a value slinit cannot check depends on the family. A
// condition that is left out lets the service start where systemd would
// have skipped it: that is a WARN, not a refusal, because conditions are
// mostly applicability gates (run the update tool only when /usr
// changed, skip the daemon in a container) and a service that runs
// where it is not needed shows itself, while a unit refused over a
// condition never runs anywhere. An assertion is the author saying that
// starting without the check is an error, so an assertion slinit cannot
// check refuses the unit instead, as the confinement folds do.

// sdPred is one slinit predicate line: its parameter and whether it is
// negated.
type sdPred struct {
	param  string
	negate bool
}

// sdCondKind is one systemd condition type.
type sdCondKind struct {
	// pred is the slinit predicate name, after condition-/assert-.
	pred string
	// path: systemd parses the value as an absolute path
	// (config_parse_unit_condition_path); a relative one is ignored with
	// a warning when the unit is loaded.
	path bool
	// conv turns the value, its "|" and "!" already taken off, into the
	// predicates that check the same thing. nil passes the value on.
	// Several predicates are ANDed, as slinit ANDs separate lines; none
	// at all means the check always holds. An error is a value slinit
	// cannot check the way systemd does.
	conv func(v string, negate bool) ([]sdPred, []SystemdWarning, error)
	// gap is set when slinit has no evaluator that means the same thing.
	gap string
	// noAssert: systemd has no Assert form of this condition.
	noAssert bool
}

// sdAssertUnchecked marks the errors an unchecked assertion adds to
// cfg.errs, so that an empty Assert*= (which empties systemd's list of
// assertions) can take them back.
const sdAssertUnchecked = "is an assertion slinit cannot check"

var sdConditionKinds = map[string]sdCondKind{
	// The six that applyUnitKey mapped by hand before the registry
	// (ConditionPathExists ... ConditionVirtualization). They come
	// through here now as well, so that "|", an empty reset and "$"
	// behave the same for every kind; their bespoke cases are dead.
	"PathExists":        {pred: "path-exists", path: true},
	"PathIsDirectory":   {pred: "path-is-directory", path: true},
	"PathIsMountPoint":  {pred: "path-is-mount-point", path: true},
	"FileNotEmpty":      {pred: "file-not-empty", path: true},
	"KernelCommandLine": {pred: "kernel-command-line"},
	"Virtualization":    {pred: "virtualization", conv: sdCondVirtualization},

	"PathExistsGlob":         {pred: "path-exists-glob", path: true, conv: sdCondGlob},
	"PathIsSymbolicLink":     {pred: "path-is-symbolic-link", path: true},
	"PathIsReadWrite":        {pred: "path-is-read-write", path: true},
	"PathIsSocket":           {pred: "path-is-socket", path: true},
	"FileIsExecutable":       {pred: "file-is-executable", path: true},
	"DirectoryNotEmpty":      {pred: "directory-not-empty", path: true},
	"NeedsUpdate":            {pred: "needs-update", path: true},
	"KernelModuleLoaded":     {pred: "kernel-module-loaded"},
	"FirstBoot":              {pred: "first-boot", conv: sdCondBool},
	"ACPower":                {pred: "ac-power", conv: sdCondBool},
	"Architecture":           {pred: "architecture", conv: sdCondArchitecture},
	"CPUFeature":             {pred: "cpu-feature", conv: sdCondCPUFeature},
	"CPUs":                   {pred: "cpus", conv: sdCondCPUs},
	"Memory":                 {pred: "memory", conv: sdCondMemory},
	"KernelVersion":          {pred: "kernel-version", conv: sdCondVersion},
	"Version":                {pred: "kernel-version", conv: sdCondVersion},
	"OSRelease":              {pred: "os-release", conv: sdCondOSRelease},
	"Host":                   {pred: "host", conv: sdCondHost},
	"MachineTag":             {pred: "machine-tag", conv: sdCondMachineTag},
	"Fraction":               {pred: "fraction", conv: sdCondFraction},
	"Firmware":               {pred: "firmware", conv: sdCondFirmware, noAssert: true},
	"Security":               {pred: "security", conv: sdCondSecurity},
	"User":                   {pred: "user", conv: sdCondUser},
	"Group":                  {pred: "group", conv: sdCondGroup},
	"Environment":            {pred: "environment", conv: sdCondEnvironment},
	"Credential":             {pred: "credential"},
	"ControlGroupController": {pred: "control-group-controller", conv: sdCondCgroupController},

	// No slinit evaluator means the same thing.
	"Capability":      {gap: "slinit has no check of its own capability bounding set"},
	"PathIsEncrypted": {path: true, gap: "slinit has no check for a path on an encrypted block device"},
	"MemoryPressure":  {gap: sdPSIGap},
	"CPUPressure":     {gap: sdPSIGap},
	"IOPressure":      {gap: sdPSIGap},
}

const sdPSIGap = "slinit's pressure checks hold when the 10s 'some' average is AT LEAST the value " +
	"and fail without PSI; systemd's hold when the (default 5min, 'full') average is AT MOST it, " +
	"and pass without PSI"

func init() {
	registerFinisher(sdFinishTriggers)
	for kind, k := range sdConditionKinds {
		k := k
		registerUnit("Condition"+kind, sdRule{custom: func(cfg *SystemdConfig, key, val string) []SystemdWarning {
			return cfg.applyCondition(key, val, k, false)
		}})
		if !k.noAssert {
			registerUnit("Assert"+kind, sdRule{custom: func(cfg *SystemdConfig, key, val string) []SystemdWarning {
				return cfg.applyCondition(key, val, k, true)
			}})
		}
	}

	// The start rate limit. systemd keeps it in [Unit] (the [Service]
	// spellings are older aliases of the same fields).
	registerUnit("StartLimitIntervalSec", sdRule{key: "restart-limit-interval", conv: sdStartLimitInterval})
	registerUnit("StartLimitInterval", sdRule{key: "restart-limit-interval", conv: sdStartLimitInterval})
	registerUnit("StartLimitBurst", sdRule{key: "restart-limit-count", conv: sdStartLimitBurst})
	registerUnit("StartLimitAction", sdRule{key: "start-limit-action", conv: sdEmergencyAction})
	registerUnit("FailureAction", sdRule{key: "failure-action", conv: sdEmergencyAction})
	registerUnit("SuccessAction", sdRule{key: "success-action", conv: sdSuccessAction})
	registerUnit("RebootArgument", sdRule{custom: sdRebootArgument})
	registerUnit("JobTimeoutSec", sdRule{key: "job-timeout-sec", conv: sdJobTimeout})

	sdUnitNoEquivalent["FailureActionExitStatus"] = sdGap{why: "failure-action = exit ends slinit with its own exit status, not a chosen one"}
	sdUnitNoEquivalent["SuccessActionExitStatus"] = sdGap{why: "success-action = exit ends slinit with its own exit status, not a chosen one"}
	sdUnitNoEquivalent["JobRunningTimeoutSec"] = sdGap{why: "slinit has no timeout that starts when the start job begins to run; " +
		"start-timeout bounds the start command, job-timeout-sec the whole start including the wait for dependencies"}
	sdUnitNoEquivalent["JobTimeoutAction"] = sdGap{why: "a job-timeout-sec that fires fails the service, so failure-action is what runs then"}
	sdUnitNoEquivalent["JobTimeoutRebootArgument"] = sdGap{why: "slinit has no job-timeout action to pass it to"}
	sdUnitNoEquivalent["SurviveFinalKillSignal"] = sdGap{why: "slinit's final kill sweep at shutdown spares no process; " +
		"its survive-final-kill-signal setting is something else (it skips the SIGKILL when a stop times out)"}

	sdUnitIgnored["AllowIsolate"] = "slinit has no isolate operation for it to permit"
	sdUnitIgnored["IgnoreOnSnapshot"] = "systemd itself ignores it (snapshot units are gone)"
	sdUnitIgnored["CollectMode"] = "tunes when systemd unloads a unit object; slinit keeps a loaded service's state until it is unloaded"
	sdUnitIgnored["SourcePath"] = "records the file a generator made the unit from, for systemd's own status output"
	sdUnitIgnored["Tags"] = "labels for systemd's own unit queries; nothing in the service depends on them"
}

// applyCondition maps one Condition*= or Assert*= assignment, following
// config_parse_unit_condition_string / _path: an empty value empties the
// list (conditions and assertions are two lists), "|" makes the check a
// triggering one, "!" negates it, and specifiers are expanded.
func (cfg *SystemdConfig) applyCondition(key, val string, k sdCondKind, assert bool) []SystemdWarning {
	v := strings.TrimSpace(trimQuotes(val))
	if v == "" {
		cfg.resetConditions(assert)
		return nil
	}
	// The string form skips blanks after "|" and "!"; the path form
	// does not, and then fails the absolute-path check below, as in
	// systemd.
	skip := func(s string) string {
		if k.path {
			return s
		}
		return strings.TrimLeft(s, " \t\n\r")
	}
	trigger := strings.HasPrefix(v, "|")
	if trigger {
		v = skip(v[1:])
	}
	negate := strings.HasPrefix(v, "!")
	if negate {
		v = skip(v[1:])
	}
	// The four path conditions in specifierKeys were expanded by
	// parseSystemdUnit already; expanding twice would turn "%%n" into
	// the name.
	if !specifierKeys[key] {
		exp, w := expandSpecifiers(cfg, key, v)
		if len(w) > 0 {
			const why = "it uses a specifier slinit cannot expand"
			if trigger {
				// It still counts among the unit's triggering
				// checks, so that another is not taken for the
				// only one.
				st := sdUnitStateOf(cfg)
				st.triggers = append(st.triggers, sdTrigger{key: key, val: val, k: k, assert: assert, unchecked: why})
				return nil
			}
			return cfg.conditionUnchecked(key, val, assert, why)
		}
		v = exp
	}
	if k.path && !strings.HasPrefix(v, "/") {
		// systemd drops it when it loads the unit, condition or
		// assertion alike, so dropping it here is what systemd does.
		return []SystemdWarning{{"WARN", fmt.Sprintf(
			"[Unit] %s=%s ignored: the path must be absolute, and systemd ignores this assignment too", key, val)}}
	}
	if trigger {
		// Whether slinit can check it depends on the other
		// triggering checks of the unit, drop-ins included:
		// sdFinishTriggers decides.
		st := sdUnitStateOf(cfg)
		st.triggers = append(st.triggers, sdTrigger{key: key, val: val, k: k, assert: assert, v: v, negate: negate})
		return nil
	}
	return cfg.emitCondition(key, val, k, assert, v, negate, false)
}

// emitCondition maps one check, its prefixes taken off and its value
// expanded, onto slinit's predicates; trigger keeps it triggering ("|").
func (cfg *SystemdConfig) emitCondition(key, val string, k sdCondKind, assert bool, v string, negate, trigger bool) []SystemdWarning {
	if v == "" {
		return cfg.conditionUnchecked(key, val, assert, "nothing follows the prefix")
	}
	if k.gap != "" {
		return cfg.conditionUnchecked(key, val, assert, k.gap)
	}
	preds := []sdPred{{v, negate}}
	var warns []SystemdWarning
	if k.conv != nil {
		var err error
		preds, warns, err = k.conv(v, negate)
		if err != nil {
			return append(warns, cfg.conditionUnchecked(key, val, assert, err.Error())...)
		}
	}
	if trigger && len(preds) > 1 {
		// Several slinit checks that must all hold: as triggers they
		// would be alternatives instead.
		return append(warns, cfg.conditionUnchecked(key, val, assert,
			"it maps onto several slinit checks, which cannot together be one triggering (|) check")...)
	}
	prefix := "condition-"
	if assert {
		prefix = "assert-"
	}
	for _, p := range preds {
		// slinit expands $VAR in a predicate's value; systemd does not.
		param := strings.ReplaceAll(p.param, "$", "$$")
		if p.negate {
			param = "!" + param
		}
		if trigger {
			param = "|" + param
		}
		cfg.conditions = append(cfg.conditions, condDir{prefix + k.pred, param})
	}
	return warns
}

// sdUnitState is the [Unit] state sdFinishTriggers reads.
type sdUnitState struct {
	triggers []sdTrigger
}

// sdTrigger is one triggering ("|") check, read up to its value.
type sdTrigger struct {
	key, val  string
	k         sdCondKind
	assert    bool
	v         string // expanded, "|" and "!" taken off; "" when unexpandable
	negate    bool
	unchecked string // why slinit cannot check it whatever the others say
}

func sdUnitStateOf(cfg *SystemdConfig) *sdUnitState {
	return sdState(cfg, "unit", func() *sdUnitState { return &sdUnitState{} })
}

// sdFinishTriggers maps the triggering checks. systemd requires every
// plain check of a list (conditions, assertions) to hold, and, when the
// list has triggering ones, at least one of those
// (condition_test_list); slinit's "|" means the same (since 4.1). A
// list with one slinit cannot check is left out whole: mapping the rest
// would make "at least one of them" stricter than systemd's, and the
// service could be skipped where systemd runs it.
func sdFinishTriggers(cfg *SystemdConfig) []SystemdWarning {
	var warns []SystemdWarning
	st := sdUnitStateOf(cfg)
	for _, assert := range []bool{false, true} {
		var list []sdTrigger
		for _, t := range st.triggers {
			if t.assert == assert {
				list = append(list, t)
			}
		}
		var bad *sdTrigger
		for i := range list {
			if list[i].unchecked != "" || list[i].k.gap != "" {
				bad = &list[i]
				break
			}
		}
		for _, t := range list {
			switch {
			case t.unchecked != "":
				warns = append(warns, cfg.conditionUnchecked(t.key, t.val, assert, t.unchecked)...)
			case t.k.gap != "":
				warns = append(warns, cfg.conditionUnchecked(t.key, t.val, assert, t.k.gap)...)
			case bad != nil:
				warns = append(warns, cfg.conditionUnchecked(t.key, t.val, assert, fmt.Sprintf(
					"another triggering (|) check of the unit, %s=%s, cannot be checked, and without it "+
						"\"at least one of them\" would be stricter than systemd's", bad.key, bad.val))...)
			default:
				warns = append(warns, cfg.emitCondition(t.key, t.val, t.k, assert, t.v, t.negate, len(list) > 1)...)
			}
		}
	}
	return warns
}

// conditionUnchecked reports a check slinit cannot make: a condition is
// left out with a WARN, an assertion refuses the unit (see the top of
// this file for why they differ).
func (cfg *SystemdConfig) conditionUnchecked(key, val string, assert bool, why string) []SystemdWarning {
	if assert {
		cfg.errs = append(cfg.errs, fmt.Sprintf(
			"%s=%s %s (%s), and left out the service could start where systemd fails it",
			key, val, sdAssertUnchecked, why))
		return nil
	}
	return []SystemdWarning{{"WARN", fmt.Sprintf(
		"[Unit] %s=%s ignored: %s; the service starts without this check, where systemd skips it when the check fails",
		key, val, why)}}
}

// resetConditions is an empty Condition*= (assert false) or Assert*=
// (assert true): systemd empties that whole list, whatever the kinds in
// it. Requisite='s assert-service-started is a dependency, not one of
// the unit's assertions, and stays.
func (cfg *SystemdConfig) resetConditions(assert bool) {
	kept := cfg.conditions[:0]
	for _, c := range cfg.conditions {
		var drop bool
		if assert {
			drop = strings.HasPrefix(c.name, "assert-") && c.name != "assert-service-started"
		} else {
			drop = strings.HasPrefix(c.name, "condition-")
		}
		if !drop {
			kept = append(kept, c)
		}
	}
	cfg.conditions = kept
	st := sdUnitStateOf(cfg)
	keptT := st.triggers[:0]
	for _, t := range st.triggers {
		if t.assert != assert {
			keptT = append(keptT, t)
		}
	}
	st.triggers = keptT
	if assert {
		errs := cfg.errs[:0]
		for _, e := range cfg.errs {
			if !strings.Contains(e, sdAssertUnchecked) {
				errs = append(errs, e)
			}
		}
		cfg.errs = errs
	}
}

// --- per-kind value conversions ---

func sdOne(v string, negate bool) []sdPred { return []sdPred{{v, negate}} }

// ucParseBool is systemd's parse_boolean.
func ucParseBool(v string) (bool, bool) {
	switch strings.ToLower(v) {
	case "1", "yes", "y", "true", "t", "on":
		return true, true
	case "0", "no", "n", "false", "f", "off":
		return false, true
	}
	return false, false
}

// sdCondBool: FirstBoot= and ACPower= take any systemd boolean; slinit's
// checks take yes or no.
func sdCondBool(v string, negate bool) ([]sdPred, []SystemdWarning, error) {
	b, ok := ucParseBool(v)
	if !ok {
		return nil, nil, fmt.Errorf("%q is not a boolean", v)
	}
	return sdOne(boolWord(strconv.FormatBool(b)), negate), nil, nil
}

// sdCondVirtualization: systemd compares "yes"/"no" (any boolean), the
// categories "vm" and "container", "private-users", or one detector id.
// slinit's detector is a port of systemd's (pkg/service predicate_virt.go)
// and takes all of them, but its "qemu" also matches a KVM guest whose
// DMI says QEMU, as slinit's detector before 4.1 reported KVM guests,
// and systemd's does not: "qemu" is the one id left unchecked.
var sdVirtIDs = map[string]bool{
	"vm": true, "container": true, "private-users": true,
	"kvm": true, "amazon": true, "bochs": true, "xen": true, "uml": true, "vmware": true,
	"oracle": true, "microsoft": true, "zvm": true, "parallels": true, "bhyve": true, "qnx": true,
	"acrn": true, "powervm": true, "apple": true, "sre": true, "google": true, "vm-other": true,
	"systemd-nspawn": true, "lxc-libvirt": true, "lxc": true, "openvz": true, "docker": true,
	"podman": true, "rkt": true, "wsl": true, "proot": true, "pouch": true, "container-other": true,
}

func sdCondVirtualization(v string, negate bool) ([]sdPred, []SystemdWarning, error) {
	if b, ok := ucParseBool(v); ok {
		return sdOne(boolWord(strconv.FormatBool(b)), negate), nil, nil
	}
	if sdVirtIDs[v] {
		return sdOne(v, negate), nil, nil
	}
	if v == "qemu" {
		return nil, nil, errors.New("slinit's virtualization check also takes a KVM guest that DMI calls QEMU for \"qemu\", where systemd says \"kvm\"")
	}
	return nil, nil, fmt.Errorf("slinit's virtualization check cannot tell %q the way systemd does", v)
}

// sdCondGlob: systemd's glob(3) takes braces and "[!...]"; Go's does
// neither, and its "*" also matches a leading dot, which glob(3) does
// not.
var sdGlobLeadingWild = regexp.MustCompile(`(^|/)[*?\[]`)

func sdCondGlob(v string, negate bool) ([]sdPred, []SystemdWarning, error) {
	if strings.Contains(v, "{") || strings.Contains(v, "[!") {
		return nil, nil, errors.New("slinit's glob has no {a,b} or [!...] forms")
	}
	var warns []SystemdWarning
	if sdGlobLeadingWild.MatchString(v) {
		warns = append(warns, SystemdWarning{"NOTE", fmt.Sprintf(
			"path-exists-glob %s: slinit's wildcard also matches names that begin with a dot, which systemd's does not", v)})
	}
	return sdOne(v, negate), warns, nil
}

// sdArchNames are systemd's architecture names, mapped to the names
// slinit's check compares (pkg/service archAlias). A name for an
// architecture slinit cannot run on maps to itself and never matches,
// as in systemd.
var sdArchNames = map[string]string{
	"x86-64": "x86_64", "mips-le": "mipsle", "loongarch64": "loong64",
	"x86": "x86", "arm": "arm", "arm64": "arm64", "arm64-be": "arm64-be", "arm-be": "arm-be",
	"alpha": "alpha", "arc": "arc", "arc-be": "arc-be", "cris": "cris", "ia64": "ia64",
	"m68k": "m68k", "mips64-le": "mips64-le", "mips64": "mips64", "mips": "mips", "nios2": "nios2",
	"parisc64": "parisc64", "parisc": "parisc", "ppc64-le": "ppc64-le", "ppc64": "ppc64",
	"ppc": "ppc", "ppc-le": "ppc-le", "riscv32": "riscv32", "riscv64": "riscv64",
	"s390x": "s390x", "s390": "s390", "sh64": "sh64", "sh": "sh", "sparc64": "sparc64",
	"sparc": "sparc", "tilegx": "tilegx",
}

func sdCondArchitecture(v string, negate bool) ([]sdPred, []SystemdWarning, error) {
	if v == "native" {
		// The architecture slinit was built for, which is the one its
		// check compares: it always holds.
		if negate {
			return nil, nil, errors.New("!native never holds, and slinit has no check that always fails")
		}
		return nil, nil, nil
	}
	a, ok := sdArchNames[v]
	if !ok {
		return nil, nil, fmt.Errorf("%q is not an architecture systemd knows", v)
	}
	return sdOne(a, negate), nil, nil
}

// sdCPUFeatures are the x86 flags systemd's ConditionCPUFeature= knows
// (has_cpu_with_flag reads them from CPUID), under the name
// /proc/cpuinfo gives them, which is what slinit reads. systemd's other
// names are false on every machine. osxsave is left out: the kernel
// does not list it.
var sdCPUFeatures = map[string]string{
	"fpu": "fpu", "vme": "vme", "de": "de", "pse": "pse", "tsc": "tsc", "msr": "msr", "pae": "pae",
	"mce": "mce", "cx8": "cx8", "apic": "apic", "sep": "sep", "mtrr": "mtrr", "pge": "pge",
	"mca": "mca", "cmov": "cmov", "pat": "pat", "pse36": "pse36", "clflush": "clflush", "mmx": "mmx",
	"fxsr": "fxsr", "sse": "sse", "sse2": "sse2", "ht": "ht",
	"pni": "pni", "pclmul": "pclmulqdq", "monitor": "monitor", "ssse3": "ssse3", "fma3": "fma",
	"cx16": "cx16", "sse4_1": "sse4_1", "sse4_2": "sse4_2", "movbe": "movbe", "popcnt": "popcnt",
	"aes": "aes", "xsave": "xsave", "avx": "avx", "f16c": "f16c", "rdrand": "rdrand",
	"bmi1": "bmi1", "avx2": "avx2", "bmi2": "bmi2", "rdseed": "rdseed", "adx": "adx", "sha_ni": "sha_ni",
	"syscall": "syscall", "rdtscp": "rdtscp", "lm": "lm", "lahf_lm": "lahf_lm", "abm": "abm",
	"constant_tsc": "constant_tsc",
}

// sdARM64Features are the arm64 hardware capabilities systemd's
// has_cpu_with_flag knows (AT_HWCAP, AT_HWCAP2, AT_HWCAP3; virt.c
// hwcap[], hwcap2[], hwcap3[]). Its names are the kernel's, which the
// "Features" line of /proc/cpuinfo prints for the same bits, so slinit
// reads the same answer there.
var sdARM64Features = map[string]bool{
	"fp": true, "asimd": true, "evtstrm": true, "aes": true, "pmull": true, "sha1": true,
	"sha2": true, "crc32": true, "atomics": true, "fphp": true, "asimdhp": true, "cpuid": true,
	"asimdrdm": true, "jscvt": true, "fcma": true, "lrcpc": true, "dcpop": true, "sha3": true,
	"sm3": true, "sm4": true, "asimddp": true, "sha512": true, "sve": true, "asimdfhm": true,
	"dit": true, "uscat": true, "ilrcpc": true, "flagm": true, "ssbs": true, "sb": true, "paca": true,
	"pacg": true, "gcs": true, "cmpbr": true, "fprcvt": true, "f8mm8": true, "f8mm4": true,
	"svef16mm": true, "sveeltperm": true, "sveaes2": true, "svebfscale": true, "sve2p2": true,
	"sme2p2": true, "smesbitperm": true, "smeaes": true, "smesfexpa": true, "smestmop": true,
	"smesmop4": true, "dcpodp": true, "sve2": true, "sveaes": true, "svepmull": true,
	"svebitperm": true, "svesha3": true, "svesm4": true, "flagm2": true, "frint": true,
	"svei8mm": true, "svef32mm": true, "svef64mm": true, "svebf16": true, "i8mm": true, "bf16": true,
	"dgh": true, "rng": true, "bti": true, "mte": true, "ecv": true, "afp": true, "rpres": true,
	"mte3": true, "sme": true, "smei16i64": true, "smef64f64": true, "smei8i32": true,
	"smef16f32": true, "smeb16f32": true, "smef32f32": true, "smefa64": true, "wfxt": true,
	"ebf16": true, "sveebf16": true, "cssc": true, "rprfm": true, "sve2p1": true, "sme2": true,
	"sme2p1": true, "smei16i32": true, "smebi32i32": true, "smeb16b16": true, "smef16f16": true,
	"mops": true, "hbc": true, "sveb16b16": true, "lrcpc3": true, "lse128": true, "fpmr": true,
	"lut": true, "faminmax": true, "f8cvt": true, "f8fma": true, "f8dp4": true, "f8dp2": true,
	"f8e4m3": true, "f8e5m2": true, "smelutv2": true, "smef8f16": true, "smef8f32": true,
	"smesf8fma": true, "smesf8dp4": true, "smesf8dp2": true, "poe": true, "mtefar": true,
	"mtestoreonly": true, "lsfe": true, "ls64": true, "sveb16mm": true, "sve2p3": true,
	"smelut6": true, "sme2p3": true, "f16mm": true, "f16f32dot": true, "f16f32mm": true,
	"svelut6": true,
}

func sdCondCPUFeature(v string, negate bool) ([]sdPred, []SystemdWarning, error) {
	arch, feature, prefixed := strings.Cut(v, ".")
	if !prefixed {
		feature = v
		// Without a prefix the feature is looked up on the machine the
		// service runs on, taken to be this one.
		arch = map[string]string{"amd64": "x86-64", "386": "x86", "arm64": "arm64"}[runtime.GOARCH]
		if arch == "" {
			return nil, nil, errors.New("slinit's cpu-feature reads the x86 and arm64 CPU features only")
		}
	}
	lower := strings.ToLower(feature)
	var f string
	switch arch {
	case "x86", "x86-64":
		var ok bool
		if f, ok = sdCPUFeatures[lower]; !ok {
			return nil, nil, fmt.Errorf("systemd does not know the CPU feature %q", feature)
		}
	case "arm64":
		if !sdARM64Features[lower] {
			return nil, nil, fmt.Errorf("systemd does not know the CPU feature %q", feature)
		}
		f = lower
	default:
		return nil, nil, errors.New("slinit's cpu-feature reads the x86 and arm64 CPU features only")
	}
	if prefixed {
		f = sdArchNames[arch] + "." + f
	}
	return sdOne(f, negate), nil, nil
}

// sdOrderOp splits the comparison operator off a ConditionCPUs= or
// ConditionMemory= value, as parse_compare_operator with no flags does,
// and returns slinit's spelling. unequal is "!=" or "<>", which slinit
// has no operator for: it is "==" negated. ok is false without one.
func sdOrderOp(v string) (op, rest string, unequal, ok bool) {
	for _, o := range []struct{ sd, sl string }{
		{"<>", "=="}, {"<=", "<="}, {">=", ">="}, {"<", "<"}, {">", ">"},
		{"==", "=="}, {"=", "=="}, {"!=", "=="},
	} {
		if strings.HasPrefix(v, o.sd) {
			return o.sl, strings.TrimLeft(v[len(o.sd):], " \t"), o.sd == "<>" || o.sd == "!=", true
		}
	}
	return "", v, false, false
}

// sdCondCPUs: systemd compares with ">=" when no operator is given;
// slinit with "==", so the operator is always written out.
func sdCondCPUs(v string, negate bool) ([]sdPred, []SystemdWarning, error) {
	op, rest, unequal, ok := sdOrderOp(v)
	if !ok {
		op = ">="
	}
	n, err := strconv.ParseUint(rest, 10, 31)
	if err != nil {
		return nil, nil, fmt.Errorf("%q is not a number of CPUs", rest)
	}
	return sdOne(op+strconv.FormatUint(n, 10), negate != unequal), nil, nil
}

// sdCondMemory: as CPUs=, with a size in systemd's parse_size form
// (base 1024, "1.5G"), written out in bytes for slinit.
func sdCondMemory(v string, negate bool) ([]sdPred, []SystemdWarning, error) {
	op, rest, unequal, ok := sdOrderOp(v)
	if !ok {
		op = ">="
	}
	n, err := ucParseSize(rest)
	if err != nil {
		return nil, nil, err
	}
	return sdOne(op+strconv.FormatUint(n, 10), negate != unequal), nil, nil
}

// ucParseSize is systemd's parse_size(t, 1024) for one number with an
// optional fraction and suffix. A sum ("1G 512M") is not taken.
func ucParseSize(s string) (uint64, error) {
	s = strings.TrimSpace(s)
	i := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	if i == 0 {
		return 0, fmt.Errorf("%q is not a size", s)
	}
	whole, err := strconv.ParseUint(s[:i], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%q is not a size", s)
	}
	frac := ""
	if i < len(s) && s[i] == '.' {
		j := i + 1
		for j < len(s) && s[j] >= '0' && s[j] <= '9' {
			j++
		}
		frac, i = s[i+1:j], j
	}
	mult := uint64(1)
	switch strings.TrimSpace(s[i:]) {
	case "", "B":
	case "K":
		mult = 1 << 10
	case "M":
		mult = 1 << 20
	case "G":
		mult = 1 << 30
	case "T":
		mult = 1 << 40
	case "P":
		mult = 1 << 50
	case "E":
		mult = 1 << 60
	default:
		return 0, fmt.Errorf("%q is not a size", s)
	}
	if whole > math.MaxInt64/mult {
		return 0, fmt.Errorf("size %q is too large", s)
	}
	n := whole * mult
	if frac != "" {
		// frac * mult / 10^digits, as parse_size does, without the
		// rounding a digit-by-digit mult/10 would add for 1024-based
		// factors. 18 digits keep 10^digits in a uint64.
		if len(frac) > 18 {
			frac = frac[:18]
		}
		f, _ := strconv.ParseUint(frac, 10, 64)
		scale := uint64(1)
		for range frac {
			scale *= 10
		}
		hi, lo := bits.Mul64(f, mult)
		q, _ := bits.Div64(hi, lo, scale)
		n += q
	}
	if n > math.MaxInt64 {
		return 0, fmt.Errorf("size %q is too large", s)
	}
	return n, nil
}

// sdCondVersion is ConditionKernelVersion= and ConditionVersion=, which
// are one condition in systemd (condition_test_version): an optional
// "kernel", "systemd" or "glibc" word, then one or more "OP VERSION"
// expressions that must all hold, compared with strverscmp_improved, or
// a glob matched against the whole release string.
//
// slinit compares only the leading numbers of the release ("6.12.0" of
// "6.12.0-1-default"), padding the shorter with zeros. For ">=" and "<"
// against a version of at most three numbers that gives systemd's answer
// for every kernel release, which has three; for ">" and "<=" it does
// not: "6.12.0-1" is newer than "6.12.0" to systemd and equal to slinit.
// "=" and "!=" compare whole strings in systemd.
var sdNumericVersion = regexp.MustCompile(`^[0-9]+(\.[0-9]+){0,2}$`)

func sdCondVersion(v string, negate bool) ([]sdPred, []SystemdWarning, error) {
	if strings.ContainsAny(v, "'\"\\") {
		return nil, nil, errors.New("slinit's kernel-version takes no quoting")
	}
	word := v
	if i := strings.IndexAny(v, "!<=>$ \t"); i >= 0 {
		word = v[:i]
	}
	switch word {
	case "systemd", "glibc":
		return nil, nil, fmt.Errorf("slinit has no %s version to compare", word)
	case "kernel":
		v = v[len(word):]
	}
	fields := strings.Fields(v)
	var preds []sdPred
	for i := 0; i < len(fields); i++ {
		f := fields[i]
		var op string
		for _, o := range []string{"$=", "!$=", "<>", "<=", ">=", "<", ">", "==", "=", "!="} {
			if strings.HasPrefix(f, o) {
				op, f = o, f[len(o):]
				break
			}
		}
		if op == "" {
			return nil, nil, errors.New("a version with no operator is a glob match on the release, which slinit cannot do")
		}
		if f == "" {
			// systemd allows a blank after the first operator only.
			if i != 0 || i+1 == len(fields) {
				return nil, nil, fmt.Errorf("%q has no version after %s", v, op)
			}
			i++
			f = fields[i]
		}
		if op != ">=" && op != "<" {
			return nil, nil, fmt.Errorf("slinit compares versions as systemd does only with >= and <, not %s", op)
		}
		if !sdNumericVersion.MatchString(f) {
			return nil, nil, fmt.Errorf("slinit compares only versions of up to three numbers, not %q", f)
		}
		preds = append(preds, sdPred{op + f, negate})
	}
	if len(preds) == 0 {
		if negate {
			return nil, nil, errors.New("an empty version check always holds, and slinit cannot negate that")
		}
		return nil, nil, nil
	}
	if negate && len(preds) > 1 {
		return nil, nil, errors.New("slinit cannot negate several version checks together")
	}
	return preds, nil, nil
}

// sdCondOSRelease: systemd takes blank-separated KEY OP VALUE words that
// must all hold, with the version and glob operators too; slinit takes
// one KEY=VALUE, compared as a string, which is systemd's "=". Its "!="
// is that negated: a key missing from os-release is "" to systemd, and
// to slinit a failed check, which the negation turns the same way.
var sdEnvName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func sdCondOSRelease(v string, negate bool) ([]sdPred, []SystemdWarning, error) {
	if strings.ContainsAny(v, "'\"\\") {
		return nil, nil, errors.New("slinit's os-release check takes no quoting")
	}
	words := strings.Fields(v)
	if negate && len(words) > 1 {
		return nil, nil, errors.New("slinit cannot negate several os-release checks together")
	}
	var preds []sdPred
	for _, w := range words {
		i := strings.IndexAny(w, "!<=>$")
		if i <= 0 || !sdEnvName.MatchString(w[:i]) {
			return nil, nil, fmt.Errorf("%q is not KEY=VALUE", w)
		}
		key, rest := w[:i], w[i:]
		neg := negate
		switch {
		case strings.HasPrefix(rest, "!=") && !strings.HasPrefix(rest, "!$="):
			rest, neg = rest[2:], !negate
		case strings.HasPrefix(rest, "=") && !strings.HasPrefix(rest, "=="):
			rest = rest[1:]
		default:
			return nil, nil, fmt.Errorf("slinit's os-release check compares only with = and !=, as in %q", w)
		}
		if rest == "" {
			return nil, nil, fmt.Errorf("%q has no value", w)
		}
		preds = append(preds, sdPred{key + "=" + rest, neg})
	}
	return preds, nil, nil
}

// sdIsID128 reports a value systemd's ConditionHost= first compares with
// the machine ID, boot ID and product UUID.
var sdID128 = regexp.MustCompile(`^([0-9a-fA-F]{32}|[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12})$`)

// sdCondHost: systemd matches the hostname as a case-insensitive glob,
// or an ID; slinit compares the hostname, case-insensitively, as a
// plain string.
func sdCondHost(v string, negate bool) ([]sdPred, []SystemdWarning, error) {
	if strings.ContainsAny(v, "*?[") {
		return nil, nil, errors.New("slinit's host check takes no wildcards")
	}
	if sdID128.MatchString(v) {
		return nil, nil, errors.New("slinit's host check does not compare the machine ID, boot ID or product UUID")
	}
	return sdOne(v, negate), nil, nil
}

// sdCondMachineTag: a glob against the TAGS= of machine-info in systemd,
// an exact tag in slinit.
func sdCondMachineTag(v string, negate bool) ([]sdPred, []SystemdWarning, error) {
	if strings.ContainsAny(v, "*?[") {
		return nil, nil, errors.New("slinit's machine-tag check takes no wildcards")
	}
	return sdOne(v, negate), nil, nil
}

// sdCondFraction: systemd's "[TAG ]PERCENT" (the percent with its "%",
// or in ‰ or ‱) becomes slinit's "TAG:PERCENT".
func sdCondFraction(v string, negate bool) ([]sdPred, []SystemdWarning, error) {
	f := strings.Fields(v)
	var tag, pct string
	switch len(f) {
	case 1:
		pct = f[0]
	case 2:
		tag, pct = f[0], f[1]
	default:
		return nil, nil, fmt.Errorf("%q is not [TAG ]PERCENT", v)
	}
	if strings.Contains(tag, ":") {
		return nil, nil, errors.New("slinit's fraction takes no ':' in the tag")
	}
	pm, err := sdParsePermyriad(pct)
	if err != nil {
		return nil, nil, err
	}
	p := strconv.FormatFloat(float64(pm)/100, 'f', -1, 64)
	return sdOne(tag+":"+p, negate), []SystemdWarning{{"NOTE", fmt.Sprintf(
		"Fraction=%s: slinit picks its share of machines with a hash of its own, "+
			"so on a mixed fleet it is not the same machines systemd picks", v)}}, nil
}

// sdParsePermyriad is systemd's parse_permyriad: "N%" with up to two
// decimals, "N‰" with one, "N‱".
func sdParsePermyriad(s string) (int, error) {
	var unit, places int
	switch {
	case strings.HasSuffix(s, "‱"):
		s, unit, places = strings.TrimSuffix(s, "‱"), 1, 0
	case strings.HasSuffix(s, "‰"):
		s, unit, places = strings.TrimSuffix(s, "‰"), 10, 1
	case strings.HasSuffix(s, "%"):
		s, unit, places = strings.TrimSuffix(s, "%"), 100, 2
	default:
		return 0, fmt.Errorf("%q is not a percentage", s)
	}
	whole, frac, _ := strings.Cut(s, ".")
	if whole == "" || len(frac) > places || strings.Contains(s, ".") && frac == "" {
		return 0, fmt.Errorf("%q is not a percentage", s)
	}
	n, err := strconv.Atoi(whole)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("%q is not a percentage", s)
	}
	v := n * unit
	for k, m := 0, unit/10; k < len(frac); k, m = k+1, m/10 {
		if frac[k] < '0' || frac[k] > '9' {
			return 0, fmt.Errorf("%q is not a percentage", s)
		}
		v += int(frac[k]-'0') * m
	}
	if v > 10000 {
		return 0, fmt.Errorf("%q is more than 100%%", s)
	}
	return v, nil
}

// sdCondFirmware: of systemd's forms slinit checks "uefi" and
// "device-tree" alike; device-tree-compatible(...) and smbios-field(...)
// it does not have, and any other value is false to systemd while slinit
// would look for it in the DMI product name.
func sdCondFirmware(v string, negate bool) ([]sdPred, []SystemdWarning, error) {
	if v == "uefi" || v == "device-tree" {
		return sdOne(v, negate), nil, nil
	}
	return nil, nil, fmt.Errorf("slinit's firmware check has no %q", v)
}

// sdCondSecurity: the frameworks slinit detects as systemd does.
// "uefi-secureboot", "tpm2" and "cvm" it does not know.
var sdSecurityOK = map[string]bool{
	"selinux": true, "apparmor": true, "smack": true, "ima": true, "tomoyo": true,
	"measured-uki": true, "measured-os": true, "audit": true,
}

func sdCondSecurity(v string, negate bool) ([]sdPred, []SystemdWarning, error) {
	if !sdSecurityOK[v] {
		return nil, nil, fmt.Errorf("slinit's security check cannot tell %q the way systemd does", v)
	}
	if v == "audit" {
		// use_audit asks both: the kernel's audit socket, and whether
		// systemd can load libaudit.so.1 to talk to it.
		return sdOne(v, negate), []SystemdWarning{{"NOTE", "Security=audit: slinit asks the kernel's audit socket only; " +
			"systemd also answers no when libaudit.so.1 is not installed"}}, nil
	}
	return sdOne(v, negate), nil, nil
}

// sdCondUser: root, nobody, a UID, a name and "@system" mean the same to
// both; "uid:N" is slinit's own spelling, and a name to systemd.
func sdCondUser(v string, negate bool) ([]sdPred, []SystemdWarning, error) {
	if strings.HasPrefix(v, "uid:") {
		return nil, nil, fmt.Errorf("%q is a user name to systemd and a UID to slinit", v)
	}
	return sdOne(v, negate), nil, nil
}

// sdCondGroup: systemd's ConditionGroup= has no "@system" (it is a group
// name that does not exist); slinit's does.
func sdCondGroup(v string, negate bool) ([]sdPred, []SystemdWarning, error) {
	if v == "@system" || strings.HasPrefix(v, "gid:") {
		return nil, nil, fmt.Errorf("%q is a group name to systemd and something else to slinit", v)
	}
	return sdOne(v, negate), nil, nil
}

func sdCondEnvironment(v string, negate bool) ([]sdPred, []SystemdWarning, error) {
	return sdOne(v, negate), []SystemdWarning{{"NOTE", fmt.Sprintf(
		"Environment=%s: slinit looks in the environment it was started with; "+
			"a variable set later with slinitctl setenv is not seen, where systemctl set-environment's is", v)}}, nil
}

// sdCondCgroupController: "v2" always holds and "v1" never does on a
// systemd that has no cgroup v1 left; otherwise a blank-separated list
// of controllers that must all be available, systemd ignoring names it
// does not know. slinit checks one name in the root cgroup.controllers,
// which is where systemd looks for the v2 controllers.
var sdCgroupV2Controllers = map[string]bool{"cpu": true, "cpuset": true, "io": true, "memory": true, "pids": true}
var sdCgroupOtherControllers = map[string]bool{
	"cpuacct": true, "blkio": true, "devices": true, "bpf-firewall": true, "bpf-devices": true,
	"bpf-foreign": true, "bpf-socket-bind": true, "bpf-restrict-network-interfaces": true,
}

func sdCondCgroupController(v string, negate bool) ([]sdPred, []SystemdWarning, error) {
	always := func(holds bool) ([]sdPred, []SystemdWarning, error) {
		if holds != negate {
			return nil, nil, nil
		}
		return nil, nil, errors.New("the check never holds, and slinit has no check that always fails")
	}
	switch v {
	case "v2":
		return always(true)
	case "v1":
		return always(false)
	}
	var preds []sdPred
	for _, c := range strings.Fields(v) {
		switch {
		case sdCgroupV2Controllers[c]:
			preds = append(preds, sdPred{c, negate})
		case sdCgroupOtherControllers[c]:
			return nil, nil, fmt.Errorf("slinit cannot check the %s controller as systemd does", c)
		}
	}
	if len(preds) == 0 {
		return always(true)
	}
	if negate && len(preds) > 1 {
		return nil, nil, errors.New("slinit cannot negate several controller checks together")
	}
	return preds, nil, nil
}

// --- the rate limit, the actions and the job timeout ---

// sdStartLimitInterval: a systemd time span; "infinity" never ends the
// window, and 0 turns the limit off, in both.
func sdStartLimitInterval(v string) (string, []SystemdWarning, error) {
	s, ok := parseSystemdSeconds(v, "inf")
	if !ok {
		return "", nil, fmt.Errorf("%q is not a time span", v)
	}
	return s, nil, nil
}

// sdStartLimitBurst: systemd counts every start in the window, slinit
// the automatic restarts. A burst of N lets a service that keeps failing
// right after boot run N times in systemd and N+1 in slinit; one that
// ran for longer than the window before it started failing, N+1 in
// both. N, not N-1: slinit takes 0 as no limit at all.
func sdStartLimitBurst(v string) (string, []SystemdWarning, error) {
	n, err := strconv.ParseUint(v, 10, 31)
	if err != nil {
		return "", nil, fmt.Errorf("%q is not a count", v)
	}
	var warns []SystemdWarning
	if n > 0 {
		warns = append(warns, SystemdWarning{"NOTE", fmt.Sprintf(
			"StartLimitBurst=%s mapped to restart-limit-count: slinit counts automatic restarts only, "+
				"so starts asked for by hand are not limited, and the count allows one restart more "+
				"when the service fails right after it was first started", v)})
	}
	return strconv.FormatUint(n, 10), warns, nil
}

// sdEmergencyAction maps systemd's emergency actions onto slinit's
// system actions. slinit has one way to reach each end state, the
// orderly one; soft-reboot and kexec it does not offer as an action.
func sdEmergencyAction(v string) (string, []SystemdWarning, error) {
	switch v {
	case "none", "reboot", "poweroff", "halt", "exit":
		return v, nil, nil
	case "reboot-force", "reboot-immediate", "poweroff-force", "poweroff-immediate",
		"halt-force", "halt-immediate", "exit-force":
		base := v[:strings.IndexByte(v, '-')]
		return base, []SystemdWarning{{"NOTE", fmt.Sprintf(
			"%s mapped to %s: slinit stops the services first, which systemd's %s skips", v, base, v)}}, nil
	case "soft-reboot", "soft-reboot-force", "kexec", "kexec-force":
		return "", nil, fmt.Errorf("slinit has no %s action", strings.TrimSuffix(v, "-force"))
	}
	return "", nil, fmt.Errorf("%q is not an action systemd knows", v)
}

// sdSuccessAction: systemd runs it whenever the unit goes from active to
// inactive, a stop asked for by hand included; slinit only when the
// service ends on its own with status 0.
func sdSuccessAction(v string) (string, []SystemdWarning, error) {
	a, warns, err := sdEmergencyAction(v)
	if err == nil && a != "none" {
		warns = append(warns, SystemdWarning{"NOTE", fmt.Sprintf(
			"SuccessAction=%s: slinit runs it only when the service ends on its own with status 0, "+
				"not when it is stopped by hand as systemd does", v)})
	}
	return a, warns, err
}

// sdRebootArgument is RebootArgument=: specifiers are expanded, as
// config_parse_reboot_parameter does, and an empty value clears it.
func sdRebootArgument(cfg *SystemdConfig, key, val string) []SystemdWarning {
	v := strings.TrimSpace(trimQuotes(val))
	if v == "" {
		cfg.dropMapped("reboot-argument")
		return nil
	}
	v, w := expandSpecifiers(cfg, key, v)
	if len(w) > 0 {
		return []SystemdWarning{{"WARN", fmt.Sprintf("[Unit] %s=%s ignored: it uses a specifier slinit cannot expand", key, val)}}
	}
	out := strings.ReplaceAll(v, "$", "$$")
	if err := validateSlinitSetting("reboot-argument", out); err != nil {
		return []SystemdWarning{{"WARN", fmt.Sprintf("[Unit] %s=%s ignored: %v", key, val, err)}}
	}
	cfg.setMapped("reboot-argument", out)
	return []SystemdWarning{{"NOTE", fmt.Sprintf(
		"%s=%s mapped to reboot-argument, which slinit records but does not pass to reboot(2) yet", key, val)}}
}

// sdJobTimeout: JobTimeoutSec= takes a time span, 0 and "infinity"
// meaning none. slinit's job-timeout-sec bounds the start job the same
// way, from the moment it is asked for, but what it does at the end
// differs: systemd cancels the job and leaves the unit as it is, slinit
// stops the service and marks it failed, so failure-action runs.
func sdJobTimeout(v string) (string, []SystemdWarning, error) {
	s, ok := parseSystemdSeconds(v, "0")
	if !ok {
		return "", nil, fmt.Errorf("%q is not a time span", v)
	}
	var warns []SystemdWarning
	if f, err := strconv.ParseFloat(s, 64); err == nil && f > 0 {
		warns = append(warns, SystemdWarning{"NOTE", fmt.Sprintf(
			"JobTimeoutSec=%s mapped to job-timeout-sec: when it fires slinit stops the service and marks it failed "+
				"(so failure-action runs), where systemd only cancels the start job; a stop job is not bounded", v)})
	}
	return s, warns, nil
}
