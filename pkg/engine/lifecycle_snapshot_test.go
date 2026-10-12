package engine

import (
	"context"
	"os"
	"os/signal"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/assetnote/h3fuzz/pkg/ffuf"
)

type countingCloseRunner struct {
	closeCalls int32
}

func (r *countingCloseRunner) Prepare(map[string][]byte, *ffuf.Request) (ffuf.Request, error) {
	return ffuf.Request{}, nil
}

func (r *countingCloseRunner) Execute(*ffuf.Request) (ffuf.Response, error) {
	return ffuf.Response{}, nil
}

func (r *countingCloseRunner) Dump(*ffuf.Request) ([]byte, error) { return nil, nil }

func (r *countingCloseRunner) Close() error {
	atomic.AddInt32(&r.closeCalls, 1)
	return nil
}

func TestJobSnapshotReturnsConsistentLifecycleState(t *testing.T) {
	conf := &ffuf.Config{Noninteractive: true}
	j := NewJob(conf)
	j.Output = NewNullOutput()
	j.setRunning(true)
	j.setRunningJob(true)
	j.setCounter(17)
	atomic.StoreInt64(&j.responseCounter, 14)
	atomic.StoreInt64(&j.errorCounter, 3)
	j.setError("target stopped responding")
	startedAt := time.Now().Add(-2 * time.Minute).Round(0)
	startedAtJob := time.Now().Add(-time.Minute).Round(0)
	j.setStartTime(startedAt)
	j.setStartTimeJob(startedAtJob)
	j.queue.push(QueueJob{Url: "https://example.test/FUZZ"})
	j.SetExternallyManaged(true)
	j.Pause()

	snapshot := j.Snapshot()
	if !snapshot.Running || !snapshot.RunningJob || !snapshot.Paused {
		t.Fatalf("unexpected lifecycle state: %+v", snapshot)
	}
	if snapshot.Error != "target stopped responding" {
		t.Fatalf("Error = %q", snapshot.Error)
	}
	if snapshot.StartedAt != startedAt || snapshot.StartedAtJob != startedAtJob {
		t.Fatalf("unexpected start times: %+v", snapshot)
	}
	if snapshot.Progress.ReqCount != 17 || snapshot.Progress.ResponseCount != 14 || snapshot.Progress.ErrorCount != 3 {
		t.Fatalf("unexpected progress: %+v", snapshot.Progress)
	}
	if snapshot.Progress.QueuePos != 0 || snapshot.Progress.QueueTotal != 1 {
		t.Fatalf("unexpected queue progress: %+v", snapshot.Progress)
	}

	j.Resume()
}

func TestJobSnapshotIsRaceSafeWithLifecycleChanges(t *testing.T) {
	j := NewJob(&ffuf.Config{Noninteractive: true})
	j.Output = NewNullOutput()
	j.SetExternallyManaged(true)
	j.setRunning(true)
	j.setRunningJob(true)

	var wg sync.WaitGroup
	for worker := 0; worker < 4; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for iteration := 0; iteration < 1_000; iteration++ {
				j.Pause()
				_ = j.Snapshot()
				j.Resume()
			}
		}()
	}
	for worker := 0; worker < 4; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for iteration := 0; iteration < 1_000; iteration++ {
				j.setError("transient")
				j.incCounter()
				_ = j.Snapshot()
			}
		}()
	}
	wg.Wait()
	j.Resume()
}

func TestJobStopIsIdempotentUnderConcurrency(t *testing.T) {
	var cancelCalls int32
	runner := &countingCloseRunner{}
	j := NewJob(&ffuf.Config{
		Context: context.Background(),
		Cancel:  func() { atomic.AddInt32(&cancelCalls, 1) },
	})
	j.Output = NewNullOutput()
	j.Runner = runner
	j.setRunning(true)

	var wg sync.WaitGroup
	for caller := 0; caller < 32; caller++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			j.Stop()
		}()
	}
	wg.Wait()

	if got := atomic.LoadInt32(&cancelCalls); got != 1 {
		t.Fatalf("Cancel called %d times, want 1", got)
	}
	if got := atomic.LoadInt32(&runner.closeCalls); got != 1 {
		t.Fatalf("Runner.Close called %d times, want 1", got)
	}
	if j.Snapshot().Running {
		t.Fatal("job still reports running after Stop")
	}
}

func TestExternallyManagedJobLeavesProcessSignalsToHost(t *testing.T) {
	hostSignals := make(chan os.Signal, 1)
	signal.Notify(hostSignals, os.Interrupt)
	defer signal.Stop(hostSignals)

	j := NewJob(&ffuf.Config{Context: context.Background()})
	j.Output = NewNullOutput()
	j.SetExternallyManaged(true)
	j.setRunning(true)
	stopMonitoring := j.interruptMonitor()
	defer stopMonitoring()

	process, err := os.FindProcess(os.Getpid())
	if err != nil {
		t.Fatalf("find process: %v", err)
	}
	if err := process.Signal(os.Interrupt); err != nil {
		t.Fatalf("signal process: %v", err)
	}
	select {
	case <-hostSignals:
	case <-time.After(time.Second):
		t.Fatal("host did not receive interrupt")
	}

	// Signal delivery is asynchronous. Give an incorrectly installed Job
	// handler time to observe the same interrupt and stop the job.
	time.Sleep(25 * time.Millisecond)
	if !j.Snapshot().Running {
		t.Fatal("externally managed job consumed a process signal")
	}
}

func TestUnmanagedNoninteractiveJobRetainsPauseCheckpointFastPath(t *testing.T) {
	j := NewJob(&ffuf.Config{Noninteractive: true})
	j.Output = NewNullOutput()
	j.setRunning(true)
	j.Pause()

	checkpointPassed := make(chan struct{})
	go func() {
		j.pauseCheckpoint()
		close(checkpointPassed)
	}()
	select {
	case <-checkpointPassed:
	case <-time.After(time.Second):
		t.Fatal("unmanaged noninteractive checkpoint unexpectedly blocked")
	}
	j.Resume()
}

func TestStoppedJobCannotBeRestarted(t *testing.T) {
	var cancelCalls int32
	j := NewJob(&ffuf.Config{
		Context: context.Background(),
		Cancel:  func() { atomic.AddInt32(&cancelCalls, 1) },
	})
	j.Stop()
	j.Start()

	if j.Snapshot().Running {
		t.Fatal("Start resurrected a stopped job")
	}
	if got := atomic.LoadInt32(&cancelCalls); got != 1 {
		t.Fatalf("Cancel called %d times, want 1", got)
	}
}
