package journal

import (
	"testing"
	"time"
)

// FuzzUIDLimiter drives the per-uid limit of /dev/log and events.sock
// with arbitrary senders and window changes. One unprivileged user can
// send as many distinct uids as it has subordinate ones, through a user
// namespace and SCM_CREDENTIALS; until 2729311 that filled the table
// and kept root's messages (sudo, login) out of the journal, and gave
// every overflowing uid a notice of its own.
//
// Each 3 input bytes are one message: the first two pick the uid (0,
// root, when both are zero; otherwise one of 65535 subordinate-range
// uids), the third, when below 16, opens a new window first. Checked:
//   - root's first UIDRateLimit(0) messages in a window always pass;
//   - no uid gets more than UIDRateLimit(uid) through in a window;
//   - the table holds at most syslogMaxRateTracks uids besides root;
//   - a new window reports at most one notice per tracked uid plus one
//     for the overflow, each with a positive count.
func FuzzUIDLimiter(f *testing.F) {
	f.Add([]byte{0, 0, 255, 1, 2, 255, 0, 0, 255})
	seq := make([]byte, 0, 3*600)
	for i := 0; i < 600; i++ {
		seq = append(seq, byte(i>>8)+1, byte(i), 255)
	}
	f.Add(append(seq, 0, 0, 255, 0, 0, 0, 0, 0, 255))
	f.Fuzz(func(t *testing.T, data []byte) {
		l := NewUIDLimiter()
		now := time.Unix(1000, 0)
		l.now = func() time.Time { return now }

		passed := map[uint32]int{}
		rootSent := 0
		for i := 0; i+2 < len(data); i += 3 {
			uid := uint32(0)
			if data[i] != 0 || data[i+1] != 0 {
				uid = 100000 + (uint32(data[i])<<8 | uint32(data[i+1]))
			}
			if data[i+2] < 16 {
				now = now.Add(syslogRateWindow)
			}
			notices := 0
			ok := l.Allow(uid, func(u uint32, n int) {
				notices++
				if n <= 0 {
					t.Fatalf("notice for uid %d with count %d", u, n)
				}
			})
			if notices > 0 || data[i+2] < 16 {
				if notices > syslogMaxRateTracks+2 {
					t.Fatalf("a new window opened with %d notices", notices)
				}
				if data[i+2] < 16 {
					clear(passed)
					rootSent = 0
				}
			}
			if uid == 0 {
				rootSent++
				if !ok && rootSent <= UIDRateLimit(0) {
					t.Fatalf("root's message %d of the window was suppressed", rootSent)
				}
			}
			if ok {
				passed[uid]++
				if passed[uid] > UIDRateLimit(uid) {
					t.Fatalf("uid %d got %d messages through, limit %d", uid, passed[uid], UIDRateLimit(uid))
				}
			}
			others := len(l.counts)
			if _, r := l.counts[0]; r {
				others--
			}
			if others > syslogMaxRateTracks {
				t.Fatalf("limiter tracks %d uids besides root, max %d", others, syslogMaxRateTracks)
			}
		}
	})
}
