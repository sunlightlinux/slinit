package control

import (
	"bytes"
	"encoding/binary"
	"testing"

	"github.com/sunlightlinux/slinit/pkg/service"
)

func TestCatLogEncodeDecode(t *testing.T) {
	// Test request encode/decode
	req := EncodeCatLogRequest(42, true)
	flags, handle, err := DecodeCatLogRequest(req)
	if err != nil {
		t.Fatalf("DecodeCatLogRequest: %v", err)
	}
	if handle != 42 {
		t.Errorf("handle = %d, want 42", handle)
	}
	if flags&CatLogFlagClear == 0 {
		t.Error("clear flag not set")
	}

	// Test without clear
	req2 := EncodeCatLogRequest(7, false)
	flags2, handle2, err := DecodeCatLogRequest(req2)
	if err != nil {
		t.Fatalf("DecodeCatLogRequest: %v", err)
	}
	if handle2 != 7 {
		t.Errorf("handle = %d, want 7", handle2)
	}
	if flags2&CatLogFlagClear != 0 {
		t.Error("clear flag should not be set")
	}

	// Test response encode/decode
	data := []byte("hello world log output\n")
	resp := EncodeSvcLog(data)
	rFlags, logData, err := DecodeSvcLog(resp)
	if err != nil {
		t.Fatalf("DecodeSvcLog: %v", err)
	}
	if rFlags != 0 {
		t.Errorf("flags = %d, want 0", rFlags)
	}
	if !bytes.Equal(logData, data) {
		t.Errorf("logData = %q, want %q", logData, data)
	}

	// Test empty response
	emptyResp := EncodeSvcLog([]byte{})
	_, emptyData, err := DecodeSvcLog(emptyResp)
	if err != nil {
		t.Fatalf("DecodeSvcLog empty: %v", err)
	}
	if len(emptyData) != 0 {
		t.Errorf("empty logData len = %d, want 0", len(emptyData))
	}
}

func TestCatLogCommand_NoBuffer(t *testing.T) {
	server, sockPath := setupTestServer(t)
	defer server.Stop()

	// Add a process service without log buffer
	svc := service.NewProcessService(server.services, "test-svc")
	server.services.AddService(svc)

	conn := connectTest(t, sockPath)
	defer conn.Close()

	// Load service to get handle
	if err := WritePacket(conn, CmdLoadService, EncodeServiceName("test-svc")); err != nil {
		t.Fatalf("WritePacket load: %v", err)
	}
	rply, payload, err := ReadPacket(conn)
	if err != nil {
		t.Fatalf("ReadPacket load: %v", err)
	}
	if rply != RplyServiceRecord {
		t.Fatalf("expected RplyServiceRecord, got %d", rply)
	}
	handle := binary.LittleEndian.Uint32(payload[1:5])

	// Send CatLog request
	catlogReq := EncodeCatLogRequest(handle, false)
	if err := WritePacket(conn, CmdCatLog, catlogReq); err != nil {
		t.Fatalf("WritePacket catlog: %v", err)
	}

	rply2, _, err := ReadPacket(conn)
	if err != nil {
		t.Fatalf("ReadPacket catlog: %v", err)
	}
	if rply2 != RplyNAK {
		t.Errorf("expected RplyNAK for service without buffer, got %d", rply2)
	}
}

func TestCatLogCommand_WithBuffer(t *testing.T) {
	server, sockPath := setupTestServer(t)
	defer server.Stop()

	// Create a process service with log buffer
	svc := service.NewProcessService(server.services, "buffered-svc")
	svc.SetLogType(service.LogToBuffer)
	svc.SetLogBufMax(4096)
	server.services.AddService(svc)

	// Set up a log buffer with data
	lb := service.NewLogBuffer(4096)
	lb.WriteTestData([]byte("test output line 1\ntest output line 2\n"))
	svc.SetTestLogBuffer(lb)

	conn := connectTest(t, sockPath)
	defer conn.Close()

	// Load service
	if err := WritePacket(conn, CmdLoadService, EncodeServiceName("buffered-svc")); err != nil {
		t.Fatalf("WritePacket load: %v", err)
	}
	rply, payload, err := ReadPacket(conn)
	if err != nil {
		t.Fatalf("ReadPacket load: %v", err)
	}
	if rply != RplyServiceRecord {
		t.Fatalf("expected RplyServiceRecord, got %d", rply)
	}
	handle := binary.LittleEndian.Uint32(payload[1:5])

	// Send CatLog request
	catlogReq := EncodeCatLogRequest(handle, false)
	if err := WritePacket(conn, CmdCatLog, catlogReq); err != nil {
		t.Fatalf("WritePacket catlog: %v", err)
	}

	rply2, payload2, err := ReadPacket(conn)
	if err != nil {
		t.Fatalf("ReadPacket catlog: %v", err)
	}
	if rply2 != RplySvcLog {
		t.Fatalf("expected RplySvcLog, got %d", rply2)
	}

	_, logData, err := DecodeSvcLog(payload2)
	if err != nil {
		t.Fatalf("DecodeSvcLog: %v", err)
	}

	expected := "test output line 1\ntest output line 2\n"
	if string(logData) != expected {
		t.Errorf("logData = %q, want %q", logData, expected)
	}
}

func TestCatLogCommand_Clear(t *testing.T) {
	server, sockPath := setupTestServer(t)
	defer server.Stop()

	svc := service.NewProcessService(server.services, "clear-svc")
	svc.SetLogType(service.LogToBuffer)
	svc.SetLogBufMax(4096)
	server.services.AddService(svc)

	lb := service.NewLogBuffer(4096)
	lb.WriteTestData([]byte("data to be cleared\n"))
	svc.SetTestLogBuffer(lb)

	conn := connectTest(t, sockPath)
	defer conn.Close()

	// Load service
	if err := WritePacket(conn, CmdLoadService, EncodeServiceName("clear-svc")); err != nil {
		t.Fatalf("WritePacket: %v", err)
	}
	rply, payload, err := ReadPacket(conn)
	if err != nil || rply != RplyServiceRecord {
		t.Fatalf("load failed: rply=%d err=%v", rply, err)
	}
	handle := binary.LittleEndian.Uint32(payload[1:5])

	// Send CatLog with clear flag
	catlogReq := EncodeCatLogRequest(handle, true)
	if err := WritePacket(conn, CmdCatLog, catlogReq); err != nil {
		t.Fatalf("WritePacket catlog: %v", err)
	}

	rply2, payload2, err := ReadPacket(conn)
	if err != nil || rply2 != RplySvcLog {
		t.Fatalf("catlog failed: rply=%d err=%v", rply2, err)
	}

	_, logData, _ := DecodeSvcLog(payload2)
	if string(logData) != "data to be cleared\n" {
		t.Errorf("logData = %q, want %q", logData, "data to be cleared\n")
	}

	// Buffer should be empty now - send another catlog
	catlogReq2 := EncodeCatLogRequest(handle, false)
	if err := WritePacket(conn, CmdCatLog, catlogReq2); err != nil {
		t.Fatalf("WritePacket catlog2: %v", err)
	}

	rply3, payload3, err := ReadPacket(conn)
	if err != nil || rply3 != RplySvcLog {
		t.Fatalf("catlog2 failed: rply=%d err=%v", rply3, err)
	}

	_, logData2, _ := DecodeSvcLog(payload3)
	if len(logData2) != 0 {
		t.Errorf("buffer should be empty after clear, got %q", logData2)
	}
}
