// slinit-fstabinfo — OpenRC-compatible /etc/fstab query utility.
//
// Drop-in replacement for OpenRC's fstabinfo(8): parses /etc/fstab and
// prints (or acts on) selected entries. Ported so init.d scripts that
// call `fstabinfo -o /mnt/foo` (options), `fstabinfo -b /` (block
// device), etc. keep working under slinit.
package main

import (
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"

	"github.com/sunlightlinux/slinit/pkg/fstab"
)

const (
	exitOK       = 0
	exitFailure  = 1
	exitBadUsage = 2
)

// output selects what we print / do per entry.
type outputMode int

const (
	outputFile outputMode = iota
	outputBlockDev
	outputOptions
	outputMountArgs
	outputPassno
	outputMount
	outputRemount
)

// selector is one --fstype or --passno option, kept in command-line
// order because OpenRC appends each one's matches to a single list.
type selector struct {
	fstypes     []string // --fstype TYPE[,TYPE...]
	passnoOp    byte     // --passno OP N: '=', '<' or '>'
	passnoValue int
	file        string // --passno MOUNTPOINT (plain form)
}

type options struct {
	mode      outputMode
	selectors []selector
	files     []string // positional mountpoints
	fstabPath string
}

func main() {
	opts, err := parseArgs(os.Args[1:])
	if err != nil {
		switch err {
		case errHelp:
			os.Exit(exitOK)
		case errVersion:
			fmt.Printf("slinit-fstabinfo %s\n", version)
			os.Exit(exitOK)
		}
		fmt.Fprintln(os.Stderr, err)
		os.Exit(exitBadUsage)
	}
	if _, err := os.Stat(opts.fstabPath); err != nil {
		fmt.Fprintf(os.Stderr, "%s does not exist\n", opts.fstabPath)
		os.Exit(exitFailure)
	}
	entries, err := fstab.ReadFile(opts.fstabPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "parse %s: %v\n", opts.fstabPath, err)
		os.Exit(exitFailure)
	}
	os.Exit(run(entries, opts))
}

// run applies filters and executes the chosen output mode, mirroring
// fstabinfo.c's post-getopt block.
func run(entries []fstab.Entry, opts options) int {
	// Each --fstype / --passno OP N scans the whole fstab and appends its
	// matches to one list, so several of them select the union (an entry
	// matched twice is listed twice). A plain --passno MOUNTPOINT appends
	// that name without counting as a filter.
	var list []string
	filtered := false
	for _, s := range opts.selectors {
		switch {
		case s.fstypes != nil:
			filtered = true
			for _, t := range s.fstypes {
				for _, e := range entries {
					if e.VFSType == t {
						list = append(list, e.File)
					}
				}
			}
		case s.passnoOp != 0:
			filtered = true
			for _, e := range entries {
				if e.File == "none" {
					continue
				}
				p := e.PassNo
				// C ops: `i == p`, `i > p && p != 0`, `i < p && p != 0`.
				if (s.passnoOp == '=' && s.passnoValue == p) ||
					(s.passnoOp == '<' && s.passnoValue > p && p != 0) ||
					(s.passnoOp == '>' && s.passnoValue < p && p != 0) {
					list = append(list, e.File)
				}
			}
		default:
			list = append(list, s.file)
		}
	}

	if len(opts.files) > 0 {
		if len(list) > 0 {
			// Keep the listed names that are also positional arguments.
			var kept []string
			for _, f := range list {
				if slices.Contains(opts.files, f) {
					kept = append(kept, f)
				}
			}
			list = kept
		} else {
			// Nothing selected (even by a filter that matched
			// nothing): the positional names are the list.
			list = opts.files
		}
	} else if !filtered {
		for _, e := range entries {
			list = append(list, e.File)
		}
		if len(list) == 0 {
			fmt.Fprintln(os.Stderr, "empty fstab")
			return exitFailure
		}
	}

	if len(list) == 0 {
		return exitFailure
	}

	// Suppress printing when EINFO_QUIET is truthy (OpenRC convention).
	quiet := isTruthy(os.Getenv("EINFO_QUIET"))
	result := exitOK

	for _, f := range list {
		// Like getmntfile(3): the first entry with this mountpoint.
		ep := fstab.FindByFile(entries, f)
		if ep == nil {
			result = exitFailure
			continue
		}
		e := *ep
		switch opts.mode {
		case outputMount:
			result += doMount(e, false)
		case outputRemount:
			result += doMount(e, true)
		}
		if quiet {
			continue
		}
		switch opts.mode {
		case outputBlockDev:
			fmt.Println(e.Spec)
		case outputMountArgs:
			fmt.Printf("-o %s -t %s %s %s\n", e.MntOps, e.VFSType, e.Spec, f)
		case outputOptions:
			fmt.Println(e.MntOps)
		case outputFile:
			fmt.Println(f)
		case outputPassno:
			fmt.Println(e.PassNo)
		}
	}
	// As in fstabinfo.c: mount(8) exit codes add up, and a name missing
	// from fstab sets the status to 1.
	return result
}

// doMount shells out to mount(8) to actually (re)mount an entry.
// Matches the C original's arg layout so the same errors surface to
// callers.
func doMount(e fstab.Entry, remount bool) int {
	var args []string
	if remount {
		args = []string{"-o", e.MntOps, "-t", e.VFSType, "-o", "remount", e.Spec, e.File}
	} else {
		args = []string{"-o", e.MntOps, "-t", e.VFSType, e.Spec, e.File}
	}
	cmd := exec.Command("mount", args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return exitErr.ExitCode()
		}
		return exitFailure
	}
	return 0
}

func isTruthy(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "y", "yes", "true", "on":
		return true
	}
	return false
}

// parsePassNoArg splits the operator prefix ("=" / "<" / ">") from the
// numeric tail; a bare arg means "look up passno for a specific
// mountpoint" and returns op=0, val=0, plain=arg.
func parsePassNoArg(arg string) (op byte, val int, plain string, err error) {
	if arg == "" {
		return 0, 0, "", fmt.Errorf("--passno: empty argument")
	}
	switch arg[0] {
	case '=', '<', '>':
		n, perr := strconv.Atoi(arg[1:])
		if perr != nil {
			return 0, 0, "", fmt.Errorf("--passno: bad number %q", arg[1:])
		}
		return arg[0], n, "", nil
	}
	return 0, 0, arg, nil
}
