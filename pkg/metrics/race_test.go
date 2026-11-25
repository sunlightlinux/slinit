package metrics

import (
	"io"
	"sync"
	"testing"
	"time"

	"github.com/sunlightlinux/slinit/pkg/service"
)

// Render and BuildStatus run on the HTTP goroutine and read fields the
// state machine writes under its lock (startFailed, startedTime,
// startRequestTime, the boot times). They read them bare, which -race
// reported against initiateStart and Started. Run under -race.
func TestMetricsReadUnderGraphLock(t *testing.T) {
	ss := testSet(t)
	svc := service.NewInternalService(ss, "svc")
	ss.AddService(svc)

	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			ss.StartService(svc)
			ss.StopService(svc)
		}
	}()

	deadline := time.Now().Add(200 * time.Millisecond)
	for time.Now().Before(deadline) {
		if err := Render(io.Discard, ss, "test"); err != nil {
			t.Fatal(err)
		}
		BuildStatus(ss, "test", time.Now())
	}
	close(stop)
	wg.Wait()
}
