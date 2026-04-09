// slinit-checkpath creates or verifies one or more filesystem paths with
// a specified type, mode, and ownership. It is the slinit equivalent of
// OpenRC's checkpath(8) helper, intended for use from service pre-start
// commands.
//
// Usage:
//
//	slinit-checkpath [-d|-D|-f|-F|-p] [-m MODE] [-o USER[:GROUP]] [-W] PATH...
//
// Flags are modelled after OpenRC's checkpath(8):
//
//	-d / --directory           ensure directory exists (create if missing)
//	-D / --directory-truncate  ensure directory exists AND is empty
//	-f / --file                ensure regular file exists
//	-F / --file-truncate       ensure file exists AND truncate to zero
//	-p / --pipe                ensure named pipe (FIFO) exists
//	-m / --mode MODE           desired mode (octal, e.g. 0755)
//	-o / --owner USER[:GROUP]  desired owner (name or numeric id)
//	-W / --writable            success if path is already writable
//
// Exit codes: 0 on success for every path, 1 on the first failure.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/sunlightlinux/slinit/pkg/checkpath"
)

func main() {
	var (
		dirFlag       bool
		dirTruncFlag  bool
		fileFlag      bool
		fileTruncFlag bool
		pipeFlag      bool
		modeStr       string
		ownerStr      string
		writable      bool
	)

	flag.BoolVar(&dirFlag, "d", false, "ensure directory exists")
	flag.BoolVar(&dirFlag, "directory", false, "ensure directory exists")
	flag.BoolVar(&dirTruncFlag, "D", false, "ensure directory exists and is empty")
	flag.BoolVar(&dirTruncFlag, "directory-truncate", false, "ensure directory exists and is empty")
	flag.BoolVar(&fileFlag, "f", false, "ensure regular file exists")
	flag.BoolVar(&fileFlag, "file", false, "ensure regular file exists")
	flag.BoolVar(&fileTruncFlag, "F", false, "ensure file exists and truncate")
	flag.BoolVar(&fileTruncFlag, "file-truncate", false, "ensure file exists and truncate")
	flag.BoolVar(&pipeFlag, "p", false, "ensure named pipe exists")
	flag.BoolVar(&pipeFlag, "pipe", false, "ensure named pipe exists")
	flag.StringVar(&modeStr, "m", "", "mode (octal, e.g. 0755)")
	flag.StringVar(&modeStr, "mode", "", "mode (octal, e.g. 0755)")
	flag.StringVar(&ownerStr, "o", "", "owner USER[:GROUP] (name or numeric)")
	flag.StringVar(&ownerStr, "owner", "", "owner USER[:GROUP] (name or numeric)")
	flag.BoolVar(&writable, "W", false, "success if path is already writable")
	flag.BoolVar(&writable, "writable", false, "success if path is already writable")

	flag.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: slinit-checkpath [-d|-D|-f|-F|-p] [-m MODE] [-o USER[:GROUP]] [-W] PATH...")
		flag.PrintDefaults()
	}
	flag.Parse()

	paths := flag.Args()
	if len(paths) == 0 {
		flag.Usage()
		os.Exit(1)
	}

	// Exactly one of {-d, -D, -f, -F, -p} may be set; -W with none is legal
	// (writable check only, no creation).
	typ, trunc, err := resolveType(dirFlag, dirTruncFlag, fileFlag, fileTruncFlag, pipeFlag)
	if err != nil {
		fmt.Fprintf(os.Stderr, "slinit-checkpath: %v\n", err)
		os.Exit(1)
	}

	var mode os.FileMode
	if modeStr != "" {
		mode, err = checkpath.ParseMode(modeStr)
		if err != nil {
			fmt.Fprintf(os.Stderr, "slinit-checkpath: %v\n", err)
			os.Exit(1)
		}
	}

	owner, err := checkpath.ParseOwner(ownerStr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "slinit-checkpath: %v\n", err)
		os.Exit(1)
	}

	exit := 0
	for _, p := range paths {
		spec := checkpath.Spec{
			Path:     p,
			Type:     typ,
			Mode:     mode,
			Owner:    owner,
			Truncate: trunc,
			Writable: writable,
		}
		if _, err := checkpath.Apply(spec); err != nil {
			fmt.Fprintln(os.Stderr, err)
			exit = 1
		}
	}
	os.Exit(exit)
}

// resolveType maps the mutually-exclusive CLI flags onto a (type, truncate)
// pair. At most one type flag may be set; zero is legal (for -W alone).
func resolveType(d, D, f, F, p bool) (checkpath.PathType, bool, error) {
	count := 0
	for _, b := range []bool{d, D, f, F, p} {
		if b {
			count++
		}
	}
	if count > 1 {
		return checkpath.TypeUnknown, false, fmt.Errorf("-d/-D/-f/-F/-p are mutually exclusive")
	}
	switch {
	case d:
		return checkpath.TypeDir, false, nil
	case D:
		return checkpath.TypeDir, true, nil
	case f:
		return checkpath.TypeFile, false, nil
	case F:
		return checkpath.TypeFile, true, nil
	case p:
		return checkpath.TypeFifo, false, nil
	}
	return checkpath.TypeUnknown, false, nil
}
