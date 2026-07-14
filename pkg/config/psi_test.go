package config

import (
	"strings"
	"testing"
	"time"
)

func TestParsePSIPressureWatch(t *testing.T) {
	input := "type = process\ncommand = /bin/true\n" +
		"memory-pressure-watch = yes\n" +
		"memory-pressure-threshold = 150ms\n" +
		"cpu-pressure-watch = yes\n" +
		"cpu-pressure-threshold = 500ms\n" +
		"io-pressure-watch = yes\n" +
		"io-pressure-threshold = 1s\n"
	desc, err := Parse(strings.NewReader(input), "svc", "test")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !desc.MemoryPressureWatch {
		t.Error("MemoryPressureWatch: got false want true")
	}
	if desc.MemoryPressureThreshold != 150*time.Millisecond {
		t.Errorf("MemoryPressureThreshold: got %v want 150ms", desc.MemoryPressureThreshold)
	}
	if !desc.CPUPressureWatch {
		t.Error("CPUPressureWatch: got false want true")
	}
	if desc.CPUPressureThreshold != 500*time.Millisecond {
		t.Errorf("CPUPressureThreshold: got %v want 500ms", desc.CPUPressureThreshold)
	}
	if !desc.IOPressureWatch {
		t.Error("IOPressureWatch: got false want true")
	}
	if desc.IOPressureThreshold != time.Second {
		t.Errorf("IOPressureThreshold: got %v want 1s", desc.IOPressureThreshold)
	}
}

// TestParsePSIWatchWithoutThreshold pins the intentional zero-value
// behaviour: the loader/watcher substitutes a 200ms default when the
// threshold key is omitted. Parser leaves the field at its zero value
// so the loader can distinguish "unset" from "explicitly zero".
func TestParsePSIWatchWithoutThreshold(t *testing.T) {
	input := "type = process\ncommand = /bin/true\n" +
		"memory-pressure-watch = yes\n"
	desc, err := Parse(strings.NewReader(input), "svc", "test")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !desc.MemoryPressureWatch {
		t.Error("MemoryPressureWatch: got false want true")
	}
	if desc.MemoryPressureThreshold != 0 {
		t.Errorf("MemoryPressureThreshold: got %v want 0 (loader default)", desc.MemoryPressureThreshold)
	}
}

func TestParsePSIWatchDisabled(t *testing.T) {
	input := "type = process\ncommand = /bin/true\n" +
		"memory-pressure-watch = no\n" +
		"cpu-pressure-watch = no\n" +
		"io-pressure-watch = no\n"
	desc, err := Parse(strings.NewReader(input), "svc", "test")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if desc.MemoryPressureWatch || desc.CPUPressureWatch || desc.IOPressureWatch {
		t.Errorf("all pressure watches should be off: %+v", desc)
	}
}

func TestParsePSIRejectsBadDuration(t *testing.T) {
	input := "type = process\ncommand = /bin/true\n" +
		"memory-pressure-threshold = not-a-duration\n"
	if _, err := Parse(strings.NewReader(input), "svc", "test"); err == nil {
		t.Fatal("expected error for invalid memory-pressure-threshold")
	}
}

func TestParsePSIRejectsBadBool(t *testing.T) {
	input := "type = process\ncommand = /bin/true\n" +
		"cpu-pressure-watch = maybe\n"
	if _, err := Parse(strings.NewReader(input), "svc", "test"); err == nil {
		t.Fatal("expected error for invalid cpu-pressure-watch bool")
	}
}
