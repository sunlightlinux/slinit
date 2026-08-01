package main

import (
	"os"
	"testing"
)

// TestResolveSocketPathEnvFallback covers the dinit-parity change:
// DINIT_SOCKET_PATH env var (and its SLINIT_ alias) act as a
// pre-mode fallback for slinitctl's socket resolution when
// --socket-path is absent. Test isolates each env var and
// verifies the precedence order (flag > DINIT_ > SLINIT_ >
// mode-based default).
func TestResolveSocketPathEnvFallback(t *testing.T) {
	// Snapshot the vars we mutate so we don't leak state between tests.
	restore := func(name, prev string, had bool) {
		if had {
			os.Setenv(name, prev)
		} else {
			os.Unsetenv(name)
		}
	}
	dinitPrev, dinitHad := os.LookupEnv("DINIT_SOCKET_PATH")
	slinitPrev, slinitHad := os.LookupEnv("SLINIT_SOCKET_PATH")
	defer restore("DINIT_SOCKET_PATH", dinitPrev, dinitHad)
	defer restore("SLINIT_SOCKET_PATH", slinitPrev, slinitHad)

	cases := []struct {
		name        string
		flagValue   string
		dinitEnv    string
		slinitEnv   string
		systemMode  bool
		want        string
	}{
		{
			name:      "flag wins over everything",
			flagValue: "/tmp/from-flag.sock",
			dinitEnv:  "/tmp/from-dinit.sock",
			slinitEnv: "/tmp/from-slinit.sock",
			want:      "/tmp/from-flag.sock",
		},
		{
			name:      "DINIT_ takes precedence over SLINIT_",
			dinitEnv:  "/tmp/from-dinit.sock",
			slinitEnv: "/tmp/from-slinit.sock",
			want:      "/tmp/from-dinit.sock",
		},
		{
			name:      "SLINIT_ used when only it is set",
			slinitEnv: "/tmp/from-slinit.sock",
			want:      "/tmp/from-slinit.sock",
		},
		{
			name:       "env vars override system-mode default",
			dinitEnv:   "/tmp/env-wins.sock",
			systemMode: true,
			want:       "/tmp/env-wins.sock",
		},
		{
			name:       "empty env falls through to system default",
			systemMode: true,
			want:       defaultSystemSocket,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			os.Unsetenv("DINIT_SOCKET_PATH")
			os.Unsetenv("SLINIT_SOCKET_PATH")
			if c.dinitEnv != "" {
				os.Setenv("DINIT_SOCKET_PATH", c.dinitEnv)
			}
			if c.slinitEnv != "" {
				os.Setenv("SLINIT_SOCKET_PATH", c.slinitEnv)
			}
			got := resolveSocketPath(c.flagValue, c.systemMode, false)
			if got != c.want {
				t.Errorf("resolveSocketPath: got %q, want %q", got, c.want)
			}
		})
	}
}
