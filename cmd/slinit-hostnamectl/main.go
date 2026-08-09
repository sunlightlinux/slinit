// slinit-hostnamectl — systemd hostnamectl(1) parity, D-Bus-free.
//
// Reads and writes the local system hostname settings directly to the
// on-disk sources of truth: /etc/hostname (static + transient), and
// /etc/machine-info (pretty name, icon, chassis, deployment, location).
// No D-Bus, no systemd-hostnamed — a native slinit binary that matches
// the systemd CLI surface for muscle-memory compatibility.
//
// Commands:
//
//	status                        show all hostname settings + host meta
//	hostname [NAME]               get/set system hostname
//	icon-name [NAME]              get/set the host icon name
//	chassis [TYPE]                get/set the chassis type
//	deployment [ENV]              get/set the deployment environment
//	location [LOC]                get/set the physical location
//
// Global flags (see printHelp for the full list):
//
//	--transient / --static / --pretty  scope for `hostname`
//	--json=pretty|short|off | -j       machine-readable output
//	--host / --machine                 accepted but not supported
//	--no-ask-password                  accepted; no interactive prompt
package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

// version stamped at build time via ldflags -X main.version=vX.Y.Z.
var version = "dev"

// options collects the parsed CLI form for either a status query or a
// setter invocation. The `scope` bitfield tracks --transient / --static
// / --pretty independently so setters can honor "at least one, else
// all" semantics that systemd uses.
type options struct {
	cmd      string   // status | hostname | icon-name | chassis | deployment | location | ""
	args     []string // positional args after the subcommand
	scope    scope    // union of --transient / --static / --pretty
	jsonMode string   // "" | "off" | "pretty" | "short"
	// Accepted but no-op / rejected:
	host          string // --host (rejected: not supported)
	machine       string // --machine (rejected: not supported)
	noAskPassword bool   // --no-ask-password (no-op)
	// Meta:
	showHelp    bool
	showVersion bool
}

type scope uint8

const (
	scopeTransient scope = 1 << iota
	scopeStatic
	scopePretty
)

func (s scope) has(x scope) bool { return s&x != 0 }
func (s scope) any() bool        { return s != 0 }
func (s scope) all() scope       { return scopeTransient | scopeStatic | scopePretty }

func main() {
	opts, err := parseArgs(os.Args[1:])
	if err != nil {
		fmt.Fprintf(os.Stderr, "hostnamectl: %v\n", err)
		os.Exit(2)
	}
	if opts.showHelp {
		printHelp(os.Stdout)
		return
	}
	if opts.showVersion {
		fmt.Println(version)
		return
	}
	if opts.host != "" || opts.machine != "" {
		fmt.Fprintln(os.Stderr, "hostnamectl: --host / --machine are not supported by slinit-hostnamectl")
		os.Exit(1)
	}
	if err := dispatch(os.Stdout, opts); err != nil {
		fmt.Fprintf(os.Stderr, "hostnamectl: %v\n", err)
		os.Exit(1)
	}
}

// parseArgs walks argv, letting flags appear before or after the
// subcommand (matches systemd's getopt_long behavior — hostnamectl
// doesn't require flags-before-subcommand).
func parseArgs(argv []string) (options, error) {
	opts := options{}
	positional := []string{}
	i := 0
	for i < len(argv) {
		a := argv[i]
		switch {
		case a == "-h" || a == "--help":
			opts.showHelp = true
			return opts, nil
		case a == "--version":
			opts.showVersion = true
			return opts, nil
		case a == "--transient":
			opts.scope |= scopeTransient
		case a == "--static":
			opts.scope |= scopeStatic
		case a == "--pretty":
			opts.scope |= scopePretty
		case a == "--no-ask-password":
			opts.noAskPassword = true
		case a == "-j":
			// systemd: -j = --json=pretty on TTY, --json=short otherwise.
			opts.jsonMode = defaultJSONForTTY()
		case a == "--json":
			if i+1 >= len(argv) {
				return opts, errors.New("--json requires an argument (pretty|short|off)")
			}
			i++
			opts.jsonMode = argv[i]
		case strings.HasPrefix(a, "--json="):
			opts.jsonMode = strings.TrimPrefix(a, "--json=")
		case a == "-H" || a == "--host":
			if i+1 >= len(argv) {
				return opts, errors.New("--host requires an argument")
			}
			i++
			opts.host = argv[i]
		case strings.HasPrefix(a, "--host="):
			opts.host = strings.TrimPrefix(a, "--host=")
		case a == "-M" || a == "--machine":
			if i+1 >= len(argv) {
				return opts, errors.New("--machine requires an argument")
			}
			i++
			opts.machine = argv[i]
		case strings.HasPrefix(a, "--machine="):
			opts.machine = strings.TrimPrefix(a, "--machine=")
		case strings.HasPrefix(a, "-"):
			return opts, fmt.Errorf("unknown flag %q (try --help)", a)
		default:
			positional = append(positional, a)
		}
		i++
	}
	if opts.jsonMode != "" {
		switch opts.jsonMode {
		case "off", "pretty", "short":
		default:
			return opts, fmt.Errorf("--json must be off|pretty|short (got %q)", opts.jsonMode)
		}
	}
	if len(positional) > 0 {
		opts.cmd = positional[0]
		opts.args = positional[1:]
	} else {
		opts.cmd = "status"
	}
	return opts, nil
}

// dispatch routes the parsed options to the right subcommand handler.
func dispatch(out io.Writer, opts options) error {
	switch opts.cmd {
	case "status":
		return runStatus(out, opts)
	case "hostname":
		return runHostname(out, opts)
	case "icon-name":
		return runSimpleField(out, "ICON_NAME", opts)
	case "chassis":
		return runSimpleField(out, "CHASSIS", opts)
	case "deployment":
		return runSimpleField(out, "DEPLOYMENT", opts)
	case "location":
		return runSimpleField(out, "LOCATION", opts)
	default:
		return fmt.Errorf("unknown command %q (try --help)", opts.cmd)
	}
}

// defaultJSONForTTY implements systemd's -j convention. Kept in main
// so unit tests can drive parseArgs without needing a stub.
func defaultJSONForTTY() string {
	fi, err := os.Stdout.Stat()
	if err != nil {
		return "short"
	}
	if (fi.Mode() & os.ModeCharDevice) != 0 {
		return "pretty"
	}
	return "short"
}

func printHelp(out io.Writer) {
	fmt.Fprint(out, `Usage: hostnamectl [OPTIONS...] COMMAND ...

Query or change system hostname.

Commands:
  status                       Show current hostname settings
  hostname [NAME]              Get/set system hostname
  icon-name [NAME]             Get/set icon name for host
  chassis [TYPE]               Get/set chassis type for host
  deployment [ENV]             Get/set deployment environment for host
  location [LOCATION]          Get/set location for host

Options:
  -h, --help                   Show this help
      --version                Show package version
      --no-ask-password        Do not prompt for password (accepted, no-op)
      --transient              Only set transient hostname
      --static                 Only set static hostname
      --pretty                 Only set pretty hostname
      --json=MODE              Output as JSON (off|pretty|short)
  -j                           Equivalent to --json=pretty on TTY, else --json=short

Not supported by slinit-hostnamectl (accepted for CLI compatibility):
  -H, --host=[USER@]HOST       Operate on remote host (D-Bus over SSH)
  -M, --machine=CONTAINER      Operate on local container (nspawn)
`)
}
