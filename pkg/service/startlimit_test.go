package service

import (
	"testing"
	"time"
)

func TestStartLimiter_UnlimitedByDefault(t *testing.T) {
	sl := NewStartLimiter(0, 10*time.Second)
	set, _ := newTestSet()
	svc := NewInternalService(set, "unlimited")

	ok, _ := sl.Acquire(svc)
	if !ok {
		t.Error("expected unlimited limiter to always grant slot")
	}
	sl.Release(svc) // should be no-op
}

func TestStartLimiter_BasicConcurrency(t *testing.T) {
	sl := NewStartLimiter(2, 10*time.Second)
	set, _ := newTestSet()

	svc1 := NewInternalService(set, "svc1")
	svc2 := NewInternalService(set, "svc2")
	svc3 := NewInternalService(set, "svc3")

	// First two should get slots
	ok1, _ := sl.Acquire(svc1)
	ok2, _ := sl.Acquire(svc2)
	if !ok1 || !ok2 {
		t.Fatal("first two services should get slots")
	}

	if sl.ActiveCount() != 2 {
		t.Errorf("expected 2 active, got %d", sl.ActiveCount())
	}

	// Third should be queued
	ok3, waitCh := sl.Acquire(svc3)
	if ok3 {
		t.Fatal("third service should be queued")
	}
	if waitCh == nil {
		t.Fatal("expected wait channel for queued service")
	}
	if sl.WaiterCount() != 1 {
		t.Errorf("expected 1 waiter, got %d", sl.WaiterCount())
	}

	// Release svc1 → svc3 should get slot
	sl.Release(svc1)

	select {
	case <-waitCh:
		// expected
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for slot")
	}

	if sl.ActiveCount() != 2 {
		t.Errorf("expected 2 active after wake, got %d", sl.ActiveCount())
	}
	if sl.WaiterCount() != 0 {
		t.Errorf("expected 0 waiters after wake, got %d", sl.WaiterCount())
	}
}

func TestStartLimiter_SlowThreshold(t *testing.T) {
	sl := NewStartLimiter(1, 50*time.Millisecond)
	set, _ := newTestSet()

	svc1 := NewInternalService(set, "slow")
	svc2 := NewInternalService(set, "fast")

	ok1, _ := sl.Acquire(svc1)
	if !ok1 {
		t.Fatal("first service should get slot")
	}

	// Before threshold, svc2 should wait
	ok2, _ := sl.Acquire(svc2)
	if ok2 {
		t.Fatal("second service should wait before threshold")
	}

	// Wait for svc1 to become "slow"
	time.Sleep(60 * time.Millisecond)

	// Now svc1 is slow, so active count should be 0
	if sl.ActiveCount() != 0 {
		t.Errorf("expected 0 active (svc1 is slow), got %d", sl.ActiveCount())
	}

	// svc2 should now get a slot
	ok2b, _ := sl.Acquire(svc2)
	if !ok2b {
		t.Fatal("second service should get slot after threshold")
	}
}

func TestStartLimiter_CancelWait(t *testing.T) {
	sl := NewStartLimiter(1, 10*time.Second)
	set, _ := newTestSet()

	svc1 := NewInternalService(set, "blocker")
	svc2 := NewInternalService(set, "cancelled")

	sl.Acquire(svc1)
	ok2, _ := sl.Acquire(svc2)
	if ok2 {
		t.Fatal("svc2 should be queued")
	}

	if sl.WaiterCount() != 1 {
		t.Errorf("expected 1 waiter, got %d", sl.WaiterCount())
	}

	sl.CancelWait(svc2)

	if sl.WaiterCount() != 0 {
		t.Errorf("expected 0 waiters after cancel, got %d", sl.WaiterCount())
	}
}

func TestStartLimiter_MultipleWaitersOrdering(t *testing.T) {
	sl := NewStartLimiter(1, 10*time.Second)
	set, _ := newTestSet()

	blocker := NewInternalService(set, "blocker")
	waiter1 := NewInternalService(set, "waiter1")
	waiter2 := NewInternalService(set, "waiter2")

	sl.Acquire(blocker)

	_, ch1 := sl.Acquire(waiter1)
	_, ch2 := sl.Acquire(waiter2)

	// Release blocker → waiter1 should wake first
	sl.Release(blocker)

	select {
	case <-ch1:
		// expected - FIFO
	case <-time.After(time.Second):
		t.Fatal("waiter1 should wake first")
	}

	// waiter2 should still be waiting
	select {
	case <-ch2:
		t.Fatal("waiter2 should still be waiting")
	default:
		// expected
	}

	// Release waiter1 → waiter2 wakes
	sl.Release(waiter1)

	select {
	case <-ch2:
		// expected
	case <-time.After(time.Second):
		t.Fatal("waiter2 should wake after waiter1 releases")
	}
}

func TestStartLimiter_TotalStarting(t *testing.T) {
	sl := NewStartLimiter(5, 10*time.Second)
	set, _ := newTestSet()

	svcs := make([]Service, 3)
	for i := 0; i < 3; i++ {
		svcs[i] = NewInternalService(set, "s"+string(rune('0'+i)))
		sl.Acquire(svcs[i])
	}

	if sl.TotalStarting() != 3 {
		t.Errorf("expected 3 total starting, got %d", sl.TotalStarting())
	}

	sl.Release(svcs[1])
	if sl.TotalStarting() != 2 {
		t.Errorf("expected 2 total starting, got %d", sl.TotalStarting())
	}
}
