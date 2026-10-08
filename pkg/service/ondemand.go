package service

import (
	"os"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// socket-activation = on-demand, systemd-style.
//
// Starting the service opens its listening sockets and marks it STARTED
// without launching anything: the socket already accepts, so dependents
// can proceed, and a client that connects early simply waits in the
// backlog. The first connection (or datagram) launches the process, which
// inherits the sockets via LISTEN_FDS and accepts the pending connection
// itself. When the process exits the service stays STARTED and goes back
// to listening, so the next client launches it again. Only an explicit
// stop (or a dependency-driven one) closes the sockets.
//
// The watcher never accepts: it polls the sockets for readability. An
// Accept() would take the first connection out of the backlog, and the
// service would never see that client.

// armOnDemand starts watching the service's sockets for the first client.
// Caller must hold queueMu. Any previous watcher is disarmed first.
func (s *ProcessService) armOnDemand() error {
	s.disarmOnDemand()

	var fds []int
	closeAll := func() {
		for _, fd := range fds {
			unix.Close(fd)
		}
	}
	socks := append([]*os.File{s.socketFD}, s.socketFDs...)
	for _, f := range socks {
		if f == nil {
			continue
		}
		// The watcher owns duplicates, so closeSocket can close the
		// originals at any time without pulling an fd out from under
		// a poll in progress.
		fd, err := unix.FcntlInt(f.Fd(), unix.F_DUPFD_CLOEXEC, 0)
		if err != nil {
			closeAll()
			return err
		}
		fds = append(fds, fd)
	}

	var wake [2]int
	if err := unix.Pipe2(wake[:], unix.O_CLOEXEC); err != nil {
		closeAll()
		return err
	}
	s.demandWake = os.NewFile(uintptr(wake[1]), "on-demand-wake")
	s.demandGen++
	gen := s.demandGen

	go s.watchOnDemand(fds, wake[0], gen)
	return nil
}

// disarmOnDemand stops the watcher, if any. It does not wait for the
// goroutine: the goroutine may be blocked on queueMu, which the caller
// holds. Bumping the generation makes any activation it still attempts
// a no-op.
func (s *ProcessService) disarmOnDemand() {
	s.demandGen++
	if s.demandWake != nil {
		s.demandWake.Close() // EOF on the read end wakes the poll
		s.demandWake = nil
	}
}

func (s *ProcessService) watchOnDemand(fds []int, wakeFD int, gen uint64) {
	defer func() {
		for _, fd := range fds {
			unix.Close(fd)
		}
		unix.Close(wakeFD)
	}()

	pfds := make([]unix.PollFd, 0, len(fds)+1)
	for _, fd := range fds {
		pfds = append(pfds, unix.PollFd{Fd: int32(fd), Events: unix.POLLIN})
	}
	pfds = append(pfds, unix.PollFd{Fd: int32(wakeFD), Events: unix.POLLIN})

	for {
		_, err := unix.Poll(pfds, -1)
		if err == syscall.EINTR {
			continue
		}
		if err != nil {
			return
		}
		if pfds[len(pfds)-1].Revents != 0 {
			return // disarmed
		}
		active := false
		for _, p := range pfds[:len(pfds)-1] {
			if p.Revents&(unix.POLLIN|unix.POLLERR|unix.POLLHUP) != 0 {
				active = true
			}
		}
		if active {
			break
		}
	}

	s.services.queueMu.Lock()
	defer s.services.queueMu.Unlock()
	if s.demandGen != gen {
		return
	}
	s.demandWake.Close()
	s.demandWake = nil
	s.activateOnDemandLocked()
	s.services.processQueuesLocked()
}

// onDemandListening reports whether the service is STARTED and waiting
// for a client, with no process. Caller must hold queueMu.
func (s *ProcessService) onDemandListening() bool {
	return s.socketOnDemand && s.state.Load() == StateStarted
}

// activateOnDemandLocked launches the process for the first client. The
// steps BringUp defers for an on-demand service run here: pre-start
// command, start-delay, fork/exec, post-start command. A failure goes
// back to listening rather than stopping the service, as it would for
// a process that exited. Caller must hold queueMu.
func (s *ProcessService) activateOnDemandLocked() {
	if !s.onDemandListening() || s.pid > 0 {
		return
	}
	s.services.logger.Info("Service '%s': socket activity, launching process", s.serviceName)

	if len(s.preStartCommand) > 0 {
		if err := s.runHookCommand(s.preStartCommand, "pre-start-command"); err != nil {
			s.services.logger.Error("Service '%s': pre-start-command failed: %v",
				s.serviceName, err)
			s.rearmOnDemandLocked()
			return
		}
	}

	launch := func() {
		if err := s.startProcess(); err != nil {
			s.services.logger.Error("Service '%s': failed to start: %v", s.serviceName, err)
			s.rearmOnDemandLocked()
			return
		}
		s.launchPostStartCommand()
	}
	if s.startDelay > 0 {
		s.services.logger.Info("Service '%s': waiting %v before start (start-delay)",
			s.serviceName, s.startDelay)
		gen := s.demandGen
		s.startDelayT = time.AfterFunc(s.startDelay, func() {
			s.services.queueMu.Lock()
			defer s.services.queueMu.Unlock()
			s.startDelayT = nil
			if s.demandGen != gen || !s.onDemandListening() || s.pid > 0 {
				return
			}
			launch()
			s.services.processQueuesLocked()
		})
		return
	}
	launch()
}

// rearmOnDemandLocked puts a STARTED on-demand service back to listening
// after its process is gone. Caller must hold queueMu.
func (s *ProcessService) rearmOnDemandLocked() {
	s.stopHealthChecker()
	s.stopCronRunner()
	if err := s.armOnDemand(); err != nil {
		s.services.logger.Error("Service '%s': cannot re-arm socket activation: %v",
			s.serviceName, err)
		return
	}
	s.services.logger.Info("Service '%s': listening for the next client", s.serviceName)
}
