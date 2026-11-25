package control

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"path/filepath"
	"testing"

	"github.com/sunlightlinux/slinit/pkg/logging"
	"github.com/sunlightlinux/slinit/pkg/service"
)

// benchServer starts a control server over n internal services.
func benchServer(b *testing.B, n int) (*Server, *service.ServiceSet, string) {
	b.Helper()
	sock := filepath.Join(b.TempDir(), "s")
	ss := service.NewServiceSet(&testLogger{})
	srv := NewServer(ss, sock, logging.New(logging.LevelError))
	if err := srv.Start(context.Background()); err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { srv.Stop() })
	for i := 0; i < n; i++ {
		ss.AddService(service.NewInternalService(ss, fmt.Sprintf("service-number-%04d", i)))
	}
	return srv, ss, sock
}

// BenchmarkListServices5 is one list round trip, with the client
// reading the reply stream packet by packet straight from the socket
// (as slinitctl does) or through a bufio.Reader.
func BenchmarkListServices5(b *testing.B) {
	for _, n := range []int{100, 1000, 4000} {
		for _, buffered := range []bool{false, true} {
			b.Run(fmt.Sprintf("%d/buffered=%v", n, buffered), func(b *testing.B) {
				_, _, sock := benchServer(b, n)
				conn, err := net.Dial("unix", sock)
				if err != nil {
					b.Fatal(err)
				}
				defer conn.Close()
				var r io.Reader = conn
				if buffered {
					r = bufio.NewReaderSize(conn, 64<<10)
				}
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					if err := WritePacket(conn, CmdListServices5, nil); err != nil {
						b.Fatal(err)
					}
					cnt := 0
					for {
						rply, _, err := ReadPacket(r)
						if err != nil {
							b.Fatal(err)
						}
						if rply == RplyListDone {
							break
						}
						cnt++
					}
					if cnt != n {
						b.Fatalf("listed %d services, want %d", cnt, n)
					}
				}
			})
		}
	}
}

// BenchmarkServiceEventSubscribed starts and stops a target that
// waits-for 1000 services, with or without one control connection
// holding a handle on every one of them: each transition then writes
// the connection a v5 and a v4 event under queueMu.
func BenchmarkServiceEventSubscribed(b *testing.B) {
	for _, sub := range []bool{false, true} {
		b.Run(fmt.Sprint("sub=", sub), func(b *testing.B) {
			_, ss, sock := benchServer(b, 0)
			boot := service.NewInternalService(ss, "boot")
			ss.AddService(boot)
			for i := 0; i < 1000; i++ {
				s := service.NewInternalService(ss, fmt.Sprintf("leaf-%d", i))
				ss.AddService(s)
				boot.Record().AddDep(s, service.DepWaitsFor)
			}
			conn, err := net.Dial("unix", sock)
			if err != nil {
				b.Fatal(err)
			}
			defer conn.Close()
			if sub {
				for i := 0; i < 1000; i++ {
					if err := WritePacket(conn, CmdFindService, EncodeServiceName(fmt.Sprintf("leaf-%d", i))); err != nil {
						b.Fatal(err)
					}
					if _, _, err := ReadPacket(conn); err != nil {
						b.Fatal(err)
					}
				}
				go io.Copy(io.Discard, conn)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				ss.StartService(boot)
				ss.StopService(boot)
			}
		})
	}
}
