package service

import (
	"os"
	"sync"
)

const defaultLogBufMax = 8192

// LogBuffer manages a bounded in-memory buffer that captures service output.
// It is safe for concurrent use: the reader goroutine writes, and the control
// handler reads. This replaces dinit's log_output_watcher + log_buffer.
type LogBuffer struct {
	mu      sync.Mutex
	buf     []byte
	bufMax  int
	pipeR   *os.File // read end of the pipe (parent keeps)
	pipeW   *os.File // write end of the pipe (passed to child, then closed in parent)
	doneCh  chan struct{}
	running bool
}

// NewLogBuffer creates a LogBuffer with the given max size.
func NewLogBuffer(maxSize int) *LogBuffer {
	if maxSize <= 0 {
		maxSize = defaultLogBufMax
	}
	return &LogBuffer{
		bufMax: maxSize,
	}
}

// CreatePipe creates an os.Pipe and returns the write end for passing to
// ExecParams.OutputPipe. The caller MUST call CloseWriteEnd() after
// StartProcess() returns.
func (lb *LogBuffer) CreatePipe() (*os.File, error) {
	r, w, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	lb.pipeR = r
	lb.pipeW = w
	return w, nil
}

// CloseWriteEnd closes the parent's copy of the write end after fork.
// This is essential: the pipe won't get EOF until all write-end fds are closed.
func (lb *LogBuffer) CloseWriteEnd() {
	if lb.pipeW != nil {
		lb.pipeW.Close()
		lb.pipeW = nil
	}
}

// StartReader starts the goroutine that reads from the pipe into the buffer.
func (lb *LogBuffer) StartReader() {
	if lb.pipeR == nil {
		return
	}
	lb.doneCh = make(chan struct{})
	lb.running = true
	go lb.readLoop()
}

// readLoop reads from pipeR into buf, respecting bufMax.
// When buffer is full, excess data is read and discarded (matching dinit behavior).
func (lb *LogBuffer) readLoop() {
	defer func() {
		lb.pipeR.Close()
		lb.pipeR = nil
		lb.mu.Lock()
		lb.running = false
		lb.mu.Unlock()
		close(lb.doneCh)
	}()

	tmp := make([]byte, 4096)
	for {
		n, err := lb.pipeR.Read(tmp)
		if n > 0 {
			lb.mu.Lock()
			remaining := lb.bufMax - len(lb.buf)
			if remaining > 0 {
				toAppend := n
				if toAppend > remaining {
					toAppend = remaining
				}
				lb.buf = append(lb.buf, tmp[:toAppend]...)
			}
			// else: buffer full, discard (matches dinit proc-service.cc:267-278)
			lb.mu.Unlock()
		}
		if err != nil {
			return
		}
	}
}

// GetBuffer returns a copy of the current buffer contents.
func (lb *LogBuffer) GetBuffer() []byte {
	lb.mu.Lock()
	defer lb.mu.Unlock()
	if len(lb.buf) == 0 {
		return nil
	}
	result := make([]byte, len(lb.buf))
	copy(result, lb.buf)
	return result
}

// GetBufferAndClear returns the buffer contents and clears the buffer.
func (lb *LogBuffer) GetBufferAndClear() []byte {
	lb.mu.Lock()
	defer lb.mu.Unlock()
	result := lb.buf
	lb.buf = nil
	return result
}

// AppendRestartMarker appends a restart notification message to the buffer.
func (lb *LogBuffer) AppendRestartMarker() {
	lb.mu.Lock()
	defer lb.mu.Unlock()
	if len(lb.buf) == 0 {
		return
	}
	msg := "(slinit: note: service restarted)\n"
	if lb.buf[len(lb.buf)-1] != '\n' {
		msg = "\n" + msg
	}
	remaining := lb.bufMax - len(lb.buf)
	if remaining < len(msg) {
		return
	}
	lb.buf = append(lb.buf, msg...)
}

// WriteTestData writes data directly to the buffer (for testing only).
func (lb *LogBuffer) WriteTestData(data []byte) {
	lb.mu.Lock()
	defer lb.mu.Unlock()
	lb.buf = append(lb.buf, data...)
}

// Close stops the reader and cleans up resources.
func (lb *LogBuffer) Close() {
	if lb.pipeW != nil {
		lb.pipeW.Close()
		lb.pipeW = nil
	}
	if lb.pipeR != nil {
		lb.pipeR.Close()
		// readLoop will see EOF and exit
	}
	if lb.doneCh != nil && lb.running {
		<-lb.doneCh
	}
}
