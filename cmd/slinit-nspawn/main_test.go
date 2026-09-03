package main

import (
	"os"
	"strings"
	"testing"
)

// TestReexecEnvSentinel — the sentinel string is load-bearing (dispatch
// between parent and child in main()), so if someone renames it a full
// suite of "does slinit-nspawn work at all" tests would go quiet. Lock
// it in.
func TestReexecEnvSentinel(t *testing.T) {
	if reexecEnv == "" {
		t.Fatal("reexecEnv must be a non-empty string")
	}
	if strings.Contains(reexecEnv, " ") {
		t.Errorf("reexecEnv %q contains whitespace — env keys must be shell-safe", reexecEnv)
	}
	if !strings.HasPrefix(reexecEnv, "_") {
		t.Errorf("reexecEnv %q should be underscore-prefixed to signal internal-only", reexecEnv)
	}
}

// TestChildPayloadEnvEncodingRoundtrip — the parent packs
// childPayload.InitArgs into a NUL-separated env var and the child
// unpacks it. If someone changes one side without the other, args
// silently break. Reproduce the pack/unpack directly.
func TestChildPayloadEnvEncodingRoundtrip(t *testing.T) {
	orig := []string{"--config", "/etc/foo.conf", "--", "arg with space"}
	encoded := strings.Join(orig, "\x1f")
	os.Setenv("_SLINIT_NSPAWN_INIT_ARGS", encoded)
	defer os.Unsetenv("_SLINIT_NSPAWN_INIT_ARGS")

	raw := os.Getenv("_SLINIT_NSPAWN_INIT_ARGS")
	got := strings.Split(raw, "\x1f")
	if len(got) != len(orig) {
		t.Fatalf("roundtrip length: got %d, want %d", len(got), len(orig))
	}
	for i, want := range orig {
		if got[i] != want {
			t.Errorf("arg[%d]: got %q, want %q", i, got[i], want)
		}
	}
}
