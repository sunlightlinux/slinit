// slinit-supervise-daemon — OpenRC-compatible daemon supervisor.
//
// Drop-in replacement for OpenRC's supervise-daemon(8): starts a daemon
// that MUST NOT fork, keeps it alive across crashes with configurable
// respawn policy, and forwards --signal / --stop to the running
// daemon.
//
// Model (mirrors OpenRC):
//
//   - --start: the top-level process re-exec's itself with the private
//     supervisorMarker as its first argument. That inner instance
//     becomes the detached
//     supervisor, writes its own pidfile, forks the daemon, and loops.
//     The top-level waits for the pidfile to appear (or a timeout),
//     then exits so the caller's `start()` init.d function returns.
//
//   - --stop: read supervisor pidfile, SIGTERM it (escalating per
//     --retry). The supervisor's shutdown path kills the daemon and
//     cleans up its pidfiles.
//
//   - --signal SIG: read the daemon's pidfile (companion file next to
//     the supervisor pidfile) and deliver SIG directly to the daemon.
//     Bypasses the supervisor, so users must not send SIGTERM this way
//     — the supervisor would just respawn.
package main

import (
	"fmt"
	"os"
)

var version = "dev"

func main() {
	// Re-exec branch: cmdStart detaches by exec'ing itself with
	// supervisorMarker as the first argument. That inner instance parses
	// the remaining args exactly the same way but jumps straight into the
	// supervisor loop after the parser returns.
	inSupervisor, args := splitSupervisorMarker(os.Args[1:])
	// The mode switch used to be the environment variable itself, which
	// the supervisor passed on to the daemon and the daemon to everything
	// it started. Any --stop run from a shell, cron job or login under a
	// supervised daemon then became a supervisor: it overwrote and
	// deleted svc.pid / svc.pid.daemon and left the real supervisor and
	// daemon running untracked. Drop it so nothing started from here
	// carries it, whatever an older supervisor left in our environment.
	_ = os.Unsetenv(legacySupervisorEnvVar)

	opts, err := parseArgs(args)
	if err != nil {
		switch err {
		case errHelp:
			os.Exit(exitOK)
		case errVersion:
			fmt.Printf("slinit-supervise-daemon %s\n", version)
			os.Exit(exitOK)
		}
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(exitBadUsage)
	}

	if opts.Service == "" {
		fmt.Fprintln(os.Stderr,
			"service name is required as the first positional argument")
		os.Exit(exitBadUsage)
	}

	if inSupervisor {
		// Only cmdStart re-execs with the marker, and only for --start.
		if opts.Mode != "start" {
			fmt.Fprintf(os.Stderr, "%s is only valid with --start\n", supervisorMarker)
			os.Exit(exitBadUsage)
		}
		os.Exit(runSupervisor(opts))
	}

	switch opts.Mode {
	case "start":
		os.Exit(cmdStart(opts))
	case "stop":
		os.Exit(cmdStop(opts))
	case "signal":
		os.Exit(cmdSignal(opts))
	default:
		fmt.Fprintf(os.Stderr, "unknown mode: %s\n", opts.Mode)
		os.Exit(exitBadUsage)
	}
}

func printUsage() {
	fmt.Print(`Usage: slinit-supervise-daemon SVCNAME [MODE] [OPTIONS] [-- ARGS...]

Modes (default: --start):
  -S, --start                     start & supervise the daemon
  -K, --stop                      stop the supervisor (and its daemon)
  -s, --signal SIG                send SIG to the supervised daemon

Process:
  -x, --exec PATH                 binary to run under supervision
  -p, --pidfile PATH              supervisor pidfile (daemon pid at PATH.daemon)
  -u, --user USER[:GROUP]         drop credentials before exec
  -g, --group GROUP               daemon primary group
  -d, --chdir DIR                 chdir before exec
  -r, --chroot DIR                chroot before exec
  -N, --nicelevel N               setpriority(2)
      --oom-score-adj N           /proc/PID/oom_score_adj
  -k, --umask OCT                 process umask
  -I, --ionice CLASS[:LEVEL]      ioprio_set (rt|be|idle)
  -e, --env KEY=VAL               append env var (repeatable)
  -0, --stdin FILE                redirect daemon stdin
  -1, --stdout FILE               redirect daemon stdout (appended)
  -2, --stderr FILE               redirect daemon stderr (appended)
      --stdout-logger CMD         pipe stdout to CMD's stdin
      --stderr-logger CMD         pipe stderr to CMD's stdin

Respawn policy:
  -D, --respawn-delay DUR         fixed delay before restart (default 0)
  -P, --respawn-period DUR        window used to count restarts (default 12sec)
  -m, --respawn-max N             max restarts within period; 0 = unlimited (default 10)
      --respawn-delay-step DUR    delay increment per restart (default 128ms)
      --respawn-delay-cap DUR     ceiling for stepped delay (default 30sec)

Stop escalation:
  -R, --retry SPEC                "N" or "TERM/30/KILL/5" (default TERM/5)

Hardening (require slinit-runner on PATH):
      --capabilities LIST         ambient+bounding caps
      --secbits BITS              PR_SET_SECUREBITS
      --no-new-privs              PR_SET_NO_NEW_PRIVS

Accepted but not implemented (OpenRC parity):
  -a, --healthcheck-timer DUR     runs no healthcheck (init.d hook not owned by us)
  -A, --healthcheck-delay DUR     same
      --notify SPEC               readiness protocol — supervise-daemon is the
                                  reader; ignored for now, supervisor is
                                  considered ready once the daemon exec's

Common:
  -v, --verbose                   extra diagnostics on stderr
  -h, --help                      this help
  -V, --version                   version string

Exit codes: 0=ok  1=already-{running,stopped}  2=usage  3=unsupported
            4=insufficient-privs  5=stale-pidfile
`)
}

// splitSupervisorMarker reports whether args start with the private
// supervisorMarker cmdStart re-execs with, and returns the arguments
// after it. An argument vector is not inherited: unlike an environment
// variable it cannot leak from the supervisor into the daemon and on
// into whatever the daemon runs.
func splitSupervisorMarker(args []string) (bool, []string) {
	if len(args) > 0 && args[0] == supervisorMarker {
		return true, args[1:]
	}
	return false, args
}
