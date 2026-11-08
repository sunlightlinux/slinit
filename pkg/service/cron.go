package service

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/sunlightlinux/slinit/pkg/process"
)

// cronPersistDir is the directory where CronRunner writes lastRun
// timestamps for services marked cron-persistent=yes. Overridable
// via SetCronPersistDir for tests / user-mode instances.
var cronPersistDir = "/var/lib/slinit/cron"

// SetCronPersistDir overrides the on-disk lastRun store location.
// Meant for tests and user-mode daemons where /var/lib/slinit is
// unwritable; production use should stick with the default.
func SetCronPersistDir(dir string) { cronPersistDir = dir }

// CronRunner manages a periodic sub-task for a service.
// When the parent service reaches STARTED, Start() is called to begin
// the periodic execution. Stop() blocks until any in-progress execution
// completes, then returns.
//
// Two scheduling modes:
//
//   - Interval mode (default): runs every `interval` duration.
//   - Calendar mode: when `calendar != nil`, fire times are derived from
//     a systemd-style OnCalendar expression. `interval` is unused.
//
// Optional modifiers, all of which apply to both modes:
//
//   - randomizedDelay: an upper bound on jitter added to each fire time,
//     drawn uniformly from [0, d).
//   - fixedRandomDelay: draw that offset once, from the machine-id and
//     the service name, instead of per fire. systemd FixedRandomDelay=.
//   - accuracy: snap fire times up to a bucket so timers coalesce.
//   - persistent: if a fire was missed while the daemon was down, run
//     once immediately on startup to catch up. The last-run instant is
//     kept on disk under cronPersistDir, so catch-up survives a reboot.
type CronRunner struct {
	command         []string
	interval        time.Duration
	delay           time.Duration
	onError         string // "continue" (default) or "stop"
	calendar        *CalendarSpec
	randomizedDelay time.Duration
	// accuracy: snap computed fire times to a multiple of this duration
	// so many timers coalesce onto a small set of wake-ups. 0 = no
	// coalescing (fire at exact calendar match). Matches systemd
	// AccuracySec= semantics: the effective fire time is picked from
	// [nominal, nominal + accuracy) so a fleet averages out.
	accuracy   time.Duration
	persistent bool
	lastRun    time.Time

	// fixedRandomDelay pins the jitter offset for the life of the host
	// instead of redrawing it. fixedOffset caches the derived value;
	// both are touched only from the cron loop goroutine.
	fixedRandomDelay bool
	fixedOffset      time.Duration
	fixedOffsetSet   bool

	svc    Service // parent service (for logging context)
	logger ServiceLogger

	mu      sync.Mutex
	running bool          // true while a cron-command execution is in progress
	stopCh  chan struct{} // closed to signal the cron loop to exit
	doneCh  chan struct{} // closed when the cron loop has fully exited
}

// Schedule describes when the task runs, for the log. A calendar runner has no
// interval: printing its zero value read as a task in a tight loop.
func (cr *CronRunner) Schedule(now time.Time) string {
	if cr.calendar == nil {
		return fmt.Sprintf("interval=%v", cr.interval)
	}
	return "calendar, next at " + cr.calendar.NextAfter(now).Format("2006-01-02 15:04:05")
}

// NewCronRunner creates a new CronRunner in interval mode.
func NewCronRunner(svc Service, cmd []string, interval, delay time.Duration, onError string, logger ServiceLogger) *CronRunner {
	if onError == "" {
		onError = "continue"
	}
	if interval <= 0 {
		interval = 60 * time.Second
	}
	return &CronRunner{
		command:  cmd,
		interval: interval,
		delay:    delay,
		onError:  onError,
		svc:      svc,
		logger:   logger,
	}
}

// NewCalendarCronRunner creates a new CronRunner in calendar mode. The
// command is invoked at every instant matching `calendar`. Optional
// `randomizedDelay` (>=0) adds uniform jitter to each fire time;
// `persistent` enables catch-up on startup when a fire was missed.
func NewCalendarCronRunner(
	svc Service, cmd []string, calendar *CalendarSpec,
	randomizedDelay time.Duration, persistent bool,
	onError string, logger ServiceLogger,
) *CronRunner {
	if onError == "" {
		onError = "continue"
	}
	return &CronRunner{
		command:         cmd,
		calendar:        calendar,
		randomizedDelay: randomizedDelay,
		persistent:      persistent,
		onError:         onError,
		svc:             svc,
		logger:          logger,
	}
}

