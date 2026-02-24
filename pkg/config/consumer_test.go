package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sunlightlinux/slinit/pkg/service"
)

func TestConsumerOfConfigParsing(t *testing.T) {
	input := `
type = process
command = /bin/logger
consumer-of: my-producer
`
	desc, err := Parse(strings.NewReader(input), "my-consumer", "test-file")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if desc.ConsumerOf != "my-producer" {
		t.Errorf("expected ConsumerOf='my-producer', got '%s'", desc.ConsumerOf)
	}
}

func TestConsumerOfLoaderSetup(t *testing.T) {
	dir := t.TempDir()
	ss := service.NewServiceSet(&testConsumerLogger{})
	loader := NewDirLoader(ss, []string{dir})
	ss.SetLoader(loader)

	// Create producer with log-type = pipe
	writeConsumerServiceFile(t, dir, "producer", "type = process\ncommand = /bin/produce\nlog-type = pipe\n")

	// Create consumer with consumer-of: producer
	writeConsumerServiceFile(t, dir, "consumer", "type = process\ncommand = /bin/consume\nconsumer-of: producer\n")

	consumer, err := loader.LoadService("consumer")
	if err != nil {
		t.Fatalf("load consumer failed: %v", err)
	}

	producer := ss.FindService("producer", false)
	if producer == nil {
		t.Fatal("producer should be loaded")
	}

	// Verify bidirectional links
	if consumer.Record().ConsumerFor() != producer {
		t.Error("consumer's ConsumerFor should point to producer")
	}
	if producer.Record().LogConsumer() != consumer {
		t.Error("producer's LogConsumer should point to consumer")
	}
}

func TestConsumerOfProducerNotPipe(t *testing.T) {
	dir := t.TempDir()
	ss := service.NewServiceSet(&testConsumerLogger{})
	loader := NewDirLoader(ss, []string{dir})
	ss.SetLoader(loader)

	// Producer WITHOUT log-type = pipe
	writeConsumerServiceFile(t, dir, "producer", "type = process\ncommand = /bin/produce\n")

	// Consumer referencing it
	writeConsumerServiceFile(t, dir, "consumer", "type = process\ncommand = /bin/consume\nconsumer-of: producer\n")

	_, err := loader.LoadService("consumer")
	if err == nil {
		t.Fatal("expected error when producer doesn't have log-type = pipe")
	}
	if !strings.Contains(err.Error(), "log-type = pipe") {
		t.Errorf("error should mention log-type = pipe, got: %v", err)
	}
}

func TestConsumerOfAlreadyHasConsumer(t *testing.T) {
	dir := t.TempDir()
	ss := service.NewServiceSet(&testConsumerLogger{})
	loader := NewDirLoader(ss, []string{dir})
	ss.SetLoader(loader)

	// Producer
	writeConsumerServiceFile(t, dir, "producer", "type = process\ncommand = /bin/produce\nlog-type = pipe\n")

	// First consumer
	writeConsumerServiceFile(t, dir, "consumer1", "type = process\ncommand = /bin/consume1\nconsumer-of: producer\n")

	// Second consumer
	writeConsumerServiceFile(t, dir, "consumer2", "type = process\ncommand = /bin/consume2\nconsumer-of: producer\n")

	_, err := loader.LoadService("consumer1")
	if err != nil {
		t.Fatalf("first consumer load failed: %v", err)
	}

	_, err = loader.LoadService("consumer2")
	if err == nil {
		t.Fatal("expected error when producer already has a consumer")
	}
	if !strings.Contains(err.Error(), "already has consumer") {
		t.Errorf("error should mention 'already has consumer', got: %v", err)
	}
}

// Helpers

type testConsumerLogger struct{}

func (l *testConsumerLogger) ServiceStarted(name string)              {}
func (l *testConsumerLogger) ServiceStopped(name string)              {}
func (l *testConsumerLogger) ServiceFailed(name string, dep bool)     {}
func (l *testConsumerLogger) Error(format string, args ...interface{}) {}
func (l *testConsumerLogger) Info(format string, args ...interface{})  {}

func writeConsumerServiceFile(t *testing.T, dir, name, content string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("failed to write service file: %v", err)
	}
}
