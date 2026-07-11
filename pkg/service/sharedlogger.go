package service

import (
	"bufio"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

// defaultLossyQueueSize bounds the mux's per-instance channel when
// lossy mode is enabled. Sized to smooth ~1s of moderate bursts (a
// few hundred lines/sec) without letting a slow logger balloon memory.
const defaultLossyQueueSize = 1024

// defaultLossyReportInterval is how often the writer goroutine
// synthesises a "[shared-logger] dropped N lines" heartbeat when
// drops have occurred but the mux would otherwise be quiet.
const defaultLossyReportInterval = 30 * time.Second

// SharedLogMuxOptions carries per-mux tuning that reaches the mux only
// at construction time. Zero-value = classic blocking behavior.
type SharedLogMuxOptions struct {
	// Lossy enables the drop-instead-of-block path. Producers writing
	// to a full internal queue increment a counter and move on; the
	// mux emits a synthetic "dropped N lines" line at most every
	// ReportInterval.
	Lossy bool
	// QueueSize is the buffered channel depth. 0 selects the default.
	QueueSize int
	// ReportInterval controls how often drop reports are emitted.
	// 0 selects the default.
	ReportInterval time.Duration
}

// SharedLogMux multiplexes output from multiple producer services into a single
// pipe that feeds the logger service's stdin. Each line is prefixed with the
// producer's service name: "[service-name] original line\n".
//
// This implements the "multi-service logger" pattern from dinit's TODO:
// a single logger process receives output from multiple services via
// fd-passing (here implemented as an in-process multiplexer).
type SharedLogMux struct {
	mu        sync.Mutex
	producers map[string]*sharedLogProducer
	writerMu  sync.Mutex // serializes writes to loggerW
	loggerW   *os.File   // write-end → logger's stdin
	loggerR   *os.File   // read-end → passed to logger as InputPipe
	closed    bool

	// Lossy mode: when queue is non-nil, producer goroutines feed
	// pre-formatted line buffers into it and a dedicated writer
	// goroutine drains it into loggerW with blocking writes. If the
	// queue is full at the moment the producer tries to send, the
	// line is dropped and dropCount is incremented atomically. The
	// writer emits a synthetic report line at most every
	// reportInterval when drops > 0.
	queue          chan []byte
	writerDone     chan struct{}
	dropCount      atomic.Uint64
	reportInterval time.Duration
}

// sharedLogProducer tracks one producer's pipe and reader goroutine.
type sharedLogProducer struct {
	name   string
	pipeR  *os.File      // read-end of producer's output pipe
	pipeW  *os.File      // write-end passed to producer's stdout
	stopCh chan struct{} // closed to signal reader to stop
	doneCh chan struct{} // closed when reader goroutine exits
}

// NewSharedLogMux creates a new multiplexer. It creates the internal pipe
// that the logger service will read from (via InputPipe()).
func NewSharedLogMux() (*SharedLogMux, error) {
	return NewSharedLogMuxWithOptions(SharedLogMuxOptions{})
}

// NewSharedLogMuxWithOptions creates a mux with tuning applied.
// Zero-value opts is equivalent to NewSharedLogMux (classic blocking).
func NewSharedLogMuxWithOptions(opts SharedLogMuxOptions) (*SharedLogMux, error) {
	r, w, err := os.Pipe()
	if err != nil {
		return nil, fmt.Errorf("shared-logger: failed to create mux pipe: %w", err)
	}
	m := &SharedLogMux{
		producers: make(map[string]*sharedLogProducer),
		loggerW:   w,
		loggerR:   r,
	}
	if opts.Lossy {
		size := opts.QueueSize
		if size <= 0 {
			size = defaultLossyQueueSize
		}
		m.reportInterval = opts.ReportInterval
		if m.reportInterval <= 0 {
			m.reportInterval = defaultLossyReportInterval
		}
		m.queue = make(chan []byte, size)
		m.writerDone = make(chan struct{})
		go m.lossyWriter()
	}
	return m, nil
}

// DropCount reports the number of lines the lossy path has dropped
// since construction. Callers use this for observability (metrics /
// slinitctl status). Always 0 in classic blocking mode.
func (m *SharedLogMux) DropCount() uint64 {
	return m.dropCount.Load()
}

// InputPipe returns the read-end of the mux pipe. This should be passed
// to the logger service as its stdin (InputPipe in ExecParams).
func (m *SharedLogMux) InputPipe() *os.File {
	return m.loggerR
}

// AddProducer registers a new producer service. It creates a pipe for the
// producer's stdout and starts a goroutine that reads lines and forwards
// them (prefixed) to the logger pipe.
// Returns the write-end of the pipe (to be set as the producer's OutputPipe).
func (m *SharedLogMux) AddProducer(name string) (*os.File, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.closed {
		return nil, fmt.Errorf("shared-logger: mux is closed")
	}

	// If producer already exists, stop old reader first
	if old, ok := m.producers[name]; ok {
		close(old.stopCh)
		old.pipeR.Close() // unblock scanner.Scan()
		<-old.doneCh
		// Don't close old.pipeW — it may still be in use by the old process
	}

	r, w, err := os.Pipe()
	if err != nil {
		return nil, fmt.Errorf("shared-logger: failed to create producer pipe for '%s': %w", name, err)
	}

	p := &sharedLogProducer{
		name:   name,
		pipeR:  r,
		pipeW:  w,
		stopCh: make(chan struct{}),
		doneCh: make(chan struct{}),
	}
	m.producers[name] = p

	go m.readProducer(p)

	return w, nil
}

// RemoveProducer stops reading from a producer and cleans up its pipe.
func (m *SharedLogMux) RemoveProducer(name string) {
	m.mu.Lock()
	p, ok := m.producers[name]
	if !ok {
		m.mu.Unlock()
		return
	}
	delete(m.producers, name)
	m.mu.Unlock()

	close(p.stopCh)
	// Close the read-end to unblock scanner.Scan()
	p.pipeR.Close()
	<-p.doneCh
}

// ProducerCount returns the number of active producers.
func (m *SharedLogMux) ProducerCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.producers)
}