// SetAccuracy configures wake-up coalescing. When non-zero, each
// computed fire time is snapped up to a multiple of `d` (0 disables).
// Matches systemd AccuracySec=. Safe to call before Start().
func (cr *CronRunner) SetAccuracy(d time.Duration) { cr.accuracy = d }

// SetRandomizedDelay sets the jitter bound and whether the offset is
// fixed per host. Interval mode gets its modifiers this way; calendar
// mode can take the bound through NewCalendarCronRunner as well, and
// this overrides it. Safe to call before Start().
func (cr *CronRunner) SetRandomizedDelay(d time.Duration, fixed bool) {
	cr.randomizedDelay = d
	cr.fixedRandomDelay = fixed
}

// SetPersistent enables missed-fire catch-up from the on-disk last-run
// record. Safe to call before Start().
func (cr *CronRunner) SetPersistent(b bool) { cr.persistent = b }

// cronStopWait is how long a stopping runner lets a run finish by itself, and
// cronStopGrace how long a run told to end gets between SIGTERM and SIGKILL.
// Variables for the tests. The wait holds a shutdown back, so it has a limit;
// killing at once would be the worst moment to interrupt a system update.
var (
	cronStopWait  = time.Minute
	cronStopGrace = 10 * time.Second
)

// Start launches the periodic execution goroutine. Safe to call from any
// goroutine, and again after Stop: a service restarted used to come back with
// a runner that refused to start, so its task never ran again until a reboot.
func (cr *CronRunner) Start() {
	cr.mu.Lock()
	if cr.stopCh != nil {
		select {
		case <-cr.stopCh:
		default:
			select {
			case <-cr.doneCh: // ended on its own, by on-error=stop
			default:
				cr.mu.Unlock()
				return // already running
			}
		}
		doneCh := cr.doneCh
		cr.mu.Unlock()
		<-doneCh
		cr.mu.Lock()
	}
	cr.stopCh = make(chan struct{})
	cr.doneCh = make(chan struct{})
	cr.mu.Unlock()

	go cr.loop()
}

// active reports whether the loop is running and has not been told to stop.
func (cr *CronRunner) active() bool {
	cr.mu.Lock()
	defer cr.mu.Unlock()
	if cr.stopCh == nil {
		return false
	}
	select {
	case <-cr.stopCh:
		return false
	case <-cr.doneCh:
		return false
	default:
		return true
	}
}

// Stop signals the cron loop to exit and waits for a command under way, which
// has cronStopWait to finish before it is ended (see executeCommand). Safe to
// call multiple times.
func (cr *CronRunner) Stop() {
	cr.mu.Lock()
	if cr.stopCh == nil {
		cr.mu.Unlock()
		return
	}
	select {
	case <-cr.stopCh:
		// already stopped
	default:
		close(cr.stopCh)
	}
	doneCh := cr.doneCh
	cr.mu.Unlock()

	if doneCh != nil {
		<-doneCh
	}
}

// IsRunning returns true if a cron-command is currently executing.
func (cr *CronRunner) IsRunning() bool {
	cr.mu.Lock()
	defer cr.mu.Unlock()
	return cr.running
}

func (cr *CronRunner) loop() {
	defer close(cr.doneCh)

	if cr.calendar != nil {
		cr.loopCalendar()
		return
	}
	cr.loopInterval()
}

