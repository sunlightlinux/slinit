//go:build linux

package service

import (
	"net"
	"os"
	"testing"

	"golang.org/x/sys/unix"
)

// TestMain turns off core dumps for the test process and everything it
// forks.
//
// Several tests drive the watchdog and timeout-abort paths, which send
// SIGABRT to the child on purpose — systemd parity, so the child leaves
// a core for diagnosis. With the common core_pattern of "core" and no
// core limit, that child dumps into the test's working directory, which
// is the package directory: `go test ./...` left a pkg/service/core
// behind, half a megabyte of a `sleep` process's memory sitting in the
// source tree.
//
// The limit is inherited across fork and exec, so setting it once here
// covers every process a test starts. It changes nothing about how
// slinit behaves in production, where the abort is what an operator
// wants a core from.
func TestMain(m *testing.M) {
	_ = unix.Setrlimit(unix.RLIMIT_CORE, &unix.Rlimit{Cur: 0, Max: 0})
	if os.Getenv(acceptOneEnv) != "" {
		os.Exit(acceptOneHelper())
	}
	os.Exit(m.Run())
}

// acceptOneEnv turns the test binary into a socket-activated server for
// tests that need a real child: it accepts one connection on the
// inherited LISTEN_FDS socket (fd 3), writes "hello\n" and exits.
const acceptOneEnv = "SLINIT_TEST_ACCEPT_ONE"

func acceptOneHelper() int {
	ln, err := net.FileListener(os.NewFile(3, "listen-fd"))
	if err != nil {
		return 2
	}
	conn, err := ln.Accept()
	if err != nil {
		return 3
	}
	conn.Write([]byte("hello\n"))
	conn.Close()
	return 0
}
