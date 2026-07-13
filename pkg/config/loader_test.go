package config

import "testing"

func TestParseIOPrio(t *testing.T) {
	tests := []struct {
		input     string
		wantClass int
		wantLevel int
	}{
		{"be:4", 2, 4},
		{"best-effort:0", 2, 0},
		{"rt:7", 1, 7},
		{"realtime:3", 1, 3},
		{"idle", 3, 0},
		{"2:5", 2, 5},
		{"invalid", -1, 0},
		{"be", 2, 0},
	}

	for _, tt := range tests {
		class, level := parseIOPrio(tt.input)
		if class != tt.wantClass || level != tt.wantLevel {
			t.Errorf("parseIOPrio(%q): got (%d, %d), want (%d, %d)",
				tt.input, class, level, tt.wantClass, tt.wantLevel)
		}
	}
}

func TestParseRlimitHelper(t *testing.T) {
	tests := []struct {
		input    string
		wantSoft uint64
		wantHard uint64
		wantErr  bool
	}{
		{"1024", 1024, 1024, false},
		{"1024:4096", 1024, 4096, false},
		{"unlimited", ^uint64(0), ^uint64(0), false},
		{"abc", 0, 0, true},
	}

	for _, tt := range tests {
		lim, err := parseRlimit(tt.input)
		if (err != nil) != tt.wantErr {
			t.Errorf("parseRlimit(%q): error = %v, wantErr = %v", tt.input, err, tt.wantErr)
			continue
		}
		if err != nil {
			continue
		}
		if lim[0] != tt.wantSoft || lim[1] != tt.wantHard {
			t.Errorf("parseRlimit(%q): got [%d, %d], want [%d, %d]",
				tt.input, lim[0], lim[1], tt.wantSoft, tt.wantHard)
		}
	}
}

// TestResolveAlertLevel pins the fallback matrix: file empty →
// disabled (-1), file set + level parsed → level unchanged, file set +
// level default (-1) → warn (4) so operators declaring only the file
// still get sane routing.
func TestResolveAlertLevel(t *testing.T) {
	tests := []struct {
		name     string
		file     string
		parsed   int
		expected int
	}{
		{"no-file-no-level", "", -1, -1},
		{"no-file-with-level", "", 3, -1},
		{"file-no-level", "/tmp/a.log", -1, 4},
		{"file-with-level", "/tmp/a.log", 2, 2},
		{"file-with-emerg", "/tmp/a.log", 0, 0},
	}
	for _, tt := range tests {
		got := resolveAlertLevel(tt.file, tt.parsed)
		if got != tt.expected {
			t.Errorf("%s: resolveAlertLevel(%q, %d) = %d, want %d",
				tt.name, tt.file, tt.parsed, got, tt.expected)
		}
	}
}
