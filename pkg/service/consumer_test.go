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

// Unloading a producer whose consumer stays loaded leaves a placeholder
// that holds the pipe and the link, both ways (dinit 1e50862/b58775b), so
// the consumer's input survives. Unloading the consumer clears the
// producer's side, and removes a placeholder it was the only user of.
func TestConsumerOfUnloadHandsOverToPlaceholder(t *testing.T) {
	set, _ := newTestSet()

	producer := NewProcessService(set, "producer")
	set.AddService(producer)
	consumer := NewProcessService(set, "consumer")
	set.AddService(consumer)
	producer.Record().SetLogConsumer(consumer)
	consumer.Record().SetConsumerFor(producer)
	if err := producer.Record().EnsureOutputPipe(); err != nil {
		t.Fatal(err)
	}
	r, w := producer.Record().OutputPipeR(), producer.Record().OutputPipeW()

	set.UnloadService(producer)

	ph := consumer.Record().ConsumerFor()
	if ph == nil || ph.Type() != TypePlaceholder || ph.Name() != "producer" {
		t.Fatalf("consumer's producer after unload: %v", ph)
	}
	if ph.Record().LogConsumer() != consumer {
		t.Error("placeholder does not link back to the consumer")
	}
	if ph.Record().OutputPipeR() != r || ph.Record().OutputPipeW() != w {
		t.Error("placeholder did not take the open pipe")
	}
	if set.FindService("producer", false) != nil || set.FindService("producer", true) != ph {
		t.Error("placeholder must be findable only when asking for placeholders")
	}
	for _, s := range set.ListServices() {
		if s == ph {
			t.Error("placeholder listed as a loaded service")
		}
	}

	// Unloading the consumer removes the placeholder and closes the pipe.
	set.UnloadService(consumer)
	if set.FindService("producer", true) != nil {
		t.Error("orphaned placeholder left in the set")
	}
	if ph.Record().OutputPipeW() != nil {
		t.Error("orphaned placeholder's pipe left open")
	}

	// Unloading a consumer whose producer is loaded clears the producer's side.
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

// A log consumer does not keep its producer from being unloaded, as in
// dinit's has_lone_ref: the placeholder takes over.
func TestConsumerOfHasLoneRefWithConsumer(t *testing.T) {
	set, _ := newTestSet()

	producer := NewProcessService(set, "producer")
	set.AddService(producer)
	consumer := NewProcessService(set, "consumer")
	set.AddService(consumer)
	producer.Record().SetLogConsumer(consumer)
	consumer.Record().SetConsumerFor(producer)

	if !producer.Record().HasLoneRef(0) {
		t.Error("a producer with a consumer must be unloadable")
	}
}
