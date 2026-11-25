package main

import (
	"strings"
	"testing"
)

// TestFollowBacklogHonoursLimitAndCursor — -f used to send the daemon
// no limit when a client-side filter was set, and ignored the cursor
// options (measured: -f -n 3 -t sshd printed 10 entries, -f
// --after-cursor <last> printed all 20).
func TestFollowBacklogHonoursLimitAndCursor(t *testing.T) {
	sock := startFakeJournalServer(t)
	cases := []struct {
		args []string
		want string
	}{
		{[]string{"-f", "-n", "3", "-t", "sshd"}, "msg 15\nmsg 17\nmsg 19\n"},
		{[]string{"-f", "-n", "2", "-g", "msg 1"}, "msg 18\nmsg 19\n"},
		{[]string{"-f", "-n", "2"}, "msg 18\nmsg 19\n"},
	}
	for _, c := range cases {
		if out, err := queryLive(t, sock, c.args...); err != nil || out != c.want {
			t.Errorf("%v: %q, %v; want %q", c.args, out, err, c.want)
		}
	}

	out, err := queryLive(t, sock, "-n", "2", "--show-cursor")
	if err != nil {
		t.Fatal(err)
	}
	_, cur, _ := strings.Cut(out, "-- cursor: ")
	cur = strings.TrimSpace(cur)
	if out, err := queryLive(t, sock, "-f", "--after-cursor", cur); err != nil || out != "" {
		t.Errorf("-f --after-cursor <last>: %q, %v; want nothing", out, err)
	}
	if out, err := queryLive(t, sock, "-f", "-c", cur); err != nil || out != "msg 19\n" {
		t.Errorf("-f -c <last>: %q, %v; want msg 19", out, err)
	}
	if _, err := queryLive(t, sock, "-f", "-c", "garbage"); err == nil {
		t.Error("-f -c garbage: want an error")
	}
	// Without -n the whole matching buffer is replayed, as documented.
	if out, _ := queryLive(t, sock, "-f"); strings.Count(out, "\n") != 20 {
		t.Errorf("-f: %d lines, want 20", strings.Count(out, "\n"))
	}
}
