package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sunlightlinux/slinit/pkg/service"
)

// A port of dinit's igr test log-via-pipe (dinit fe7ace7): a producer
// with log-type = pipe and a consumer declaring consumer-of it. Starting
// and stopping the producer, and unloading it and loading it again, must
// not break the pipe the consumer reads: every run's output reaches the
// consumer, which stays up throughout.
//
// slinit used to close the pipe whenever the producer stopped, so the
// consumer read EOF and exited after the first run, and it refused to
// unload a producer that had a consumer at all.

func waitState(t *testing.T, svc service.Service, want service.ServiceState) {
	t.Helper()
	for end := time.Now().Add(5 * time.Second); time.Now().Before(end); time.Sleep(10 * time.Millisecond) {
		if svc.State() == want {
			return
		}
	}
	t.Fatalf("%s: state %v, want %v", svc.Name(), svc.State(), want)
}

func waitLines(t *testing.T, path string, n int, what string) {
	t.Helper()
	var got string
	for end := time.Now().Add(3 * time.Second); time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
		b, _ := os.ReadFile(path)
		got = string(b)
		if strings.Count(got, "Producing output...\n") >= n {
			break
		}
	}
	if want := strings.Repeat("Producing output...\n", n); got != want {
		t.Fatalf("%s: consumer logged %q, want %q", what, got, want)
	}
}

func testLogViaPipe(t *testing.T, producer string) {
	dir := t.TempDir()
	out := filepath.Join(t.TempDir(), "logged-output")
	writeServiceFile(t, dir, "consumer", "type = process\ncommand = /bin/sh -c \"exec cat > "+out+"\"\nconsumer-of = producer\n")
	writeServiceFile(t, dir, "producer", producer+"log-type = pipe\n")

	ss := service.NewServiceSet(&testReloadLogger{})
	l := NewDirLoader(ss, []string{dir})
	ss.SetLoader(l)
	consumer, err := l.LoadService("consumer")
	if err != nil {
		t.Fatal(err)
	}
	ss.StartService(consumer)
	waitState(t, consumer, service.StateStarted)
	t.Cleanup(func() {
		// Also on a failed run: a bgprocess producer's daemon outlives
		// the test otherwise.
		if p := ss.FindService("producer", false); p != nil {
			ss.StopService(p)
			waitState(t, p, service.StateStopped)
		}
		ss.StopService(consumer)
	})

	run := func(n int, unload bool) {
		t.Helper()
		if unload {
			p := ss.FindService("producer", false)
			if p == nil {
				t.Fatal("producer not loaded before unload")
			}
			if !p.Record().HasLoneRef(0) {
				t.Fatalf("run %d: a producer with a consumer must be unloadable, as in dinit", n)
			}
			ss.UnloadService(p)
			if ss.FindService("producer", false) != nil {
				t.Fatalf("run %d: producer still loaded after unload", n)
			}
		}
		p, err := l.LoadService("producer")
		if err != nil {
			t.Fatalf("run %d: load producer: %v", n, err)
		}
		if p.Record().LogConsumer() != consumer || consumer.Record().ConsumerFor() != p {
			t.Fatalf("run %d: producer and consumer are not linked both ways", n)
		}
		ss.StartService(p)
		waitState(t, p, service.StateStarted)
		waitLines(t, out, n, "after run")
		ss.StopService(p)
		waitState(t, p, service.StateStopped)
		// Give a consumer that read EOF the time to exit and be reaped.
		time.Sleep(200 * time.Millisecond)
		if consumer.State() != service.StateStarted {
			t.Fatalf("run %d: consumer is %v; the producer stopping must not end its input", n, consumer.State())
		}
	}
	run(1, false)
	run(2, false)
	run(3, true)
	run(4, true)
}

func TestLogViaPipeScriptedProducer(t *testing.T) {
	testLogViaPipe(t, "type = scripted\ncommand = /bin/sh -c \"echo Producing output...\"\n")
}

func TestLogViaPipeProcessProducer(t *testing.T) {
	// A process producer stays up until stopped; it writes once and waits.
	testLogViaPipe(t, "type = process\ncommand = /bin/sh -c \"echo Producing output...; exec sleep 60\"\n")
}

// A producer loaded again in a form that cannot feed its consumer is
// refused, and the placeholder keeps the pipe and the link until a good
// description comes.
func TestPlaceholderSurvivesFailedLoad(t *testing.T) {
	dir := t.TempDir()
	writeServiceFile(t, dir, "consumer", "type = process\ncommand = /bin/sh -c \"exec cat > /dev/null\"\nconsumer-of = producer\n")
	writeServiceFile(t, dir, "producer", "type = scripted\ncommand = /bin/true\nlog-type = pipe\n")
	ss := service.NewServiceSet(&testReloadLogger{})
	l := NewDirLoader(ss, []string{dir})
	ss.SetLoader(l)
	consumer, err := l.LoadService("consumer")
	if err != nil {
		t.Fatal(err)
	}
	producer := ss.FindService("producer", false)
	if err := producer.Record().EnsureOutputPipe(); err != nil {
		t.Fatal(err)
	}
	w := producer.Record().OutputPipeW()
	ss.UnloadService(producer)
	ph := ss.FindService("producer", true)

	writeServiceFile(t, dir, "producer", "type = scripted\ncommand = /bin/true\nlog-type = file\nlogfile = /dev/null\n")
	if _, err := l.LoadService("producer"); err == nil {
		t.Fatal("a producer without log-type = pipe was accepted for a consumer")
	}
	if ss.FindService("producer", true) != ph || consumer.Record().ConsumerFor() != ph || ph.Record().OutputPipeW() != w {
		t.Fatal("the failed load did not leave the placeholder, its link and its pipe in place")
	}

	// A failure after the record joined the set (a missing dependency)
	// must put the placeholder back too.
	writeServiceFile(t, dir, "producer", "type = scripted\ncommand = /bin/true\nlog-type = pipe\ndepends-on: no-such-service\n")
	if _, err := l.LoadService("producer"); err == nil {
		t.Fatal("a producer with a missing dependency loaded")
	}
	if ss.FindService("producer", true) != ph || consumer.Record().ConsumerFor() != ph || ph.Record().OutputPipeW() != w {
		t.Fatal("a load failing after joining the set did not restore the placeholder")
	}

	writeServiceFile(t, dir, "producer", "type = scripted\ncommand = /bin/true\nlog-type = pipe\n")
	p, err := l.LoadService("producer")
	if err != nil {
		t.Fatal(err)
	}
	if consumer.Record().ConsumerFor() != p || p.Record().OutputPipeW() != w {
		t.Error("the good load did not take the link and the same pipe")
	}
	if ss.FindService("producer", true) != p {
		t.Error("placeholder still in the set after the producer loaded")
	}
}

func TestLogViaPipeBGProcessProducer(t *testing.T) {
	// A forking producer: the launcher writes the line and leaves a
	// daemon behind whose pid it records. `$$` is a literal `$` in a
	// command (slinit expands $VAR when it loads the file).
	pidfile := filepath.Join(t.TempDir(), "producer.pid")
	testLogViaPipe(t, "type = bgprocess\ncommand = /bin/sh -c \"echo Producing output...; sleep 60 >/dev/null 2>&1 & echo $$! > "+pidfile+"\"\npid-file = "+pidfile+"\n")
}