// ProducerNames returns the names of all active producers.
func (m *SharedLogMux) ProducerNames() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	names := make([]string, 0, len(m.producers))
	for name := range m.producers {
		names = append(names, name)
	}
	return names
}

// Close shuts down all producer readers and closes the mux pipe.
func (m *SharedLogMux) Close() {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	m.closed = true

	// Collect producers to stop
	producers := make([]*sharedLogProducer, 0, len(m.producers))
	for _, p := range m.producers {
		producers = append(producers, p)
	}
	m.producers = make(map[string]*sharedLogProducer)
	m.mu.Unlock()

	// Stop all readers
	for _, p := range producers {
		close(p.stopCh)
		p.pipeR.Close() // unblock scanner.Scan()
		<-p.doneCh
	}

	// Lossy mode: tear down the writer goroutine before closing the
	// mux pipe. The writer might be blocked on Write to loggerW (a
	// silent logger fills the OS pipe buffer), so we first close the
	// read end to break the pipe with EPIPE, then close the queue so
	// the writer sees the sentinel and returns.
	if m.queue != nil {
		m.loggerR.Close()
		close(m.queue)
		<-m.writerDone
		m.loggerW.Close()
		return
	}

	// Classic mode: no writer goroutine to coordinate with.
	m.loggerW.Close()
	m.loggerR.Close()
}

// readProducer reads lines from a producer's pipe and writes them prefixed
// to the logger pipe. Exits when the pipe is closed or stopCh is signaled.
// In classic mode writes go directly to loggerW (three small writes under
// writerMu). In lossy mode each line is coalesced into one buffer and
// handed to the queue with a non-blocking send; a full queue drops the
// line rather than blocking the producer.
func (m *SharedLogMux) readProducer(p *sharedLogProducer) {
	defer close(p.doneCh)

	scanner := bufio.NewScanner(p.pipeR)
	// Use a reasonable max line size (64KB)
	scanner.Buffer(make([]byte, 4096), 64*1024)

	// Pre-build the prefix bytes once: "[name] "
	prefix := []byte("[" + p.name + "] ")

	for scanner.Scan() {
		select {
		case <-p.stopCh:
			return
		default:
		}

		lineBytes := scanner.Bytes()

		if m.queue != nil {
			// Lossy path: coalesce prefix+line+nl into one buffer and
			// hand it to the writer via a non-blocking send. Buffer
			// ownership transfers to the writer which will discard it
			// after Write returns.
			buf := make([]byte, 0, len(prefix)+len(lineBytes)+1)
			buf = append(buf, prefix...)
			buf = append(buf, lineBytes...)
			buf = append(buf, '\n')
			select {
			case m.queue <- buf:
			default:
				m.dropCount.Add(1)
			}
			continue
		}

		// Classic blocking path
		m.writerMu.Lock()
		_, err := m.loggerW.Write(prefix)
		if err == nil {
			_, err = m.loggerW.Write(lineBytes)
		}
		if err == nil {
			_, err = m.loggerW.Write([]byte{'\n'})
		}
		m.writerMu.Unlock()

		if err != nil {
			// Logger pipe broken — stop silently
			return
		}
	}
	// Pipe closed (producer exited) — goroutine exits naturally
}

// lossyWriter drains the queue in FIFO order, writing each buffered
// line to loggerW with a blocking write (guaranteeing per-line
// atomicity). Between writes it checks whether it's time to emit a
// drop report — the report piggybacks on the natural cadence of
// incoming traffic so we don't need a separate timer goroutine when
// the queue is idle.
func (m *SharedLogMux) lossyWriter() {
	defer close(m.writerDone)

	var lastReport time.Time
	var lastReported uint64

	emitReport := func() {
		total := m.dropCount.Load()
		delta := total - lastReported
		if delta == 0 {
			return
		}
		lastReported = total
		lastReport = time.Now()
		msg := fmt.Sprintf("[shared-logger] dropped %d lines (backpressure)\n", delta)
		m.writerMu.Lock()
		_, _ = m.loggerW.Write([]byte(msg))
		m.writerMu.Unlock()
	}

	for buf := range m.queue {
		m.writerMu.Lock()
		_, err := m.loggerW.Write(buf)
		m.writerMu.Unlock()
		// A write error means the logger pipe is broken (logger
		// crashed / restart in flight). We discard the buffer and
		// keep looping so that ReplaceLoggerPipe can rescue us on
		// the next iteration; producers continue reading from their
		// pipes and, if the queue backs up, drop rather than block.
		_ = err
		if m.dropCount.Load() > lastReported && time.Since(lastReport) >= m.reportInterval {
			emitReport()
		}
	}
	// Queue closed → final flush of the drop counter so the last few
	// dropped lines are not silently forgotten.
	emitReport()
}

// ReplaceLoggerPipe replaces the mux's logger pipe (used when logger restarts).
// Returns the new read-end to pass to the restarted logger.
func (m *SharedLogMux) ReplaceLoggerPipe() (*os.File, error) {
	r, w, err := os.Pipe()
	if err != nil {
		return nil, err
	}

	m.writerMu.Lock()
	oldW := m.loggerW
	m.loggerW = w
	m.writerMu.Unlock()

	oldW.Close()

	m.mu.Lock()
	oldR := m.loggerR
	m.loggerR = r
	m.mu.Unlock()

	oldR.Close()

	return r, nil
}
