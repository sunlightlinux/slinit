// slinit-start-stop-daemon — Debian/OpenRC-compatible daemon runner.
//
// Drop-in replacement for OpenRC's start-stop-daemon(8): starts, stops,
// or queries the status of a daemon so ported /etc/init.d scripts keep
// working under slinit. All hardening (--capabilities/--secbits/
// --no-new-privs) is routed through slinit-runner, and --notify covers
// every readiness mode Debian/OpenRC document (none, manual, pidfile,
// fd:N, stderr, signal[:SIG]).
package main

import (
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Exit codes track Debian LSB conventions so init.d scripts that check
// $? get the answer they expect.
const (
	exitOK              = 0
	exitAlready         = 1 // already running / not running
	exitBadUsage        = 2
	exitUnsupported     = 3
	exitInsufficientPri = 4
	exitStalePidfile    = 5
)

type Options struct {
	Mode         string // "start" | "stop" | "status"
	Exec         string
	Startas      string
	PidFile      string
	Name         string
	MatchUser    string
	ChUID        string // user[:group] for start
	Group        string
	ChDir        string
	Chroot       string
	Background   bool
	MakePidfile  bool
	Nice         *int
	OOMScoreAdj  *int
	Umask        *uint32
	Stdin        string
	Stdout       string
	Stderr       string
	StdoutLogger string // command whose stdin receives child's stdout
	StderrLogger string // command whose stdin receives child's stderr
	Env          []string
	IOClass      int
	IOLevel      int
	Signal       syscall.Signal
	Retry        string
	Wait         int
	Test         bool
	Quiet        bool
	Verbose      bool
	OKnodo       bool
	Progress     bool
	Interpreted  bool
	Notify       string // "readiness=none" | "readiness=pidfile"
	// Runner-wrapped hardening (require slinit-runner on PATH).
	Capabilities string
	Securebits   string
	NoNewPrivs   bool
	// Real-time scheduling (parent-side sched_setattr on child PID).
	Scheduler         string
	SchedulerPriority int
	Args              []string
}

func main() {
	opts, rest, err := parseArgs(os.Args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(exitBadUsage)
	}
	opts.Args = rest

	if opts.Mode == "" {
		fmt.Fprintln(os.Stderr, "one of --start, --stop, --status is required")
		os.Exit(exitBadUsage)
	}
	if opts.Exec == "" && opts.PidFile == "" && opts.Name == "" {
		fmt.Fprintln(os.Stderr, "at least one of --exec, --pidfile, --name is required")
		os.Exit(exitBadUsage)
	}

	switch opts.Mode {
	case "start":
		os.Exit(cmdStart(opts))
	case "stop":
		os.Exit(cmdStop(opts))
	case "status":
		os.Exit(cmdStatus(opts))
	default:
		fmt.Fprintf(os.Stderr, "unknown mode: %s\n", opts.Mode)
		os.Exit(exitBadUsage)
	}
}

// parseArgs is a manual walker rather than flag.Parse: getopt_long allows
// arbitrarily-ordered short and long flags with fused or spaced values,
// which Go's flag package cannot express.
func parseArgs(args []string) (Options, []string, error) {
	var opts Options
	opts.Signal = syscall.SIGTERM
	var rest []string

	i := 0
	// need is a small helper that pulls the value for a flag whose
	// argument may follow as the next argv element ("--flag val") or
	// be attached ("--flag=val").
	need := func(name string, attached string) (string, error) {
		if attached != "" {
			return attached, nil
		}
		if i+1 >= len(args) {
			return "", fmt.Errorf("flag %s requires an argument", name)
		}
		i++
		return args[i], nil
	}

	for i = 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			rest = append(rest, args[i+1:]...)
			break
		}
		if !strings.HasPrefix(a, "-") || a == "-" {
			rest = append(rest, a)
			continue
		}

		name := a
		attached := ""
		if strings.HasPrefix(name, "--") {
			if eq := strings.IndexByte(name, '='); eq >= 0 {
				attached = name[eq+1:]
				name = name[:eq]
			}
		} else if len(name) > 2 {
			attached = name[2:]
			name = name[:2]
		}

		switch name {
		case "-S", "--start":
			opts.Mode = "start"
		case "-K", "--stop":
			opts.Mode = "stop"
		case "--status":
			opts.Mode = "status"
		case "-x", "--exec":
			v, err := need(name, attached)
			if err != nil {
				return opts, nil, err
			}
			opts.Exec = v
		case "-a", "--startas":
			v, err := need(name, attached)
			if err != nil {
				return opts, nil, err
			}
			opts.Startas = v
		case "-p", "--pidfile":
			v, err := need(name, attached)
			if err != nil {
				return opts, nil, err
			}
			opts.PidFile = v
		case "-n", "--name":
			v, err := need(name, attached)
			if err != nil {
				return opts, nil, err
			}
			opts.Name = v
		case "-u", "--user":
			v, err := need(name, attached)
			if err != nil {
				return opts, nil, err
			}
			opts.MatchUser = v
		case "-c", "--chuid":
			v, err := need(name, attached)
			if err != nil {
				return opts, nil, err
			}
			opts.ChUID = v
		case "-g", "--group":
			v, err := need(name, attached)
			if err != nil {
				return opts, nil, err
			}
			opts.Group = v
		case "-d", "--chdir":
			v, err := need(name, attached)
			if err != nil {
				return opts, nil, err
			}
			opts.ChDir = v
		case "-r", "--chroot":
			v, err := need(name, attached)
			if err != nil {
				return opts, nil, err
			}
			opts.Chroot = v
		case "-b", "--background":
			opts.Background = true
		case "-m", "--make-pidfile":
			opts.MakePidfile = true
		case "-N", "--nicelevel":
			v, err := need(name, attached)
			if err != nil {
				return opts, nil, err
			}
			n, err := strconv.Atoi(v)
			if err != nil {
				return opts, nil, fmt.Errorf("--nicelevel: %w", err)
			}
			opts.Nice = &n
		case "--oom-score-adj":
			v, err := need(name, attached)
			if err != nil {
				return opts, nil, err
			}
			n, err := strconv.Atoi(v)
			if err != nil {
				return opts, nil, fmt.Errorf("--oom-score-adj: %w", err)
			}
			opts.OOMScoreAdj = &n
		case "-k", "--umask":
			v, err := need(name, attached)
			if err != nil {
				return opts, nil, err
			}
			n, err := strconv.ParseUint(v, 8, 32)
			if err != nil {
				return opts, nil, fmt.Errorf("--umask: %w", err)
			}
			u := uint32(n)
			opts.Umask = &u
		case "-0", "--stdin":
			v, err := need(name, attached)
			if err != nil {
				return opts, nil, err
			}
			opts.Stdin = v
		case "-1", "--stdout":
			v, err := need(name, attached)
			if err != nil {
				return opts, nil, err
			}
			opts.Stdout = v
		case "-2", "--stderr":
			v, err := need(name, attached)
			if err != nil {
				return opts, nil, err
			}
			opts.Stderr = v
		case "-e", "--env":
			v, err := need(name, attached)
			if err != nil {
				return opts, nil, err
			}
			opts.Env = append(opts.Env, v)
		case "-I", "--ionice":
			v, err := need(name, attached)
			if err != nil {
				return opts, nil, err
			}
			cls, lvl, err := parseIONice(v)
			if err != nil {
				return opts, nil, err
			}
			opts.IOClass = cls
			opts.IOLevel = lvl
		case "-s", "--signal":
			v, err := need(name, attached)
			if err != nil {
				return opts, nil, err
			}
			sig, err := ParseSignal(v)
			if err != nil {
				return opts, nil, err
			}
			opts.Signal = sig
		case "-R", "--retry":
			v, err := need(name, attached)
			if err != nil {
				return opts, nil, err
			}
			opts.Retry = v
		case "-w", "--wait":
			v, err := need(name, attached)
			if err != nil {
				return opts, nil, err
			}
			n, err := strconv.Atoi(v)
			if err != nil {
				return opts, nil, fmt.Errorf("--wait: %w", err)
			}
			opts.Wait = n
		case "-t", "--test":
			opts.Test = true
		case "-q", "--quiet":
			opts.Quiet = true
		case "-v", "--verbose":
			opts.Verbose = true
		case "-o", "--oknodo":
			opts.OKnodo = true
		case "-P", "--progress":
			opts.Progress = true
		case "-i", "--interpreted":
			opts.Interpreted = true
		case "--stdout-logger", "-3":
			v, err := need(name, attached)
			if err != nil {
				return opts, nil, err
			}
			opts.StdoutLogger = v
		case "--stderr-logger", "-4":
			v, err := need(name, attached)
			if err != nil {
				return opts, nil, err
			}
			opts.StderrLogger = v
		case "--notify":
			v, err := need(name, attached)
			if err != nil {
				return opts, nil, err
			}
			opts.Notify = v
		case "--capabilities":
			v, err := need(name, attached)
			if err != nil {
				return opts, nil, err
			}
			opts.Capabilities = v
		case "--secbits":
			v, err := need(name, attached)
			if err != nil {
				return opts, nil, err
			}
			opts.Securebits = v
		case "--no-new-privs":
			opts.NoNewPrivs = true
		case "--scheduler":
			v, err := need(name, attached)
			if err != nil {
				return opts, nil, err
			}
			opts.Scheduler = v
		case "--scheduler-priority":
			v, err := need(name, attached)
			if err != nil {
				return opts, nil, err
			}
			n, err := strconv.Atoi(v)
			if err != nil {
				return opts, nil, fmt.Errorf("--scheduler-priority: %w", err)
			}
			opts.SchedulerPriority = n
		case "-h", "--help":
			printUsage()
			os.Exit(exitOK)
		case "-V", "--version":
			fmt.Printf("slinit-start-stop-daemon %s\n", version)
			os.Exit(exitOK)
		default:
			return opts, nil, fmt.Errorf("unknown flag: %s", name)
		}
	}
	return opts, rest, nil
}

