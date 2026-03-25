package performance

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sunlightlinux/slinit/pkg/config"
)

// BenchmarkParseService measures config parsing throughput for a single service file.
func BenchmarkParseService(b *testing.B) {
	content := `type = process
command = /bin/sleep 999
restart = true
restart-delay = 1
smooth-recovery = true
stop-timeout = 10
start-timeout = 30
working-dir = /tmp
log-type = buffer
log-buffer-size = 8192
`
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := config.Parse(strings.NewReader(content), "bench-svc", "bench-svc")
		if err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkParseServiceComplex measures parsing a service with many directives.
func BenchmarkParseServiceComplex(b *testing.B) {
	content := `type = process
command = /usr/bin/myapp --flag1 --flag2 --config /etc/myapp.conf
stop-command = /usr/bin/myapp --stop
restart = true
restart-delay = 2
restart-limit-count = 5
restart-limit-interval = 60
smooth-recovery = true
stop-timeout = 15
start-timeout = 45
working-dir = /var/lib/myapp
run-as = nobody
log-type = buffer
log-buffer-size = 16384
ready-notification = pipefd:3
depends-on: dep-a
depends-on: dep-b
waits-for: dep-c
waits-for: dep-d
env-file = /etc/myapp.env
nice = -5
oom-score-adj = -100
cpu-affinity = 0-3
`
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := config.Parse(strings.NewReader(content), "complex-svc", "complex-svc")
		if err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkParseBatch measures parsing N service files from disk sequentially.
func BenchmarkParseBatch(b *testing.B) {
	for _, count := range []int{10, 50, 100, 500} {
		b.Run(fmt.Sprintf("services_%d", count), func(b *testing.B) {
			dir := b.TempDir()
			for i := 0; i < count; i++ {
				writeServiceFile(dir, fmt.Sprintf("svc-%04d", i), fmt.Sprintf(`type = process
command = /bin/sleep %d
restart = true
stop-timeout = 10
`, i+1))
			}

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				for j := 0; j < count; j++ {
					f, _ := os.Open(filepath.Join(dir, fmt.Sprintf("svc-%04d", j)))
					_, err := config.Parse(f, fmt.Sprintf("svc-%04d", j), fmt.Sprintf("svc-%04d", j))
					f.Close()
					if err != nil {
						b.Fatal(err)
					}
				}
			}
		})
	}
}

// BenchmarkParseCPUAffinity measures cpu-affinity string parsing.
func BenchmarkParseCPUAffinity(b *testing.B) {
	specs := []struct {
		name string
		val  string
	}{
		{"single", "0"},
		{"range_small", "0-3"},
		{"range_large", "0-63"},
		{"mixed", "0-3,8-11,16-19,24-27"},
	}
	for _, s := range specs {
		b.Run(s.name, func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				_, err := config.ParseCPUAffinity(s.val)
				if err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func writeServiceFile(dir, name, content string) {
	os.WriteFile(filepath.Join(dir, name), []byte(content), 0644)
}
