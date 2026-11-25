package control

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/sunlightlinux/slinit/pkg/service"
)

// A list reply used to be one write per service. It now goes out in
// chunks of at most writeChunk bytes; the bytes on the wire must be
// exactly what one WritePacket per service and one for RplyListDone
// produced, also when the reply spans several chunks.
func TestListRepliesAreBatchedWithUnchangedBytes(t *testing.T) {
	server, sockPath := setupTestServer(t)
	defer server.Stop()
	ss := server.services
	long := strings.Repeat("n", 60)
	for i := 0; i < 2000; i++ {
		ss.AddService(service.NewInternalService(ss, fmt.Sprintf("%s-%04d", long, i)))
	}

	for _, tc := range []struct {
		cmd    uint8
		encode func(service.Service) []byte
	}{
		{CmdListServices, EncodeSvcInfo},
		{CmdListServices5, EncodeSvcInfo5},
	} {
		var want bytes.Buffer
		for _, svc := range ss.ListServices() {
			if err := WritePacket(&want, RplySvcInfo, tc.encode(svc)); err != nil {
				t.Fatal(err)
			}
		}
		if err := WritePacket(&want, RplyListDone, nil); err != nil {
			t.Fatal(err)
		}
		if want.Len() <= 2*writeChunk {
			t.Fatalf("reply is %d bytes; the test needs more than two chunks", want.Len())
		}

		conn := connectTest(t, sockPath)
		if err := WritePacket(conn, tc.cmd, nil); err != nil {
			t.Fatal(err)
		}
		got := make([]byte, want.Len())
		conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		if _, err := io.ReadFull(conn, got); err != nil {
			t.Fatalf("cmd %d: reading the reply: %v", tc.cmd, err)
		}
		// ListServices walks a map, so compare packets as a set; the
		// lengths already match, and RplyListDone must come last.
		gotPkts, wantPkts := splitPackets(t, got), splitPackets(t, want.Bytes())
		if last := gotPkts[len(gotPkts)-1]; last != string([]byte{RplyListDone, 0, 0}) {
			t.Errorf("cmd %d: reply does not end with RplyListDone", tc.cmd)
		}
		sort.Strings(gotPkts)
		sort.Strings(wantPkts)
		if !slices.Equal(gotPkts, wantPkts) {
			t.Errorf("cmd %d: reply packets differ from per-packet framing", tc.cmd)
		}
		// Nothing after RplyListDone, and the connection still serves.
		if err := WritePacket(conn, CmdQueryVersion, nil); err != nil {
			t.Fatal(err)
		}
		if rply, _, err := ReadPacket(conn); err != nil || rply != RplyCPVersion {
			t.Errorf("cmd %d: next reply %d, %v; want RplyCPVersion", tc.cmd, rply, err)
		}
		conn.Close()
	}
}

// writePackets keeps writePacket's failure rule: a failed write closes
// the connection, and later writes return errConnClosed at once.
func TestWritePacketsFailureClosesConnection(t *testing.T) {
	saved := writeDeadline
	writeDeadline = 50 * time.Millisecond
	defer func() { writeDeadline = saved }()

	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()
	c := &Connection{
		conn:       serverConn,
		handles:    make(map[uint32]service.Service),
		revHandles: make(map[service.Service]uint32),
		nextHandle: 1,
	}

	// The client reads the first chunk, then stops: the second
	// chunk's write times out.
	buf := bytes.Repeat([]byte{0}, writeChunk+10)
	go io.ReadFull(clientConn, make([]byte, writeChunk))
	err := c.writePackets(buf)
	if ne, ok := err.(net.Error); !ok || !ne.Timeout() {
		t.Fatalf("writePackets with a stalled reader: %v, want a timeout", err)
	}
	if !c.closed {
		t.Error("a failed writePackets did not mark the connection closed")
	}
	if _, err := serverConn.Write([]byte{0}); err == nil {
		t.Error("a failed writePackets left the socket open")
	}
	if err := c.writePackets([]byte{1}); err != errConnClosed {
		t.Errorf("writePackets after a failure: %v, want errConnClosed", err)
	}
}

// splitPackets splits a reply stream into its framed packets.
func splitPackets(t *testing.T, b []byte) []string {
	t.Helper()
	var pkts []string
	r := bytes.NewReader(b)
	for r.Len() > 0 {
		typ, payload, err := ReadPacket(r)
		if err != nil {
			t.Fatalf("reply does not frame: %v", err)
		}
		var one bytes.Buffer
		WritePacket(&one, typ, payload)
		pkts = append(pkts, one.String())
	}
	return pkts
}
