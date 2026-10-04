package journalbin

import (
	"encoding/binary"
	"fmt"
	"os"
)

// VerifyResult reports the outcome of a Verify() call.
type VerifyResult struct {
	// SealingEnabled is true when the file was written with FSS
	// (CompatFlagSealed set). If false, Verify short-circuits with
	// no error — nothing to check.
	SealingEnabled bool
	// TagsChecked is the number of TAG objects successfully verified.
	TagsChecked int
	// FirstBadTagOffset is the file offset of the first TAG whose
	// HMAC did not match the recomputed value. Zero when everything
	// verified cleanly.
	FirstBadTagOffset uint64
	// FirstBadTagSeqnum is the seqnum stored in that bad TAG (for
	// operator-facing "corruption starts around tag N" messages).
	FirstBadTagSeqnum uint64
	// EntriesSealed is how many ENTRY objects lie inside a byte range
	// that a TAG verified.
	EntriesSealed int
	// HiddenEntries is how many of those sealed entries cannot be
	// reached by walking the ENTRY_ARRAY chain.
	//
	// Entry arrays are mutable metadata, rewritten as the journal
	// grows, so they are deliberately outside HMAC scope — which left
	// a gap: zeroing one slot of an array truncates what the
	// array-based query path (EntryOffsets, SeekRealtime, and so
	// `--since`/`--until`) can see, while every TAG still verifies.
	// Measured: 4 of 10 entries vanished from that path with the file
	// reporting "OK (2 tags verified)".
	//
	// The writer links an entry into the array before any TAG can seal
	// it (appendEntryOffsetLocked runs before writeSealTagLocked), so a
	// sealed entry that is unreachable is tampering, not a write race.
	HiddenEntries int
	// UnsealedTailBytes is how many bytes sit between the last verified
	// TAG and the tail object. Nothing seals them — a journald killed
	// before its closing TAG leaves such a tail — so they are
	// reportable, not verifiable.
	UnsealedTailBytes uint64
}

// OK returns true when the file is sealing-disabled OR every TAG
// verified cleanly AND no sealed entry has been hidden from the
// ENTRY_ARRAY chain.
func (v VerifyResult) OK() bool {
	return v.FirstBadTagOffset == 0 && v.HiddenEntries == 0
}

// Verify walks the TAG chain of a journal file, recomputing each
// tag's HMAC over the covered byte range and comparing to the
// stored value. Returns as soon as a mismatch is found (the first
// tamper point is what an operator needs — everything after that
// tag is unreliable).
//
// The caller supplies the FSS key that was in force at the file's
// original write time. Keys are per-machine (not per-file), so
// operators pass the same /etc/slinit/journal-key that seed'd the
// journald daemon.
//
// Returns an error only for I/O or format failures (missing file,
// corrupt header, TAG object shorter than expected). A clean-verify
// or a first-bad-tag both surface via VerifyResult, not err.
func Verify(path string, key *FSSKey) (VerifyResult, error) {
	if key == nil {
		return VerifyResult{}, fmt.Errorf("journalbin: verify needs an FSS key")
	}
	f, err := os.Open(path)
	if err != nil {
		return VerifyResult{}, fmt.Errorf("journalbin: verify open %s: %w", path, err)
	}
	defer f.Close()

	hdrBuf := make([]byte, HeaderSize)
	if _, err := f.ReadAt(hdrBuf, 0); err != nil {
		return VerifyResult{}, fmt.Errorf("journalbin: verify read header: %w", err)
	}
	h, err := DecodeHeader(hdrBuf)
	if err != nil {
		return VerifyResult{}, err
	}

	res := VerifyResult{SealingEnabled: h.CompatFlags&CompatFlagSealed != 0}
	if !res.SealingEnabled {
		return res, nil
	}

	// Walk objects linearly, verifying each TAG we hit.
	// State: prevTagEnd (byte offset of the byte-range start for the
	// NEXT tag we encounter). Starts at HeaderSize — everything after
	// the header up to (but not including) the first TAG's start is
	// what that TAG covers.
	prevTagEnd := uint64(HeaderSize)
	off := uint64(HeaderSize)
	// Entries seen since prevTagEnd; promoted to sealedEntries when the
	// TAG covering them verifies.
	var pendingEntries, sealedEntries []uint64
	for off < h.TailObjectOffset {
		var ohBuf [ObjectHeaderSize]byte
		if _, err := f.ReadAt(ohBuf[:], int64(off)); err != nil {
			return res, fmt.Errorf("journalbin: verify read obj hdr at %d: %w", off, err)
		}
		oh, err := DecodeObjectHeader(ohBuf[:])
		if err != nil {
			return res, err
		}
		if oh.Size < ObjectHeaderSize {
			return res, fmt.Errorf("journalbin: verify: bad object size %d at %d", oh.Size, off)
		}
		if off+oh.Size > uint64(fileSize(f)) {
			return res, fmt.Errorf("journalbin: verify: obj at %d claims size past file end", off)
		}
		if oh.Type == ObjectEntry {
			pendingEntries = append(pendingEntries, off)
		}
		if oh.Type == ObjectTag {
			// TAG covers [prevTagEnd .. off). Recompute HMAC, compare.
			var tagBody [tagObjectSize - ObjectHeaderSize]byte
			if _, err := f.ReadAt(tagBody[:], int64(off)+int64(ObjectHeaderSize)); err != nil {
				return res, fmt.Errorf("journalbin: verify read tag body at %d: %w", off, err)
			}
			seqnum := binary.LittleEndian.Uint64(tagBody[0:8])
			epoch := int64(binary.LittleEndian.Uint64(tagBody[8:16]))
			storedHMAC := tagBody[16 : 16+FSSSealTagSize]

			key32, err := key.DeriveEpochKey(epoch)
			if err != nil {
				return res, err
			}
			// Reuse the same immutable-only collector the writer uses.
			// Any tamper in an entry body or a data payload changes
			// the HMAC input; tamper in mutable metadata (hash tables,
			// entry arrays) is outside our scope but caught elsewhere
			// (chain walk or Reader.Iter surface it as decode errors).
			digestInput, err := collectHMACInput(f, prevTagEnd, off)
			if err != nil {
				return res, err
			}
			computed := SealHMACBytes(key32, digestInput)
			if !hmacEqual(storedHMAC, computed) {
				res.FirstBadTagOffset = off
				res.FirstBadTagSeqnum = seqnum
				return res, nil
			}
			res.TagsChecked++
			sealedEntries = append(sealedEntries, pendingEntries...)
			pendingEntries = pendingEntries[:0]
			prevTagEnd = off + AlignUp(oh.Size)
		}
		off += AlignUp(oh.Size)
	}

	// Anything after the last verified TAG is sealed by nothing. Say how
	// much rather than leaving a clean verdict to imply it was checked.
	if h.TailObjectOffset > prevTagEnd {
		res.UnsealedTailBytes = h.TailObjectOffset - prevTagEnd
	}

	// Reconcile the two enumeration paths. A sealed ENTRY that the
	// array chain cannot reach has been unlinked by someone editing
	// metadata the HMAC does not cover.
	res.EntriesSealed = len(sealedEntries)
	if res.EntriesSealed > 0 {
		reachable := make(map[uint64]struct{}, res.EntriesSealed)
		if err := walkEntryOffsets(f, h.EntryArrayOffset, func(eo uint64) bool {
			reachable[eo] = struct{}{}
			return true
		}); err != nil {
			// A broken array chain is itself a finding, but it is a
			// decode failure rather than a silent omission, so it goes
			// back as an error the caller already surfaces.
			return res, fmt.Errorf("journalbin: verify walk entry arrays: %w", err)
		}
		for _, eo := range sealedEntries {
			if _, ok := reachable[eo]; !ok {
				res.HiddenEntries++
			}
		}
	}
	return res, nil
}

