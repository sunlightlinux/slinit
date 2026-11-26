// Smoke tests for the OCF resource agent shell script.
//
// The script is pure /bin/sh and shells out to slinitctl, so the test
// builds a tiny stub slinitctl that records its arguments and emits a
// configurable State line, then drives the agent with each OCF action.
// No real slinit daemon, no QEMU.
package agent_test

import (
	"encoding/xml"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// agentPath returns the path to the slinit-resource script under test.
func agentPath(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	p := filepath.Join(wd, "slinit-resource")
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("agent script not found at %s: %v", p, err)
	}
	return p
}

// installStubSlinitctl writes a /bin/sh stub at <dir>/slinitctl that:
//   - on `status <svc>`, prints "  State:   <state>" with the state read
//     from the file <dir>/state.
//   - on `stop <svc>`, transitions <dir>/state to STOPPED so a follow-up
//     monitor probe agrees with reality.
//   - on `start <svc>`, transitions <dir>/state to STARTED.
//   - on any other command, exits 0 silently.
//   - if <dir>/sticky exists, start and stop leave the state untouched
//     (a service stuck mid-transition).
//
// The initial state in <dir>/state is `defaultState`. The stub also
// appends its argv to <dir>/calls so tests can assert on it.
func installStubSlinitctl(t *testing.T, dir, defaultState string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "state"), []byte(defaultState), 0o644); err != nil {
		t.Fatalf("write state: %v", err)
	}
	stub := fmt.Sprintf(`#!/bin/sh
echo "$@" >> %q
# Skip --socket-path PATH and any leading flags so we can match the verb.
while [ $# -gt 0 ]; do
    case "$1" in
        --socket-path) shift 2 ;;
        --*) shift ;;
        *) break ;;
    esac
done
case "$1" in
    status)
        _state=$(cat %q 2>/dev/null || echo STOPPED)
        echo "Service: $2"
        echo "  State:   $_state"
        echo "  Target:  STARTED"
        echo "  Type:    process"
        ;;
    start) [ -e %q ] || echo STARTED > %q ;;
    stop)  [ -e %q ] || echo STOPPED > %q ;;
    *) ;;
esac
exit 0
`,
		filepath.Join(dir, "calls"),
		filepath.Join(dir, "state"),
		filepath.Join(dir, "sticky"),
		filepath.Join(dir, "state"),
		filepath.Join(dir, "sticky"),
		filepath.Join(dir, "state"),
	)
	path := filepath.Join(dir, "slinitctl")
	if err := os.WriteFile(path, []byte(stub), 0o755); err != nil {
		t.Fatalf("write stub: %v", err)
	}
}

// setState writes the State the stub will report on the next `status` call.
func setState(t *testing.T, dir, state string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "state"), []byte(state), 0o644); err != nil {
		t.Fatalf("write state: %v", err)
	}
}

// runAgent invokes the agent with the given OCF action, using the stub
// slinitctl in <stubDir>. Returns stdout, stderr, exit code.
func runAgent(t *testing.T, agent, stubDir, action, service string, extraEnv ...string) (string, string, int) {
	t.Helper()
	cmd := exec.Command(agent, action)
	cmd.Env = append(os.Environ(),
		"PATH="+stubDir+string(os.PathListSeparator)+os.Getenv("PATH"),
		"OCF_RESKEY_service="+service,
		"OCF_RESKEY_socket=/tmp/slinit-resource-test.socket",
		"OCF_RESKEY_slinitctl=slinitctl",
	)
	cmd.Env = append(cmd.Env, extraEnv...)
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	rc := 0
	if exitErr, ok := err.(*exec.ExitError); ok {
		rc = exitErr.ExitCode()
	} else if err != nil {
		t.Fatalf("agent %s: %v", action, err)
	}
	return stdout.String(), stderr.String(), rc
}

func TestMetaDataIsWellFormedXML(t *testing.T) {
	agent := agentPath(t)
	cmd := exec.Command(agent, "meta-data")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("meta-data: %v", err)
	}

	dec := xml.NewDecoder(strings.NewReader(string(out)))
	for {
		_, err := dec.Token()
		if err != nil {
			if err.Error() == "EOF" {
				break
			}
			t.Fatalf("XML parse: %v", err)
		}
	}

	for _, want := range []string{
		`<resource-agent name="slinit-resource"`,
		`<parameter name="service" required="1"`,
		`<parameter name="socket"`,
		`<action name="start"`,
		`<action name="stop"`,
		`<action name="monitor"`,
		`<action name="meta-data"`,
		`<action name="validate-all"`,
	} {
		if !strings.Contains(string(out), want) {
			t.Errorf("meta-data missing %q", want)
		}
	}
}

