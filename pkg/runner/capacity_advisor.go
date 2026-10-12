package runner

import (
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/assetnote/h3fuzz/pkg/ffuf"
)

var (
	errCapacitySubscriberActive = errors.New("capacity advisor already has an active subscriber")
	capacityEpochSequence       atomic.Uint64
)

// capacityAdvisor owns the one bounded subscription associated with an HTTP/3
// pool. Keeping this state outside the engine lets future transport-generation
// recovery publish through the same ordered tuple protocol without importing a
// concrete runner into the engine.
type capacityAdvisor struct {
	mu sync.Mutex

	desired               int
	timeout               time.Duration
	tuneInterval          time.Duration
	subscriber            *capacitySubscription
	pending               *ffuf.CapacityProposal
	lastPublished         time.Time
	lastGeneration        uint64
	lastProposalEpoch     uint64
	lastProposedWorkers   int
	timedOutEpochs        map[uint64]struct{}
	suspended             bool
	staleAcknowledgements uint64
	lateAcknowledgements  uint64
	timedOutProposals     uint64
	republishedProposals  uint64
	supersededProposals   uint64
	missingListener       uint64
	lastAcknowledgement   ffuf.CapacityAck
	closed                bool
	latestAcknowledgement atomic.Pointer[ffuf.CapacityAck]
	acknowledgementReady  chan struct{}
	outcomes              chan capacityAdvisorOutcome
}

func newCapacityAdvisor(initial int, timeout, tuneInterval time.Duration) *capacityAdvisor {
	if initial < 1 {
		initial = 1
	}
	if timeout <= 0 {
		timeout = 2 * time.Second
	}
	if tuneInterval <= 0 {
		tuneInterval = time.Second
	}
	return &capacityAdvisor{
		desired:              initial,
		timeout:              timeout,
		tuneInterval:         tuneInterval,
		acknowledgementReady: make(chan struct{}, 1),
		outcomes:             make(chan capacityAdvisorOutcome, 1),
		timedOutEpochs:       make(map[uint64]struct{}),
	}
}

func (a *capacityAdvisor) subscribe(generation uint64) (ffuf.CapacitySubscription, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return nil, errors.New("capacity advisor is closed")
	}
	if a.subscriber != nil {
		return nil, errCapacitySubscriberActive
	}
	subscription := &capacitySubscription{
		advisor:    a,
		generation: generation,
		proposals:  make(chan ffuf.CapacityProposal, 1),
	}
	a.subscriber = subscription
	a.lastGeneration = generation
	clear(a.timedOutEpochs)
	a.suspended = false
	a.publishLockedAt(a.desired, ffuf.CapacityCauseSubscriberReady, time.Now())
	return subscription, nil
}

func (a *capacityAdvisor) propose(desired int, cause ffuf.CapacityCause) {
	a.proposeAt(desired, cause, time.Now())
}

func (a *capacityAdvisor) setDesired(desired int) {
	if desired < 1 {
		desired = 1
	}
	a.mu.Lock()
	if !a.closed {
		a.desired = desired
	}
	a.mu.Unlock()
}

func (a *capacityAdvisor) proposeAt(desired int, cause ffuf.CapacityCause, publishedAt time.Time) {
	if desired < 1 {
		desired = 1
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return
	}
	a.desired = desired
	if a.subscriber == nil {
		a.missingListener++
		a.suspended = true
		a.emitOutcomeLocked(capacityAdvisorOutcome{Status: capacityOutcomeMissingListener})
		return
	}
	a.publishLockedAt(desired, cause, publishedAt)
}

