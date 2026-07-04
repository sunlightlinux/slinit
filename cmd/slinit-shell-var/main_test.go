package main

import "testing"

func TestIsAlnumRange(t *testing.T) {
	// Sample the boundaries so the ASCII map is intact.
	yes := []byte{'0', '9', 'a', 'z', 'A', 'Z', 'M', '5'}
	no := []byte{' ', '-', '.', '/', '_', '@', '!', '~', 0x00, 0x7f, '\t', '\n'}
	for _, c := range yes {
		if !isAlnum(c) {
			t.Errorf("isAlnum(%q) = false, want true", c)
		}
	}
	for _, c := range no {
		if isAlnum(c) {
			t.Errorf("isAlnum(%q) = true, want false", c)
		}
	}
}

// sanitize is the transform embedded in main(); factored via a helper
// makes the fixed-input test-side straightforward without shelling out.
func sanitize(args ...string) string {
	var out []byte
	for i, arg := range args {
		if i != 0 {
			out = append(out, ' ')
		}
		for j := 0; j < len(arg); j++ {
			c := arg[j]
			if !isAlnum(c) {
				c = '_'
			}
			out = append(out, c)
		}
	}
	return string(out)
}

func TestSanitizeSingleArg(t *testing.T) {
	cases := map[string]string{
		"":                "",
		"foo":             "foo",
		"my-thing.d/1":    "my_thing_d_1",
		"NET_ETH0":        "NET_ETH0",
		"has spaces here": "has_spaces_here",
		"127.0.0.1":       "127_0_0_1",
		"CAPS_low_42":     "CAPS_low_42",
	}
	for in, want := range cases {
		got := sanitize(in)
		if got != want {
			t.Errorf("sanitize(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSanitizeMultipleArgsJoinedWithLiteralSpace(t *testing.T) {
	// Args are separated by a real space; inner spaces still get _.
	got := sanitize("a.b", "c-d")
	if got != "a_b c_d" {
		t.Errorf("got %q, want %q", got, "a_b c_d")
	}
	got = sanitize("has space", "next")
	if got != "has_space next" {
		t.Errorf("got %q, want %q", got, "has_space next")
	}
}

func TestSanitizeEmptyArgListPrintsNothing(t *testing.T) {
	if got := sanitize(); got != "" {
		t.Errorf("empty argv should produce empty output, got %q", got)
	}
}

func TestSanitizeAllPunctuationBecomesUnderscore(t *testing.T) {
	got := sanitize("!@#$%^&*()+=[]{}|;:'\"<>,.?/")
	// Every byte should be '_'.
	for i := 0; i < len(got); i++ {
		if got[i] != '_' {
			t.Errorf("byte %d = %q, want '_'", i, got[i])
		}
	}
}
