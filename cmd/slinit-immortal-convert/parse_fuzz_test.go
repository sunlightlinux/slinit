package main

import (
	"bufio"
	"strings"
	"testing"
)

// FuzzParseImmortalYAML fuzzes the hand-written reader for immortal's
// run.yml subset. A hand-rolled parser is the right trade here (slinit
// carries no YAML dependency and this is PID 1's module) but it is also
// exactly the kind of code that panics on input the author did not
// picture: an indented line with no block open, a colon with nothing
// before it, a list item where a map was expected, a number where a
// number was promised.
//
// A panic would abort the conversion of a whole immortaldir, so the
// contract is only that the parser returns — an error is a fine answer,
// a crash is not.
func FuzzParseImmortalYAML(f *testing.F) {
	// immortal's own README example.
	f.Add(`# pkg install go-www
cmd: www
cwd: /usr/ports
log:
    file: /var/log/www.log
    age: 10  # seconds
    num: 7   # int
    size: 1  # MegaBytes
wait: 1
require:
  - foo
  - bar
`)
	// Every key the converter maps.
	f.Add(`cmd: /usr/sbin/nginx
cwd: /var/www
user: www
retries: 3
wait: 2
env:
    PORT: 8080
logger: /usr/bin/logger
log:
    file: /var/log/x.log
    timestamp: true
stderr:
    file: /var/log/x.err
require:
  - network
require_cmd: /bin/true
post_exit: /bin/true
pid:
    follow: /run/x.pid
    parent: /run/y.pid
    child: /run/z.pid
`)
	// Adversarial: shapes the grammar does not cover, which must be
	// reported rather than crash or be silently taken as something else.
	f.Add("")
	f.Add("cmd:")
	f.Add(":")
	f.Add(": value")
	f.Add("cmd: /bin/true\n    orphan: indented with no block\n")
	f.Add("log:\n  - a list where a map belongs\n")
	f.Add("require:\n    file: a map where a list belongs\n")
	f.Add("cmd: /bin/true\nwait: not-a-number\n")
	f.Add("cmd: /bin/true\nretries: 1e9999\n")
	f.Add("log:\n    size: -1\n    num: -1\n    age: -1\n")
	f.Add("cmd: /bin/true\nenv:\n    : novalue\n")
	f.Add("---\ncmd: /bin/true\n")
	f.Add("cmd: \"quoted: with a colon\"\n")
	f.Add("cmd: /bin/sh -c 'echo a # not a comment'\n")
	f.Add("cmd: /bin/true\n\x00\n")
	f.Add(strings.Repeat("cmd: /bin/true\n", 500))
	f.Add("log:\n" + strings.Repeat("    file: /x\n", 500))
	f.Add(strings.Repeat("  ", 1000) + "cmd: /bin/true")

	f.Fuzz(func(t *testing.T, data string) {
		cfg, warns, err := parseImmortalYAML(bufio.NewScanner(strings.NewReader(data)))
		if err != nil {
			return // a refusal is a valid outcome
		}
		for _, w := range warns {
			_ = w
		}
		// Emitting must survive whatever the parser accepted, since
		// that is the other half of a conversion run.
		var sink strings.Builder
		for _, w := range emitSlinitFile(&sink, cfg, "fuzzed") {
			_ = w
		}
	})
}
