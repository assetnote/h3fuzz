package engine

import (
	"sync"
	"sync/atomic"
	"time"

	"github.com/assetnote/h3fuzz/pkg/ffuf"
)

const maxAdaptiveRequestWorkers = 65_536

type executionTask struct {
	input     map[string][]byte
	position  int
	logicalID uint64
}

type workerSupervisorState string

const (
	workerSupervisorCreated  workerSupervisorState = "created"
	workerSupervisorRunning  workerSupervisorState = "running"
	workerSupervisorDraining workerSupervisorState = "draining"
	workerSupervisorStopped  workerSupervisorState = "stopped"
)

type workerSupervisorConfig struct {
	Generation     uint64
	InitialWorkers int
	Remaining      int
	MaxWorkers     int
	Tasks          <-chan executionTask
	Subscription   ffuf.CapacitySubscription
	Run            func(executionTask)
}

type supervisedWorker struct {
	id       uint64
	retire   chan struct{}
	retiring atomic.Bool
}

// workerSupervisor is the sole owner of worker creation and retirement. Only
// its loop calls workers.Add, and wait first joins that loop, so Add can never
// race Wait during stop, recursion transitions, or input exhaustion.
type workerSupervisor struct {
	config workerSupervisorConfig

	startOnce sync.Once
	drainOnce sync.Once
	workers   sync.WaitGroup
	loop      sync.WaitGroup
	stop      chan struct{}
	exits     chan uint64
	completed chan struct{}

	workersByID map[uint64]*supervisedWorker
	nextWorker  uint64
	pending     *ffuf.CapacityProposal
	lastEpoch   uint64

	live             atomic.Int64
	desired          atomic.Int64
	retiring         atomic.Int64
	peak             atomic.Int64
	remaining        atomic.Int64
	state            atomic.Int32
	staleProposals   atomic.Uint64
	proposalEpoch    atomic.Uint64
	ackGeneration    atomic.Uint64
	ackEpoch         atomic.Uint64
	proposedWorkers  atomic.Int64
	acknowledgedLive atomic.Int64
}

func newWorkerSupervisor(config workerSupervisorConfig) *workerSupervisor {
	if config.InitialWorkers < 1 && config.Remaining > 0 {
		config.InitialWorkers = 1
	}
	if config.MaxWorkers <= 0 {
		config.MaxWorkers = maxAdaptiveRequestWorkers
	}
	if config.Run == nil {
		config.Run = func(executionTask) {}
	}
	s := &workerSupervisor{
		config:      config,
		stop:        make(chan struct{}),
		exits:       make(chan uint64, config.MaxWorkers),
		completed:   make(chan struct{}, 1),
		workersByID: make(map[uint64]*supervisedWorker),
	}
	s.remaining.Store(int64(maxIntEngine(0, config.Remaining)))
	s.state.Store(int32(workerStateCreated))
	return s
}

func executionWorkerBounds(config *ffuf.Config) (initial, maximum int) {
	initial = 1
	maximum = maxAdaptiveRequestWorkers
	if config == nil {
		return initial, maximum
	}
	if config.Threads > 0 {
		initial = config.Threads
	}
	if config.ThreadsExplicit {
		// Preserve the legacy meaning exactly: explicit -t is fixed and is its
		// own hard cap, even when it is equal to the default value.
		return initial, initial
	}
	if config.Http3MaxWorkers > 0 && config.Http3MaxWorkers < maximum {
		maximum = config.Http3MaxWorkers
	}
	if initial > maximum {
		initial = maximum
	}
	return initial, maximum
}

func capacitySubscriptionForExecution(candidate any, threadsExplicit bool, generation uint64) ffuf.CapacitySubscription {
	if threadsExplicit {
		return nil
	}
	advisor, ok := candidate.(ffuf.CapacityAdvisor)
	if !ok {
		return nil
	}
	subscription, err := advisor.SubscribeCapacity(generation)
	if err != nil {
		return nil
	}
	return subscription
}

const (
	workerStateCreated int32 = iota
	workerStateRunning
	workerStateDraining
	workerStateStopped
)

