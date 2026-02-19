package process

// ProcessHandle holds the state needed to track a running process.
type ProcessHandle struct {
	PID    int
	ExitCh <-chan ChildExit
}

// IsRunning returns true if the process handle has a valid PID.
func (h *ProcessHandle) IsRunning() bool {
	return h.PID > 0
}

// Clear resets the process handle.
func (h *ProcessHandle) Clear() {
	h.PID = 0
	h.ExitCh = nil
}
