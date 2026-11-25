package main

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"io"
	"strings"
	"testing"
)

// parseExport reads systemd export-format entries — KEY=value lines,
// or KEY, a newline, a little-endian 64-bit length, the value and a
// newline — separated by blank lines.
func parseExport(t *testing.T, data []byte) []map[string]string {
	t.Helper()
	r := bufio.NewReader(bytes.NewReader(data))
	var entries []map[string]string
	cur := map[string]string{}
	for {
		line, err := r.ReadString('\n')
		if err == io.EOF && line == "" {
			break
		}
		if err != nil {
			t.Fatalf("truncated export stream at %q", line)
		}
		line = strings.TrimSuffix(line, "\n")
		if line == "" {
			entries = append(entries, cur)
			cur = map[string]string{}
			continue
		}
		if k, v, ok := strings.Cut(line, "="); ok {
			cur[k] = v
			continue
		}
		var n uint64
		if err := binary.Read(r, binary.LittleEndian, &n); err != nil {
			t.Fatalf("field %q: no length: %v", line, err)
		}
		v := make([]byte, n+1)
		if _, err := io.ReadFull(r, v); err != nil || v[n] != '\n' {
			t.Fatalf("field %q: bad binary value", line)
		}
		cur[line] = string(v[:n])
	}
	return entries
}

// TestRenderExportBinarySafe — a value with a newline or another
// control character used to be written as KEY=value, so a multi-line
// MESSAGE split the entry and its second line became a field.
func TestRenderExportBinarySafe(t *testing.T) {
	e := mkEvent()
	e.Msg = "first line\n\nCUSTOM_KEY=forged\x01"
	e.Fields = map[string]string{"TAB_FIELD": "a\tb", "BAD_UTF8": "x\xffy", "PLAIN": "ok"}
	var buf bytes.Buffer
	for i := 0; i < 2; i++ {
		if err := render(&buf, fmtExport, e, renderOpts{}); err != nil {
			t.Fatal(err)
		}
	}
	var n [8]byte
	binary.LittleEndian.PutUint64(n[:], uint64(len(e.Msg)))
	if !bytes.Contains(buf.Bytes(), []byte("MESSAGE\n"+string(n[:])+e.Msg+"\n")) {
		t.Errorf("MESSAGE not in binary form:\n%q", buf.String())
	}
	entries := parseExport(t, buf.Bytes())
	if len(entries) != 2 {
		t.Fatalf("parsed %d entries, want 2:\n%q", len(entries), buf.String())
	}
	for _, got := range entries {
		for k, want := range map[string]string{"MESSAGE": e.Msg, "TAB_FIELD": "a\tb", "BAD_UTF8": "x\xffy", "PLAIN": "ok", "_SLINIT_UNIT": "sshd"} {
			if got[k] != want {
				t.Errorf("%s = %q, want %q", k, got[k], want)
			}
		}
		if _, forged := got["CUSTOM_KEY"]; forged {
			t.Errorf("message line read as a field: %q", got["CUSTOM_KEY"])
		}
	}
}
