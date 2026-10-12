package engine

import (
	"runtime"
	"testing"
	"time"
)

// BenchmarkWorkerSupervisorIdle5000 measures the resident Go heap and goroutine
// population of the PERF-102 acceptance point without network traffic. Metrics
// are reported per live idle worker so changes in stack/runtime costs remain
// visible across Go releases.
func BenchmarkWorkerSupervisorIdle5000(b *testing.B) {
	const workers = 5_000
	for range b.N {
		runtime.GC()
		var before runtime.MemStats
		runtime.ReadMemStats(&before)
		goroutinesBefore := runtime.NumGoroutine()

		tasks := make(chan executionTask)
		supervisor := newWorkerSupervisor(workerSupervisorConfig{
			Generation:     1,
			InitialWorkers: workers,
			Remaining:      workers,
			MaxWorkers:     workers,
			Tasks:          tasks,
		})
		supervisor.start()
		deadline := time.Now().Add(10 * time.Second)
		for supervisor.snapshot().LiveWorkers != workers {
			if time.Now().After(deadline) {
				b.Fatal("timed out starting 5,000 idle workers")
			}
			runtime.Gosched()
		}
		runtime.GC()
		var after runtime.MemStats
		runtime.ReadMemStats(&after)
		goroutinesAfter := runtime.NumGoroutine()

		heapDelta := int64(after.HeapAlloc) - int64(before.HeapAlloc)
		stackDelta := int64(after.StackInuse) - int64(before.StackInuse)
		goroutineDelta := goroutinesAfter - goroutinesBefore
		b.ReportMetric(float64(heapDelta)/workers, "heap_bytes/worker")
		b.ReportMetric(float64(stackDelta)/workers, "stack_bytes/worker")
		b.ReportMetric(float64(heapDelta+stackDelta)/workers, "live_bytes/worker")
		b.ReportMetric(float64(goroutineDelta)/workers, "goroutines/worker")
		b.ReportMetric(float64(supervisor.snapshot().PeakWorkers), "peak_workers")

		supervisor.beginDrain()
		close(tasks)
		supervisor.wait()
		if snapshot := supervisor.snapshot(); snapshot.LiveWorkers != 0 || snapshot.RetiringWorkers != 0 {
			b.Fatalf("benchmark drain = %+v", snapshot)
		}
	}
}
