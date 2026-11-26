package main

import (
	"runtime"
	"runtime/pprof"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// TestReserveThreadsServesLaterDemand: threads reserved at startup must
// be the ones the runtime uses later, so a demand for threads after the
// reserve creates none. Without the reserve, slinit booting 2000
// services under a task limit died 4 runs in 20 with "pthread_create
// failed" when the runtime made a thread on demand.
//
// The demand here is every P busy plus goroutines parked in blocking
// system calls, each of which holds a thread of its own.
func TestReserveThreadsServesLaterDemand(t *testing.T) {
	const blocked = 8
	procs := runtime.GOMAXPROCS(0)
	reserveThreads(procs + threadReserveExtra)

	created := pprof.Lookup("threadcreate")
	before := created.Count()

	// Goroutines in a read on a blocking pipe: a system call the
	// netpoller does not see, so each holds its thread until the write.
	var pipes [blocked][2]int
	for i := range pipes {
		if err := syscall.Pipe2(pipes[i][:], syscall.O_CLOEXEC); err != nil {
			t.Fatal(err)
		}
	}
	defer func() {
		for _, p := range pipes {
			syscall.Close(p[0])
			syscall.Close(p[1])
		}
	}()
	var wg sync.WaitGroup
	for i := range pipes {
		wg.Add(1)
		go func(fd int) {
			defer wg.Done()
			var b [1]byte
			syscall.Read(fd, b[:])
		}(pipes[i][0])
	}
	// And every P busy, so a P that sysmon takes from a blocked
	// goroutine needs a thread to run on.
	var stop atomic.Bool
	for i := 0; i < procs; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for !stop.Load() {
			}
		}()
	}
	time.Sleep(200 * time.Millisecond)
	stop.Store(true)
	for _, p := range pipes {
		syscall.Write(p[1], []byte{0})
	}
	wg.Wait()

	if n := created.Count() - before; n != 0 {
		t.Fatalf("runtime made %d new threads after reserving %d: the reserve was not used", n, procs+threadReserveExtra)
	}
}
