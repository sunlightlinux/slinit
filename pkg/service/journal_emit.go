package service

import (
	"fmt"

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
func emitJournalLogLine(serviceName string, lineLevel int, matchLine []byte) {
	journal.Emit(&journal.Event{
		Unit:      serviceName,
		Prio:      journal.Priority(lineLevel),
		Transport: journal.TransportStdout,
		Msg:       string(matchLine),
	})
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

	journal.Emit(&journal.Event{
		Unit:      sr.serviceName,
		Prio:      prio,
		Transport: journal.TransportDriver,
		Msg:       fmt.Sprintf("service %q: %s", sr.serviceName, event.String()),
		Fields: map[string]string{
			"SLINIT_EVENT":         event.String(),
			"SLINIT_SERVICE_STATE": sr.state.Load().String(),
		},
	})
}
