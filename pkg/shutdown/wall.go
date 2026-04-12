package shutdown

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/sunlightlinux/slinit/pkg/logging"
	"github.com/sunlightlinux/slinit/pkg/service"
	"github.com/sunlightlinux/slinit/pkg/utmp"
)

// wallEnabled controls whether Wall() actually broadcasts. Tests and
// callers that want to suppress wall messages set this to false.
var wallEnabled = true

// SetWallEnabled enables or disables shutdown wall broadcasts globally.
func SetWallEnabled(v bool) { wallEnabled = v }

// WallEnabled reports whether wall broadcasts are currently enabled.
func WallEnabled() bool { return wallEnabled }

// ttyListFunc returns the list of active user TTYs. Overridable for tests.
var ttyListFunc = utmp.ListUserTTYs

// ttyDir is the directory prepended to each ut_line when opening the TTY.
// Tests override this to point at a temp directory.
var ttyDir = "/dev"

// hostnameFunc is overridable for tests.
var hostnameFunc = os.Hostname

// WallShutdownNotice broadcasts a shutdown notice to all logged-in users.
// If delay is zero, the message reports an immediate shutdown; otherwise it
// announces the scheduled time. Errors writing to individual TTYs are logged
// at debug level — a broken TTY should never block shutdown.
func WallShutdownNotice(st service.ShutdownType, delay time.Duration, logger *logging.Logger) {
	if !wallEnabled {
		return
	}
	msg := formatShutdownMessage(st, delay)
	Wall(msg, logger)
}

// WallShutdownCancelled broadcasts that a previously scheduled shutdown
// has been cancelled.
func WallShutdownCancelled(st service.ShutdownType, logger *logging.Logger) {
	if !wallEnabled {
		return
	}
	msg := fmt.Sprintf(
		"The scheduled %s has been CANCELLED.",
		shutdownActionLabel(st),
	)
	Wall(msg, logger)
}

// Wall writes msg to every TTY listed in the utmp database.
// The message is wrapped in a standard "Broadcast message" header so it
// matches the traditional wall(1) format. Safe to call from any context:
// TTYs are opened O_NONBLOCK and errors are swallowed.
func Wall(msg string, logger *logging.Logger) {
	if !wallEnabled {
		return
	}

	ttys := ttyListFunc()
	if len(ttys) == 0 {
		if logger != nil {
			logger.Debug("Wall: no logged-in users")
		}
		return
	}

	host, _ := hostnameFunc()
	if host == "" {
		host = "localhost"
	}

	banner := fmt.Sprintf(
		"\r\nBroadcast message from slinit@%s (%s):\r\n\r\n",
		host,
		time.Now().Format("Mon Jan  2 15:04:05 2006"),
	)

	// Ensure the body ends with a newline and uses CRLF line endings for TTYs.
	body := strings.ReplaceAll(msg, "\n", "\r\n")
	if !strings.HasSuffix(body, "\r\n") {
		body += "\r\n"
	}
	full := banner + body + "\r\n"

	for _, line := range ttys {
		writeToTTY(line, full, logger)
	}
}

func writeToTTY(line, payload string, logger *logging.Logger) {
	// Defend against absolute paths or escape attempts from a tampered utmp.
	clean := strings.TrimLeft(filepath.Clean(line), "/")
	if clean == "" || clean == "." {
		return
	}
	path := filepath.Join(ttyDir, clean)

	// O_NONBLOCK so a stuck TTY (no carrier) cannot block shutdown.
	f, err := os.OpenFile(path, os.O_WRONLY|syscall.O_NONBLOCK|syscall.O_NOCTTY, 0)
	if err != nil {
		if logger != nil {
			logger.Debug("Wall: open %s: %v", path, err)
		}
		return
	}
	defer f.Close()

	// Best-effort write; ignore short writes and EAGAIN.
	if _, err := f.WriteString(payload); err != nil && logger != nil {
		logger.Debug("Wall: write %s: %v", path, err)
	}
}

func formatShutdownMessage(st service.ShutdownType, delay time.Duration) string {
	action := shutdownActionLabel(st)
	if delay <= 0 {
		return fmt.Sprintf(
			"The system is going down for %s NOW!\r\nPlease save your work and log out.",
			action,
		)
	}
	when := time.Now().Add(delay).Format("15:04:05")
	return fmt.Sprintf(
		"The system is going down for %s at %s (in %s).\r\nPlease save your work and log out.",
		action, when, humanDuration(delay),
	)
}

func shutdownActionLabel(st service.ShutdownType) string {
	switch st {
	case service.ShutdownReboot:
		return "reboot"
	case service.ShutdownHalt:
		return "halt"
	case service.ShutdownPoweroff:
		return "power-off"
	case service.ShutdownSoftReboot:
		return "soft-reboot"
	case service.ShutdownKexec:
		return "kexec reboot"
	default:
		return "maintenance"
	}
}

func humanDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	h := int(d / time.Hour)
	m := int((d % time.Hour) / time.Minute)
	if m == 0 {
		return fmt.Sprintf("%dh", h)
	}
	return fmt.Sprintf("%dh%dm", h, m)
}
