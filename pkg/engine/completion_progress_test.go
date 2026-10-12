package engine

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/assetnote/h3fuzz/pkg/ffuf"
)

type completionProgressInput struct{}

func (*completionProgressInput) ActivateKeywords([]string)                  {}
func (*completionProgressInput) AddProvider(ffuf.InputProviderConfig) error { return nil }
func (*completionProgressInput) Keywords() []string                         { return []string{"FUZZ"} }
func (*completionProgressInput) Next() bool                                 { return false }
func (*completionProgressInput) Position() int                              { return 0 }
func (*completionProgressInput) SetPosition(int)                            {}
func (*completionProgressInput) Reset()                                     {}
func (*completionProgressInput) Value() map[string][]byte                   { return nil }
func (*completionProgressInput) Total() int                                 { return 1 }

type completionProgressOutput struct {
	*NullOutput
	progress chan ffuf.Progress
}

func newCompletionProgressOutput(buffer int) *completionProgressOutput {
	return &completionProgressOutput{
		NullOutput: NewNullOutput(),
		progress:   make(chan ffuf.Progress, buffer),
	}
}

func (o *completionProgressOutput) Progress(progress ffuf.Progress) {
	o.progress <- progress
}

func TestLogicalCompletionCountExcludesRetryAttempts(t *testing.T) {
	runner := &retryTelemetryRunner{errors: []error{errors.New("retry me")}}
	job := NewJob(&ffuf.Config{
		Context:        context.Background(),
		MatcherManager: &fakeMatcherManager{},
	})
	job.Output = NewNullOutput()
	job.Runner = runner

	job.runLogicalTask(
		jobContext{basereq: ffuf.Request{Method: "GET", Url: "https://example.test/FUZZ"}},
		map[string][]byte{"FUZZ": []byte("one")},
		1,
		0,
	)

	if got := runner.calls.Load(); got != 2 {
		t.Fatalf("runner attempts = %d, want 2", got)
	}
	if got := job.Snapshot().Progress.CompletedCount; got != 1 {
		t.Fatalf("completed logical inputs = %d, want 1", got)
	}
}

func TestLogicalCompletionCountIncludesTerminalFailures(t *testing.T) {
	runner := &retryTelemetryRunner{errors: []error{
		errors.New("initial failure"),
		errors.New("terminal failure"),
	}}
	job := NewJob(&ffuf.Config{
		Context:        context.Background(),
		MatcherManager: &fakeMatcherManager{},
	})
	job.Output = NewNullOutput()
	job.Runner = runner

	job.runLogicalTask(
		jobContext{basereq: ffuf.Request{Method: "GET", Url: "https://example.test/FUZZ"}},
		map[string][]byte{"FUZZ": []byte("one")},
		1,
		0,
	)

	if got := runner.calls.Load(); got != 2 {
		t.Fatalf("runner attempts = %d, want 2", got)
	}
	if got := job.Snapshot().Progress.CompletedCount; got != 1 {
		t.Fatalf("completed logical inputs = %d, want 1", got)
	}
	if got := job.Snapshot().Progress.ResponseCount; got != 0 {
		t.Fatalf("successful responses = %d, want 0", got)
	}
}

func TestResetStartsANewLogicalCompletionCycle(t *testing.T) {
	job := NewJob(&ffuf.Config{})
	job.Input = &completionProgressInput{}
	job.Output = NewNullOutput()
	job.incCompletedCounter()

	job.Reset(false)

	if got := job.Snapshot().Progress.CompletedCount; got != 0 {
		t.Fatalf("completed logical inputs after reset = %d, want 0", got)
	}
}

func TestProgressUpdatePublishesLogicalCompletionCount(t *testing.T) {
	output := newCompletionProgressOutput(1)
	job := NewJob(&ffuf.Config{})
	job.Input = &completionProgressInput{}
	job.Output = output
	job.queue.push(QueueJob{Url: "https://example.test/FUZZ"})
	job.incCompletedCounter()

	job.updateProgress()

	progress := <-output.progress
	if progress.CompletedCount != 1 {
		t.Fatalf("published completed logical inputs = %d, want 1", progress.CompletedCount)
	}
}

func TestProgressPublicationContinuesUntilLogicalInputsDrain(t *testing.T) {
	output := newCompletionProgressOutput(16)
	job := NewJob(&ffuf.Config{ProgressFrequency: 1})
	job.Input = &completionProgressInput{}
	job.Output = output
	job.queue.push(QueueJob{Url: "https://example.test/FUZZ"})
	job.setRunning(true)
	job.setRunningJob(true)
	job.incCounter()

	var background sync.WaitGroup
	background.Add(1)
	go job.runBackgroundTasks(&background)
	done := make(chan struct{})
	go func() {
		background.Wait()
		close(done)
	}()

	select {
	case <-output.progress:
	case <-time.After(time.Second):
		t.Fatal("first progress update was not published")
	}
	select {
	case <-output.progress:
		// A second update proves publication stayed alive after all input was
		// scheduled while its request was still in flight.
	case <-done:
		t.Fatal("progress publication stopped before the scheduled request completed")
	case <-time.After(time.Second):
		t.Fatal("second progress update was not published")
	}

	job.incCompletedCounter()
	select {
	case <-done:
	case <-time.After(time.Second):
		job.setRunning(false)
		t.Fatal("progress publication did not stop after logical inputs drained")
	}
}

func TestProgressPublicationStopsWithoutWaitingForDrainAfterStop(t *testing.T) {
	output := newCompletionProgressOutput(16)
	job := NewJob(&ffuf.Config{ProgressFrequency: 1})
	job.Input = &completionProgressInput{}
	job.Output = output
	job.queue.push(QueueJob{Url: "https://example.test/FUZZ"})
	job.setRunning(true)
	job.setRunningJob(true)
	job.incCounter()

	var background sync.WaitGroup
	background.Add(1)
	go job.runBackgroundTasks(&background)
	done := make(chan struct{})
	go func() {
		background.Wait()
		close(done)
	}()

	select {
	case <-output.progress:
	case <-time.After(time.Second):
		t.Fatal("first progress update was not published")
	}
	job.Stop()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("progress publication did not stop after job cancellation")
	}
}

func TestProgressPublicationHonorsPauseWhileWaitingForDrain(t *testing.T) {
	output := newCompletionProgressOutput(16)
	job := NewJob(&ffuf.Config{ProgressFrequency: 1})
	job.Input = &completionProgressInput{}
	job.Output = output
	job.queue.push(QueueJob{Url: "https://example.test/FUZZ"})
	job.SetExternallyManaged(true)
	job.setRunning(true)
	job.setRunningJob(true)
	job.incCounter()
	job.Pause()
	defer func() {
		job.Resume()
		job.setRunning(false)
	}()

	var background sync.WaitGroup
	background.Add(1)
	go job.runBackgroundTasks(&background)
	done := make(chan struct{})
	go func() {
		background.Wait()
		close(done)
	}()

	select {
	case <-output.progress:
		t.Fatal("progress update bypassed the pause gate")
	case <-time.After(25 * time.Millisecond):
	}

	job.Resume()
	select {
	case <-output.progress:
	case <-time.After(time.Second):
		t.Fatal("progress publication did not resume")
	}
	job.incCompletedCounter()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("progress publication did not stop after resume and drain")
	}
}
