package watchdog

import (
	"errors"
	"sync"
	"time"
)

const (
	// LatePollInterval is how often Late looks for a device that is not
	// there yet.
	LatePollInterval = 500 * time.Millisecond

	// LateGrace is how long after boot is ready Late keeps looking. Modules
	// are loaded by then; this leaves room for a slow one.
	LateGrace = 30 * time.Second
)

// LateHooks reports what a Late does. All are required.
type LateHooks struct {
	Waiting     func(err error) // no device yet; still looking
	Armed       func(f *Feeder) // armed: the caller must now run f
	Unavailable func(err error) // going on without a watchdog
}

// Late owns the feeder for the life of PID 1, including the time before there
// is one. slinit starts ahead of whatever loads the driver, so on a machine
// whose watchdog is a module the first look finds nothing, and only a soft
// reboot, with the module already loaded, used to arm it.
//
// Looking starts at once rather than at boot ready, so that a boot hanging
// after the driver loads is covered. Closing and arming share a lock, because
// the one wrong order is arming after Close: nothing would feed the device, and
// the machine would reset in the middle of its shutdown.
type Late struct {
	cfg   Config
	hooks LateHooks
	open  func(Config) (*Feeder, error)
	poll  time.Duration
	grace time.Duration

	mu       sync.Mutex
	feeder   *Feeder
	closed   bool
	giveUpAt time.Time
	stop     chan struct{}
}

// NewLate prepares a Late; Start begins its work.
func NewLate(cfg Config, hooks LateHooks) *Late {
	return &Late{
		cfg:   cfg,
		hooks: hooks,
		open:  Open,
		poll:  LatePollInterval,
		grace: LateGrace,
		stop:  make(chan struct{}),
	}
}

// Start makes the first attempt before returning, so a device already there is
// armed as early as it ever was. With retry, a missing device is looked for
// again; any other failure, such as a device someone else holds, is final.
func (l *Late) Start(retry bool) {
	done, err := l.attempt()
	if done {
		return
	}
	if !retry || !errors.Is(err, ErrNoDevice) {
		l.hooks.Unavailable(err)
		return
	}
	l.hooks.Waiting(err)
	go l.loop()
}

func (l *Late) loop() {
	t := time.NewTicker(l.poll)
	defer t.Stop()
	for {
		select {
		case <-l.stop:
			return
		case <-t.C:
		}
		done, err := l.attempt()
		if done {
			return
		}
		if !errors.Is(err, ErrNoDevice) || l.expired() {
			l.hooks.Unavailable(err)
			return
		}
	}
}

// attempt tries to arm. It is done when armed or closed.
func (l *Late) attempt() (done bool, err error) {
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return true, nil
	}
	f, err := l.open(l.cfg)
	if err != nil {
		l.mu.Unlock()
		return false, err
	}
	l.feeder = f
	l.mu.Unlock()
	l.hooks.Armed(f)
	return true, nil
}

func (l *Late) expired() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return !l.giveUpAt.IsZero() && time.Now().After(l.giveUpAt)
}

// BootReady starts the countdown to giving up. A boot that never gets there
// keeps looking, as that is a machine that may yet need the watchdog.
func (l *Late) BootReady() {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.giveUpAt.IsZero() {
		l.giveUpAt = time.Now().Add(l.grace)
	}
}

// Close disarms the feeder if there is one, and rules out arming afterwards.
// Idempotent.
func (l *Late) Close() error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return nil
	}
	l.closed = true
	close(l.stop)
	if l.feeder == nil {
		return nil
	}
	return l.feeder.Close()
}
