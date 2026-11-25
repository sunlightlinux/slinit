package control

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/sunlightlinux/slinit/pkg/service"
)

// A reply larger than MaxPayloadSize cannot be sent: WritePacket fails,
// serve closes the connection and the client sees EOF with no reply.
// These cases used to build such replies.

func catlog(t *testing.T, sockPath, name string, clear bool) (uint8, []byte) {
	t.Helper()
	conn := connectTest(t, sockPath)
	defer conn.Close()
	if err := WritePacket(conn, CmdLoadService, EncodeServiceName(name)); err != nil {
		t.Fatal(err)
	}
	_, p, err := ReadPacket(conn)
	if err != nil || len(p) < 5 {
		t.Fatalf("load %s: %v", name, err)
	}
	h := binary.LittleEndian.Uint32(p[1:5])
	if err := WritePacket(conn, CmdCatLog, EncodeCatLogRequest(h, clear)); err != nil {
		t.Fatal(err)
	}
	rply, payload, err := ReadPacket(conn)
	if err != nil {
		t.Fatalf("catlog %s: no reply: %v", name, err)
	}
	return rply, payload
}

// The tail of a log file whose last 64 KiB holds no newline (a progress
// bar redrawn with \r) was 65536 bytes plus a 5-byte header.
func TestCatlogFileTailFitsInAPacket(t *testing.T) {
	server, sockPath := setupTestServer(t)
	defer server.Stop()
	logPath := filepath.Join(t.TempDir(), "app.log")
	content := bytes.Repeat([]byte("progress 42%\r"), 8000)
	if err := os.WriteFile(logPath, content, 0o644); err != nil {
		t.Fatal(err)
	}
	svc := service.NewProcessService(server.services, "f")
	svc.SetLogType(service.LogToFile)
	svc.SetLogFileDetails(logPath, 0o644, -1, -1)
	server.services.AddService(svc)

	rply, p := catlog(t, sockPath, "f", false)
	if rply != RplySvcLog {
		t.Fatalf("rply = %d, want RplySvcLog", rply)
	}
	_, data, err := DecodeSvcLog(p)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasSuffix(content, data) || len(data) < 60000 {
		t.Errorf("got %d bytes, want the file's last ~64 KiB", len(data))
	}
}

// log-buffer-size has no upper bound. With more buffered than fits,
// catlog --clear emptied the buffer and then failed to send it: the
// log was gone and the client got nothing.
func TestCatlogClearOfALargeBufferSendsItsTail(t *testing.T) {
	server, sockPath := setupTestServer(t)
	defer server.Stop()
	svc := service.NewProcessService(server.services, "b")
	svc.SetLogType(service.LogToBuffer)
	svc.SetLogBufMax(1 << 20)
	server.services.AddService(svc)
	lb := service.NewLogBuffer(1 << 20)
	var buf bytes.Buffer
	for i := 0; i < 20000; i++ {
		buf.WriteString("line\n")
	}
	buf.WriteString("LAST LINE\n")
	lb.WriteTestData(buf.Bytes())
	svc.SetTestLogBuffer(lb)

	rply, p := catlog(t, sockPath, "b", true)
	if rply != RplySvcLog {
		t.Fatalf("rply = %d, want RplySvcLog", rply)
	}
	_, data, _ := DecodeSvcLog(p)
	if !bytes.HasSuffix(data, []byte("LAST LINE\n")) {
		t.Errorf("reply does not end with the newest output (%d bytes)", len(data))
	}
}

func TestRunActionLargeOutputFitsInAPacket(t *testing.T) {
	server, sockPath := setupTestServer(t)
	defer server.Stop()
	svc := service.NewProcessService(server.services, "a")
	svc.Record().SetExtraCommands(map[string][]string{
		"dump": {"/bin/sh", "-c", "head -c 70000 /dev/zero | tr '\\0' x; echo END"},
	})
	server.services.AddService(svc)
	conn := connectTest(t, sockPath)
	defer conn.Close()
	if err := WritePacket(conn, CmdLoadService, EncodeServiceName("a")); err != nil {
		t.Fatal(err)
	}
	_, p, err := ReadPacket(conn)
	if err != nil || len(p) < 5 {
		t.Fatalf("load: %v", err)
	}
	h := binary.LittleEndian.Uint32(p[1:5])
	req := append(EncodeHandle(h), EncodeServiceName("dump")...)
	if err := WritePacket(conn, CmdRunAction, req); err != nil {
		t.Fatal(err)
	}
	rply, out, err := ReadPacket(conn)
	if err != nil {
		t.Fatalf("run-action: no reply: %v", err)
	}
	if rply != RplyActionOutput || !bytes.HasSuffix(out, []byte("END\n")) {
		t.Errorf("rply = %d, %d bytes; want the output's tail", rply, len(out))
	}
}

// run-action buffered the whole output before keeping its tail, so an
// action that printed without end grew PID 1's memory without end. Only
// the tail is kept now: 64 MiB of output must not cost 64 MiB.
func TestRunActionKeepsOnlyTheTail(t *testing.T) {
	server, sockPath := setupTestServer(t)
	defer server.Stop()
	svc := service.NewProcessService(server.services, "a")
	svc.Record().SetExtraCommands(map[string][]string{
		"flood": {"/bin/sh", "-c", "head -c 67108864 /dev/zero; echo END"},
	})
	server.services.AddService(svc)
	conn := connectTest(t, sockPath)
	defer conn.Close()
	if err := WritePacket(conn, CmdLoadService, EncodeServiceName("a")); err != nil {
		t.Fatal(err)
	}
	_, p, err := ReadPacket(conn)
	if err != nil || len(p) < 5 {
		t.Fatalf("load: %v", err)
	}
	h := binary.LittleEndian.Uint32(p[1:5])

	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	if err := WritePacket(conn, CmdRunAction, append(EncodeHandle(h), EncodeServiceName("flood")...)); err != nil {
		t.Fatal(err)
	}
	conn.SetReadDeadline(time.Now().Add(30 * time.Second))
	rply, out, err := ReadPacket(conn)
	runtime.ReadMemStats(&after)
	if err != nil {
		t.Fatalf("run-action: no reply: %v", err)
	}
	if rply != RplyActionOutput || !bytes.HasSuffix(out, []byte("END\n")) {
		t.Errorf("rply = %d, %d bytes; want the output's tail", rply, len(out))
	}
	if grew := after.TotalAlloc - before.TotalAlloc; grew > 16<<20 {
		t.Errorf("server allocated %d MiB for 64 MiB of action output", grew>>20)
	}
}

func TestTailBufferKeepsLastBytes(t *testing.T) {
	tb := &tailBuffer{max: 4}
	for _, s := range []string{"ab", "cdefg", "h", "", "ijklmnop"} {
		tb.Write([]byte(s))
	}
	if got := string(lastBytes(tb.buf, 4)); got != "mnop" {
		t.Errorf("tail = %q, want mnop", got)
	}
	if len(tb.buf) > 8 {
		t.Errorf("buffer holds %d bytes, max 2*4", len(tb.buf))
	}
}
