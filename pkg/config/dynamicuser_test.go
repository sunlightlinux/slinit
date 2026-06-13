package config

import (
	"strings"
	"testing"
)

func TestParseDynamicUser(t *testing.T) {
	for _, c := range []struct {
		val  string
		want bool
	}{
		{"yes", true},
		{"true", true},
		{"1", true},
		{"no", false},
		{"false", false},
		{"0", false},
	} {
		input := "type = process\ncommand = /bin/true\ndynamic-user = " + c.val + "\n"
		desc, err := Parse(strings.NewReader(input), "svc", "test")
		if err != nil {
			t.Errorf("%q: %v", c.val, err)
			continue
		}
		if desc.DynamicUser != c.want {
			t.Errorf("%q: got %v want %v", c.val, desc.DynamicUser, c.want)
		}
	}
}

func TestParseDynamicUserDefault(t *testing.T) {
	input := "type = process\ncommand = /bin/true\n"
	desc, err := Parse(strings.NewReader(input), "svc", "test")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if desc.DynamicUser {
		t.Error("DynamicUser default should be false")
	}
}

func TestParseDynamicUserRejectsBogus(t *testing.T) {
	input := "type = process\ncommand = /bin/true\ndynamic-user = maybe\n"
	if _, err := Parse(strings.NewReader(input), "svc", "test"); err == nil {
		t.Error("expected error for bogus dynamic-user value")
	}
}
