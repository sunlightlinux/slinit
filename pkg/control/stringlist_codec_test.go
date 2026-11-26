package control

import (
	"bytes"
	"reflect"
	"testing"
)

// The string-list encoding carries RplyProfileList, RplyBundleMembers
// and the three name lists inside RplyActivateResult. The layout is
// fixed on the wire, so pin the bytes, not only the round trip.
func TestEncodeStringListLayout(t *testing.T) {
	got := EncodeStringList([]string{"ab", "", "xyz"})
	want := []byte{
		3, 0, // count
		2, 0, 'a', 'b',
		0, 0,
		3, 0, 'x', 'y', 'z',
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("EncodeStringList = %v, want %v", got, want)
	}
	if got := EncodeStringList(nil); !bytes.Equal(got, []byte{0, 0}) {
		t.Fatalf("EncodeStringList(nil) = %v, want [0 0]", got)
	}
}

func TestStringListRoundTrip(t *testing.T) {
	cases := [][]string{
		{},
		{""},
		{"default"},
		{"a", "", "bb", "service-with-a-longer-name"},
		{string(bytes.Repeat([]byte{'x'}, 300))}, // length needs the high byte
	}
	for _, in := range cases {
		enc := EncodeStringList(in)
		// Trailing bytes belong to the next field and must not be eaten.
		buf := append(append([]byte{}, enc...), 0xEE, 0xEE)
		out, n, err := DecodeStringList(buf)
		if err != nil {
			t.Fatalf("%q: decode: %v", in, err)
		}
		if n != len(enc) {
			t.Fatalf("%q: consumed %d bytes, encoding is %d", in, n, len(enc))
		}
		if !reflect.DeepEqual(out, in) {
			t.Fatalf("round trip: got %q, want %q", out, in)
		}
	}
}

// Every proper prefix of a valid encoding is truncated somewhere and
// must be refused rather than read past its end.
func TestDecodeStringListRejectsTruncation(t *testing.T) {
	enc := EncodeStringList([]string{"ab", "", "xyz"})
	for i := 0; i < len(enc); i++ {
		if out, n, err := DecodeStringList(enc[:i]); err == nil {
			t.Fatalf("prefix len %d: accepted (%q, n=%d)", i, out, n)
		}
	}
}

func TestDecodeStringListRejectsOverlongLengths(t *testing.T) {
	cases := map[string][]byte{
		"count exceeds entries":  {0xFF, 0xFF},
		"entry length past end":  {1, 0, 0xFF, 0xFF, 'a', 'b', 'c'},
		"entry length one over":  {1, 0, 4, 0, 'a', 'b', 'c'},
		"second entry truncated": {2, 0, 1, 0, 'a', 1},
	}
	for name, data := range cases {
		if out, _, err := DecodeStringList(data); err == nil {
			t.Errorf("%s: accepted %q", name, out)
		}
	}
}

func TestActivateResultRoundTrip(t *testing.T) {
	active := "work"
	stopped := []string{"home-sync"}
	started := []string{"vpn", "work-mail"}
	kept := []string{}
	enc := EncodeActivateResult(active, stopped, started, kept)

	a, s, st, k, err := DecodeActivateResult(enc)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if a != active {
		t.Errorf("active = %q, want %q", a, active)
	}
	if !reflect.DeepEqual(s, stopped) {
		t.Errorf("stopped = %q, want %q", s, stopped)
	}
	if !reflect.DeepEqual(st, started) {
		t.Errorf("started = %q, want %q", st, started)
	}
	if !reflect.DeepEqual(k, kept) {
		t.Errorf("kept = %q, want %q", k, kept)
	}

	// The layout is the name followed by the three lists, in order.
	want := append([]byte{}, EncodeServiceName(active)...)
	want = append(want, EncodeStringList(stopped)...)
	want = append(want, EncodeStringList(started)...)
	want = append(want, EncodeStringList(kept)...)
	if !bytes.Equal(enc, want) {
		t.Fatalf("EncodeActivateResult = %v, want %v", enc, want)
	}
}

// A cut anywhere — in the name, or in any of the three lists — is an
// error, and the decoder never returns partial results with it.
func TestDecodeActivateResultRejectsTruncation(t *testing.T) {
	enc := EncodeActivateResult("p", []string{"s1"}, []string{"s2", "s3"}, []string{"k"})
	for i := 0; i < len(enc); i++ {
		a, s, st, k, err := DecodeActivateResult(enc[:i])
		if err == nil {
			t.Fatalf("prefix len %d: accepted (%q %q %q %q)", i, a, s, st, k)
		}
		if a != "" || s != nil || st != nil || k != nil {
			t.Fatalf("prefix len %d: partial result with error: %q %q %q %q", i, a, s, st, k)
		}
	}
}
