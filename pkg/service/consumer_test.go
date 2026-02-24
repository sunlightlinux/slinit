package service

import (
	"testing"
)

func TestConsumerOfPipeCreation(t *testing.T) {
	set, _ := newTestSet()

	svc := NewProcessService(set, "producer")
	set.AddService(svc)

	rec := svc.Record()

	// Pipe should not exist initially
	if rec.OutputPipeW() != nil || rec.OutputPipeR() != nil {
		t.Fatal("pipe should not exist initially")
	}

	// First EnsureOutputPipe creates the pipe
	if err := rec.EnsureOutputPipe(); err != nil {
		t.Fatalf("EnsureOutputPipe failed: %v", err)
	}

	w := rec.OutputPipeW()
	r := rec.OutputPipeR()
	if w == nil || r == nil {
		t.Fatal("pipe fds should be non-nil after EnsureOutputPipe")
	}

	// Second call should return same fds
	if err := rec.EnsureOutputPipe(); err != nil {
		t.Fatalf("second EnsureOutputPipe failed: %v", err)
	}
	if rec.OutputPipeW() != w || rec.OutputPipeR() != r {
		t.Error("second EnsureOutputPipe should return same fds")
	}

	// Clean up
	rec.CloseOutputPipe()
}

func TestConsumerOfCloseOutputPipe(t *testing.T) {
	set, _ := newTestSet()

	svc := NewProcessService(set, "producer")
	set.AddService(svc)

	rec := svc.Record()

	if err := rec.EnsureOutputPipe(); err != nil {
		t.Fatalf("EnsureOutputPipe failed: %v", err)
	}

	rec.CloseOutputPipe()

	if rec.OutputPipeW() != nil || rec.OutputPipeR() != nil {
		t.Error("pipe fds should be nil after CloseOutputPipe")
	}

	// Closing again should be safe (no-op)
	rec.CloseOutputPipe()
}

func TestConsumerOfTransferOutputPipe(t *testing.T) {
	set, _ := newTestSet()

	svc := NewProcessService(set, "producer")
	set.AddService(svc)

	rec := svc.Record()

	if err := rec.EnsureOutputPipe(); err != nil {
		t.Fatalf("EnsureOutputPipe failed: %v", err)
	}

	origW := rec.OutputPipeW()
	origR := rec.OutputPipeR()

	r, w := rec.TransferOutputPipe()

	if r != origR || w != origW {
		t.Error("TransferOutputPipe should return original fds")
	}

	if rec.OutputPipeW() != nil || rec.OutputPipeR() != nil {
		t.Error("pipe fds should be nil after TransferOutputPipe")
	}

	// Install on a new record
	newSvc := NewProcessService(set, "new-producer")
	newRec := newSvc.Record()
	newRec.SetOutputPipeFDs(r, w)

	if newRec.OutputPipeW() != origW || newRec.OutputPipeR() != origR {
		t.Error("SetOutputPipeFDs should install transferred fds")
	}

	// Clean up
	newRec.CloseOutputPipe()
}

func TestConsumerOfBidirectionalLinks(t *testing.T) {
	set, _ := newTestSet()

	producer := NewProcessService(set, "producer")
	set.AddService(producer)

	consumer := NewProcessService(set, "consumer")
	set.AddService(consumer)

	producer.Record().SetLogConsumer(consumer)
	consumer.Record().SetConsumerFor(producer)

	if producer.Record().LogConsumer() != consumer {
		t.Error("producer should have consumer as LogConsumer")
	}
	if consumer.Record().ConsumerFor() != producer {
		t.Error("consumer should have producer as ConsumerFor")
	}
}

func TestConsumerOfUnloadClearsLinks(t *testing.T) {
	set, _ := newTestSet()

	producer := NewProcessService(set, "producer")
	set.AddService(producer)

	consumer := NewProcessService(set, "consumer")
	set.AddService(consumer)

	producer.Record().SetLogConsumer(consumer)
	consumer.Record().SetConsumerFor(producer)

	// Unload producer should clear both sides
	set.UnloadService(producer)

	if consumer.Record().ConsumerFor() != nil {
		t.Error("consumer's ConsumerFor should be nil after producer unload")
	}

	// Now test from the other direction: unload consumer
	producer2 := NewProcessService(set, "producer2")
	set.AddService(producer2)

	consumer2 := NewProcessService(set, "consumer2")
	set.AddService(consumer2)

	producer2.Record().SetLogConsumer(consumer2)
	consumer2.Record().SetConsumerFor(producer2)

	set.UnloadService(consumer2)

	if producer2.Record().LogConsumer() != nil {
		t.Error("producer's LogConsumer should be nil after consumer unload")
	}
}

func TestConsumerOfHasLoneRefWithConsumer(t *testing.T) {
	set, _ := newTestSet()

	producer := NewProcessService(set, "producer")
	set.AddService(producer)

	consumer := NewProcessService(set, "consumer")
	set.AddService(consumer)

	// Without consumer, HasLoneRef should be true
	if !producer.Record().HasLoneRef(0) {
		t.Error("should have lone ref without consumer")
	}

	// With consumer, HasLoneRef should be false
	producer.Record().SetLogConsumer(consumer)
	consumer.Record().SetConsumerFor(producer)

	if producer.Record().HasLoneRef(0) {
		t.Error("should not have lone ref with active consumer")
	}
}
