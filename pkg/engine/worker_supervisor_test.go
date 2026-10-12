package engine

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/assetnote/h3fuzz/pkg/ffuf"
)

func TestWorkerSupervisorReachesFiveThousandAndFullyDrains(t *testing.T) {
	tasks := make(chan executionTask)
	subscription := newFakeCapacitySubscription()
	supervisor := newWorkerSupervisor(workerSupervisorConfig{
		Generation:     1,
		InitialWorkers: 10,
		Remaining:      6_000,
		Tasks:          tasks,
		Subscription:   subscription,
		Run:            func(executionTask) {},
	})
	supervisor.start()

	subscription.proposals <- ffuf.CapacityProposal{
		ExecutionGeneration: 1,
		Epoch:               1,
		DesiredWorkers:      5_000,
		Cause:               ffuf.CapacityCausePoolGrowth,
		PublishedAt:         time.Now(),
	}
	ack := receiveCapacityAck(t, subscription.acknowledgements)
	if ack.ExecutionGeneration != 1 || ack.Epoch != 1 || ack.ActualLiveWorkers != 5_000 || ack.RemainingInputCap != 6_000 {
		t.Fatalf("capacity acknowledgement = %+v", ack)
	}
	if snapshot := supervisor.snapshot(); snapshot.LiveWorkers != 5_000 || snapshot.PeakWorkers != 5_000 {
		t.Fatalf("live supervisor snapshot = %+v", snapshot)
	}

	supervisor.beginDrain()
	close(tasks)
	supervisor.wait()
	if snapshot := supervisor.snapshot(); snapshot.LiveWorkers != 0 || snapshot.RetiringWorkers != 0 || snapshot.State != workerSupervisorStopped {
		t.Fatalf("drained supervisor snapshot = %+v", snapshot)
	}
}

