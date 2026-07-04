package main

import (
	"syscall"
	"time"
)

// Exit codes mirror OpenRC's supervise-daemon so init.d wrappers that
// check $? get the same shape they'd get from the C original.
const (
	exitOK              = 0
	exitAlready         = 1 // already running / not running
	exitBadUsage        = 2
	exitUnsupported     = 3
	exitInsufficientPri = 4
	exitStalePidfile    = 5
)

// runnerEnvVar names the env variable the top-level process sets when
// it re-exec's itself as the detached supervisor. The child inspects
// this in main() and jumps straight into runSupervisor(), skipping the
// arg parser.
const runnerEnvVar = "SLINIT_SSD_SUPERVISOR"

// Defaults track OpenRC's supervise-daemon(8) so a script that
// omits a flag sees the same behaviour on both.
const (
	defaultRespawnMax    = 10
	defaultRespawnPeriod = 12 * time.Second
	defaultRespawnDelay  = 0
	defaultDelayStep     = 128 * time.Millisecond
	defaultDelayCap      = 30 * time.Second
	// pidfileReadyTimeout is how long the top-level process waits for
	// the supervisor's pidfile to appear before giving up on the
	// re-exec — matches the ceiling `slinit-start-stop-daemon --notify`
	// uses.
	pidfileReadyTimeout = 30 * time.Second
)

// Options captures the full flag surface. Anything OpenRC accepts is
// parsed; a small tail is accepted-and-ignored (documented in main.go)
// because emulating it faithfully needs shell hooks the init.d script
// itself is expected to own.
type Options struct {
	// Mode.
	Mode string // "start" | "stop" | "signal"

	// Positional service name (first non-flag argv[]).
	Service string

	// Process attrs.
	Exec         string
	PidFile      string
	User         string
	Group        string
	ChDir        string
	Chroot       string
	Nice         *int
	OOMScoreAdj  *int
	Umask        *uint32
	IOClass      int
	IOLevel      int
	Env          []string
	Stdin        string
	Stdout       string
	Stderr       string
	StdoutLogger string
	StderrLogger string

	// Respawn policy.
	RespawnMax       int
	RespawnPeriod    time.Duration
	RespawnDelay     time.Duration
	RespawnDelayStep time.Duration
	RespawnDelayCap  time.Duration

	// Stop escalation.
	Signal syscall.Signal // for --signal
	Retry  string         // for --stop

	// Hardening (runner-wrapped).
	Capabilities string
	Securebits   string
	NoNewPrivs   bool

	// Accepted but currently no-op (documented as such in --help).
	HealthcheckTimer time.Duration
	HealthcheckDelay time.Duration
	Notify           string // fd:N | socket:ready — only fd:N acted on for now

	// UX.
	Verbose bool

	// Tail: everything after `--` — passed straight to the daemon.
	Args []string
}
