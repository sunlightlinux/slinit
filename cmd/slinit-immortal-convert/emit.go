package main

import (
	"fmt"
	"io"
	"strings"
)

// bytesPerMegabyte is what immortal means by `log.size`, which its
// README documents as MegaBytes. slinit's logfile-max-size is bytes.
const bytesPerMegabyte = 1024 * 1024

// emitSlinitFile writes the converted service and returns the warnings
// the mapping itself produced (the parser's are separate).
//
// Every immortal key either becomes a directive here or appears in a
// warning. There is no third category: a key that is recognised,
// dropped and not mentioned is how a migration tool loses someone's
// configuration without telling them.
func emitSlinitFile(w io.Writer, cfg *immortalConfig, name string) []warning {
	var warns []warning

	fmt.Fprintf(w, "# Converted from an immortal run.yml by slinit-immortal-convert.\n")
	fmt.Fprintf(w, "# Service name taken from the file name, as immortaldir does.\n#\n")
	fmt.Fprintf(w, "# Check this file before trusting it: a converter maps what the two\n")
	fmt.Fprintf(w, "# systems share, and the notes on stderr say what it could not.\n\n")

	// immortal supervises one long-running command, which is slinit's
	// `process`. A service that exits on its own is immortal's business
	// to restart, so `process` is right rather than `scripted`.
	fmt.Fprintf(w, "type = process\n")
	fmt.Fprintf(w, "command = %s\n", cfg.Cmd)

	if cfg.Cwd != "" {
		fmt.Fprintf(w, "working-dir = %s\n", cfg.Cwd)
	}
	if cfg.User != "" {
		fmt.Fprintf(w, "run-as = %s\n", cfg.User)
	}

	// `wait` delays the first start. start-delay is a timer, so unlike
	// the pre-start-command trick it does not hold slinit's scheduling
	// lock while it waits.
	if cfg.Wait > 0 {
		fmt.Fprintf(w, "start-delay = %d\n", cfg.Wait)
	}

	// retries: -1 forever (immortal's default), 0 run once, N times.
	switch {
	case !cfg.HasRetries || cfg.Retries < 0:
		fmt.Fprintf(w, "restart = yes\n")
	case cfg.Retries == 0:
		fmt.Fprintf(w, "restart = no\n")
	default:
		fmt.Fprintf(w, "restart = yes\n")
		fmt.Fprintf(w, "restart-limit-count = %d\n", cfg.Retries)
	}

	// immortal's `env:` is an inline map. slinit has no directive that
	// sets a variable inline — only env-file, env-dir and
	// env-generator, which all read from somewhere else. So the
	// variables are written into the file as a comment the operator can
	// lift straight into an env-file, and the warning says to. Emitting
	// a made-up `env =` directive instead produced a service file that
	// would not load at all, which slinit-check caught.
	if len(cfg.EnvKeys) > 0 {
		fmt.Fprintf(w, "# env: from the immortal config. slinit has no inline\n")
		fmt.Fprintf(w, "# environment directive; put these in a file and point\n")
		fmt.Fprintf(w, "# env-file at it, or one file per variable for env-dir.\n")
		for _, k := range cfg.EnvKeys {
			fmt.Fprintf(w, "#   %s=%s\n", k, cfg.Env[k])
		}
		var pairs []string
		for _, k := range cfg.EnvKeys {
			pairs = append(pairs, k+"="+cfg.Env[k])
		}
		warns = append(warns, warning{"WARN", fmt.Sprintf(
			"env: %s not mapped — slinit sets a service's environment from a file "+
				"(env-file), a directory (env-dir) or a command (env-generator), "+
				"not inline. The values are in a comment in the output; move them "+
				"to a file and add `env-file = <path>`",
			strings.Join(pairs, " "))})
	}

	// require: the named services must already be RUNNING, and immortal
	// does not start them — "if foo and bar are not running, the service
	// will not be started". That is a precondition, not a dependency,
	// which is why it becomes assert-service-started and not depends-on:
	// depends-on would start them, which is the opposite instruction.
	for _, dep := range cfg.Require {
		fmt.Fprintf(w, "assert-service-started = %s\n", dep)
	}
	if len(cfg.Require) > 0 {
		warns = append(warns, warning{"NOTE", fmt.Sprintf(
			"require: %s became assert-service-started, which does NOT start them "+
				"(neither does immortal). Add `depends-on:` if you want slinit to "+
				"bring them up, and `after:` if they could be starting at the same moment",
			strings.Join(cfg.Require, ", "))})
	}

	if cfg.RequireCmd != "" {
		fmt.Fprintf(w, "exec-condition = %s\n", cfg.RequireCmd)
		warns = append(warns, warning{"NOTE",
			"require_cmd became exec-condition, which SKIPS the service when the " +
				"command fails. Use assert-exec-condition to fail the start instead, " +
				"which is closer to immortal refusing to run"})
	}

	// post_exit gets the exit code as its argument in immortal.
	// finish-command gets the exit code and the wait status, so the
	// script keeps working and gains a second argument.
	if cfg.PostExit != "" {
		fmt.Fprintf(w, "finish-command = %s\n", cfg.PostExit)
		warns = append(warns, warning{"NOTE",
			"post_exit became finish-command. immortal passes it the exit code; " +
				"slinit passes the exit code AND the wait status, so an existing " +
				"script still reads $1 the same way"})
	}

	warns = append(warns, emitLogging(w, cfg)...)
	warns = append(warns, emitPid(w, cfg)...)
	return warns
}