// loopInterval drives the "every N seconds" schedule.
//
// Both modifiers used to be dropped here: jitter and persistence reached
// only the calendar loop, so `cron-interval` plus `cron-randomized-delay`
// did nothing at all. Jitter now applies to the initial delay and to
// every period, and a missed period is caught up from the on-disk record.
//
// Honouring cron-persistent for a monotonic schedule is a deliberate
// divergence: systemd's Persistent= applies to OnCalendar= only, because
// a monotonic timer has no absolute time to have missed. slinit keeps the
// last-run instant on disk either way, which makes "the period elapsed
// while we were down" a well-defined question — and answering it is what
// an operator writing `cron-interval = 24h` plainly means.
func (cr *CronRunner) loopInterval() {
	if cr.persistent {
		if t, ok := cr.readPersisted(); ok {
			cr.lastRun = t
		}
	}

	// A period that elapsed while the daemon was down: run now and skip
	// the initial delay, which exists to stagger a normal start rather
	// than to hold back a run that is already late.
	caughtUp := false
	if cr.persistent && !cr.lastRun.IsZero() {
		if missed := cr.lastRun.Add(cr.interval); missed.Before(time.Now()) {
			cr.logger.Info("Service '%s': cron catch-up (interval elapsed at %v while down)",
				cr.svc.Name(), missed.Format(time.RFC3339))
			if !cr.runOnce() {
				return
			}
			cr.persist(time.Now())
			caughtUp = true
		}
	}

	if !caughtUp {
		if d := cr.delay + cr.jitterFor(); d > 0 && !cr.sleep(d) {
			return
		}
		if !cr.runOnce() {
			return
		}
		cr.persist(time.Now())
	}

	// The ticker keeps the cadence anchored to the interval rather than to
	// how long each run took; jitter is an extra wait after each tick, so
	// a slow run cannot make the schedule drift. A jitter bound larger
	// than the interval therefore stretches the effective period — the
	// man page says to keep it smaller.
	ticker := time.NewTicker(cr.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			if j := cr.jitterFor(); j > 0 && !cr.sleep(j) {
				return
			}
			if !cr.runOnce() {
				return
			}
			cr.persist(time.Now())
		case <-cr.stopCh:
			return
		}
	}
}

// sleep waits for d, reporting false if a stop was requested first.
func (cr *CronRunner) sleep(d time.Duration) bool {
	if d <= 0 {
		return true
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-cr.stopCh:
		return false
	}
}

// persist records a run instant when persistence is enabled, and does
// nothing otherwise, so callers need no condition of their own.
func (cr *CronRunner) persist(t time.Time) {
	if cr.persistent {
		cr.writePersisted(t)
	}
}

// jitterFor returns the offset to add to a nominal fire time. The
// configured value is an upper bound, never the offset itself.
func (cr *CronRunner) jitterFor() time.Duration {
	if cr.randomizedDelay <= 0 {
		return 0
	}
	if cr.fixedRandomDelay {
		return cr.stableJitter()
	}
	return time.Duration(rand.Int63n(int64(cr.randomizedDelay)))
}

// stableJitter derives an offset in [0, randomizedDelay) from the host's
// machine-id and the service name, so this host fires in the same slot
// every time while a fleet still spreads across the window. systemd's
// FixedRandomDelay=. Same FNV-1a-over-machine-id shape as
// ConditionFraction= in predicate.go, separator byte included, so the
// two features bucket independently.
//
// Derived on first use rather than at construction: PID 1 parses service
// descriptions before the filesystem holding /etc/machine-id is
// necessarily readable, and the first fire is always later than that.
func (cr *CronRunner) stableJitter() time.Duration {
	if cr.fixedOffsetSet {
		return cr.fixedOffset
	}
	cr.fixedOffsetSet = true

	mid, err := os.ReadFile("/etc/machine-id")
	if err != nil {
		// Nothing stable to key on. One draw kept for the life of the
		// process still avoids the per-fire herding the directive is
		// about; it just cannot survive a restart.
		cr.fixedOffset = time.Duration(rand.Int63n(int64(cr.randomizedDelay)))
		cr.logger.Info(
			"Service '%s': cron-fixed-random-delay: /etc/machine-id: %v; "+
				"using a per-process offset of %v instead",
			cr.svc.Name(), err, cr.fixedOffset)
		return cr.fixedOffset
	}
	h := fnv.New32a()
	h.Write([]byte(strings.TrimSpace(string(mid))))
	h.Write([]byte{0})
	h.Write([]byte(cr.svc.Name()))
	cr.fixedOffset = time.Duration(uint64(h.Sum32()) % uint64(cr.randomizedDelay))
	return cr.fixedOffset
}

// snapToAccuracy moves a fire time up to the next accuracy bucket so many
// timers coalesce onto few wake-ups.
//
// Rounds up, where this used to truncate: systemd's AccuracySec= window
// is [nominal, nominal+accuracy], and firing before the time the operator
// wrote is wrong whatever it buys in coalescing. Buckets align to UTC, so
// in a zone whose offset is not a whole number of buckets the boundaries
// sit somewhere inside the hour — which costs nothing, since coalescing
// only needs every timer on the host to agree with the others.
func snapToAccuracy(t time.Time, acc time.Duration) time.Time {
	if acc <= 0 {
		return t
	}
	if b := t.Truncate(acc); b.Before(t) {
		return b.Add(acc)
	}
	return t
}