func TestWorkerSupervisorShrinkWaitsBetweenTasksWithoutLossOrDuplication(t *testing.T) {
	const tasksCount = 200
	tasks := make(chan executionTask, tasksCount)
	subscription := newFakeCapacitySubscription()
	release := make(chan struct{})
	seen := make([]atomic.Int32, tasksCount)
	var started atomic.Int64
	supervisor := newWorkerSupervisor(workerSupervisorConfig{
		Generation:     2,
		InitialWorkers: 10,
		Remaining:      1_000,
		Tasks:          tasks,
		Subscription:   subscription,
		Run: func(task executionTask) {
			started.Add(1)
			<-release
			seen[task.position].Add(1)
		},
	})
	supervisor.start()
	subscription.proposals <- ffuf.CapacityProposal{ExecutionGeneration: 2, Epoch: 1, DesiredWorkers: tasksCount, PublishedAt: time.Now()}
	if ack := receiveCapacityAck(t, subscription.acknowledgements); ack.ActualLiveWorkers != tasksCount {
		t.Fatalf("growth acknowledgement = %+v", ack)
	}
	for i := 0; i < tasksCount; i++ {
		tasks <- executionTask{position: i}
	}
	waitForEngineCondition(t, 5*time.Second, "every worker to enter one task", func() bool {
		return started.Load() == tasksCount
	})

	subscription.proposals <- ffuf.CapacityProposal{ExecutionGeneration: 2, Epoch: 2, DesiredWorkers: 20, PublishedAt: time.Now()}
	select {
	case ack := <-subscription.acknowledgements:
		t.Fatalf("shrink acknowledged before in-flight tasks completed: %+v", ack)
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	if ack := receiveCapacityAck(t, subscription.acknowledgements); ack.ActualLiveWorkers != 20 {
		t.Fatalf("shrink acknowledgement = %+v", ack)
	}

	supervisor.beginDrain()
	close(tasks)
	supervisor.wait()
	for position := range seen {
		if got := seen[position].Load(); got != 1 {
			t.Fatalf("task %d executions = %d, want 1", position, got)
		}
	}
}

func TestWorkerSupervisorRegrowthWhileWorkersRetireNeverExceedsMaximum(t *testing.T) {
	const workers = 200
	tasks := make(chan executionTask, workers)
	subscription := newFakeCapacitySubscription()
	release := make(chan struct{})
	var started atomic.Int64
	supervisor := newWorkerSupervisor(workerSupervisorConfig{
		Generation:     3,
		InitialWorkers: 10,
		Remaining:      1_000,
		MaxWorkers:     workers,
		Tasks:          tasks,
		Subscription:   subscription,
		Run: func(executionTask) {
			started.Add(1)
			<-release
		},
	})
	supervisor.start()
	subscription.proposals <- ffuf.CapacityProposal{ExecutionGeneration: 3, Epoch: 1, DesiredWorkers: workers, PublishedAt: time.Now()}
	if ack := receiveCapacityAck(t, subscription.acknowledgements); ack.ActualLiveWorkers != workers {
		t.Fatalf("growth acknowledgement = %+v", ack)
	}
	for i := 0; i < workers; i++ {
		tasks <- executionTask{position: i}
	}
	waitForEngineCondition(t, 5*time.Second, "every worker to enter one task", func() bool {
		return started.Load() == workers
	})

	subscription.proposals <- ffuf.CapacityProposal{ExecutionGeneration: 3, Epoch: 2, DesiredWorkers: 20, PublishedAt: time.Now()}
	waitForEngineCondition(t, time.Second, "shrink retirement selection", func() bool {
		return supervisor.snapshot().RetiringWorkers == workers-20
	})
	subscription.proposals <- ffuf.CapacityProposal{ExecutionGeneration: 3, Epoch: 3, DesiredWorkers: workers, PublishedAt: time.Now()}
	waitForEngineCondition(t, time.Second, "regrowth proposal", func() bool {
		return supervisor.snapshot().ProposalEpoch == 3
	})
	if snapshot := supervisor.snapshot(); snapshot.LiveWorkers > workers || snapshot.PeakWorkers > workers {
		t.Fatalf("regrowth exceeded maximum while prior workers retired: %+v", snapshot)
	}

	close(release)
	ack := receiveCapacityAck(t, subscription.acknowledgements)
	if ack.Epoch != 3 || ack.ActualLiveWorkers != workers {
		t.Fatalf("regrowth acknowledgement = %+v", ack)
	}
	if snapshot := supervisor.snapshot(); snapshot.LiveWorkers != workers || snapshot.RetiringWorkers != 0 || snapshot.PeakWorkers > workers {
		t.Fatalf("regrown supervisor snapshot = %+v", snapshot)
	}

	supervisor.beginDrain()
	close(tasks)
	supervisor.wait()
}

func TestWorkerSupervisorCapsGrowthByRemainingInputAndRejectsStaleTuples(t *testing.T) {
	tasks := make(chan executionTask)
	subscription := newFakeCapacitySubscription()
	supervisor := newWorkerSupervisor(workerSupervisorConfig{
		Generation:     8,
		InitialWorkers: 10,
		Remaining:      3,
		MaxWorkers:     5_000,
		Tasks:          tasks,
		Subscription:   subscription,
	})
	supervisor.start()
	if snapshot := supervisor.snapshot(); snapshot.LiveWorkers != 3 {
		t.Fatalf("initial workers = %d, want remaining-input cap 3", snapshot.LiveWorkers)
	}

	subscription.proposals <- ffuf.CapacityProposal{ExecutionGeneration: 7, Epoch: 100, DesiredWorkers: 100}
	waitForEngineCondition(t, time.Second, "old-generation rejection", func() bool {
		return supervisor.snapshot().StaleProposals == 1
	})
	subscription.proposals <- ffuf.CapacityProposal{ExecutionGeneration: 8, Epoch: 101, DesiredWorkers: 5_000}
	if ack := receiveCapacityAck(t, subscription.acknowledgements); ack.Epoch != 101 || ack.ActualLiveWorkers != 3 || ack.RemainingInputCap != 3 {
		t.Fatalf("remaining-capped acknowledgement = %+v", ack)
	}
	subscription.proposals <- ffuf.CapacityProposal{ExecutionGeneration: 8, Epoch: 100, DesiredWorkers: 1}
	waitForEngineCondition(t, time.Second, "old-epoch rejection", func() bool {
		return supervisor.snapshot().StaleProposals == 2
	})
	if snapshot := supervisor.snapshot(); snapshot.DesiredWorkers != 3 || snapshot.LiveWorkers != 3 {
		t.Fatalf("stale tuple changed capacity: %+v", snapshot)
	}

	supervisor.beginDrain()
	close(tasks)
	supervisor.wait()
	if snapshot := supervisor.snapshot(); snapshot.LiveWorkers != 0 || snapshot.RetiringWorkers != 0 {
		t.Fatalf("input-exhausted supervisor snapshot = %+v", snapshot)
	}
}

func TestExecutionWorkerBoundsPreserveExplicitThreadSemantics(t *testing.T) {
	tests := []struct {
		name        string
		config      *ffuf.Config
		wantInitial int
		wantMaximum int
	}{
		{name: "nil config", wantInitial: 1, wantMaximum: maxAdaptiveRequestWorkers},
		{name: "automatic default", config: &ffuf.Config{Threads: 40}, wantInitial: 40, wantMaximum: maxAdaptiveRequestWorkers},
		{name: "automatic configured ceiling", config: &ffuf.Config{Threads: 5_000, Http3MaxWorkers: 4_000}, wantInitial: 4_000, wantMaximum: 4_000},
		{name: "explicit default t40", config: &ffuf.Config{Threads: 40, ThreadsExplicit: true, Http3MaxWorkers: 1}, wantInitial: 40, wantMaximum: 40},
		{name: "explicit t1000", config: &ffuf.Config{Threads: 1_000, ThreadsExplicit: true, Http3MaxWorkers: 10}, wantInitial: 1_000, wantMaximum: 1_000},
		{name: "legacy explicit above adaptive safety cap", config: &ffuf.Config{Threads: 100_000, ThreadsExplicit: true}, wantInitial: 100_000, wantMaximum: 100_000},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			initial, maximum := executionWorkerBounds(test.config)
			if initial != test.wantInitial || maximum != test.wantMaximum {
				t.Fatalf("worker bounds = (%d, %d), want (%d, %d)", initial, maximum, test.wantInitial, test.wantMaximum)
			}
		})
	}
}

