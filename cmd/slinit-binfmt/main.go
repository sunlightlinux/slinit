// slinit-binfmt — systemd-binfmt(1) clone.
//
// Registers custom binary formats with the kernel by writing spec
// lines from binfmt.d(5) config files to
// /proc/sys/fs/binfmt_misc/register. Necessary for QEMU user-mode
// emulation (running foreign-architecture binaries transparently),
// Mono/.NET (.exe dispatched to `mono`), WSL interop, and any other
// scheme where an inode's magic bytes or filename extension should
// route into an interpreter.
//
// Usage
//
//	slinit-binfmt                # apply every /etc/binfmt.d/*.conf etc.
//	slinit-binfmt PATH1 PATH2    # apply only the named files
//	slinit-binfmt --unregister   # tear down every currently-registered format
//	slinit-binfmt --root=DIR     # test seam: prefix DIR onto every path
//
// Exit codes: 0 success  1 partial failure  2 usage  3 binfmt_misc
// kernel module not loaded (nothing to do).
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	exitOK             = 0
	exitFailure        = 1
	exitBadUsage       = 2
	exitBinfmtNotAvail = 3
)

var version = "dev"

func main() {
	opts, err := parseArgs(os.Args[1:])
	if err != nil {
		switch err {
		case errHelp:
			os.Exit(exitOK)
		case errVersion:
			fmt.Printf("slinit-binfmt %s\n", version)
			os.Exit(exitOK)
		}
		fmt.Fprintln(os.Stderr, err)
		os.Exit(exitBadUsage)
	}

	// --root prefixes every hardcoded path so the tests (and any
	// operator who wants to preview a scratch config) can point the
	// tool at a fake tree.
	if opts.root != "" {
		binfmtDirs = prefixDirs(opts.root, binfmtDirs)
		registerPath = filepath.Join(opts.root, registerPath)
		binfmtStatusDir = filepath.Join(opts.root, binfmtStatusDir)
	}

	if !binfmtMounted() {
		fmt.Fprintln(os.Stderr,
			"binfmt_misc kernel filesystem not available; skipping")
		os.Exit(exitBinfmtNotAvail)
	}

	if opts.unregister {
		res, err := unregisterAll()
		if err != nil {
			fmt.Fprintf(os.Stderr, "unregister: %v\n", err)
			os.Exit(exitFailure)
		}
		if opts.verbose {
			fmt.Fprintln(os.Stderr, res.String())
		}
		return
	}

	paths := opts.files
	if len(paths) == 0 {
		paths = discover(binfmtDirs)
	}
	res, err := applyFiles(paths)
	if err != nil {
		fmt.Fprintf(os.Stderr, "apply: %v\n", err)
		os.Exit(exitFailure)
	}
	for _, e := range res.errors {
		fmt.Fprintln(os.Stderr, e)
	}
	if opts.verbose {
		fmt.Fprintln(os.Stderr, res.String())
	}
	if len(res.errors) > 0 {
		os.Exit(exitFailure)
	}
}

// prefixDirs joins root onto every entry — used by the --root test
// seam so an in-repo fixture tree replaces the system paths.
func prefixDirs(root string, dirs []string) []string {
	out := make([]string, len(dirs))
	for i, d := range dirs {
		out[i] = filepath.Join(root, strings.TrimPrefix(d, "/"))
	}
	return out
}
