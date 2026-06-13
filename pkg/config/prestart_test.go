package config

import (
	"strings"
	"testing"
)

func TestParsePreStartCommand(t *testing.T) {
	input := `
type = process
command = /usr/bin/myservice
pre-start-command = /usr/local/bin/setup --first
pre-start-command += --second
post-start-command = /usr/local/bin/notify started
`
	desc, err := Parse(strings.NewReader(input), "svc", "test")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	wantPre := []string{"/usr/local/bin/setup", "--first", "--second"}
	if len(desc.PreStartCommand) != len(wantPre) {
		t.Fatalf("pre-start-command: got %v want %v", desc.PreStartCommand, wantPre)
	}
	for i, w := range wantPre {
		if desc.PreStartCommand[i] != w {
			t.Errorf("pre[%d]: got %q want %q", i, desc.PreStartCommand[i], w)
		}
	}
	wantPost := []string{"/usr/local/bin/notify", "started"}
	if len(desc.PostStartCommand) != len(wantPost) {
		t.Fatalf("post-start-command: got %v want %v", desc.PostStartCommand, wantPost)
	}
	for i, w := range wantPost {
		if desc.PostStartCommand[i] != w {
			t.Errorf("post[%d]: got %q want %q", i, desc.PostStartCommand[i], w)
		}
	}
}

func TestParsePrePostStartEmptyByDefault(t *testing.T) {
	input := "type = process\ncommand = /bin/true\n"
	desc, err := Parse(strings.NewReader(input), "svc", "test")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if desc.PreStartCommand != nil {
		t.Errorf("PreStartCommand should be nil by default, got %v", desc.PreStartCommand)
	}
	if desc.PostStartCommand != nil {
		t.Errorf("PostStartCommand should be nil by default, got %v", desc.PostStartCommand)
	}
}
