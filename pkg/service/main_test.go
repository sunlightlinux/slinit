//go:build linux

package service

import (
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
	os.Exit(m.Run())
}
