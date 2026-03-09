package config

import (
	"testing"
)

func TestValidateServiceName(t *testing.T) {
	valid := []string{
		"myservice",
		"my-service",
		"my_service",
		"my.service",
		"svc@arg",
		"svc@anything/goes/here",
		"boot",
		"a",
		"foo123",
		"café", // UTF-8
	}
	for _, name := range valid {
		if err := ValidateServiceName(name); err != nil {
			t.Errorf("ValidateServiceName(%q) = %v, want nil", name, err)
		}
	}

	invalid := []string{
		"",          // empty
		".hidden",   // starts with '.'
		"@template", // starts with '@'
	}
	for _, name := range invalid {
		if err := ValidateServiceName(name); err == nil {
			t.Errorf("ValidateServiceName(%q) = nil, want error", name)
		}
	}
}

func TestValidateServiceNamePathTraversal(t *testing.T) {
	// Names starting with '.' are rejected (covers "../" traversal)
	if err := ValidateServiceName("../../../etc/passwd"); err == nil {
		t.Error("expected error for path traversal name")
	}
	if err := ValidateServiceName(".secret"); err == nil {
		t.Error("expected error for dot-prefixed name")
	}
}
