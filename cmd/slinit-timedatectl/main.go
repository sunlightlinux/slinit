// slinit-timedatectl — systemd timedatectl(1) parity, D-Bus-free.
//
// Reads and writes the local system time / timezone / RTC settings
// directly to the on-disk sources of truth: /etc/localtime symlink,
// /etc/timezone file (when present), /etc/adjtime (RTC-in-local
// flag), and /dev/rtc via RTC_RD_TIME. No D-Bus, no timedated —
// same design shape as slinit-hostnamectl.
//
// Commands (fully supported):
//
//	status                     show current time / RTC / TZ / NTP
//	show                       KEY=VALUE dump of the same fields
//	set-time TIME              wall-clock set (RFC3339, systemd form, @epoch)
//	set-timezone ZONE          swap /etc/localtime symlink atomically
//	list-timezones             list every zone under /usr/share/zoneinfo
//	set-local-rtc BOOL         write /etc/adjtime line 3 (UTC / LOCAL)
//	set-ntp BOOL               enable/disable the local time-sync service
//
// Not supported (systemd-timesyncd-specific; rejected at runtime):
//
//	timesync-status, show-timesync, ntp-servers, revert
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

type options struct {
	cmd  string
	args []string

	// Formatting.
	jsonMode string // "" | off | pretty | short
	noPager  bool

	// set-local-rtc modifier.
	adjustClock bool

	// Accepted-but-rejected remote target flags.
	host    string
	machine string

	// Compatibility flags parsed but with no runtime effect here.
	noAskPassword bool
	monitor       bool
	all           bool
	value         bool
	property      []string

	showHelp    bool
	showVersion bool
}

func main() {
	opts, err := parseArgs(os.Args[1:])
	if err != nil {
		fmt.Fprintf(os.Stderr, "timedatectl: %v\n", err)
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
		fmt.Fprintln(os.Stderr, "timedatectl: --host / --machine are not supported by slinit-timedatectl")
		os.Exit(1)
	}
	if err := dispatch(os.Stdout, opts); err != nil {
		fmt.Fprintf(os.Stderr, "timedatectl: %v\n", err)
		os.Exit(1)
	}
}

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
		case a == "--no-pager":
			opts.noPager = true
		case a == "--no-ask-password":
			opts.noAskPassword = true
		case a == "--monitor":
			opts.monitor = true
		case a == "-a" || a == "--all":
			opts.all = true
		case a == "--value":
			opts.value = true
		case a == "--adjust-system-clock":
			opts.adjustClock = true
		case a == "-j":
			opts.jsonMode = defaultJSONForTTY()
		case a == "--json":
			if i+1 >= len(argv) {
				return opts, errors.New("--json requires an argument (pretty|short|off)")
			}
			i++
			opts.jsonMode = argv[i]
		case strings.HasPrefix(a, "--json="):
			opts.jsonMode = strings.TrimPrefix(a, "--json=")
		case a == "-p" || a == "--property":
			if i+1 >= len(argv) {
				return opts, errors.New("--property requires an argument")
			}
			i++
			opts.property = append(opts.property, argv[i])
		case strings.HasPrefix(a, "--property="):
			opts.property = append(opts.property, strings.TrimPrefix(a, "--property="))
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

func dispatch(out io.Writer, opts options) error {
	switch opts.cmd {
	case "status":
		return runStatus(out, opts, "text")
	case "show":
		return runStatus(out, opts, "show")
	case "set-time":
		return runSetTime(opts)
	case "set-timezone":
		return runSetTimezone(opts)
	case "list-timezones":
		return runListTimezones(out, opts)
	case "set-local-rtc":
		return runSetLocalRTC(opts)
	case "set-ntp":
		return runSetNTP(opts)
	case "timesync-status", "show-timesync", "ntp-servers", "revert":
		return fmt.Errorf("%q is not supported by slinit-timedatectl (systemd-timesyncd-specific)", opts.cmd)
	default:
		return fmt.Errorf("unknown command %q (try --help)", opts.cmd)
	}
}

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
	fmt.Fprint(out, `Usage: timedatectl [OPTIONS...] COMMAND ...

Query or change system time and date settings.

Commands:
  status                       Show current time settings (default)
  show                         Show properties as KEY=VALUE
  set-time TIME                Set system time
  set-timezone ZONE            Set system timezone
  list-timezones               Show known timezones
  set-local-rtc BOOL           Control whether RTC is in local time
  set-ntp BOOL                 Enable/disable network time sync

Options:
  -h, --help                   Show this help
      --version                Show slinit version
      --no-pager               Do not pipe output into a pager
      --no-ask-password        Accepted (no prompt)
      --adjust-system-clock    With set-local-rtc: also adjust system clock
      --json=MODE              JSON output (off|pretty|short)
  -j                           Shorthand for --json=pretty (TTY) or short

Not supported by slinit-timedatectl (parsed but rejected):
  -H, --host=[USER@]HOST       Remote host (D-Bus over SSH)
  -M, --machine=CONTAINER      systemd-nspawn container
  timesync-status              systemd-timesyncd introspection
  show-timesync                systemd-timesyncd introspection
  ntp-servers                  runtime NTP server list
  revert                       revert runtime NTP settings
`)
}