func emitLogging(w io.Writer, cfg *immortalConfig) []warning {
	var warns []warning

	if cfg.Log.File != "" {
		fmt.Fprintf(w, "logfile = %s\n", cfg.Log.File)
	}
	if cfg.Logger != "" {
		fmt.Fprintf(w, "output-logger = %s\n", cfg.Logger)
	}
	// immortal writes to the file and the logger at the same time
	// through a multiwriter; slinit does too when both are named.
	if cfg.Log.File != "" && cfg.Logger != "" {
		warns = append(warns, warning{"NOTE",
			"log.file and logger are both set: slinit writes to both, as immortal's " +
				"multiwriter does"})
	}

	if cfg.Stderr.File != "" {
		fmt.Fprintf(w, "stderr-logfile = %s\n", cfg.Stderr.File)
	}

	// Rotation. immortal keeps a separate set of knobs per stream;
	// slinit's logfile-* settings govern both files, so a stderr block
	// that asks for different rotation cannot be honoured.
	if cfg.Log.Size > 0 {
		fmt.Fprintf(w, "logfile-max-size = %d\n", cfg.Log.Size*bytesPerMegabyte)
	}
	if cfg.Log.Num > 0 {
		fmt.Fprintf(w, "logfile-max-files = %d\n", cfg.Log.Num)
	}
	if cfg.Log.Age > 0 {
		fmt.Fprintf(w, "logfile-rotate-time = %d\n", cfg.Log.Age)
	}
	// immortal's `timestamp` is a boolean; slinit's log-timestamp names
	// a format (tai64n, human, iso8601) and rejects anything else — an
	// earlier `= yes` here made the service file unloadable. iso8601 is
	// the closest to what immortal prepends and the one a human reading
	// the file can also parse.
	if cfg.Log.Timestamp || cfg.Stderr.Timestamp {
		fmt.Fprintf(w, "log-timestamp = iso8601\n")
	}

	if differsInRotation(cfg.Log, cfg.Stderr) {
		warns = append(warns, warning{"WARN",
			"stderr asks for different rotation from log: slinit's logfile-* " +
				"settings apply to both streams, so the log block's values were " +
				"used for both. Check whether that is acceptable"})
	}

	// immortal skips rotation entirely when age, num and size are all
	// absent. slinit does the same for a plain logfile, so silence here
	// is correct rather than a gap — but say so, because an operator
	// migrating a file with no rotation may expect to have gained some.
	if cfg.Log.File != "" && cfg.Log.Age == 0 && cfg.Log.Num == 0 && cfg.Log.Size == 0 {
		warns = append(warns, warning{"NOTE",
			"log has no age/num/size, so no rotation was emitted — immortal does " +
				"not rotate in that case either"})
	}
	return warns
}

// differsInRotation reports whether the stderr block asks for rotation
// that is not identical to the log block's.
func differsInRotation(log, errStream logBlock) bool {
	if !errStream.set || errStream.File == "" {
		return false
	}
	if errStream.Age == 0 && errStream.Num == 0 && errStream.Size == 0 {
		return false
	}
	return errStream.Age != log.Age || errStream.Num != log.Num || errStream.Size != log.Size
}

func emitPid(w io.Writer, cfg *immortalConfig) []warning {
	var warns []warning

	// pid.child is where immortal WRITES the supervised process's pid.
	// slinit's pid-file is the opposite: a file the service writes and
	// slinit reads, for a daemon that forks. Mapping one to the other
	// would invert the direction, so it is reported instead.
	if cfg.PidChild != "" {
		warns = append(warns, warning{"WARN", fmt.Sprintf(
			"pid.child (%s) not mapped: immortal WRITES the child's pid there, "+
				"while slinit's pid-file is a file the daemon writes and slinit "+
				"READS (type = bgprocess). If the service forks and writes that "+
				"file itself, use `type = bgprocess` + `pid-file = %s`",
			cfg.PidChild, cfg.PidChild)})
	}
	if cfg.PidParent != "" {
		warns = append(warns, warning{"NOTE", fmt.Sprintf(
			"pid.parent (%s) not mapped: it is immortal's own supervisor pid, and "+
				"slinit is PID 1 rather than one supervisor per service",
			cfg.PidParent)})
	}
	if cfg.PidFollow != "" {
		warns = append(warns, warning{"WARN", fmt.Sprintf(
			"pid.follow (%s) not mapped: supervising a process slinit did not "+
				"start has no equivalent yet. The service will be started and "+
				"supervised by slinit instead, which is a different arrangement — "+
				"check it is the one you want", cfg.PidFollow)})
	}
	return warns
}
