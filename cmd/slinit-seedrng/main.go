// slinit-seedrng — persist entropy across reboots.
//
// Runs one cycle of the SeedRNG protocol: consumes any existing seed
// files in the seed directory (feeding them to the kernel RNG via
// RNDADDENTROPY, then unlinking them so they cannot be replayed on a
// subsequent boot), then writes a fresh seed for the next boot from
// getrandom(). Intended to be invoked twice on a slinit-managed system:
//
//	early boot   (as a scripted service, before user services start)
//	shutdown     (as a stop-command / shutdown hook)
//
// Idempotent — a second invocation without any state change simply
// consumes the (fresh) seed the previous invocation just wrote and
// produces another one. Not meant to run at high frequency.
//
// See seedrng(8) upstream (Jason Donenfeld) and OpenRC's seedrng for
// the wire-compatible protocol.
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/sunlightlinux/slinit/pkg/rng"
)

var version = "dev"

func main() {
	var (
		seedDir    string
		skipCredit bool
		quiet      bool
		showVer    bool
	)

	flag.StringVar(&seedDir, "seed-dir", rng.DefaultSeedDir,
		"directory for seed files")
	flag.BoolVar(&skipCredit, "skip-credit", false,
		"skip crediting entropy of seeds (safer if an attacker may have controlled them)")
	flag.BoolVar(&quiet, "q", false, "suppress informational output")
	flag.BoolVar(&quiet, "quiet", false, "suppress informational output")
	flag.BoolVar(&showVer, "version", false, "print version and exit")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr,
			"Usage: %s [--seed-dir DIR] [--skip-credit] [--quiet]\n",
			os.Args[0])
		flag.PrintDefaults()
	}
	flag.Parse()

	if showVer {
		fmt.Printf("slinit-seedrng %s\n", version)
		return
	}

	if os.Geteuid() != 0 {
		fmt.Fprintf(os.Stderr, "slinit-seedrng: superuser access is required\n")
		os.Exit(1)
	}

	log := func(format string, args ...any) {
		fmt.Fprintf(os.Stderr, "slinit-seedrng: "+format+"\n", args...)
	}
	if quiet {
		log = func(string, ...any) {}
	}

	_, err := rng.Run(rng.Options{
		SeedDir:    seedDir,
		SkipCredit: skipCredit,
		Log:        log,
	})
	if err != nil {
		// The library always writes a fresh seed unless something
		// catastrophic happened (unwritable dir, etc). A partial
		// failure — e.g. RNDADDENTROPY denied because we're not
		// privileged enough on some hardened kernels — is still a
		// non-zero exit so operators notice, but the fresh seed is
		// on disk for the next boot regardless.
		errs := unwrapJoined(err)
		for _, e := range errs {
			fmt.Fprintf(os.Stderr, "slinit-seedrng: %v\n", e)
		}
		os.Exit(1)
	}
}

// unwrapJoined splits an errors.Join value back into its parts so we
// print one line per underlying failure instead of a jammed-together
// blob.
func unwrapJoined(err error) []error {
	type multi interface{ Unwrap() []error }
	if m, ok := err.(multi); ok {
		return m.Unwrap()
	}
	return []error{errors.Unwrap(err)}
}
