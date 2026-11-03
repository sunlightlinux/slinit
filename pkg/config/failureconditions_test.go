package config

import (
	"strings"
	"testing"

	"github.com/sunlightlinux/slinit/pkg/service"
)

func TestParseFailureConditionsDirective(t *testing.T) {
	desc, err := Parse(strings.NewReader("type = process\ncommand = /bin/true\n"+
		"failure-conditions = process-stops dependency-fails\n"), "svc", "svc")
	if err != nil {
		t.Fatal(err)
	}
	want := service.FailureConditions{Primary: service.FailOnProcessStop, Dependency: true}
	if desc.FailureConditions != want {
		t.Errorf("got %+v, want %+v", desc.FailureConditions, want)
	}

	// Absent, it is the zero value: the behaviour slinit always had.
	desc, err = Parse(strings.NewReader("type = process\ncommand = /bin/true\n"), "svc", "svc")
	if err != nil {
		t.Fatal(err)
	}
	if desc.FailureConditions != (service.FailureConditions{}) {
		t.Errorf("default %+v, want the zero value", desc.FailureConditions)
	}

	if _, err := Parse(strings.NewReader("type = process\ncommand = /bin/true\n"+
		"failure-conditions = process-dies\n"), "svc", "svc"); err == nil {
		t.Error("an unknown condition was accepted")
	}
}
