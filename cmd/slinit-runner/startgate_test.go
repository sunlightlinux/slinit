package main

import (
	"testing"

	"github.com/sunlightlinux/slinit/pkg/process"
)

func TestStartGateEnvMatches(t *testing.T) {
	if startGateEnv != process.StartGateEnv {
		t.Errorf("runner reads %q, slinit sets %q", startGateEnv, process.StartGateEnv)
	}
}