func (a *capacityAdvisor) publishLockedAt(desired int, cause ffuf.CapacityCause, publishedAt time.Time) {
	if a.subscriber == nil {
		return
	}
	if a.pending != nil {
		a.supersededProposals++
	}
	proposal := ffuf.CapacityProposal{
		ExecutionGeneration: a.subscriber.generation,
		Epoch:               capacityEpochSequence.Add(1),
		DesiredWorkers:      desired,
		Cause:               cause,
		PublishedAt:         publishedAt,
	}
	a.pending = &proposal
	a.lastPublished = publishedAt
	a.lastProposalEpoch = proposal.Epoch
	a.lastProposedWorkers = proposal.DesiredWorkers
	a.subscriber.publish(proposal)
}

func (a *capacityAdvisor) unsubscribe(subscription *capacitySubscription) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.subscriber != subscription {
		return
	}
	if a.pending != nil {
		a.emitOutcomeLocked(capacityAdvisorOutcome{Proposal: *a.pending, Status: capacityOutcomeUnsubscribed})
		a.pending = nil
	}
	a.suspended = true
	a.subscriber = nil
	subscription.closeProposals()
}

func (a *capacityAdvisor) close() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return
	}
	a.closed = true
	if a.pending != nil {
		a.emitOutcomeLocked(capacityAdvisorOutcome{Proposal: *a.pending, Status: capacityOutcomeCancelled})
	}
	if a.subscriber != nil {
		a.subscriber.closeProposals()
		a.subscriber = nil
	}
	a.pending = nil
	a.suspended = true
}

// poll applies at most the newest acknowledgement and advances timeout /
// recovery state. The pool tuner calls it from its select loop; neither the
// supervisor nor a request goroutine waits for it.
func (a *capacityAdvisor) poll(now time.Time) {
	// Drain the wake-up independently of the atomic slot. An acknowledgement can
	// race between these two operations; in either order the newest tuple remains
	// observable without leaving a permanently-readable notification behind.
	select {
	case <-a.acknowledgementReady:
	default:
	}
	acknowledgement := a.latestAcknowledgement.Swap(nil)

	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return
	}
	if acknowledgement != nil {
		a.applyAcknowledgementLocked(*acknowledgement)
	}
	timedOutNow := false
	if a.pending != nil && !now.Before(a.pending.PublishedAt.Add(a.timeout)) {
		timedOut := *a.pending
		a.pending = nil
		a.timedOutEpochs[timedOut.Epoch] = struct{}{}
		a.timedOutProposals++
		a.suspended = true
		timedOutNow = true
		a.emitOutcomeLocked(capacityAdvisorOutcome{Proposal: timedOut, Status: capacityOutcomeTimedOut})
	}
	if !timedOutNow && a.suspended && a.pending == nil && a.subscriber != nil && !now.Before(a.lastPublished.Add(a.tuneInterval)) {
		a.republishedProposals++
		a.publishLockedAt(a.desired, ffuf.CapacityCauseRepublish, now)
	}
}

func (a *capacityAdvisor) applyAcknowledgementLocked(acknowledgement ffuf.CapacityAck) {
	if a.subscriber == nil || acknowledgement.ExecutionGeneration != a.subscriber.generation {
		a.staleAcknowledgements++
		return
	}
	if _, timedOut := a.timedOutEpochs[acknowledgement.Epoch]; timedOut {
		a.lateAcknowledgements++
		return
	}
	if a.pending == nil || acknowledgement.Epoch != a.pending.Epoch || acknowledgement.ProposedWorkers != a.pending.DesiredWorkers {
		a.staleAcknowledgements++
		return
	}
	proposal := *a.pending
	a.pending = nil
	a.suspended = false
	a.lastAcknowledgement = acknowledgement
	a.emitOutcomeLocked(capacityAdvisorOutcome{Proposal: proposal, Acknowledgement: acknowledgement, Status: capacityOutcomeApplied})
}

func (a *capacityAdvisor) acknowledge(acknowledgement ffuf.CapacityAck) {
	copy := acknowledgement
	a.latestAcknowledgement.Store(&copy)
	select {
	case a.acknowledgementReady <- struct{}{}:
	default:
	}
}

