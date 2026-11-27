// slinit-notify — send sd_notify messages from a shell, as systemd-notify(1).
//
// A service started with ready-notification = sd-notify (systemd's
// Type=notify) is STARTED when it sends READY=1 on $NOTIFY_SOCKET. A
// daemon linked against libsystemd does that itself; a shell script
// cannot, and systemd ships systemd-notify for it. On a system without
// systemd there is no systemd-notify, so this is its counterpart, taking
// the same options for the messages slinit acts on:
//
//	slinit-notify --ready
//	slinit-notify --status="loading cache"
//	slinit-notify WATCHDOG=1
//	slinit-notify --ready --status=up MAINPID=1234
//
// It exits 1 when $NOTIFY_SOCKET is not set (the service was not started
// with sd-notify readiness), as systemd-notify fails then. --booted
// reports whether the system runs systemd, which under slinit it does
// not: exit 1.
package main

import (
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
)

var version = "dev"

func main() {
	os.Exit(run(os.Args[1:], os.Getenv("NOTIFY_SOCKET"), os.Stdout, os.Stderr))
}

// run builds the message from args and sends it to socket. It returns
// the exit status.
func run(args []string, socket string, stdout, stderr io.Writer) int {
	var lines []string
	for _, a := range args {
		switch {
		case a == "--version":
			fmt.Fprintf(stdout, "slinit-notify %s\n", version)
			return 0
		case a == "-h" || a == "--help":
			usage(stdout)
			return 0
		case a == "--booted":
			// systemd-notify --booted: is this system running systemd?
			return 1
		case a == "--ready":
			lines = append(lines, "READY=1")
		case a == "--reloading":
			lines = append(lines, "RELOADING=1")
		case a == "--stopping":
			lines = append(lines, "STOPPING=1")
		case a == "--no-block":
			// Datagrams are sent without waiting for an answer anyway.
		case strings.HasPrefix(a, "--status="):
			lines = append(lines, "STATUS="+strings.TrimPrefix(a, "--status="))
		case a == "--pid":
			lines = append(lines, "MAINPID="+strconv.Itoa(os.Getppid()))
		case strings.HasPrefix(a, "--pid="):
			p := strings.TrimPrefix(a, "--pid=")
			switch p {
			case "parent":
				p = strconv.Itoa(os.Getppid())
			case "self":
				p = strconv.Itoa(os.Getpid())
			}
			if n, err := strconv.Atoi(p); err != nil || n <= 0 {
				fmt.Fprintf(stderr, "slinit-notify: invalid --pid %q\n", p)
				return 1
			}
			lines = append(lines, "MAINPID="+p)
		case strings.HasPrefix(a, "-"):
			fmt.Fprintf(stderr, "slinit-notify: unknown option %q\n", a)
			usage(stderr)
			return 1
		default:
			// VARIABLE=VALUE, sent as it is (WATCHDOG=1, ERRNO=5, ...).
			if k, _, ok := strings.Cut(a, "="); !ok || k == "" || strings.ContainsAny(a, "\n") {
				fmt.Fprintf(stderr, "slinit-notify: %q is not VARIABLE=VALUE\n", a)
				return 1
			}
			lines = append(lines, a)
		}
	}
	if len(lines) == 0 {
		fmt.Fprintln(stderr, "slinit-notify: nothing to send")
		usage(stderr)
		return 1
	}
	if err := send(socket, strings.Join(lines, "\n")); err != nil {
		fmt.Fprintf(stderr, "slinit-notify: %v\n", err)
		return 1
	}
	return 0
}

// send writes msg as one datagram to the notify socket: a path, or an
// abstract socket written "@name" as in systemd.
func send(socket, msg string) error {
	if socket == "" {
		return errors.New("$NOTIFY_SOCKET is not set: the service does not use sd-notify readiness")
	}
	name := socket
	if strings.HasPrefix(name, "@") {
		name = "\x00" + name[1:]
	} else if !strings.HasPrefix(name, "/") {
		return fmt.Errorf("$NOTIFY_SOCKET %q is neither a path nor an abstract socket", socket)
	}
	conn, err := net.DialUnix("unixgram", nil, &net.UnixAddr{Name: name, Net: "unixgram"})
	if err != nil {
		return err
	}
	defer conn.Close()
	_, err = conn.Write([]byte(msg))
	return err
}

func usage(w io.Writer) {
	fmt.Fprint(w, `Usage: slinit-notify [OPTIONS] [VARIABLE=VALUE...]

Send an sd_notify message to the service manager ($NOTIFY_SOCKET).

  --ready            the service is ready (READY=1)
  --reloading        the service is reloading (RELOADING=1)
  --stopping         the service is stopping (STOPPING=1)
  --status=TEXT      a status line (STATUS=TEXT)
  --pid[=PID]        the service's main pid (MAINPID=); parent by default
  --booted           exit 0 if the system runs systemd (never, under slinit)
  --no-block         accepted; the message is sent without waiting
  --version          print the version

Any VARIABLE=VALUE is sent as it is, e.g. WATCHDOG=1.
`)
}
