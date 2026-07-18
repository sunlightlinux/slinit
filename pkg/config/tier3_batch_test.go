package config

import (
	"testing"
	"time"
)

func TestParseJobTimeoutSec(t *testing.T) {
	desc, err := parseServiceContent(`
type = process
command = /bin/true
job-timeout-sec = 90s
`, "job-timeout-probe")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if desc.JobTimeoutSec != 90*time.Second {
		t.Errorf("JobTimeoutSec: got %v, want 90s", desc.JobTimeoutSec)
	}
	if _, err := parseServiceContent(`
type = process
command = /bin/true
job-timeout-sec = -5s
`, ""); err == nil {
		t.Error("negative job-timeout-sec must be rejected")
	}
}

func TestParseEnvGenerator(t *testing.T) {
	desc, err := parseServiceContent(`
type = process
command = /bin/true
env-generator = /usr/local/bin/my-env-gen
`, "env-gen-probe")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if desc.EnvGenerator != "/usr/local/bin/my-env-gen" {
		t.Errorf("EnvGenerator: got %q, want /usr/local/bin/my-env-gen", desc.EnvGenerator)
	}
}

func TestParseSlice(t *testing.T) {
	desc, err := parseServiceContent(`
type = process
command = /bin/true
slice = system.slice
`, "slice-probe")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if desc.Slice != "system.slice" {
		t.Errorf("Slice: got %q, want system.slice", desc.Slice)
	}
}
