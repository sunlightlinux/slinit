package utmp

import "testing"

func TestMaxLengths(t *testing.T) {
	// On Linux x86_64, ut_id is 4 bytes and ut_line is 32 bytes.
	// Just verify they are positive and reasonable.
	if MaxIDLen <= 0 || MaxIDLen > 256 {
		t.Errorf("MaxIDLen = %d, expected positive and <= 256", MaxIDLen)
	}
	if MaxLineLen <= 0 || MaxLineLen > 256 {
		t.Errorf("MaxLineLen = %d, expected positive and <= 256", MaxLineLen)
	}
}

func TestCreateAndClearEntry(t *testing.T) {
	// This test verifies the functions don't panic.
	// Actual utmp writes require root and /var/run/utmp access,
	// so we just confirm the cgo interface works without crashing.
	CreateEntry("tst", "pts/99", 99999)
	ClearEntry("tst", "pts/99")
}