func TestMissingServiceParamFailsConfigured(t *testing.T) {
	agent := agentPath(t)
	dir := t.TempDir()
	installStubSlinitctl(t, dir, "STARTED")
	// Empty OCF_RESKEY_service: agent must exit OCF_ERR_CONFIGURED=6.
	cmd := exec.Command(agent, "monitor")
	cmd.Env = append(os.Environ(),
		"PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"),
		"OCF_RESKEY_service=",
	)
	err := cmd.Run()
	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("expected ExitError, got %v", err)
	}
	if exitErr.ExitCode() != 6 {
		t.Errorf("missing service: rc=%d, want 6 (OCF_ERR_CONFIGURED)", exitErr.ExitCode())
	}
}

func TestMonitorReportsRunning(t *testing.T) {
	agent := agentPath(t)
	dir := t.TempDir()
	installStubSlinitctl(t, dir, "STARTED")

	_, _, rc := runAgent(t, agent, dir, "monitor", "nginx")
	if rc != 0 {
		t.Errorf("monitor on STARTED: rc=%d, want 0 (OCF_SUCCESS)", rc)
	}
}

func TestMonitorReportsNotRunning(t *testing.T) {
	agent := agentPath(t)
	dir := t.TempDir()
	installStubSlinitctl(t, dir, "STARTED")
	setState(t, dir, "STOPPED")

	_, _, rc := runAgent(t, agent, dir, "monitor", "nginx")
	if rc != 7 {
		t.Errorf("monitor on STOPPED: rc=%d, want 7 (OCF_NOT_RUNNING)", rc)
	}
}

// OCF_NOT_RUNNING promises a clean stop; a mid-transition service is
// not one. STARTING is reported active, STOPPING as an error so
// Pacemaker recovers it with a stop.
func TestMonitorTransitionalIsNotCleanlyStopped(t *testing.T) {
	agent := agentPath(t)
	for _, tc := range []struct {
		state string
		want  int
	}{
		{"STARTING", 0},
		{"STOPPING", 1},
	} {
		dir := t.TempDir()
		installStubSlinitctl(t, dir, tc.state)
		_, _, rc := runAgent(t, agent, dir, "monitor", "nginx")
		if rc != tc.want {
			t.Errorf("monitor during %s: rc=%d, want %d", tc.state, rc, tc.want)
		}
	}
}

// The split-brain bug: stop of a STARTING (or STOPPING) service
// returned OCF_SUCCESS without ever calling `slinitctl stop`, so the
// service kept coming up here while Pacemaker started it elsewhere.
func TestStopOfTransitionalServiceStopsIt(t *testing.T) {
	agent := agentPath(t)
	for _, state := range []string{"STARTING", "STOPPING"} {
		dir := t.TempDir()
		installStubSlinitctl(t, dir, state)
		_, stderr, rc := runAgent(t, agent, dir, "stop", "nginx")
		if rc != 0 {
			t.Errorf("stop on %s: rc=%d, want 0; stderr:\n%s", state, rc, stderr)
		}
		calls, _ := os.ReadFile(filepath.Join(dir, "calls"))
		if !strings.Contains(string(calls), "stop --ignore-unstarted nginx") {
			t.Errorf("stop on %s did not invoke 'slinitctl stop'; calls:\n%s", state, calls)
		}
		got, _ := os.ReadFile(filepath.Join(dir, "state"))
		if strings.TrimSpace(string(got)) != "STOPPED" {
			t.Errorf("stop on %s: state after = %q, want STOPPED", state, got)
		}
	}
}