func TestExecutionCapacitySubscriptionIsAdaptiveOnly(t *testing.T) {
	advisor := &recordingCapacityAdvisor{subscription: newFakeCapacitySubscription()}
	for _, explicitThreads := range []int{40, 1_000} {
		if subscription := capacitySubscriptionForExecution(advisor, true, uint64(explicitThreads)); subscription != nil {
			t.Fatalf("explicit -t %d unexpectedly subscribed", explicitThreads)
		}
	}
	if len(advisor.generations) != 0 {
		t.Fatalf("explicit -t subscription generations = %v", advisor.generations)
	}
	subscription := capacitySubscriptionForExecution(advisor, false, 17)
	if subscription != advisor.subscription || len(advisor.generations) != 1 || advisor.generations[0] != 17 {
		t.Fatalf("adaptive subscription = %v, generations %v", subscription, advisor.generations)
	}
}

func TestWorkerSupervisorStopWhilePausedDrainsWithoutStrandingOrDuplication(t *testing.T) {
	const taskCount = 20
	tasks := make(chan executionTask, taskCount)
	releasePause := make(chan struct{})
	seen := make([]atomic.Int32, taskCount)
	var started atomic.Int64
	supervisor := newWorkerSupervisor(workerSupervisorConfig{
		Generation:     23,
		InitialWorkers: taskCount,
		Remaining:      taskCount,
		MaxWorkers:     taskCount,
		Tasks:          tasks,
		Run: func(task executionTask) {
			started.Add(1)
			<-releasePause
			seen[task.position].Add(1)
		},
	})
	supervisor.start()
	for position := range taskCount {
		tasks <- executionTask{position: position}
	}
	waitForEngineCondition(t, time.Second, "paused workers to enter tasks", func() bool {
		return started.Load() == taskCount
	})
	supervisor.beginDrain()
	close(tasks)
	drained := make(chan struct{})
	go func() {
		supervisor.wait()
		close(drained)
	}()
	select {
	case <-drained:
		t.Fatal("paused in-flight work was abandoned during stop")
	case <-time.After(20 * time.Millisecond):
	}
	close(releasePause)
	select {
	case <-drained:
	case <-time.After(5 * time.Second):
		t.Fatal("workers remained stranded after pause was released")
	}
	for position := range seen {
		if got := seen[position].Load(); got != 1 {
			t.Fatalf("task %d executions = %d, want 1", position, got)
		}
	}
	if snapshot := supervisor.snapshot(); snapshot.LiveWorkers != 0 || snapshot.RetiringWorkers != 0 || snapshot.State != workerSupervisorStopped {
		t.Fatalf("stopped paused supervisor snapshot = %+v", snapshot)
	}
}