func (s *workerSupervisor) start() {
	s.startOnce.Do(func() {
		s.state.Store(workerStateRunning)
		initial := s.boundTarget(s.config.InitialWorkers)
		s.desired.Store(int64(initial))
		s.resize(initial)
		s.loop.Add(1)
		go s.run()
	})
}

func (s *workerSupervisor) run() {
	defer s.loop.Done()
	var proposals <-chan ffuf.CapacityProposal
	if s.config.Subscription != nil {
		proposals = s.config.Subscription.Proposals()
	}
	for {
		select {
		case <-s.stop:
			s.state.Store(workerStateDraining)
			return
		case proposal, ok := <-proposals:
			if !ok {
				proposals = nil
				continue
			}
			s.applyProposal(proposal)
		case id := <-s.exits:
			delete(s.workersByID, id)
			// A growth proposal can arrive while workers selected by an earlier
			// shrink are still retiring. Never replace those workers until they
			// actually exit: this keeps the live population within MaxWorkers.
			s.resize(s.boundTarget(int(s.desired.Load())))
			s.maybeAcknowledge()
		case <-s.completed:
			s.resize(s.boundTarget(int(s.desired.Load())))
			s.maybeAcknowledge()
		}
	}
}

func (s *workerSupervisor) applyProposal(proposal ffuf.CapacityProposal) {
	if s.state.Load() != workerStateRunning {
		return
	}
	if proposal.ExecutionGeneration != s.config.Generation || proposal.Epoch <= s.lastEpoch {
		s.staleProposals.Add(1)
		return
	}
	s.lastEpoch = proposal.Epoch
	s.proposalEpoch.Store(proposal.Epoch)
	s.proposedWorkers.Store(int64(proposal.DesiredWorkers))
	s.pending = &proposal
	target := s.boundTarget(proposal.DesiredWorkers)
	s.desired.Store(int64(target))
	s.resize(target)
	s.maybeAcknowledge()
}

func (s *workerSupervisor) boundTarget(desired int) int {
	if desired < 1 {
		desired = 1
	}
	if desired > s.config.MaxWorkers {
		desired = s.config.MaxWorkers
	}
	remaining := int(s.remaining.Load())
	if remaining <= 0 {
		return 0
	}
	if desired > remaining {
		desired = remaining
	}
	return desired
}

func (s *workerSupervisor) resize(target int) {
	if s.state.Load() != workerStateRunning {
		return
	}
	active := int(s.live.Load() - s.retiring.Load())
	if target > active {
		toSpawn := target - active
		available := s.config.MaxWorkers - int(s.live.Load())
		if toSpawn > available {
			toSpawn = available
		}
		for i := 0; i < toSpawn; i++ {
			s.spawnWorker()
		}
		return
	}
	toRetire := active - target
	if toRetire <= 0 {
		return
	}
	for _, worker := range s.workersByID {
		if toRetire == 0 {
			break
		}
		if !worker.retiring.CompareAndSwap(false, true) {
			continue
		}
		s.retiring.Add(1)
		worker.retire <- struct{}{}
		toRetire--
	}
}

func (s *workerSupervisor) spawnWorker() {
	s.nextWorker++
	worker := &supervisedWorker{id: s.nextWorker, retire: make(chan struct{}, 1)}
	s.workersByID[worker.id] = worker
	live := s.live.Add(1)
	recordAtomicMaxEngine(&s.peak, live)
	s.workers.Add(1)
	go s.runWorker(worker)
}

func (s *workerSupervisor) runWorker(worker *supervisedWorker) {
	defer func() {
		if worker.retiring.Load() {
			s.retiring.Add(-1)
		}
		s.live.Add(-1)
		select {
		case s.exits <- worker.id:
		case <-s.stop:
		}
		s.workers.Done()
	}()
	for {
		// Prefer retirement before taking another task. A racing task selection is
		// still safe: the worker observes retirement at the next between-task edge.
		select {
		case <-worker.retire:
			return
		default:
		}
		select {
		case <-worker.retire:
			return
		case task, ok := <-s.config.Tasks:
			if !ok {
				return
			}
			s.config.Run(task)
			s.decrementRemaining()
			select {
			case s.completed <- struct{}{}:
			default:
			}
		}
	}
}