func parseIONice(s string) (int, int, error) {
	parts := strings.SplitN(s, ":", 2)
	cls, ok := parseIOSchedClass(strings.TrimSpace(parts[0]))
	if !ok {
		return 0, 0, fmt.Errorf("--ionice: bad class %q", parts[0])
	}
	lvl := 0
	if len(parts) == 2 {
		n, err := strconv.Atoi(strings.TrimSpace(parts[1]))
		if err != nil {
			return 0, 0, fmt.Errorf("--ionice: bad level: %w", err)
		}
		if n < 0 || n > 7 {
			return 0, 0, fmt.Errorf("--ionice: level out of range (0-7)")
		}
		lvl = n
	}
	return cls, lvl, nil
}

var version = "dev"

func printUsage() {
	fmt.Print(`Usage: slinit-start-stop-daemon MODE [OPTIONS] -- [ARGS...]

Modes:
  -S, --start                     start the process
  -K, --stop                      stop the matching process(es)
      --status                    query whether the process is running

Matching (at least one required):
  -x, --exec PATH                 match/spawn this executable
  -p, --pidfile PATH              read/write PID from/to this file
  -n, --name NAME                 match /proc/PID/comm
  -u, --user USER                 match process owner

Start options:
  -a, --startas PATH              alternative argv[0]
  -c, --chuid USER[:GROUP]        change to user (and optional group) on start
  -g, --group GROUP               change to this group on start
  -d, --chdir DIR                 change to DIR before exec
  -r, --chroot DIR                chroot to DIR before exec
  -b, --background                fork and detach
  -m, --make-pidfile              write --pidfile after fork
  -N, --nicelevel N               apply nice(2) level
      --oom-score-adj N           set /proc/PID/oom_score_adj
  -k, --umask OCT                 set umask (octal)
  -0, --stdin PATH                redirect child stdin
  -1, --stdout PATH               redirect child stdout (appended)
  -2, --stderr PATH               redirect child stderr (appended)
  -e, --env KEY=VAL               append env var (repeatable)
  -I, --ionice CLASS[:LEVEL]      apply ioprio_set (class rt|be|idle)
  -w, --wait MS                   sleep MS after start (readiness fudge)

Stop options:
  -s, --signal SIG                signal (default TERM)
  -R, --retry SPEC                escalation: "N" or "TERM/30/KILL/5"

Hardening (require slinit-runner on PATH):
      --capabilities LIST         ambient+bounding caps (comma-separated names or numbers)
      --secbits BITS              PR_SET_SECUREBITS mask (names or number)
      --no-new-privs              set PR_SET_NO_NEW_PRIVS before exec

Real-time scheduling:
      --scheduler POLICY          other|fifo|rr|batch|idle (sched_setattr on child)
      --scheduler-priority N      priority for fifo/rr

Logging & readiness:
  -3, --stdout-logger CMD         pipe child stdout to CMD's stdin
  -4, --stderr-logger CMD         pipe child stderr to CMD's stdin
      --notify readiness=MODE     none | manual | pidfile | fd:N | stderr | signal[:SIG]
  -P, --progress                  print dots each second during wait loops
  -i, --interpreted               name/exec match against argv[1] when exe is an interpreter

Common:
  -t, --test                      dry run
  -q, --quiet                     silence non-error output
  -v, --verbose                   extra diagnostics
  -o, --oknodo                    return 0 when already-running / not-running
  -h, --help                      show this help
  -V, --version                   show version

Exit codes: 0=ok  1=already-{running,stopped}  2=usage  3=unsupported
            4=insufficient-privs  5=stale-pidfile
`)
}

