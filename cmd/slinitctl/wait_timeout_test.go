package main

import (
	"net"
	"testing"
	"time"
)

// TestReadReplyHonorsWaitTimeout mimics an unresponsive daemon: an
// open socket that produces no data. With waitTimeout set, readReply
// must return a net.Error with Timeout() == true rather than block
// indefinitely.
func TestReadReplyHonorsWaitTimeout(t *testing.T) {
	// Backup + restore the package-level knob so the test doesn't
	// leak state into any other cases in this binary.
	orig := waitTimeout
	defer func() { waitTimeout = orig }()

	// socketpair-style: two ends of a net.Pipe with no writer.
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()

	waitTimeout = 200 * time.Millisecond

	start := time.Now()
	_, _, err := readReply(client)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatalf("expected timeout error, got nil after %v", elapsed)
	}
	nerr, ok := err.(net.Error)
	if !ok || !nerr.Timeout() {
		t.Errorf("expected net.Error{Timeout:true}, got %T %v", err, err)
	}
	if elapsed < 150*time.Millisecond || elapsed > 900*time.Millisecond {
		t.Errorf("elapsed %v is outside the expected timeout window", elapsed)
	}
}

// TestReadReplyNoTimeoutWhenUnset confirms the zero-value fast path
// leaves the socket in its default no-deadline state — a subsequent
// blocking read must not race with a lingering deadline installed by
// a previous call. We join the reader goroutine before restoring
// waitTimeout so the read of waitTimeout inside readReply cannot race
// with the test's own write.
func TestReadReplyNoTimeoutWhenUnset(t *testing.T) {
	orig := waitTimeout
	waitTimeout = 0

	client, server := net.Pipe()

	done := make(chan error, 1)
	go func() {
		_, _, err := readReply(client)
		done <- err
	}()

	select {
	case err := <-done:
		t.Fatalf("readReply returned prematurely without a deadline: %v", err)
	case <-time.After(150 * time.Millisecond):
		// Good — still blocked, as expected.
	}

	// Unblock the goroutine and wait for it to finish so the
	// subsequent write to waitTimeout does not race with the read
	// inside readReply.
	client.Close()
	server.Close()
	<-done
	waitTimeout = orig
}