// collectHMACInput reads objects in [from..to) and returns the
// concatenation of their IMMUTABLE bytes: full ENTRY bodies + DATA
// payloads (excluding the mutable hash-chain metadata prefix
// dataFixedPart). Skips hash tables, entry arrays, TAG objects, and
// unused holes — none of which are stable across later Appends.
//
// Same iteration used by both the writer (compute HMAC before writing
// a TAG) and the verifier (recompute HMAC for a TAG we already have),
// so a byte-identical input is guaranteed.
func collectHMACInput(f *os.File, from, to uint64) ([]byte, error) {
	var out []byte
	off := from
	for off < to {
		var ohBuf [ObjectHeaderSize]byte
		if _, err := f.ReadAt(ohBuf[:], int64(off)); err != nil {
			return nil, fmt.Errorf("journalbin: collect hmac read hdr at %d: %w", off, err)
		}
		oh, err := DecodeObjectHeader(ohBuf[:])
		if err != nil {
			return nil, err
		}
		if oh.Size < ObjectHeaderSize {
			return nil, fmt.Errorf("journalbin: collect hmac bad size %d at %d", oh.Size, off)
		}
		next := off + AlignUp(oh.Size)
		switch oh.Type {
		case ObjectEntry:
			// Whole ENTRY body — immutable after write.
			body := make([]byte, oh.Size)
			if _, err := f.ReadAt(body, int64(off)); err != nil {
				return nil, fmt.Errorf("journalbin: collect hmac read entry at %d: %w", off, err)
			}
			out = append(out, body...)
		case ObjectData:
			// Only the KEY=value payload — the leading fixed part
			// (hash + next_hash + next_field + entry_offset +
			// entry_array_offset + n_entries) is mutable via chain
			// linking.
			if oh.Size > dataFixedPart {
				payload := make([]byte, oh.Size-dataFixedPart)
				if _, err := f.ReadAt(payload, int64(off)+int64(dataFixedPart)); err != nil {
					return nil, fmt.Errorf("journalbin: collect hmac read data payload at %d: %w", off, err)
				}
				out = append(out, payload...)
			}
		default:
			// Hash tables, entry arrays, TAGs, unused: skipped
			// (either mutable or out-of-scope for content integrity).
		}
		off = next
	}
	return out, nil
}

// hmacEqual compares two HMAC outputs in constant time via subtle.
// Kept as a tiny inline function to avoid pulling crypto/subtle
// into the top of the file (used in one place).
func hmacEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	var v byte
	for i := range a {
		v |= a[i] ^ b[i]
	}
	return v == 0
}

// fileSize is a helper that returns the size of an open *os.File or
// 0 on error. Used in bounds checks where a stat failure means
// something worse is wrong upstream — no point propagating.
func fileSize(f *os.File) int64 {
	st, err := f.Stat()
	if err != nil {
		return 0
	}
	return st.Size()
}