func matchCriteriaFrom(opts Options) (MatchCriteria, error) {
	m := MatchCriteria{
		Exec:        opts.Exec,
		Name:        opts.Name,
		PidFile:     opts.PidFile,
		UID:         -1,
		Interpreted: opts.Interpreted,
	}
	if opts.MatchUser != "" {
		uid, err := lookupUID(opts.MatchUser)
		if err != nil {
			return m, err
		}
		m.UID = uid
	}
	return m, nil
}

func lookupUID(s string) (int, error) {
	if n, err := strconv.Atoi(s); err == nil {
		return n, nil
	}
	u, err := user.Lookup(s)
	if err != nil {
		return -1, fmt.Errorf("user %q: %w", s, err)
	}
	return strconv.Atoi(u.Uid)
}

func lookupGID(s string) (int, error) {
	if n, err := strconv.Atoi(s); err == nil {
		return n, nil
	}
	g, err := user.LookupGroup(s)
	if err != nil {
		return -1, fmt.Errorf("group %q: %w", s, err)
	}
	return strconv.Atoi(g.Gid)
}

// resolveExec finds the binary to spawn. --startas wins over --exec so
// the ARG0-vs-executable-path split in Debian's manual is honored.
func resolveExec(opts Options) (path string, argv []string, err error) {
	binary := opts.Startas
	if binary == "" {
		binary = opts.Exec
	}
	if binary == "" {
		return "", nil, fmt.Errorf("--exec (or --startas) is required for --start")
	}
	if !filepath.IsAbs(binary) {
		abs, lerr := exec.LookPath(binary)
		if lerr != nil {
			return "", nil, fmt.Errorf("cannot resolve %q: %w", binary, lerr)
		}
		binary = abs
	}
	argv0 := binary
	if opts.Startas != "" && opts.Exec != "" {
		// Debian convention: --startas is the ARG0, --exec is the binary
		// path. Keep argv[0] pointing at --exec's basename in that case
		// (start-stop-daemon(8) behaviour).
		argv0 = opts.Exec
	}
	argv = append([]string{argv0}, opts.Args...)
	return binary, argv, nil
}

