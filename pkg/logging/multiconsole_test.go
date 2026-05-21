package logging

import "testing"

func TestSecondaryConsoleName(t *testing.T) {
	tests := []struct {
		name string
		line string
		want string
	}{
		{
			// VGA, enabled, NOT the /dev/console target -> mirror to it.
			name: "vga enabled not consdev",
			line: "tty0                 -WU (E   p  )    4:1",
			want: "tty0",
		},
		{
			// Serial is the /dev/console target ('C') -> skip (l.output covers it).
			name: "serial is consdev",
			line: "ttyS0                -W- (EC  p  )    4:64",
			want: "",
		},
		{
			// VGA is the /dev/console target -> skip.
			name: "vga is consdev",
			line: "tty0                 -WU (EC  p  )    4:3",
			want: "",
		},
		{
			// Serial enabled, not consdev -> mirror to it.
			name: "serial enabled not consdev",
			line: "ttyS0                -W- (E   p  )    4:64",
			want: "ttyS0",
		},
		{
			name: "disabled console skipped",
			line: "ttyS1                -W- (    p  )    4:65",
			want: "",
		},
		{name: "blank line", line: "", want: ""},
		{name: "garbage one field", line: "garbage", want: ""},
		{name: "no parens", line: "tty0 -WU 4:1", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := secondaryConsoleName(tt.line); got != tt.want {
				t.Errorf("secondaryConsoleName(%q) = %q, want %q", tt.line, got, tt.want)
			}
		})
	}
}
