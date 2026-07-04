package main

import (
	"fmt"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// parseArgs is the same style getopt_long-lite walker used by
// slinit-start-stop-daemon: fused (-Nvalue), spaced (-N value), and
// `--flag=value` all work. The first positional is the service name,
// the rest go into opts.Args (post-`--` is honoured explicitly).
func parseArgs(args []string) (Options, error) {
	opts := Options{
		Signal:           syscall.SIGTERM,
		RespawnMax:       defaultRespawnMax,
		RespawnPeriod:    defaultRespawnPeriod,
		RespawnDelay:     defaultRespawnDelay,
		RespawnDelayStep: defaultDelayStep,
		RespawnDelayCap:  defaultDelayCap,
	}

	positional := []string{}
	i := 0
	need := func(name, attached string) (string, error) {
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
			opts.Args = append(opts.Args, args[i+1:]...)
			break
		}
		if !strings.HasPrefix(a, "-") || a == "-" {
			positional = append(positional, a)
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
			// Short flag with attached value: -N10, -x/bin/foo.
			attached = name[2:]
			name = name[:2]
		}

		switch name {
		case "-S", "--start":
			opts.Mode = "start"
		case "-K", "--stop":
			opts.Mode = "stop"
		case "-s", "--signal":
			v, err := need(name, attached)
			if err != nil {
				return opts, err
			}
			sig, err := ParseSignal(v)
			if err != nil {
				return opts, err
			}
			opts.Signal = sig
			// --signal implies mode=signal unless a mode was already set.
			if opts.Mode == "" {
				opts.Mode = "signal"
			}
		case "-x", "--exec":
			v, err := need(name, attached)
			if err != nil {
				return opts, err
			}
			opts.Exec = v
		case "-p", "--pidfile":
			v, err := need(name, attached)
			if err != nil {
				return opts, err
			}
			opts.PidFile = v
		case "-u", "--user":
			v, err := need(name, attached)
			if err != nil {
				return opts, err
			}
			// user[:group] shorthand.
			if idx := strings.IndexByte(v, ':'); idx >= 0 {
				opts.User = v[:idx]
				if opts.Group == "" {
					opts.Group = v[idx+1:]
				}
			} else {
				opts.User = v
			}
		case "-g", "--group":
			v, err := need(name, attached)
			if err != nil {
				return opts, err
			}
			opts.Group = v
		case "-d", "--chdir":
			v, err := need(name, attached)
			if err != nil {
				return opts, err
			}
			opts.ChDir = v
		case "-r", "--chroot":
			v, err := need(name, attached)
			if err != nil {
				return opts, err
			}
			opts.Chroot = v
		case "-N", "--nicelevel":
			v, err := need(name, attached)
			if err != nil {
				return opts, err
			}
			n, err := strconv.Atoi(v)
			if err != nil {
				return opts, fmt.Errorf("--nicelevel: %w", err)
			}
			opts.Nice = &n
		case "--oom-score-adj":
			v, err := need(name, attached)
			if err != nil {
				return opts, err
			}
			n, err := strconv.Atoi(v)
			if err != nil {
				return opts, fmt.Errorf("--oom-score-adj: %w", err)
			}
			opts.OOMScoreAdj = &n
		case "-k", "--umask":
			v, err := need(name, attached)
			if err != nil {
				return opts, err
			}
			n, err := strconv.ParseUint(v, 8, 32)
			if err != nil {
				return opts, fmt.Errorf("--umask: %w", err)
			}
			u := uint32(n)
			opts.Umask = &u
		case "-I", "--ionice":
			v, err := need(name, attached)
			if err != nil {
				return opts, err
			}
			cls, lvl, err := parseIONice(v)
			if err != nil {
				return opts, err
			}
			opts.IOClass = cls
			opts.IOLevel = lvl
		case "-e", "--env":
			v, err := need(name, attached)
			if err != nil {
				return opts, err
			}
			opts.Env = append(opts.Env, v)
		case "-0", "--stdin":
			v, err := need(name, attached)
			if err != nil {
				return opts, err
			}
			opts.Stdin = v
		case "-1", "--stdout":
			v, err := need(name, attached)
			if err != nil {
				return opts, err
			}
			opts.Stdout = v
		case "-2", "--stderr":
			v, err := need(name, attached)
			if err != nil {
				return opts, err
			}
			opts.Stderr = v
		case "--stdout-logger":
			v, err := need(name, attached)
			if err != nil {
				return opts, err
			}
			opts.StdoutLogger = v
		case "--stderr-logger":
			v, err := need(name, attached)
			if err != nil {
				return opts, err
			}
			opts.StderrLogger = v
		case "-D", "--respawn-delay":
			v, err := need(name, attached)
			if err != nil {
				return opts, err
			}
			d, err := parseDuration(v)
			if err != nil {
				return opts, fmt.Errorf("--respawn-delay: %w", err)
			}
			opts.RespawnDelay = d
		case "-P", "--respawn-period":
			v, err := need(name, attached)
			if err != nil {
				return opts, err
			}
			d, err := parseDuration(v)
			if err != nil {
				return opts, fmt.Errorf("--respawn-period: %w", err)
			}
			opts.RespawnPeriod = d
		case "-m", "--respawn-max":
			v, err := need(name, attached)
			if err != nil {
				return opts, err
			}
			n, err := strconv.Atoi(v)
			if err != nil {
				return opts, fmt.Errorf("--respawn-max: %w", err)
			}
			opts.RespawnMax = n
		case "--respawn-delay-step":
			v, err := need(name, attached)
			if err != nil {
				return opts, err
			}
			d, err := parseDuration(v)
			if err != nil {
				return opts, fmt.Errorf("--respawn-delay-step: %w", err)
			}
			opts.RespawnDelayStep = d
		case "--respawn-delay-cap":
			v, err := need(name, attached)
			if err != nil {
				return opts, err
			}
			d, err := parseDuration(v)
			if err != nil {
				return opts, fmt.Errorf("--respawn-delay-cap: %w", err)
			}
			opts.RespawnDelayCap = d
		case "-R", "--retry":
			v, err := need(name, attached)
			if err != nil {
				return opts, err
			}
			opts.Retry = v
		case "-a", "--healthcheck-timer":
			v, err := need(name, attached)
			if err != nil {
				return opts, err
			}
			d, err := parseDuration(v)
			if err != nil {
				return opts, fmt.Errorf("--healthcheck-timer: %w", err)
			}
			opts.HealthcheckTimer = d
		case "-A", "--healthcheck-delay":
			v, err := need(name, attached)
			if err != nil {
				return opts, err
			}
			d, err := parseDuration(v)
			if err != nil {
				return opts, fmt.Errorf("--healthcheck-delay: %w", err)
			}
			opts.HealthcheckDelay = d
		case "--notify":
			v, err := need(name, attached)
			if err != nil {
				return opts, err
			}
			opts.Notify = v
		case "--capabilities":
			v, err := need(name, attached)
			if err != nil {
				return opts, err
			}
			opts.Capabilities = v
		case "--secbits":
			v, err := need(name, attached)
			if err != nil {
				return opts, err
			}
			opts.Securebits = v
		case "--no-new-privs":
			opts.NoNewPrivs = true
		case "-v", "--verbose":
			opts.Verbose = true
		case "-h", "--help":
			printUsage()
			return opts, errHelp
		case "-V", "--version":
			return opts, errVersion
		default:
			return opts, fmt.Errorf("unknown flag: %s", name)
		}
	}

	// First positional is the service name; extras become opts.Args
	// (mirrors OpenRC's positional semantics for `supervise-daemon
	// svcname args...`).
	if len(positional) > 0 {
		opts.Service = positional[0]
		opts.Args = append(positional[1:], opts.Args...)
	}
	// Default to --start when no mode flag is present (OpenRC's
	// behaviour per the manpage: "if --stop or --signal is not
	// provided, then we assume we are starting the daemon").
	if opts.Mode == "" {
		opts.Mode = "start"
	}
	return opts, nil
}

