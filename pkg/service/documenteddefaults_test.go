package service

import (
	"os"
	"strings"
	"testing"
	"time"
)

// Every default this package decides must be stated in
// slinit-service(5), with the value the code actually uses.
//
// This exists because a manual sweep of the page's numeric defaults
// found ten claims, one of them wrong — ready-check-interval was
// documented as 100ms and is 1s — and then found that five more
// defaults were not documented at all. stop-timeout is the one that
// matters: an operator reading the page could not learn how long a
// stop waits before SIGKILL, and the figure had to be read out of
// process.go.
//
// Three things the sweep taught, which is why the table is shaped like
// this rather than grepping for the word "default":
//
//   - matching the word is too loose. restart-limit-interval's entry
//     contains "(default) leaves the service *failed*", which is about
//     something else entirely, and scored as documented.
//   - matching digits is too strict. hook-timeout says "Default five
//     seconds" in words.
//   - so each row names the substring the page should contain, and the
//     row also pins the constant's value. Change the constant and the
//     pin fails; change the page and the substring fails. Either way a
//     human has to touch all three and notice.
func TestDocumentedDefaultsMatchTheCode(t *testing.T) {
	cases := []struct {
		directive string
		live      time.Duration // the constant this package uses
		pinned    time.Duration // the same value, written out here
		docSays   string        // substring the man-page entry must contain
	}{
		{"stop-timeout", defaultStopTimeout, 10 * time.Second, "Default *10*"},
		{"start-timeout", defaultStartTimeout, 60 * time.Second, "Default *60*"},
		{"restart-delay", defaultRestartDelay, 200 * time.Millisecond, "Default *0.2*"},
		{"restart-limit-interval", defaultRestartInterval, 10 * time.Second, "*10* seconds and *3*"},
		{"hook-timeout", defaultFinishTimeout, 5 * time.Second, "Default five seconds"},
		{"ready-check-interval", time.Second, time.Second, "Default *1s*"},
	}

	page, err := os.ReadFile("../../doc/man/slinit-service.5.md")
	if err != nil {
		t.Fatalf("reading the man page: %v", err)
	}

	for _, c := range cases {
		if c.live != c.pinned {
			t.Errorf("%s: the constant is now %v but this table still says %v — update the table AND slinit-service(5)",
				c.directive, c.live, c.pinned)
		}
		entry := manEntry(string(page), c.directive)
		if entry == "" {
			t.Errorf("%s: no entry found in slinit-service(5)", c.directive)
			continue
		}
		if !strings.Contains(entry, c.docSays) {
			t.Errorf("%s: slinit-service(5) does not state %q; its default is %v",
				c.directive, c.docSays, c.live)
		}
	}
}

// Two counts that are not durations, kept separate rather than bent
// into the table above.
func TestDocumentedNonDurationDefaults(t *testing.T) {
	page, err := os.ReadFile("../../doc/man/slinit-service.5.md")
	if err != nil {
		t.Fatalf("reading the man page: %v", err)
	}
	if defaultMaxRestarts != 3 {
		t.Errorf("defaultMaxRestarts is %d; the page says 3", defaultMaxRestarts)
	}
	if defaultScrollback != 65536 {
		t.Errorf("defaultScrollback is %d; the page says 65536", defaultScrollback)
	}
	for _, c := range []struct{ directive, docSays string }{
		{"restart-limit-interval", "*3* failures"},
		{"vtty-scrollback", "default 65536"},
	} {
		if entry := manEntry(string(page), c.directive); !strings.Contains(entry, c.docSays) {
			t.Errorf("%s: slinit-service(5) does not state %q", c.directive, c.docSays)
		}
	}
}

// manEntry returns the body of a `**name**=` definition-list entry: the
// line itself plus everything up to the blank line that ends it. The
// page separates entries by a blank line, so this is enough to keep a
// claim about one directive from matching inside its neighbour.
//
// Whitespace is collapsed to single spaces before returning, because
// the page is wrapped at column 72 and a claim can straddle a line
// break — "Defaults are *10* seconds and *3*\n    failures." The first
// row written against this function searched for "*3* failures" and
// failed on exactly that, which would otherwise be a trap for every
// row added later.
func manEntry(page, name string) string {
	marker := "**" + name + "**="
	for _, block := range strings.Split(page, "\n\n") {
		if strings.HasPrefix(strings.TrimSpace(block), marker) {
			return strings.Join(strings.Fields(block), " ")
		}
	}
	return ""
}