func writeMsg(opts Options, format string, args ...any) {
	if opts.Quiet {
		return
	}
	fmt.Fprintf(os.Stderr, format+"\n", args...)
}

func processAlive(pid int) bool {
	// signal 0 doesn't send anything but returns ESRCH if the process
	// is gone — the standard "is this pid alive?" probe.
	err := syscall.Kill(pid, 0)
	if err == nil {
		return true
	}
	return err != syscall.ESRCH
}

func waitExit(pid int, timeout time.Duration) bool {
	return waitExitProgress(pid, timeout, false)
}

// waitExitProgress polls until pid is gone or timeout elapses. When
// progress is set, prints a "." to stderr each second — the same
// heartbeat OpenRC's --progress emits during stop escalation.
func waitExitProgress(pid int, timeout time.Duration, progress bool) bool {
	dotEvery := time.Second
	nextDot := time.Now().Add(dotEvery)
	printedDot := false
	defer func() {
		if printedDot {
			fmt.Fprintln(os.Stderr)
		}
	}()
	check := func() bool {
		if progress && !time.Now().Before(nextDot) {
			fmt.Fprint(os.Stderr, ".")
			printedDot = true
			nextDot = nextDot.Add(dotEvery)
		}
		return processAlive(pid)
	}
	if timeout == 0 {
		for check() {
			time.Sleep(100 * time.Millisecond)
		}
		return true
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if !check() {
			return true
		}
		time.Sleep(100 * time.Millisecond)
	}
	return !processAlive(pid)
}