// parseIONice mirrors slinit-start-stop-daemon's parser (rt|be|idle
// or numeric class, optional 0-7 level).
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

// parseDuration accepts OpenRC's syntax: bare integer = seconds,
// otherwise an integer followed by ms | sec | min | hour. Go's
// time.ParseDuration understands the "s" | "ms" | "m" | "h" forms
// too so we normalise into that flavour.
func parseDuration(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty duration")
	}
	if n, err := strconv.Atoi(s); err == nil {
		return time.Duration(n) * time.Second, nil
	}
	// Try direct time.ParseDuration for "500ms" etc.
	if d, err := time.ParseDuration(s); err == nil {
		return d, nil
	}
	// Try OpenRC-style: split integer + unit.
	for suffixIdx := len(s); suffixIdx > 0; suffixIdx-- {
		if _, err := strconv.Atoi(s[:suffixIdx]); err == nil {
			num, _ := strconv.Atoi(s[:suffixIdx])
			unit := strings.TrimSpace(s[suffixIdx:])
			switch unit {
			case "ms":
				return time.Duration(num) * time.Millisecond, nil
			case "sec", "s":
				return time.Duration(num) * time.Second, nil
			case "min", "m":
				return time.Duration(num) * time.Minute, nil
			case "hour", "h":
				return time.Duration(num) * time.Hour, nil
			}
			break
		}
	}
	return 0, fmt.Errorf("cannot parse duration %q", s)
}

// Sentinel errors used to signal that main() should exit with 0 and
// skip normal dispatch — --help and --version are the two paths.
var (
	errHelp    = fmt.Errorf("help requested")
	errVersion = fmt.Errorf("version requested")
)
