package metrics

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"strings"
	"time"

	"github.com/sunlightlinux/slinit/pkg/service"
)

// Serve answers scrapes on ln until it is closed.
//
// The HTTP is written out by hand rather than with net/http, because
// this runs inside PID 1: net/http is not currently linked into slinit
// and pulling it in for one endpoint would add megabytes to the one
// process the machine cannot restart. What a scraper needs of HTTP/1.1
// is a request line, a status line, a Content-Type and a body, so that
// is what this speaks — and nothing else, which also means there is no
// routing, no TLS and no second endpoint to get wrong.
//
// Every connection is answered once and closed: no keep-alive, no
// concurrency beyond a goroutine per connection, and a deadline so a
// client that opens a socket and says nothing cannot hold one open.
func Serve(ln net.Listener, ss *service.ServiceSet, version string, logf func(string, ...any)) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			// Closed listener is the ordinary way out.
			return
		}
		go func() {
			defer conn.Close()
			if err := handle(conn, ss, version); err != nil && logf != nil {
				logf("metrics: %v", err)
			}
		}()
	}
}

// maxRequestLine bounds what a client can make us read before it has
// said anything useful. A scrape's request line is well under 100 bytes.
const maxRequestLine = 8 << 10

func handle(conn net.Conn, ss *service.ServiceSet, version string) error {
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))

	r := bufio.NewReaderSize(conn, maxRequestLine)
	line, err := r.ReadString('\n')
	if err != nil {
		return fmt.Errorf("read request: %w", err)
	}
	if len(line) >= maxRequestLine {
		return respond(conn, "431 Request Header Fields Too Large", "text/plain", "request line too long\n")
	}

	// "GET /metrics HTTP/1.1"
	fields := strings.Fields(line)
	if len(fields) < 2 {
		return respond(conn, "400 Bad Request", "text/plain", "malformed request\n")
	}
	method, target := fields[0], fields[1]
	if i := strings.IndexByte(target, '?'); i >= 0 {
		target = target[:i]
	}

	// Headers are read and dropped: nothing here varies by them, but
	// they have to leave the socket or the client sees a reset before
	// it reads the response.
	for {
		h, err := r.ReadString('\n')
		if err != nil || strings.TrimRight(h, "\r\n") == "" {
			break
		}
	}

	if method != "GET" && method != "HEAD" {
		return respond(conn, "405 Method Not Allowed", "text/plain", "only GET\n")
	}

	switch target {
	case "/metrics":
		var body strings.Builder
		if err := Render(&body, ss, version); err != nil {
			return respond(conn, "500 Internal Server Error", "text/plain", "render failed\n")
		}
		// The version suffix is what Prometheus itself advertises for
		// the text format; scrapers accept it and humans reading with
		// curl get plain text rather than a download prompt.
		return respond(conn, "200 OK", "text/plain; version=0.0.4; charset=utf-8", body.String())
	case "/":
		return respond(conn, "200 OK", "text/html; charset=utf-8",
			"<html><body><a href=\"/metrics\">metrics</a></body></html>\n")
	default:
		return respond(conn, "404 Not Found", "text/plain", "try /metrics\n")
	}
}

func respond(w io.Writer, status, contentType, body string) error {
	_, err := fmt.Fprintf(w,
		"HTTP/1.1 %s\r\nContent-Type: %s\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s",
		status, contentType, len(body), body)
	return err
}