// loopCalendar computes successive fire times from the CalendarSpec.
// On startup, if persistent and lastRun indicates a missed fire, runs
// once immediately to catch up; otherwise sleeps until the next match.
// Random jitter (if configured) is added between fire times.
func (cr *CronRunner) loopCalendar() {
	now := time.Now()
	// Read the on-disk lastRun so catch-up survives a daemon restart or
	// soft-reboot. In-memory cr.lastRun overrides only when the on-disk
	// read fails (missing file, empty, unparseable) — the latter is
	// treated as "no previous run", which matches the first-boot case.
	if cr.persistent {
		if t, ok := cr.readPersisted(); ok {
			cr.lastRun = t
		}
	}
	// Catch-up: if persistent and the next scheduled fire after lastRun
	// is in the past, run now once before resuming.
	if cr.persistent && !cr.lastRun.IsZero() {
		nextMissed := cr.calendar.NextAfter(cr.lastRun)
		if !nextMissed.IsZero() && nextMissed.Before(now) {
			cr.logger.Info(
				"Service '%s': calendar catch-up (missed fire at %v)",
				cr.svc.Name(), nextMissed)
			if !cr.runOnce() {
				return
			}
			cr.persist(nextMissed)
		}
	}

	for {
		now = time.Now()
		next := cr.calendar.NextAfter(now)
		if next.IsZero() {
			// Spec has no future match — exit quietly. With a year
			// constraint this is the normal end of a one-shot date.
			return
		}
		// Jitter so a fleet does not herd onto the same instant, then
		// coalesce onto a bucket. Order matters: snapping last keeps the
		// guarantee that the fire is never earlier than the calendar
		// says, and a bucket coarser than the jitter window will undo the
		// spread — the man page says to keep accuracy the smaller of the
		// two.
		next = snapToAccuracy(next.Add(cr.jitterFor()), cr.accuracy)

		if !cr.sleep(time.Until(next)) {
			return
		}
		cr.lastRun = next
		if !cr.runOnce() {
			return
		}
		cr.persist(next)
	}
}

// persistPath returns the on-disk path for this cron's lastRun record.
// Uses the parent service's name, sanitised so a "/" in the name can't
// escape the store directory.
func (cr *CronRunner) persistPath() string {
	name := strings.ReplaceAll(cr.svc.Name(), "/", "_")
	return filepath.Join(cronPersistDir, name)
}

// readPersisted returns the parsed lastRun timestamp; the bool is false
// on any error (missing file / unreadable / unparseable), matching the
// "no previous run" case exactly.
func (cr *CronRunner) readPersisted() (time.Time, bool) {
	data, err := os.ReadFile(cr.persistPath())
	if err != nil {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(string(data)))
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

// writePersisted saves the lastRun timestamp atomically (temp + rename)
// so a crash mid-write can't leave a torn file. Failures are logged
// but don't propagate — persistence is best-effort by design.
func (cr *CronRunner) writePersisted(t time.Time) {
	if err := os.MkdirAll(cronPersistDir, 0755); err != nil {
		cr.logger.Error("Service '%s': cron persist mkdir: %v", cr.svc.Name(), err)
		return
	}
	path := cr.persistPath()
	tmp, err := os.CreateTemp(cronPersistDir, filepath.Base(path)+".*")
	if err != nil {
		cr.logger.Error("Service '%s': cron persist create: %v", cr.svc.Name(), err)
		return
	}
	if _, err := fmt.Fprintln(tmp, t.Format(time.RFC3339Nano)); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		cr.logger.Error("Service '%s': cron persist write: %v", cr.svc.Name(), err)
		return
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		cr.logger.Error("Service '%s': cron persist close: %v", cr.svc.Name(), err)
		return
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		os.Remove(tmp.Name())
		cr.logger.Error("Service '%s': cron persist rename: %v", cr.svc.Name(), err)
	}
}

// runOnce executes the cron command once. Returns false if the loop
// should exit (stop requested or on-error=stop and command failed).
func (cr *CronRunner) runOnce() bool {
	// Check stop before starting
	select {
	case <-cr.stopCh:
		return false
	default:
	}

	cr.mu.Lock()
	cr.running = true
	cr.mu.Unlock()

	err := cr.executeCommand()

	cr.mu.Lock()
	cr.running = false
	cr.mu.Unlock()

	if err != nil {
		cr.logger.Error("cron-command for '%s' failed: %v", cr.svc.Name(), err)
		if cr.onError == "stop" {
			return false
		}
	}

	return true
}

