package service

import (
	"fmt"
	"strconv"

	"github.com/sunlightlinux/slinit/pkg/journal"
)

// emitJournalLogLine publishes one captured service-output line to the
// journal pipeline. Called from logrotate.go processLine after every
// filter (include/exclude/rate/level) has decided to keep the line,
// so the journal sees exactly what the logfile sees.
//
// Priority comes from the caller's already-extracted syslog level
// (0..7). MatchLine is the raw line without the trailing newline;
// journal Msg stores it as-is without sanitization since the
// consumer decides how to render binary payload.
//
// servicePID > 0 is stashed as SLINIT_TARGET_PID so short-format
// renderers can print the actual producer PID in the [N] bracket
// (defaulting to _PID = the emitter, i.e. slinit itself, was
// misleading — every event looked like it came from PID 1).
func emitJournalLogLine(serviceName string, lineLevel int, matchLine []byte, servicePID int) {
	evt := &journal.Event{
		Unit:      serviceName,
		Prio:      journal.Priority(lineLevel),
		Transport: journal.TransportStdout,
		Msg:       string(matchLine),
	}
	if servicePID > 0 {
		evt.Fields = map[string]string{
			"SLINIT_TARGET_PID": strconv.Itoa(servicePID),
		}
	}
	journal.Emit(evt)
}


// emitJournalStateEvent publishes a state-transition record to the
// journal pipeline. Called from notifyListeners so every legitimate
// state change surfaces in `slinit-journalctl -u <svc>` output even
// when no control connection is subscribed.
//
// Priority mapping:
//   - EventFailedStart → err (matches how boot-console shows [FAIL])
//   - EventStopped / EventStartCancelled / EventStopCancelled → notice
//     (state changes but not necessarily failures)
//   - EventPressureMemory / -CPU / -IO → warn (heads-up worth surfacing)
//   - EventStarted → info (the common quiet case)
//
// The event uses Transport=driver to mark it as slinit-internal, so
// query tools can distinguish "slinit said X happened" from
// "service's own stdout said Y" (Transport=stdout).
func (sr *ServiceRecord) emitJournalStateEvent(event ServiceEvent) {
	prio := journal.PriorityInfo
	switch event {
	case EventFailedStart:
		prio = journal.PriorityError
	case EventStopped, EventStartCancelled, EventStopCancelled:
		prio = journal.PriorityNotice
	case EventPressureMemory, EventPressureCPU, EventPressureIO:
		prio = journal.PriorityWarning
	}

	fields := map[string]string{
		"SLINIT_EVENT":         event.String(),
		"SLINIT_SERVICE_STATE": sr.state.Load().String(),
	}
	// Add SLINIT_TARGET_PID when the service actually has a running
	// process (Process/Scripted/BGProcess with pid > 0). Internal
	// services and services in STOPPED report -1/0 and we skip the
	// field so short renderers don't display a misleading [-1] or [0]
	// bracket. Look up via ServiceSet since ServiceRecord.PID() is a
	// base -1; overrides live on the concrete Service types.
	if sr.services != nil {
		if svc := sr.services.FindService(sr.serviceName, false); svc != nil {
			if pid := svc.PID(); pid > 0 {
				fields["SLINIT_TARGET_PID"] = strconv.Itoa(pid)
			}
		}
	}

	journal.Emit(&journal.Event{
		Unit:      sr.serviceName,
		Prio:      prio,
		Transport: journal.TransportDriver,
		Msg:       fmt.Sprintf("service %q: %s", sr.serviceName, event.String()),
		Fields:    fields,
	})
}
