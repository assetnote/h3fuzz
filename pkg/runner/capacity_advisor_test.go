package runner

import (
	"errors"
	"testing"
	"time"

	"github.com/assetnote/h3fuzz/pkg/ffuf"
)

func TestCapacityAdvisorSubscriptionReplaysAndCoalescesLatestProposal(t *testing.T) {
	advisor := newCapacityAdvisor(10, 2*time.Second, time.Second)
	t.Cleanup(advisor.close)

	subscription, err := advisor.subscribe(7)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	t.Cleanup(subscription.Close)

	initial := receiveCapacityProposal(t, subscription.Proposals())
	if initial.ExecutionGeneration != 7 || initial.DesiredWorkers != 10 || initial.Cause != ffuf.CapacityCauseSubscriberReady {
		t.Fatalf("initial proposal = %+v", initial)
	}

	advisor.propose(20, ffuf.CapacityCausePoolGrowth)
	advisor.propose(40, ffuf.CapacityCausePoolGrowth)
	latest := receiveCapacityProposal(t, subscription.Proposals())
	if latest.ExecutionGeneration != 7 || latest.DesiredWorkers != 40 {
		t.Fatalf("coalesced proposal = %+v, want generation 7 and 40 workers", latest)
	}
	if latest.Epoch <= initial.Epoch {
		t.Fatalf("coalesced epoch = %d, want newer than %d", latest.Epoch, initial.Epoch)
	}
}

func TestCapacityAdvisorBoundsSubscriptionAndRecoversMissingListener(t *testing.T) {
	advisor := newCapacityAdvisor(10, 2*time.Second, time.Second)
	t.Cleanup(advisor.close)

	first, err := advisor.subscribe(21)
	if err != nil {
		t.Fatal(err)
	}
	initial := receiveCapacityProposal(t, first.Proposals())
	if _, err := advisor.subscribe(22); !errors.Is(err, errCapacitySubscriberActive) {
		t.Fatalf("second active subscription error = %v", err)
	}

	published := make(chan struct{})
	go func() {
		defer close(published)
		for desired := 11; desired <= 10_000; desired++ {
			advisor.propose(desired, ffuf.CapacityCausePoolGrowth)
		}
	}()
	select {
	case <-published:
	case <-time.After(2 * time.Second):
		t.Fatal("bounded proposal publication blocked without a reader")
	}
	latest := receiveCapacityProposal(t, first.Proposals())
	if latest.DesiredWorkers != 10_000 || len(first.Proposals()) != 0 {
		t.Fatalf("latest-value proposal = %+v, queued=%d", latest, len(first.Proposals()))
	}

	first.Close()
	advisor.propose(64, ffuf.CapacityCausePoolShrink)
	missing := advisor.snapshot()
	if missing.MissingListenerProposals != 1 || !missing.Suspended {
		t.Fatalf("missing-listener snapshot = %+v", missing)
	}

	second, err := advisor.subscribe(22)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(second.Close)
	replayed := receiveCapacityProposal(t, second.Proposals())
	if replayed.ExecutionGeneration != 22 || replayed.DesiredWorkers != 64 || replayed.Epoch <= latest.Epoch || replayed.Cause != ffuf.CapacityCauseSubscriberReady {
		t.Fatalf("fresh-generation replay = %+v, prior = %+v", replayed, latest)
	}

	first.Acknowledge(ffuf.CapacityAck{
		ExecutionGeneration: 21,
		Epoch:               initial.Epoch,
		ProposedWorkers:     initial.DesiredWorkers,
		ActualLiveWorkers:   initial.DesiredWorkers,
	})
	advisor.poll(time.Now())
	if snapshot := advisor.snapshot(); snapshot.StaleAcknowledgements != 1 || !snapshot.Pending {
		t.Fatalf("old-generation acknowledgement snapshot = %+v", snapshot)
	}
	second.Acknowledge(ffuf.CapacityAck{
		ExecutionGeneration: 22,
		Epoch:               replayed.Epoch,
		ProposedWorkers:     replayed.DesiredWorkers,
		ActualLiveWorkers:   replayed.DesiredWorkers,
	})
	advisor.poll(time.Now())
	if snapshot := advisor.snapshot(); snapshot.Pending || snapshot.Suspended || snapshot.AcknowledgedLiveWorkers != 64 {
		t.Fatalf("recovered subscriber snapshot = %+v", snapshot)
	}
}

