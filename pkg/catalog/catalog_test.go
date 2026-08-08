package catalog

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestParseSingleEntry covers the happy path: one entry, headers +
// body, correctly round-tripped through Lookup.
func TestParseSingleEntry(t *testing.T) {
	src := `-- 12345678123456781234567812345678
Subject: Test summary
Defined-By: slinit
Support: https://example.org

Long descriptive body
spanning multiple lines.
`
	c := New()
	if err := c.parse(bytes.NewReader([]byte(src))); err != nil {
		t.Fatal(err)
	}
	if c.Len() != 1 {
		t.Fatalf("expected 1 entry, got %d", c.Len())
	}
	e := c.Lookup("12345678123456781234567812345678")
	if e == nil {
		t.Fatal("Lookup miss")
	}
	if e.Headers["Subject"] != "Test summary" {
		t.Errorf("subject: %q", e.Headers["Subject"])
	}
	if !strings.Contains(e.Body, "spanning multiple lines") {
		t.Errorf("body: %q", e.Body)
	}
}

// TestNormalizeIDAcceptsDashesAndCase — Lookup should ignore both
// dashes and case in the incoming ID.
func TestNormalizeIDAcceptsDashesAndCase(t *testing.T) {
	src := `-- ABCDEF01ABCDEF01ABCDEF01ABCDEF01
Subject: X
`
	c := New()
	_ = c.parse(bytes.NewReader([]byte(src)))
	if c.Lookup("abcdef01-abcd-ef01-abcd-ef01abcdef01") == nil {
		t.Error("dashed lowercase lookup missed")
	}
	if c.Lookup("ABCDEF01ABCDEF01ABCDEF01ABCDEF01") == nil {
		t.Error("bare uppercase lookup missed")
	}
}

// TestParseMultipleEntries — blank-line separated entries.
func TestParseMultipleEntries(t *testing.T) {
	src := `-- 11111111111111111111111111111111
Subject: One

Body one.

-- 22222222222222222222222222222222
Subject: Two

Body two.
`
	c := New()
	if err := c.parse(bytes.NewReader([]byte(src))); err != nil {
		t.Fatal(err)
	}
	if c.Len() != 2 {
		t.Fatalf("expected 2 entries, got %d", c.Len())
	}
}

// TestLoadDirsMissingIsFine — a nonexistent dir shouldn't blow up
// (fresh install w/o a slinit-catalog subpackage installed yet).
func TestLoadDirsMissingIsFine(t *testing.T) {
	c, err := LoadDirs("/nonexistent/one", "/nonexistent/two")
	if err != nil {
		t.Fatal(err)
	}
	if c.Len() != 0 {
		t.Errorf("expected 0 entries, got %d", c.Len())
	}
}

// TestSaveLoadCompiledRoundtrip — compiled cache must decode back to
// the same catalog byte-for-byte via Lookup.
func TestSaveLoadCompiledRoundtrip(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "test.catalog")
	body := `-- deadbeefcafedeadbeefcafedeadbeef
Subject: Round-trip test

Detailed explanation of the event.
`
	if err := os.WriteFile(src, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	c, err := LoadDirs(dir)
	if err != nil {
		t.Fatal(err)
	}
	cache := filepath.Join(dir, "compiled")
	if err := c.SaveCompiled(cache); err != nil {
		t.Fatal(err)
	}
	c2, err := LoadCompiled(cache)
	if err != nil {
		t.Fatal(err)
	}
	if c2.Len() != c.Len() {
		t.Errorf("entry count mismatch: %d vs %d", c2.Len(), c.Len())
	}
	e := c2.Lookup("deadbeefcafedeadbeefcafedeadbeef")
	if e == nil || e.Headers["Subject"] != "Round-trip test" {
		t.Errorf("compiled cache lookup failed: %+v", e)
	}
}

// TestDumpFormat — Dump output must be re-parseable by parse (round
// trip). Also ensures deterministic ID ordering.
func TestDumpFormat(t *testing.T) {
	c := New()
	src := `-- 22222222222222222222222222222222
Subject: Two

-- 11111111111111111111111111111111
Subject: One

Body one.
`
	if err := c.parse(bytes.NewReader([]byte(src))); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	c.Dump(&buf)
	// SortedIDs order → smallest first.
	i1 := bytes.Index(buf.Bytes(), []byte("11111111111111111111111111111111"))
	i2 := bytes.Index(buf.Bytes(), []byte("22222222222222222222222222222222"))
	if i1 < 0 || i2 < 0 || i1 > i2 {
		t.Errorf("dump order wrong: i1=%d i2=%d", i1, i2)
	}
	// Round-trip: reparse the dump, get the same set of IDs.
	c2 := New()
	if err := c2.parse(&buf); err != nil {
		t.Fatal(err)
	}
	if c2.Len() != 2 {
		t.Errorf("round-trip lost entries: %d", c2.Len())
	}
}
