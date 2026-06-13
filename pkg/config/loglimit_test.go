package config

import (
	"strings"
	"testing"
	"time"
)

func TestParseLogRateLimitAndLevelMax(t *testing.T) {
	input := `
type = process
command = /bin/true
log-rate-limit-interval = 10s
log-rate-limit-burst = 100
log-level-max = warning
`
	desc, err := Parse(strings.NewReader(input), "svc", "test")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if desc.LogRateLimitInterval != 10*time.Second {
		t.Errorf("interval: got %v", desc.LogRateLimitInterval)
	}
	if desc.LogRateLimitBurst != 100 {
		t.Errorf("burst: got %d want 100", desc.LogRateLimitBurst)
	}
	if desc.LogLevelMax != 4 {
		t.Errorf("level-max: got %d want 4 (warning)", desc.LogLevelMax)
	}
}

func TestParseLogLevelMaxDefault(t *testing.T) {
	input := "type = process\ncommand = /bin/true\n"
	desc, err := Parse(strings.NewReader(input), "svc", "test")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if desc.LogLevelMax != -1 {
		t.Errorf("default level-max should be -1 (disabled), got %d", desc.LogLevelMax)
	}
}

func TestParseLogRateLimitRejectsBadValues(t *testing.T) {
	cases := []string{
		"log-rate-limit-interval = -5s\n",
		"log-rate-limit-burst = -1\n",
		"log-rate-limit-burst = notanumber\n",
		"log-level-max = bogus\n",
	}
	for _, line := range cases {
		input := "type = process\ncommand = /bin/true\n" + line
		if _, err := Parse(strings.NewReader(input), "svc", "test"); err == nil {
			t.Errorf("expected error for %q", line)
		}
	}
}
