package journald

import (
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"strings"
)

// compressedSuffix is appended to a rotated jsonl file after
// successful compression. `.jsonl.gz` reads clearly to operators and
// standard tools (`zcat`, `zgrep`, `less`) handle it natively.
//
// The project plan named LZ4 as the preferred algorithm for its
// faster decompression, but adding a new external module for what is
// a Phase 3 quality-of-life feature would blow up the dependency
// graph beyond what a small init system should carry. gzip from
// stdlib gets 55-65% savings on JSONL (vs LZ4's 50-60%) with slower
// decompress (still ~200 MB/s single-thread) — acceptable trade for
// zero-dep. A future swap to LZ4 replaces CompressFile /
// OpenCompressed only; the on-disk .jsonl.gz convention would become
// .jsonl.lz4 at the same time.
const compressedSuffix = ".gz"

// CompressFile reads src line-by-line, writes it gzip-compressed to
// src+compressedSuffix, fsyncs, and removes the original on success.
// Compression happens synchronously with the caller — a busy daemon
// that rotates every few minutes never accumulates a backlog of
// uncompressed files.
//
// Returns the path of the compressed file on success. Nothing is
// removed on error; the original stays in place so a retry can pick
// up where the failure happened.
func CompressFile(src string) (string, error) {
	in, err := os.Open(src)
	if err != nil {
		return "", fmt.Errorf("journald: compress open %s: %w", src, err)
	}
	defer in.Close()

	dst := src + compressedSuffix
	// Create with O_EXCL to catch the case where a stale .gz from a
	// prior interrupted compression is lying around — surfacing
	// as an error is better than silently overwriting.
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0640)
	if err != nil {
		return "", fmt.Errorf("journald: compress create %s: %w", dst, err)
	}
	// gzip.BestSpeed favors compression time over ratio. For write-
	// once/read-rarely files that's the wrong tradeoff, but for the
	// synchronous-with-rotation path we want to minimise the
	// wall-clock cost of every rotation. Operators who want tighter
	// ratios can post-process with pigz or zstd — the format is
	// interoperable.
	gz, err := gzip.NewWriterLevel(out, gzip.BestSpeed)
	if err != nil {
		_ = out.Close()
		_ = os.Remove(dst)
		return "", fmt.Errorf("journald: gzip writer: %w", err)
	}
	if _, err := io.Copy(gz, in); err != nil {
		_ = gz.Close()
		_ = out.Close()
		_ = os.Remove(dst)
		return "", fmt.Errorf("journald: gzip copy: %w", err)
	}
	if err := gz.Close(); err != nil {
		_ = out.Close()
		_ = os.Remove(dst)
		return "", fmt.Errorf("journald: gzip close: %w", err)
	}
	if err := out.Sync(); err != nil {
		_ = out.Close()
		_ = os.Remove(dst)
		return "", fmt.Errorf("journald: gzip fsync: %w", err)
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(dst)
		return "", fmt.Errorf("journald: gzip close-file: %w", err)
	}
	if err := os.Remove(src); err != nil {
		// The compressed file is fine; the original just won't
		// disappear. Surface but don't roll back — vacuum will pick
		// the orphaned original up on a later sweep.
		return dst, fmt.Errorf("journald: compress: remove original %s: %w", src, err)
	}
	return dst, nil
}

// OpenCompressed opens a `.jsonl.gz` file and returns an io.ReadCloser
// that yields the decompressed stream. Caller must Close when done.
// Named symmetrically with os.Open so it slots into existing readers
// that expect an io.Reader.
func OpenCompressed(path string) (io.ReadCloser, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	gz, err := gzip.NewReader(f)
	if err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("journald: gzip reader %s: %w", path, err)
	}
	return &gzipReadCloser{gz: gz, f: f}, nil
}

// gzipReadCloser bundles the gzip.Reader with its underlying file so
// Close tears both down in the right order.
type gzipReadCloser struct {
	gz *gzip.Reader
	f  *os.File
}

func (g *gzipReadCloser) Read(p []byte) (int, error) { return g.gz.Read(p) }
func (g *gzipReadCloser) Close() error {
	err := g.gz.Close()
	if err2 := g.f.Close(); err == nil {
		err = err2
	}
	return err
}

// isCompressed returns true when path ends with the compressed
// suffix. Used by callers that want to route to OpenCompressed vs
// plain os.Open.
func isCompressed(path string) bool { return strings.HasSuffix(path, compressedSuffix) }

// CompressingRotationHook returns a RotatedHook that compresses the
// freshly rotated .jsonl (and leaves its .idx alone — .idx byte
// offsets reference the DECOMPRESSED stream, which is what any
// bisect reader wants). Wire it into FileSinkOptions.RotatedHook —
// composes with VacuumingHook by chaining hooks manually:
//
//	RotatedHook: func(rp, cp string) {
//	    journald.CompressingRotationHook()(rp, cp)
//	    journald.VacuumingHook(dir, vac)(rp, cp)
//	}
//
// Kept as a factory (rather than a bare function) so future options
// (level, algorithm) can be added without changing every callsite.
func CompressingRotationHook() func(rotatedPath, currentPath string) {
	return func(rotatedPath, _ string) {
		_, _ = CompressFile(rotatedPath)
	}
}

// ChainHooks composes multiple RotatedHooks into a single hook that
// fires them in order. Convenience for the common
// compress-then-vacuum wiring in slinit-journald main.
func ChainHooks(hooks ...func(string, string)) func(string, string) {
	return func(rotatedPath, currentPath string) {
		for _, h := range hooks {
			if h != nil {
				h(rotatedPath, currentPath)
			}
		}
	}
}
