// Command slinit-init-maker generates a minimal, bootable slinit service
// description directory. It is inspired by s6-linux-init-maker from the
// skarnet s6 suite and produces a layout suitable for use as the argument
// to `slinit --services-dir` on a real Linux system.
//
// The generator is intentionally opinionated: it emits a handful of
// services (system-init, optional system-mounts/network, N gettys) wired
// to a single top-level "boot" target. Users are expected to grow this
// directory by dropping in their own service files alongside the ones
// produced here.
//
// Usage:
//
//	slinit-init-maker -d /etc/slinit/boot.d
//	slinit-init-maker -n -t 4 --with-network --hostname node1
//	slinit-init-maker --force -d /tmp/slinit-test
package main

import (
	"flag"
	"fmt"
	"os"
)

// version is injected at build time via:
//   go build -ldflags "-X main.version=v1.10.10" ./cmd/slinit-init-maker
// Local builds without ldflags report "dev".
var version = "dev"

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run is the entry point used by main and by tests. Splitting main out
// like this lets us exercise the CLI without os.Exit'ing the test runner.
func run(args []string, stdout, stderr *os.File) int {
	fs := flag.NewFlagSet("slinit-init-maker", flag.ContinueOnError)
	fs.SetOutput(stderr)

	cfg := DefaultConfig()

	fs.StringVar(&cfg.OutputDir, "output", cfg.OutputDir, "service-description directory to populate")
	fs.StringVar(&cfg.OutputDir, "d", cfg.OutputDir, "service-description directory (short)")
	fs.BoolVar(&cfg.Force, "force", false, "overwrite existing files")
	fs.BoolVar(&cfg.Force, "f", false, "overwrite existing files (short)")
	fs.BoolVar(&cfg.DryRun, "dry-run", false, "print what would be written without touching disk")
	fs.BoolVar(&cfg.DryRun, "n", false, "dry run (short)")

	fs.StringVar(&cfg.BootServiceName, "name", cfg.BootServiceName, "name of the top-level boot target service")
	fs.StringVar(&cfg.SlinitBin, "bin", cfg.SlinitBin, "path to the slinit binary (for README hints only)")

	fs.IntVar(&cfg.GettyCount, "ttys", cfg.GettyCount, "number of virtual terminals to generate (0 disables)")
	fs.IntVar(&cfg.GettyCount, "t", cfg.GettyCount, "number of tty services (short)")
	fs.StringVar(&cfg.GettyCmd, "getty", cfg.GettyCmd, "getty binary to exec for each tty")
	fs.IntVar(&cfg.GettyBaud, "baud", cfg.GettyBaud, "getty baudrate passed as --keep-baud")

	fs.StringVar(&cfg.Hostname, "hostname", "", "initial hostname (written to env file; empty skips)")
	fs.StringVar(&cfg.Timezone, "tz", "", "default timezone (written to env file as TZ; empty skips)")

	fs.BoolVar(&cfg.WithMounts, "with-mounts", cfg.WithMounts, "include a system-mounts service that runs 'mount -a'")
	fs.BoolVar(&cfg.WithNetwork, "with-network", cfg.WithNetwork, "include a stub network service")
	fs.BoolVar(&cfg.WithShutdownHook, "with-shutdown-hook", cfg.WithShutdownHook, "emit a sample shutdown-hook script")

	var showVersion bool
	fs.BoolVar(&showVersion, "version", false, "print version and exit")

	if err := fs.Parse(args); err != nil {
		// flag already printed usage on ContinueOnError
		return 2
	}

	if showVersion {
		fmt.Fprintf(stdout, "slinit-init-maker version %s (part of slinit)\n", version)
		return 0
	}

	if err := cfg.Validate(); err != nil {
		fmt.Fprintf(stderr, "slinit-init-maker: %v\n", err)
		return 1
	}

	plan, err := Plan(cfg)
	if err != nil {
		fmt.Fprintf(stderr, "slinit-init-maker: %v\n", err)
		return 1
	}

	if cfg.DryRun {
		PrintPlan(stdout, cfg, plan)
		return 0
	}

	written, err := WriteAll(cfg, plan)
	if err != nil {
		fmt.Fprintf(stderr, "slinit-init-maker: %v\n", err)
		return 1
	}

	fmt.Fprintf(stdout, "slinit-init-maker: wrote %d file(s) to %s\n", len(written), cfg.OutputDir)
	return 0
}
