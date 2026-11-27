package main

import (
	"runtime"
	"sync"
)

// Threads for later, made while making them is still possible.
//
// The Go runtime starts OS threads lazily: when a goroutine blocks in a
// system call, when the GC wakes idle Ps for its mark workers, when a
// burst of work wants every P at once. Making one is a pthread_create,
// and when the system is out of tasks — a fork bomb under RLIMIT_NPROC,
// a container at its cgroup pids.max — that call fails, and the runtime
// cannot survive it: "runtime/cgo: pthread_create failed", SIGABRT. As
// PID 1 that is a kernel panic. The fork that hit the limit was handled
// as a failed start; it was the thread the runtime wanted next, often
// for a GC cycle, that killed slinit. Booting 2000 services with the
// task limit 800 above what the user already ran, 4 of 20 runs died
// that way at GOMAXPROCS=12.
//
// The runtime never destroys an idle thread: one that has nothing to run
// parks on its idle list (stopm/mput) and is handed out again before a
// new one is made (startm/mget). The only thread it ends is one whose
// goroutine exits while locked to it. So threads made at startup, and
// let go, are there whenever demand comes back — without a
// pthread_create at the moment the kernel would refuse it.
//
// GOMAXPROCS covers every P running at once; threadReserveExtra covers
// the goroutines that sit in blocking system calls next to them (signal
// delivery, the inotify and mountinfo watchers, a command being forked,
// file I/O). With 12 Ps a 2000-service boot and shutdown peaked at 19
// threads; GOMAXPROCS+16 is 28. A thread costs a task and a few pages.
const threadReserveExtra = 16

// reserveThreads makes the runtime create n OS threads now and leave
// them idle. Each goroutine locks itself to a thread, so none can share
// one; they hold the lock until all n are running, then unlock (exiting
// locked would end the thread) and return, and their threads go to the
// idle list.
func reserveThreads(n int) {
	var locked, done sync.WaitGroup
	release := make(chan struct{})
	for i := 0; i < n; i++ {
		locked.Add(1)
		done.Add(1)
		go func() {
			defer done.Done()
			runtime.LockOSThread()
			locked.Done()
			<-release
			runtime.UnlockOSThread()
		}()
	}
	locked.Wait()
	close(release)
	done.Wait()
}