func TestCapacityAdvisorRejectsStaleAndLateAckThenRecoversByRepublish(t *testing.T) {
	const generation = 11
	base := time.Unix(100, 0)
	advisor := newCapacityAdvisor(10, 2*time.Second, time.Second)
	t.Cleanup(advisor.close)

	subscription, err := advisor.subscribe(generation)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	t.Cleanup(subscription.Close)
	initial := receiveCapacityProposal(t, subscription.Proposals())

	advisor.proposeAt(20, ffuf.CapacityCausePoolGrowth, base)
	proposal := receiveCapacityProposal(t, subscription.Proposals())
	subscription.Acknowledge(ffuf.CapacityAck{
		ExecutionGeneration: generation,
		Epoch:               initial.Epoch,
		ProposedWorkers:     initial.DesiredWorkers,
		ActualLiveWorkers:   initial.DesiredWorkers,
		RemainingInputCap:   100,
		SupervisorState:     "running",
		AcknowledgedAt:      base,
	})
	advisor.poll(base.Add(time.Second))
	if got := advisor.snapshot().StaleAcknowledgements; got != 1 {
		t.Fatalf("stale acknowledgements = %d, want 1", got)
	}

	advisor.poll(base.Add(2 * time.Second))
	subscription.Acknowledge(ffuf.CapacityAck{
		ExecutionGeneration: generation,
		Epoch:               proposal.Epoch,
		ProposedWorkers:     proposal.DesiredWorkers,
		ActualLiveWorkers:   proposal.DesiredWorkers,
		RemainingInputCap:   100,
		SupervisorState:     "running",
		AcknowledgedAt:      base.Add(3 * time.Second),
	})
	advisor.poll(base.Add(3 * time.Second))
	if got := advisor.snapshot().LateAfterTimeoutAcknowledgements; got != 1 {
		t.Fatalf("late acknowledgements = %d, want 1", got)
	}

	advisor.poll(base.Add(4 * time.Second))
	republished := receiveCapacityProposal(t, subscription.Proposals())
	if republished.Cause != ffuf.CapacityCauseRepublish || republished.DesiredWorkers != proposal.DesiredWorkers || republished.Epoch <= proposal.Epoch {
		t.Fatalf("republished proposal = %+v, original = %+v", republished, proposal)
	}
	subscription.Acknowledge(ffuf.CapacityAck{
		ExecutionGeneration: generation,
		Epoch:               republished.Epoch,
		ProposedWorkers:     republished.DesiredWorkers,
		ActualLiveWorkers:   republished.DesiredWorkers,
		RemainingInputCap:   100,
		SupervisorState:     "running",
		AcknowledgedAt:      base.Add(4 * time.Second),
	})
	advisor.poll(base.Add(4 * time.Second))
	snapshot := advisor.snapshot()
	if snapshot.TimedOutProposals != 1 || snapshot.RepublishedProposals != 1 || snapshot.AcknowledgedLiveWorkers != 20 || snapshot.Suspended {
		t.Fatalf("recovered snapshot = %+v", snapshot)
	}
}

func TestCapacityAdvisorClassifiesEveryTimedOutTupleAsLate(t *testing.T) {
	const generation = 12
	base := time.Unix(200, 0)
	advisor := newCapacityAdvisor(10, 2*time.Second, time.Second)
	t.Cleanup(advisor.close)

	subscription, err := advisor.subscribe(generation)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(subscription.Close)
	_ = receiveCapacityProposal(t, subscription.Proposals())

	advisor.proposeAt(20, ffuf.CapacityCausePoolGrowth, base)
	first := receiveCapacityProposal(t, subscription.Proposals())
	advisor.poll(base.Add(2 * time.Second))
	advisor.poll(base.Add(3 * time.Second))
	second := receiveCapacityProposal(t, subscription.Proposals())
	advisor.poll(base.Add(5 * time.Second))
	advisor.poll(base.Add(6 * time.Second))
	current := receiveCapacityProposal(t, subscription.Proposals())

	for _, proposal := range []ffuf.CapacityProposal{first, second} {
		subscription.Acknowledge(ffuf.CapacityAck{
			ExecutionGeneration: generation,
			Epoch:               proposal.Epoch,
			ProposedWorkers:     proposal.DesiredWorkers,
			ActualLiveWorkers:   proposal.DesiredWorkers,
			AcknowledgedAt:      base.Add(6 * time.Second),
		})
		advisor.poll(base.Add(6 * time.Second))
	}

	snapshot := advisor.snapshot()
	if snapshot.LateAfterTimeoutAcknowledgements != 2 || snapshot.StaleAcknowledgements != 0 || !snapshot.Pending || snapshot.ProposalEpoch != current.Epoch {
		t.Fatalf("multiple timeout acknowledgement snapshot = %+v, current = %+v", snapshot, current)
	}
}

func TestCapacityAdvisorCloseMarksPendingProposalCancelled(t *testing.T) {
	advisor := newCapacityAdvisor(10, 2*time.Second, time.Second)
	subscription, err := advisor.subscribe(13)
	if err != nil {
		t.Fatal(err)
	}
	proposal := receiveCapacityProposal(t, subscription.Proposals())

	advisor.close()
	select {
	case outcome := <-advisor.results():
		if outcome.Status != capacityOutcomeCancelled || outcome.Proposal != proposal {
			t.Fatalf("cancellation outcome = %+v, want proposal %+v", outcome, proposal)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for cancellation outcome")
	}
	if snapshot := advisor.snapshot(); snapshot.Pending || !snapshot.Suspended {
		t.Fatalf("closed advisor snapshot = %+v", snapshot)
	}
}

func receiveCapacityProposal(t *testing.T, proposals <-chan ffuf.CapacityProposal) ffuf.CapacityProposal {
	t.Helper()
	select {
	case proposal := <-proposals:
		return proposal
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for capacity proposal")
		return ffuf.CapacityProposal{}
	}
}
