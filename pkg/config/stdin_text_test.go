package config

import (
	"strings"
	"testing"
)

// `=` replaces and `+=` appends with a newline, as for every setting —
// and as standard-input-data already did. `=` used to append, with no
// separator at all.
func TestStandardInputTextOperators(t *testing.T) {
	for _, c := range []struct {
		lines string
		want  string
	}{
		{"standard-input-text = a\nstandard-input-text = b\n", "b"},
		{"standard-input-text = a\nstandard-input-text += b\n", "a\nb"},
		{"standard-input-text += a\n", "a"},
	} {
		desc, err := Parse(strings.NewReader("type = process\ncommand = /bin/cat\n"+c.lines), "svc", "svc")
		if err != nil {
			t.Fatal(err)
		}
		if got := string(desc.StandardInput); got != c.want {
			t.Errorf("%q: stdin = %q, want %q", c.lines, got, c.want)
		}
	}
}
