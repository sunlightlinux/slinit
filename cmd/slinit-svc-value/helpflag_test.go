package main

import (
	"os"
	"testing"
)

// Help and version must work without a service context.
//
// They were unreachable: dispatch switched on `applet`, which is the
// program name, so the "-h"/"--help"/"version" cases could only fire if
// the binary were installed under the name "-h". Every real invocation
// fell through to newStore(), which needs RC_SVCNAME, so
// `slinit-svc-value --help` answered "service name required".
//
// Asserted through dispatch with RC_SVCNAME explicitly cleared, because
// a passing test that happens to have a service in its environment
// would prove nothing about the case that was broken.
func TestHelpAndVersionNeedNoServiceContext(t *testing.T) {
	for _, v := range []string{"RC_SVCNAME", "SLINIT_SERVICENAME"} {
		if old, ok := os.LookupEnv(v); ok {
			os.Unsetenv(v)
			t.Cleanup(func() { os.Setenv(v, old) })
		}
	}

	for _, arg := range []string{"-h", "--help", "help", "-V", "--version", "version"} {
		// "svc-value" is what main passes when the binary is run under
		// its own name, and it is not a known applet — so anything but
		// exitOK here means the argument never got looked at.
		// captureStdout, the helper this package already has, keeps
		// printUsage's direct stdout writes out of the suite output.
		_, got := captureStdout(t, func() int {
			return dispatch("svc-value", []string{arg})
		})
		if got != exitOK {
			t.Errorf("dispatch(%q) = %d, want %d", arg, got, exitOK)
		}
	}
}

// And a real applet still dispatches rather than being swallowed by the
// help check, which only looks at the first argument.
func TestAppletStillDispatches(t *testing.T) {
	for _, v := range []string{"RC_SVCNAME", "SLINIT_SERVICENAME"} {
		if old, ok := os.LookupEnv(v); ok {
			os.Unsetenv(v)
			t.Cleanup(func() { os.Setenv(v, old) })
		}
	}
	// No service context, so this must fail — the point is that it gets
	// past the help switch and tries, rather than printing usage.
	if got := dispatch("service_get_value", []string{"somekey"}); got == exitOK {
		t.Error("service_get_value with no RC_SVCNAME returned success")
	}
}
