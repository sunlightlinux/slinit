package performance

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sunlightlinux/slinit/pkg/control"
	"github.com/sunlightlinux/slinit/pkg/logging"
	"github.com/sunlightlinux/slinit/pkg/service"
)

// readListResponse reads all RplySvcInfo packets until RplyListDone.
func readListResponse(conn net.Conn) error {
	for {
		rply, _, err := control.ReadPacket(conn)
		if err != nil {
			return err
		}
		if rply == control.RplyListDone {
			return nil
		}
	}
}

// BenchmarkControlRoundTrip measures control protocol list-services latency.
func BenchmarkControlRoundTrip(b *testing.B) {
	for _, svcCount := range []int{5, 20, 100} {
		b.Run(fmt.Sprintf("services_%d", svcCount), func(b *testing.B) {
			set, socketPath, cleanup := startControlServer(b)
			defer cleanup()

			for i := 0; i < svcCount; i++ {
				svc := service.NewInternalService(set, fmt.Sprintf("bench-svc-%03d", i))
				set.AddService(svc)
			}

			conn, err := net.Dial("unix", socketPath)
			if err != nil {
				b.Fatal(err)
			}
			defer conn.Close()

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if err := control.WritePacket(conn, control.CmdListServices, nil); err != nil {
					b.Fatal(err)
				}
				if err := readListResponse(conn); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// BenchmarkControlServiceStatus measures status query latency via find+status.
func BenchmarkControlServiceStatus(b *testing.B) {
	set, socketPath, cleanup := startControlServer(b)
	defer cleanup()

	svc := service.NewInternalService(set, "status-target")
	set.AddService(svc)

	conn, err := net.Dial("unix", socketPath)
	if err != nil {
		b.Fatal(err)
	}
	defer conn.Close()

	// Find the service to get a handle
	nameBytes := []byte("status-target")
	payload := make([]byte, 2+len(nameBytes))
	payload[0] = byte(len(nameBytes))
	payload[1] = byte(len(nameBytes) >> 8)
	copy(payload[2:], nameBytes)

	if err := control.WritePacket(conn, control.CmdFindService, payload); err != nil {
		b.Fatal(err)
	}
	rply, rplyData, err := control.ReadPacket(conn)
	if err != nil {
		b.Fatalf("find service failed: rply=%d err=%v", rply, err)
	}
	if rply != control.RplyServiceRecord || len(rplyData) < 4 {
		b.Fatalf("unexpected reply: %d (len=%d)", rply, len(rplyData))
	}
	handle := rplyData[:4]

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := control.WritePacket(conn, control.CmdServiceStatus, handle); err != nil {
			b.Fatal(err)
		}
		_, _, err := control.ReadPacket(conn)
		if err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkWireEncoding measures packet encoding overhead.
func BenchmarkWireEncoding(b *testing.B) {
	b.Run("write_packet", func(b *testing.B) {
		r, w, _ := os.Pipe()
		defer r.Close()
		defer w.Close()
		go func() {
			buf := make([]byte, 4096)
			for {
				if _, err := r.Read(buf); err != nil {
					return
				}
			}
		}()

		payload := make([]byte, 64)
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			control.WritePacket(w, control.CmdListServices, payload)
		}
	})

	b.Run("encode_handle", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			control.EncodeHandle(uint32(i))
		}
	})
}

func startControlServer(b *testing.B) (*service.ServiceSet, string, func()) {
	b.Helper()

	logger := logging.New(logging.LevelError)
	set := service.NewServiceSet(logger)

	socketPath := filepath.Join(b.TempDir(), "bench.sock")
	srv := control.NewServer(set, socketPath, logger)

	ctx, cancel := context.WithCancel(context.Background())
	if err := srv.Start(ctx); err != nil {
		cancel()
		b.Fatal(err)
	}

	// Wait for socket
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(socketPath); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	return set, socketPath, func() {
		cancel()
		srv.Stop()
	}
}
