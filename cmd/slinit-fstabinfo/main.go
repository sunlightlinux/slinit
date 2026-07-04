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

type options struct {
	mode        outputMode
	fstypes     []string // --fstype: keep entries with a matching type
	passnoOp    byte     // '=', '<', '>', or 0 for none / plain query
	passnoValue int
	files       []string // positional filter list
	fstabPath   string
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
	// Filter chain: --fstype, --passno OP N, positional filter list.
	// A positional list without prior filtering just names mountpoints
	// to look up; combined with a filter, it narrows the earlier list.
	var candidates []fstab.Entry
	filtered := false

	if len(opts.fstypes) > 0 {
		filtered = true
		for _, e := range entries {
			for _, t := range opts.fstypes {
				if e.VFSType == t {
					candidates = append(candidates, e)
					break
				}
			}
		}
	}
	if opts.passnoOp != 0 {
		filtered = true
		src := entries
		if len(candidates) > 0 {
			src = candidates
			candidates = nil
		}
		for _, e := range src {
			if e.File == "none" {
				continue
			}
			p := e.PassNo
			switch opts.passnoOp {
			case '=':
				if opts.passnoValue == p {
					candidates = append(candidates, e)
				}
			case '<':
				// C op: `i > p && p != 0` → "passno present and less than i".
				if p != 0 && opts.passnoValue > p {
					candidates = append(candidates, e)
				}
			case '>':
				if p != 0 && opts.passnoValue < p {
					candidates = append(candidates, e)
				}
			}
		}
	}

	if len(opts.files) > 0 {
		if filtered {
			// Intersect the filter output with the positional list.
			var kept []fstab.Entry
			for _, e := range candidates {
				for _, f := range opts.files {
					if e.File == f {
						kept = append(kept, e)
						break
					}
				}
			}
			candidates = kept
		} else {
			// Positional list without a filter: look up each name.
			for _, f := range opts.files {
				if e := fstab.FindByFile(entries, f); e != nil {
					candidates = append(candidates, *e)
				}
			}
		}
	} else if !filtered {
		if len(entries) == 0 {
			fmt.Fprintln(os.Stderr, "empty fstab")
			return exitFailure
		}
		candidates = entries
	}

	if len(candidates) == 0 {
		return exitFailure
	}

	// Suppress printing when EINFO_QUIET is truthy (OpenRC convention).
	quiet := isTruthy(os.Getenv("EINFO_QUIET"))
	result := 0

	for _, e := range candidates {
		switch opts.mode {
		case outputMount:
			if rc := doMount(e, false); rc != 0 {
				result += rc
			}
		case outputRemount:
			if rc := doMount(e, true); rc != 0 {
				result += rc
			}
		}
		if quiet {
			continue
		}
		switch opts.mode {
		case outputBlockDev:
			fmt.Println(e.Spec)
		case outputMountArgs:
			fmt.Printf("-o %s -t %s %s %s\n", e.MntOps, e.VFSType, e.Spec, e.File)
		case outputOptions:
			fmt.Println(e.MntOps)
		case outputFile:
			fmt.Println(e.File)
		case outputPassno:
			fmt.Println(e.PassNo)
		}
	}
	if result > 0 {
		return exitFailure
	}
	return exitOK
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
