package service

import (
	"bufio"
	"fmt"
	"os"
	"sync"
)

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
}

// sharedLogProducer tracks one producer's pipe and reader goroutine.
type sharedLogProducer struct {
	name   string
	pipeR  *os.File      // read-end of producer's output pipe
	pipeW  *os.File      // write-end passed to producer's stdout
	stopCh chan struct{}  // closed to signal reader to stop
	doneCh chan struct{}  // closed when reader goroutine exits
}

// NewSharedLogMux creates a new multiplexer. It creates the internal pipe
// that the logger service will read from (via InputPipe()).
func NewSharedLogMux() (*SharedLogMux, error) {
	r, w, err := os.Pipe()
	if err != nil {
		return nil, fmt.Errorf("shared-logger: failed to create mux pipe: %w", err)
	}
	return &SharedLogMux{
		producers: make(map[string]*sharedLogProducer),
		loggerW:   w,
		loggerR:   r,
	}, nil
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

	// Close the mux pipe
	m.loggerW.Close()
	m.loggerR.Close()
}

// readProducer reads lines from a producer's pipe and writes them prefixed
// to the logger pipe. Exits when the pipe is closed or stopCh is signaled.
// Writes the prefix parts directly to avoid per-line string formatting allocation.
func (m *SharedLogMux) readProducer(p *sharedLogProducer) {
	defer close(p.doneCh)

	scanner := bufio.NewScanner(p.pipeR)
	// Use a reasonable max line size (64KB)
	scanner.Buffer(make([]byte, 4096), 64*1024)

	// Pre-build the prefix bytes once: "[name] "
	prefix := []byte("[" + p.name + "] ")
	nl := []byte{'\n'}

	for scanner.Scan() {
		select {
		case <-p.stopCh:
			return
		default:
		}

		lineBytes := scanner.Bytes()

		m.writerMu.Lock()
		_, err := m.loggerW.Write(prefix)
		if err == nil {
			_, err = m.loggerW.Write(lineBytes)
		}
		if err == nil {
			_, err = m.loggerW.Write(nl)
		}
		m.writerMu.Unlock()

		if err != nil {
			// Logger pipe broken — stop silently
			return
		}
	}
	// Pipe closed (producer exited) — goroutine exits naturally
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
