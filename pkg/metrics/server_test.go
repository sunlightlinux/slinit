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

	// The handler's own deadline is 10s; read until it gives up. If the
	// deadline were missing this would block until the test times out.
	_ = conn.SetReadDeadline(time.Now().Add(20 * time.Second))
	buf := make([]byte, 1)
	if _, err := conn.Read(buf); err == nil {
		t.Error("expected the server to close on a silent client, got data")
	} else if ne, ok := err.(net.Error); ok && ne.Timeout() {
		t.Error("the server kept the connection open past its own deadline")
	}
}
