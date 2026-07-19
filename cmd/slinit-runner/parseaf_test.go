package main

import "testing"

// TestParseAFList covers the token forms accepted by the AF_* parser:
// bare name ("INET"), full-prefix ("AF_INET"), case-insensitive, and
// numeric fallback. Empty tokens are dropped; unknown names surface as
// errors so a typo becomes visible in the runner argv instead of
// silently producing an empty allow-list.
func TestParseAFList(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want []int
	}{
		{"empty", nil, nil},
		{"single", []string{"AF_INET"}, []int{2}},
		{"unprefixed", []string{"INET"}, []int{2}},
		{"lowercase", []string{"af_inet"}, []int{2}},
		{"mixed", []string{"AF_UNIX", "INET6", "17"}, []int{1, 10, 17}},
		{"blanks-dropped", []string{"", " ", "AF_UNIX"}, []int{1}},
	}
	for _, tc := range cases {
		got, err := parseAFList(tc.in)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if len(got) != len(tc.want) {
			t.Fatalf("%s: len=%d want %d (got %v)", tc.name, len(got), len(tc.want), got)
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Errorf("%s[%d]=%d want %d", tc.name, i, got[i], tc.want[i])
			}
		}
	}
}

func TestParseAFListRejectsUnknown(t *testing.T) {
	if _, err := parseAFList([]string{"AF_BOGUS"}); err == nil {
		t.Errorf("expected error for AF_BOGUS")
	}
}