func (s *workerSupervisor) decrementRemaining() {
	for {
		remaining := s.remaining.Load()
		if remaining <= 0 || s.remaining.CompareAndSwap(remaining, remaining-1) {
			return
		}
	}
}

func (s *workerSupervisor) maybeAcknowledge() {
	if s.state.Load() != workerStateRunning || s.pending == nil || s.config.Subscription == nil {
		return
	}
	target := s.boundTarget(s.pending.DesiredWorkers)
	if s.retiring.Load() != 0 || int(s.live.Load()) != target {
		return
	}
	proposal := *s.pending
	s.pending = nil
	acknowledgement := ffuf.CapacityAck{
		ExecutionGeneration: proposal.ExecutionGeneration,
		Epoch:               proposal.Epoch,
		ProposedWorkers:     proposal.DesiredWorkers,
		ActualLiveWorkers:   target,
		RemainingInputCap:   int(s.remaining.Load()),
		SupervisorState:     string(workerSupervisorRunning),
		AcknowledgedAt:      time.Now(),
	}
	s.ackGeneration.Store(acknowledgement.ExecutionGeneration)
	s.ackEpoch.Store(acknowledgement.Epoch)
	s.acknowledgedLive.Store(int64(acknowledgement.ActualLiveWorkers))
	s.config.Subscription.Acknowledge(acknowledgement)
}

func (s *workerSupervisor) beginDrain() {
	s.drainOnce.Do(func() {
		s.state.Store(workerStateDraining)
		if s.config.Subscription != nil {
			s.config.Subscription.Close()
		}
		close(s.stop)
	})
}

func (s *workerSupervisor) wait() {
	s.loop.Wait()
	s.workers.Wait()
	s.state.Store(workerStateStopped)
}

type workerSupervisorSnapshot struct {
	ExecutionGeneration       uint64
	ProposalEpoch             uint64
	AcknowledgementGeneration uint64
	AcknowledgementEpoch      uint64
	StaleProposals            uint64
	ProposedWorkers           int
	AcknowledgedLiveWorkers   int
	DesiredWorkers            int
	LiveWorkers               int
	RetiringWorkers           int
	PeakWorkers               int
	RemainingInputCap         int
	State                     workerSupervisorState
}

func (s *workerSupervisor) snapshot() workerSupervisorSnapshot {
	return workerSupervisorSnapshot{
		ExecutionGeneration:       s.config.Generation,
		ProposalEpoch:             s.proposalEpoch.Load(),
		AcknowledgementGeneration: s.ackGeneration.Load(),
		AcknowledgementEpoch:      s.ackEpoch.Load(),
		StaleProposals:            s.staleProposals.Load(),
		ProposedWorkers:           int(s.proposedWorkers.Load()),
		AcknowledgedLiveWorkers:   int(s.acknowledgedLive.Load()),
		DesiredWorkers:            int(s.desired.Load()),
		LiveWorkers:               int(s.live.Load()),
		RetiringWorkers:           int(s.retiring.Load()),
		PeakWorkers:               int(s.peak.Load()),
		RemainingInputCap:         int(s.remaining.Load()),
		State:                     s.supervisorState(),
	}
}

func (s *workerSupervisor) supervisorState() workerSupervisorState {
	switch s.state.Load() {
	case workerStateRunning:
		return workerSupervisorRunning
	case workerStateDraining:
		return workerSupervisorDraining
	case workerStateStopped:
		return workerSupervisorStopped
	default:
		return workerSupervisorCreated
	}
}

func recordAtomicMaxEngine(target *atomic.Int64, candidate int64) {
	for {
		current := target.Load()
		if candidate <= current || target.CompareAndSwap(current, candidate) {
			return
		}
	}
}

func maxIntEngine(a, b int) int {
	if a > b {
		return a
	}
	return b
}