func (a *capacityAdvisor) notifications() <-chan struct{} { return a.acknowledgementReady }

func (a *capacityAdvisor) results() <-chan capacityAdvisorOutcome { return a.outcomes }

func (a *capacityAdvisor) emitOutcomeLocked(outcome capacityAdvisorOutcome) {
	select {
	case a.outcomes <- outcome:
		return
	default:
	}
	select {
	case <-a.outcomes:
	default:
	}
	select {
	case a.outcomes <- outcome:
	default:
	}
}

func (a *capacityAdvisor) snapshot() capacityAdvisorSnapshot {
	a.mu.Lock()
	defer a.mu.Unlock()
	snapshot := capacityAdvisorSnapshot{
		ExecutionGeneration:              a.lastGeneration,
		ProposalEpoch:                    a.lastProposalEpoch,
		DesiredWorkers:                   a.desired,
		Suspended:                        a.suspended,
		StaleAcknowledgements:            a.staleAcknowledgements,
		LateAfterTimeoutAcknowledgements: a.lateAcknowledgements,
		TimedOutProposals:                a.timedOutProposals,
		RepublishedProposals:             a.republishedProposals,
		SupersededProposals:              a.supersededProposals,
		MissingListenerProposals:         a.missingListener,
		AcknowledgementGeneration:        a.lastAcknowledgement.ExecutionGeneration,
		AcknowledgementEpoch:             a.lastAcknowledgement.Epoch,
		AcknowledgedLiveWorkers:          a.lastAcknowledgement.ActualLiveWorkers,
		ProposedWorkers:                  a.lastProposedWorkers,
		Pending:                          a.pending != nil,
	}
	return snapshot
}

type capacityOutcomeStatus string

const (
	capacityOutcomeApplied         capacityOutcomeStatus = "applied"
	capacityOutcomeTimedOut        capacityOutcomeStatus = "timed_out"
	capacityOutcomeMissingListener capacityOutcomeStatus = "missing_listener"
	capacityOutcomeUnsubscribed    capacityOutcomeStatus = "unsubscribed"
	capacityOutcomeCancelled       capacityOutcomeStatus = "cancelled"
)

type capacityAdvisorOutcome struct {
	Proposal        ffuf.CapacityProposal
	Acknowledgement ffuf.CapacityAck
	Status          capacityOutcomeStatus
}

type capacityAdvisorSnapshot struct {
	ExecutionGeneration              uint64
	ProposalEpoch                    uint64
	AcknowledgementGeneration        uint64
	AcknowledgementEpoch             uint64
	StaleAcknowledgements            uint64
	LateAfterTimeoutAcknowledgements uint64
	RepublishedProposals             uint64
	TimedOutProposals                uint64
	SupersededProposals              uint64
	MissingListenerProposals         uint64
	ProposedWorkers                  int
	AcknowledgedLiveWorkers          int
	DesiredWorkers                   int
	Suspended                        bool
	Pending                          bool
}

type capacitySubscription struct {
	advisor    *capacityAdvisor
	generation uint64
	proposals  chan ffuf.CapacityProposal
	closeOnce  sync.Once
}

func (s *capacitySubscription) Proposals() <-chan ffuf.CapacityProposal { return s.proposals }

func (s *capacitySubscription) Acknowledge(acknowledgement ffuf.CapacityAck) {
	s.advisor.acknowledge(acknowledgement)
}

func (s *capacitySubscription) Close() {
	s.advisor.unsubscribe(s)
}

func (s *capacitySubscription) publish(proposal ffuf.CapacityProposal) {
	select {
	case s.proposals <- proposal:
		return
	default:
	}
	select {
	case <-s.proposals:
	default:
	}
	select {
	case s.proposals <- proposal:
	default:
	}
}

func (s *capacitySubscription) closeProposals() {
	s.closeOnce.Do(func() { close(s.proposals) })
}