// fireContext bounds one run. An interval run may not outlive its period. A
// calendar run has no such limit: it used to get a minute, which killed a
// nightly system update part way through. A runner being stopped lets either
// finish, for up to cronStopWait.
func (cr *CronRunner) fireContext() (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.Background())
	if cr.interval > 0 {
		ctx, cancel = context.WithTimeout(context.Background(), cr.interval)
	}
	stopCh := cr.stopCh
	go func() {
		select {
		case <-stopCh:
		case <-ctx.Done():
			return
		}
		select {
		case <-time.After(cronStopWait):
			cancel()
		case <-ctx.Done():
		}
	}()
	return ctx, cancel
}

// executeCommand runs the cron command in a process group of its own. Ending
// it means SIGTERM to the group and SIGKILL after cronStopGrace: a kill of the
// direct child alone left the rest of `sh -c "a; b"` running with nobody
// watching. A command that finishes by itself may leave a daemon behind.
func (cr *CronRunner) executeCommand() error {
	if len(cr.command) == 0 {
		return nil
	}

	ctx, cancel := cr.fireContext()
	defer cancel()

	cmd := exec.CommandContext(ctx, cr.command[0], cr.command[1:]...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.WaitDelay = cronStopGrace
	cmd.Cancel = func() error {
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
		if err == syscall.ESRCH {
			return os.ErrProcessDone
		}
		return err
	}
	// RunAdhoc, not cmd.Run: slinit's global SIGCHLD reaper can claim
	// the zombie before os/exec's own wait sees it, and the exit code
	// gates on-error=stop. RunAdhoc registers the child so the reaper
	// hands the real status back instead.
	err := process.RunAdhoc(cmd)
	if ctx.Err() != nil && cmd.Process != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	if ctx.Err() != nil {
		select {
		case <-cr.stopCh:
			return nil // ended because of the stop: not the command's failure
		default:
		}
	}
	if isECHILDErr(err) {
		// Fallback only. With RunAdhoc the status is routed back, so
		// getting here means the router had nothing either — the
		// command still ran, and a spurious error line on every cron
		// tick is the worse of the two outcomes.
		return nil
	}
	return err
}

// cronSlot is the cron task of a service, for the types that can have one. A
// reload swaps the runner; the running one has to be stopped first, or it runs
// on with nothing left that can stop it. The replacement starts once the
// modifiers, which the loader applies last, are in.
type cronSlot struct {
	cronRunner  *CronRunner
	cronRestart bool
}

func (c *cronSlot) setCronRunner(r *CronRunner) {
	if c.cronRunner != nil && c.cronRunner.active() {
		c.cronRunner.Stop()
		c.cronRestart = true
	}
	c.cronRunner = r
}

// SetCronModifiers applies the scheduling modifiers to whichever cron runner
// was configured. One entry point for both modes on purpose: the modifiers used
// to be applied per mode, and interval mode simply never received jitter or
// persistence, so two of the three directives did nothing when combined with
// cron-interval.
func (c *cronSlot) SetCronModifiers(
	randomizedDelay time.Duration, fixedRandom, persistent bool, accuracy time.Duration,
) {
	if c.cronRunner == nil {
		return
	}
	c.cronRunner.SetRandomizedDelay(randomizedDelay, fixedRandom)
	c.cronRunner.SetPersistent(persistent)
	c.cronRunner.SetAccuracy(accuracy)
	if c.cronRestart {
		c.cronRestart = false
		c.cronRunner.Start()
	}
}

// ClearCron removes the task, as a reload of a file without one does.
func (c *cronSlot) ClearCron() {
	if c.cronRunner != nil {
		c.cronRunner.Stop()
	}
	c.cronRunner = nil
	c.cronRestart = false
}

// isECHILDErr reports whether err is a waitid/wait4 ECHILD wrapped
// in any of the shapes Go's os/exec surfaces (bare syscall.Errno,
// *os.SyscallError). Extracted so cron_test.go can assert the same
// predicate the runtime uses.
func isECHILDErr(err error) bool {
	return err != nil && errors.Is(err, syscall.ECHILD)
}