func TestWorkerSupervisorRejectsGrowthAfterDrainBegins(t *testing.T) {
	tasks := make(chan executionTask)
	supervisor := newWorkerSupervisor(workerSupervisorConfig{
		Generation:     24,
		InitialWorkers: 10,
		Remaining:      5_000,
		MaxWorkers:     5_000,
		Tasks:          tasks,
	})
	supervisor.start()
	supervisor.beginDrain()
	supervisor.applyProposal(ffuf.CapacityProposal{
		ExecutionGeneration: 24,
		Epoch:               1,
		DesiredWorkers:      5_000,
	})
	if snapshot := supervisor.snapshot(); snapshot.PeakWorkers != 10 || snapshot.State != workerSupervisorDraining {
		t.Fatalf("post-drain growth changed supervisor: %+v", snapshot)
	}
	close(tasks)
	supervisor.wait()
}

type fakeCapacitySubscription struct {
	proposals        chan ffuf.CapacityProposal
	acknowledgements chan ffuf.CapacityAck
	closeOnce        sync.Once
}

type recordingCapacityAdvisor struct {
	subscription ffuf.CapacitySubscription
	generations  []uint64
}

func (a *recordingCapacityAdvisor) SubscribeCapacity(generation uint64) (ffuf.CapacitySubscription, error) {
	a.generations = append(a.generations, generation)
	return a.subscription, nil
}

func newFakeCapacitySubscription() *fakeCapacitySubscription {
	return &fakeCapacitySubscription{
		proposals:        make(chan ffuf.CapacityProposal, 1),
		acknowledgements: make(chan ffuf.CapacityAck, 16),
	}
}

func (s *fakeCapacitySubscription) Proposals() <-chan ffuf.CapacityProposal { return s.proposals }
func (s *fakeCapacitySubscription) Acknowledge(ack ffuf.CapacityAck) {
	select {
	case s.acknowledgements <- ack:
	default:
	}
}
func (s *fakeCapacitySubscription) Close() {
	s.closeOnce.Do(func() { close(s.proposals) })
}

func receiveCapacityAck(t *testing.T, acknowledgements <-chan ffuf.CapacityAck) ffuf.CapacityAck {
	t.Helper()
	select {
	case acknowledgement := <-acknowledgements:
		return acknowledgement
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for capacity acknowledgement")
		return ffuf.CapacityAck{}
	}
}

func waitForEngineCondition(t *testing.T, timeout time.Duration, description string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", description)
		}
		time.Sleep(time.Millisecond)
	}
}
