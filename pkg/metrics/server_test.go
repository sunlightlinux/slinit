package metrics

import (
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/sunlightlinux/slinit/pkg/service"
)

// The server speaks HTTP by hand, so the test speaks it through a real
// client: net/http is the thing a scraper uses, and it is strict about
// the parts that were written out longhand — status line, headers,
// Content-Length. Using it here costs nothing, since the test binary is
// not what ships.
func serveTest(t *testing.T, ss *service.ServiceSet) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go Serve(ln, ss, "2.4.0-test", nil)
	t.Cleanup(func() { ln.Close() })
	return "http://" + ln.Addr().String()
}

func TestServeAnswersScrapes(t *testing.T) {
	ss := testSet(t)
	ss.AddService(service.NewInternalService(ss, "boot"))
	base := serveTest(t, ss)

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(base + "/metrics")
	if err != nil {
		t.Fatalf("GET /metrics: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("Content-Type = %q, want text/plain...", ct)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if !strings.Contains(string(body), "slinit_build_info") {
		t.Errorf("body is not the metrics:\n%s", body)
	}
	// Content-Length has to be right, or the client either truncates
	// the body or waits for bytes that never come.
	if int64(len(body)) != resp.ContentLength {
		t.Errorf("read %d bytes, Content-Length said %d", len(body), resp.ContentLength)
	}
}

func TestServeRejectsTheRest(t *testing.T) {
	base := serveTest(t, testSet(t))
	client := &http.Client{Timeout: 5 * time.Second}

	resp, err := client.Get(base + "/wat")
	if err != nil {
		t.Fatalf("GET /wat: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("unknown path status = %d, want 404", resp.StatusCode)
	}

	resp, err = client.Post(base+"/metrics", "text/plain", strings.NewReader("x"))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("POST status = %d, want 405", resp.StatusCode)
	}
}

// A client that connects and then says nothing must not hold a
// goroutine, or anyone who can reach the port can exhaust the daemon by
// opening sockets.
func TestServeDoesNotWaitForeverOnASilentClient(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	go Serve(ln, testSet(t), "test", nil)

	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	// The handler gives a silent client requestTimeout; read until it gives up. If the
	// deadline were missing this would block until the test times out.
	_ = conn.SetReadDeadline(time.Now().Add(20 * time.Second))
	buf := make([]byte, 1)
	if _, err := conn.Read(buf); err == nil {
		t.Error("expected the server to close on a silent client, got data")
	} else if ne, ok := err.(net.Error); ok && ne.Timeout() {
		t.Error("the server kept the connection open past its own deadline")
	}
}

// A request line or header with no newline must not be read to its end:
// the endpoint may listen on a TCP port anyone on the network can reach,
// and every byte buffered here is PID 1's memory. The server has to give
// up after maxRequestLine, not after the client stops sending.
func TestServeBoundsAnEndlessLine(t *testing.T) {
	for _, tc := range []struct {
		name   string
		prefix string
	}{
		{"request line", ""},
		{"header", "GET /metrics HTTP/1.1\r\nX-Junk: "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ln, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatalf("listen: %v", err)
			}
			defer ln.Close()
			go Serve(ln, testSet(t), "test", nil)

			conn, err := net.Dial("tcp", ln.Addr().String())
			if err != nil {
				t.Fatalf("dial: %v", err)
			}
			defer conn.Close()

			const total = 256 << 20
			sent := make(chan int, 1)
			go func() {
				n, _ := io.WriteString(conn, tc.prefix)
				chunk := []byte(strings.Repeat("A", 64<<10))
				_ = conn.SetWriteDeadline(time.Now().Add(15 * time.Second))
				for n < total {
					m, err := conn.Write(chunk)
					n += m
					if err != nil {
						break
					}
				}
				sent <- n
			}()

			select {
			case n := <-sent:
				// Socket buffers on loopback hold a few MiB at most; a
				// server that stops reading leaves the writer far short
				// of the total.
				if n >= total {
					t.Fatalf("server read all %d MiB of one line", n>>20)
				}
			case <-time.After(20 * time.Second):
				t.Fatal("writer neither finished nor failed")
			}
		})
	}
}

// Connections past maxConns are closed at once instead of each holding
// one of PID 1's descriptors until the deadline.
func TestServeCapsConcurrentConnections(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	go Serve(ln, testSet(t), "test", nil)

	var held []net.Conn
	defer func() {
		for _, c := range held {
			c.Close()
		}
	}()
	for i := 0; i < maxConns; i++ {
		c, err := net.Dial("tcp", ln.Addr().String())
		if err != nil {
			t.Fatalf("dial %d: %v", i, err)
		}
		held = append(held, c)
	}
	// Let the accept loop take every one of them.
	time.Sleep(200 * time.Millisecond)

	extra, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("dial extra: %v", err)
	}
	defer extra.Close()
	_ = extra.SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, err := extra.Read(make([]byte, 1)); err != io.EOF {
		t.Fatalf("connection past the cap: read err = %v, want EOF (closed on accept)", err)
	}
}

// maxConns connections that never send a request held every slot for
// the whole 10s deadline, so a scrape in that time was closed on accept.
// The request now has requestTimeout to arrive; once that has passed a
// scrape gets through again.
func TestServeIdleConnectionsFreeTheirSlots(t *testing.T) {
	ss := testSet(t)
	base := serveTest(t, ss)
	addr := strings.TrimPrefix(base, "http://")

	for i := 0; i < maxConns; i++ {
		c, err := net.Dial("tcp", addr)
		if err != nil {
			t.Fatalf("dial %d: %v", i, err)
		}
		defer c.Close()
	}
	time.Sleep(requestTimeout + time.Second)

	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get(base + "/metrics")
	if err != nil {
		t.Fatalf("scrape after idle connections timed out: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
}
