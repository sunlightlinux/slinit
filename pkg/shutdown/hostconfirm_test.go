package shutdown

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
)

// fakeTTY implements the io.ReadCloser + io.Writer pair
// ConfirmHostname reads from a byte buffer and writes to an
// in-memory Buffer that the test can inspect.
type fakeTTY struct {
	in  *strings.Reader
	out *bytes.Buffer
}

func (f *fakeTTY) Read(p []byte) (int, error) { return f.in.Read(p) }
func (f *fakeTTY) Close() error               { return nil }

// TestConfirmHostnameHappyPath: operator types the correct short
// hostname → nil return, boot proceeds.
func TestConfirmHostnameHappyPath(t *testing.T) {
	ft := &fakeTTY{in: strings.NewReader("prodhost\n"), out: &bytes.Buffer{}}
	openTTY := func() (io.ReadCloser, io.Writer, error) {
		return ft, ft.out, nil
	}
	getHost := func() (string, error) { return "prodhost", nil }

	err := confirmHostname("reboot", openTTY, getHost)
	if err != nil {
		t.Fatalf("want nil, got %v", err)
	}
	if !strings.Contains(ft.out.String(), "reboot") {
		t.Errorf("prompt should mention action; got: %q", ft.out.String())
	}
}

// TestConfirmHostnameCaseInsensitive matches the case-insensitive
// compare — muscle memory doesn't always hit the right case.
func TestConfirmHostnameCaseInsensitive(t *testing.T) {
	ft := &fakeTTY{in: strings.NewReader("PRODHOST\n"), out: &bytes.Buffer{}}
	openTTY := func() (io.ReadCloser, io.Writer, error) {
		return ft, ft.out, nil
	}
	getHost := func() (string, error) { return "prodhost", nil }

	if err := confirmHostname("halt", openTTY, getHost); err != nil {
		t.Errorf("case-insensitive match should succeed, got %v", err)
	}
}

// TestConfirmHostnameMismatch: wrong hostname aborts with a
// mismatch error whose message identifies what was typed vs
// expected. Anti-footgun contract.
func TestConfirmHostnameMismatch(t *testing.T) {
	ft := &fakeTTY{in: strings.NewReader("stagehost\n"), out: &bytes.Buffer{}}
	openTTY := func() (io.ReadCloser, io.Writer, error) {
		return ft, ft.out, nil
	}
	getHost := func() (string, error) { return "prodhost", nil }

	err := confirmHostname("reboot", openTTY, getHost)
	if err == nil {
		t.Fatal("mismatch should abort, got nil")
	}
	if !strings.Contains(err.Error(), "mismatch") {
		t.Errorf("error should say 'mismatch', got %v", err)
	}
}

// TestConfirmHostnameNoTTY: batch context with no /dev/tty must
// fail-closed — the operator asked for confirmation but can't
// provide one, so we do NOT reboot.
func TestConfirmHostnameNoTTY(t *testing.T) {
	openTTY := func() (io.ReadCloser, io.Writer, error) {
		return nil, nil, errors.New("no controlling tty")
	}
	getHost := func() (string, error) { return "prodhost", nil }

	err := confirmHostname("reboot", openTTY, getHost)
	if err == nil {
		t.Fatal("no-tty should fail-closed, got nil")
	}
	if !strings.Contains(err.Error(), "no controlling tty") {
		t.Errorf("error should surface tty problem, got %v", err)
	}
}

// TestConfirmHostnameEmptyHostname: an empty nodename (weird kernel
// config) aborts before prompting — the confirmation loop would
// otherwise succeed on an empty answer, which is not what the
// operator asked for.
func TestConfirmHostnameEmptyHostname(t *testing.T) {
	openTTY := func() (io.ReadCloser, io.Writer, error) {
		t.Fatal("openTTY should NOT be called when hostname is empty")
		return nil, nil, nil
	}
	getHost := func() (string, error) { return "", nil }

	err := confirmHostname("reboot", openTTY, getHost)
	if err == nil {
		t.Fatal("empty hostname must abort, got nil")
	}
}

// TestConfirmHostnameShortLabel: FQDN in kernel nodename should
// still let operators match on the first label.
func TestConfirmHostnameShortLabel(t *testing.T) {
	ft := &fakeTTY{in: strings.NewReader("prod\n"), out: &bytes.Buffer{}}
	openTTY := func() (io.ReadCloser, io.Writer, error) {
		return ft, ft.out, nil
	}
	// Simulate what resolveShortHostname would return for a FQDN
	// nodename: only the first label.
	getHost := func() (string, error) { return "prod", nil }

	if err := confirmHostname("halt", openTTY, getHost); err != nil {
		t.Errorf("short-label match should succeed, got %v", err)
	}
}
