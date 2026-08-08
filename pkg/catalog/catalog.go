// Package catalog implements a systemd-compatible message-catalog
// parser and lookup. Catalog files (.catalog) live under
// /usr/share/slinit-catalog/ (and, for compatibility, systemd's own
// /usr/lib/systemd/catalog/). Each file contains zero or more
// entries in the format:
//
//	-- 128bit-hex-MESSAGE_ID
//	Subject: short one-line summary
//	Defined-By: package-name
//	Support: https://…
//
//	Full multi-line description that may reference @VARIABLES@ from
//	the matching event's field set.
//
// Multiple entries per file are separated by blank lines followed by
// the next `-- <id>` header. Text after the header block is body.
//
// Catalogs augment `slinit-journalctl -x`: an event whose MESSAGE_ID
// field matches an entry is rendered with the entry's body appended
// below the standard message line.
//
// The compiled cache format is a simple gob-serialised map — small
// enough that JSON overhead is negligible but binary parses fast on
// large catalogs.
package catalog

import (
	"bufio"
	"encoding/gob"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Entry is one catalog record.
type Entry struct {
	// ID is the 128-bit MESSAGE_ID, lowercased hex without dashes.
	ID string
	// Headers holds Subject / Defined-By / Support / Documentation /
	// any other RFC-822-style key the source file carries. Preserves
	// case of the value; keys are folded to Title-Case on parse to
	// stabilise map keys.
	Headers map[string]string
	// Body is the full descriptive text, verbatim (leading/trailing
	// blank lines trimmed). Multi-line, may contain @VARS@.
	Body string
}

// Catalog is the parsed collection.
type Catalog struct {
	entries map[string]*Entry
}

// New returns an empty catalog. Only used by tests; production code
// prefers LoadDirs or LoadCompiled.
func New() *Catalog {
	return &Catalog{entries: map[string]*Entry{}}
}

// Len returns the number of entries.
func (c *Catalog) Len() int { return len(c.entries) }

// Lookup returns the entry for id, or nil if not present.
func (c *Catalog) Lookup(id string) *Entry {
	return c.entries[normalizeID(id)]
}

// SortedIDs returns every MESSAGE_ID present, sorted lexicographically.
func (c *Catalog) SortedIDs() []string {
	out := make([]string, 0, len(c.entries))
	for id := range c.entries {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// Dump writes all entries to w in the source format so operators can
// grep / diff. Ordering matches SortedIDs so successive runs produce
// identical output.
func (c *Catalog) Dump(w io.Writer) {
	for _, id := range c.SortedIDs() {
		e := c.entries[id]
		fmt.Fprintf(w, "-- %s\n", e.ID)
		// Stable header order.
		keys := make([]string, 0, len(e.Headers))
		for k := range e.Headers {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Fprintf(w, "%s: %s\n", k, e.Headers[k])
		}
		if e.Body != "" {
			fmt.Fprintln(w)
			fmt.Fprintln(w, e.Body)
		}
		fmt.Fprintln(w)
	}
}

// LoadDirs scans each directory for *.catalog files, parses every
// entry, and returns the merged Catalog. Later files (later dirs)
// override earlier ones on ID collision — matches systemd's
// last-wins convention that lets vendor packages get overridden by
// local site catalogs.
func LoadDirs(dirs ...string) (*Catalog, error) {
	c := New()
	for _, d := range dirs {
		entries, err := os.ReadDir(d)
		if err != nil {
			// Missing directory is fine — the operator may not have
			// installed a slinit-catalog subpackage yet.
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("catalog: read %s: %w", d, err)
		}
		for _, ent := range entries {
			if ent.IsDir() || !strings.HasSuffix(ent.Name(), ".catalog") {
				continue
			}
			path := filepath.Join(d, ent.Name())
			if err := c.LoadFile(path); err != nil {
				return nil, fmt.Errorf("catalog: %s: %w", path, err)
			}
		}
	}
	return c, nil
}

// LoadFile parses one catalog source file. Multiple entries per file
// separated by blank line + `-- ID` header. Malformed entries emit an
// error rather than being silently dropped so package authors see
// mistakes.
func (c *Catalog) LoadFile(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return c.parse(f)
}

func (c *Catalog) parse(r io.Reader) error {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	var (
		cur       *Entry
		inHeaders bool
		body      strings.Builder
	)
	flush := func() {
		if cur == nil {
			return
		}
		cur.Body = strings.TrimSpace(body.String())
		c.entries[cur.ID] = cur
		cur = nil
		body.Reset()
	}
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "-- ") {
			flush()
			id := normalizeID(strings.TrimPrefix(line, "-- "))
			cur = &Entry{ID: id, Headers: map[string]string{}}
			inHeaders = true
			continue
		}
		if cur == nil {
			// Comments before the first entry are ignored (matches
			// systemd's parser).
			continue
		}
		if inHeaders {
			if line == "" {
				inHeaders = false
				continue
			}
			if idx := strings.Index(line, ":"); idx > 0 {
				k := strings.TrimSpace(line[:idx])
				v := strings.TrimSpace(line[idx+1:])
				cur.Headers[titleCaseKey(k)] = v
				continue
			}
			// Line without colon in header block flips to body.
			inHeaders = false
		}
		body.WriteString(line)
		body.WriteByte('\n')
	}
	flush()
	return scanner.Err()
}

// SaveCompiled writes the catalog to path using gob encoding. Fast
// to load on the next --dump/--list without re-parsing every source
// file, and cheap to invalidate — just delete the cache.
func (c *Catalog) SaveCompiled(path string) error {
	tmp := path + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	enc := gob.NewEncoder(f)
	if err := enc.Encode(c.entries); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}

// LoadCompiled reads a previously-saved gob file. Any decode failure
// surfaces as an error so a corrupt cache pushes the caller to fall
// back to a fresh LoadDirs.
func LoadCompiled(path string) (*Catalog, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	c := New()
	dec := gob.NewDecoder(f)
	if err := dec.Decode(&c.entries); err != nil {
		return nil, fmt.Errorf("catalog: decode %s: %w", path, err)
	}
	return c, nil
}

// normalizeID lower-cases the ID and strips dashes so
// `12345678-1234-1234-1234-123456781234` and `12345678123412341234123456781234`
// map to the same key.
func normalizeID(id string) string {
	s := strings.ToLower(strings.TrimSpace(id))
	s = strings.ReplaceAll(s, "-", "")
	return s
}

// titleCaseKey normalises header key case per RFC 822 convention:
// each dash-separated word capitalises its first letter, the rest
// lowercased. So `defined-by`, `Defined-by`, `DEFINED-BY` all end
// up as `Defined-By`.
func titleCaseKey(k string) string {
	if k == "" {
		return k
	}
	parts := strings.Split(k, "-")
	for i, p := range parts {
		if p == "" {
			continue
		}
		parts[i] = strings.ToUpper(p[:1]) + strings.ToLower(p[1:])
	}
	return strings.Join(parts, "-")
}
