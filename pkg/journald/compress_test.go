package journald

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sunlightlinux/slinit/pkg/journal"
)

func TestCompressFileRoundtrip(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "a.jsonl")
	payload := strings.Repeat("hello journal ", 200)
	if err := os.WriteFile(src, []byte(payload), 0644); err != nil {
		t.Fatal(err)
	}
	dst, err := CompressFile(src)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(dst, ".gz") {
		t.Fatalf("compressed name should end in .gz, got %q", dst)
	}
	// Original must be gone.
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Fatalf("original should be removed, err=%v", err)
	}
	// Compressed is smaller than the original (payload is
	// repetitive).
	st, _ := os.Stat(dst)
	if st.Size() >= int64(len(payload)) {
		t.Fatalf("compressed size %d not smaller than %d", st.Size(), len(payload))
	}
	// Roundtrip via OpenCompressed.
	rc, err := OpenCompressed(dst)
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != payload {
		t.Fatalf("roundtrip mismatch: got %d bytes want %d", len(got), len(payload))
	}
}

func TestCompressFileMissingSrc(t *testing.T) {
	if _, err := CompressFile("/nonexistent/nope.jsonl"); err == nil {
		t.Fatal("expected error for missing src")
	}
}

func TestOpenCompressedBadFile(t *testing.T) {
	dir := t.TempDir()
	bad := filepath.Join(dir, "not-gzipped.jsonl.gz")
	os.WriteFile(bad, []byte("not gzip data at all"), 0644)
	if _, err := OpenCompressed(bad); err == nil {
		t.Fatal("expected error for non-gzip content")
	}
}

func TestIsCompressed(t *testing.T) {
	cases := map[string]bool{
		"foo.jsonl":    false,
		"foo.jsonl.gz": true,
		"foo.gz":       true,
		"foo":          false,
	}
	for in, want := range cases {
		if got := isCompressed(in); got != want {
			t.Errorf("isCompressed(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestCompressingRotationHookEndToEnd(t *testing.T) {
	dir := t.TempDir()
	var rotated string
	fs, err := NewFileSinkWithOptions(dir, FileSinkOptions{
		FsyncEvery: 1,
		MaxSize:    30,
		MaxAge:     0, // disable age
		RotatedHook: ChainHooks(
			func(rp, cp string) { rotated = rp },
			CompressingRotationHook(),
		),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer fs.Close()

	fs.Handle(&journal.Event{Ts: 1_000_000_000, Msg: "trigger rotation abc"})
	fs.Handle(&journal.Event{Ts: 2_000_000_000, Msg: "second"})

	if rotated == "" {
		t.Fatal("no rotation observed")
	}
	// Rotated .jsonl should be gone, .gz in its place.
	if _, err := os.Stat(rotated); !os.IsNotExist(err) {
		t.Errorf("rotated .jsonl still exists, err=%v", err)
	}
	if _, err := os.Stat(rotated + ".gz"); err != nil {
		t.Errorf(".gz not created: %v", err)
	}
}

func TestChainHooksOrder(t *testing.T) {
	var order []string
	h := ChainHooks(
		func(rp, cp string) { order = append(order, "one:"+rp) },
		func(rp, cp string) { order = append(order, "two:"+rp) },
		nil, // nil hook must be tolerated
		func(rp, cp string) { order = append(order, "three:"+rp) },
	)
	h("/tmp/x.jsonl", "/tmp/cur.jsonl")
	want := []string{"one:/tmp/x.jsonl", "two:/tmp/x.jsonl", "three:/tmp/x.jsonl"}
	if len(order) != len(want) {
		t.Fatalf("got %v, want %v", order, want)
	}
	for i, v := range want {
		if order[i] != v {
			t.Fatalf("order[%d]=%q, want %q", i, order[i], v)
		}
	}
}