// A stop that does not reach STOPPED within stop_timeout fails.
func TestStopTimesOutWhenServiceDoesNotStop(t *testing.T) {
	agent := agentPath(t)
	dir := t.TempDir()
	installStubSlinitctl(t, dir, "STARTED")
	if err := os.WriteFile(filepath.Join(dir, "sticky"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	_, _, rc := runAgent(t, agent, dir, "stop", "nginx", "OCF_RESKEY_stop_timeout=1")
	if rc != 1 {
		t.Errorf("stop stuck on STARTED: rc=%d, want 1 (OCF_ERR_GENERIC)", rc)
	}
}

// start of a STARTING service must not trust monitor's "active": it
// waits for the in-flight start via slinitctl and checks for STARTED.
func TestStartOfStartingServiceWaitsForStarted(t *testing.T) {
	agent := agentPath(t)
	dir := t.TempDir()
	installStubSlinitctl(t, dir, "STARTING")
	_, _, rc := runAgent(t, agent, dir, "start", "nginx")
	if rc != 0 {
		t.Errorf("start on STARTING: rc=%d, want 0", rc)
	}
	calls, _ := os.ReadFile(filepath.Join(dir, "calls"))
	if !strings.Contains(string(calls), "start nginx") {
		t.Errorf("start on STARTING did not invoke 'slinitctl start'; calls:\n%s", calls)
	}

	// Stuck in STARTING past start_timeout: the start failed.
	dir = t.TempDir()
	installStubSlinitctl(t, dir, "STARTING")
	if err := os.WriteFile(filepath.Join(dir, "sticky"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	_, _, rc = runAgent(t, agent, dir, "start", "nginx", "OCF_RESKEY_start_timeout=1")
	if rc != 1 {
		t.Errorf("start stuck in STARTING: rc=%d, want 1 (OCF_ERR_GENERIC)", rc)
	}
}

func TestStartIsIdempotent(t *testing.T) {
	agent := agentPath(t)
	dir := t.TempDir()
	installStubSlinitctl(t, dir, "STARTED")

	// Already STARTED → no slinitctl start should be issued.
	_, _, rc := runAgent(t, agent, dir, "start", "nginx")
	if rc != 0 {
		t.Errorf("start on STARTED: rc=%d, want 0", rc)
	}
	calls, _ := os.ReadFile(filepath.Join(dir, "calls"))
	if strings.Contains(string(calls), "start nginx") {
		t.Errorf("idempotent start should not invoke 'slinitctl start'; calls:\n%s", calls)
	}
}

func TestStopIsIdempotent(t *testing.T) {
	agent := agentPath(t)
	dir := t.TempDir()
	installStubSlinitctl(t, dir, "STOPPED")

	_, _, rc := runAgent(t, agent, dir, "stop", "nginx")
	if rc != 0 {
		t.Errorf("stop on STOPPED: rc=%d, want 0", rc)
	}
	calls, _ := os.ReadFile(filepath.Join(dir, "calls"))
	if strings.Contains(string(calls), "stop nginx") {
		t.Errorf("idempotent stop should not invoke 'slinitctl stop'; calls:\n%s", calls)
	}
}

func TestStatusIsAliasOfMonitor(t *testing.T) {
	agent := agentPath(t)
	dir := t.TempDir()
	installStubSlinitctl(t, dir, "STARTED")

	_, _, rc := runAgent(t, agent, dir, "status", "nginx")
	if rc != 0 {
		t.Errorf("status on STARTED: rc=%d, want 0", rc)
	}
	setState(t, dir, "STOPPED")
	_, _, rc = runAgent(t, agent, dir, "status", "nginx")
	if rc != 7 {
		t.Errorf("status on STOPPED: rc=%d, want 7", rc)
	}
}

func TestUnknownActionIsUnimplemented(t *testing.T) {
	agent := agentPath(t)
	cmd := exec.Command(agent, "promote")
	err := cmd.Run()
	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("expected ExitError, got %v", err)
	}
	if exitErr.ExitCode() != 3 {
		t.Errorf("unknown action: rc=%d, want 3 (OCF_ERR_UNIMPLEMENTED)", exitErr.ExitCode())
	}
}

func TestNoArgsIsUsageError(t *testing.T) {
	agent := agentPath(t)
	cmd := exec.Command(agent)
	err := cmd.Run()
	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("expected ExitError, got %v", err)
	}
	if exitErr.ExitCode() != 2 {
		t.Errorf("no args: rc=%d, want 2 (OCF_ERR_ARGS)", exitErr.ExitCode())
	}
}

func TestValidateAllSucceedsWhenServiceKnown(t *testing.T) {
	agent := agentPath(t)
	dir := t.TempDir()
	installStubSlinitctl(t, dir, "STARTED")

	_, _, rc := runAgent(t, agent, dir, "validate-all", "nginx")
	if rc != 0 {
		t.Errorf("validate-all (service known): rc=%d, want 0", rc)
	}
}
