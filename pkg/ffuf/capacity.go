package ffuf

import "time"

// CapacityCause identifies why an adaptive request-worker target changed.
type CapacityCause string

const (
	CapacityCauseSubscriberReady CapacityCause = "subscriber_ready"
	CapacityCausePoolGrowth      CapacityCause = "pool_growth"
	CapacityCausePoolShrink      CapacityCause = "pool_shrink"
	CapacityCauseRepublish       CapacityCause = "republish"
)

// CapacityProposal is the pool's latest desired application-worker capacity.
// ExecutionGeneration prevents a queued recursion job from consuming an update
// intended for the supervisor that preceded it. Epoch orders updates within and
// across generations for one process.
type CapacityProposal struct {
	ExecutionGeneration uint64        `json:"execution_generation"`
	Epoch               uint64        `json:"epoch"`
	DesiredWorkers      int           `json:"desired_workers"`
	Cause               CapacityCause `json:"cause"`
	PublishedAt         time.Time     `json:"published_at"`
}

// CapacityAck reports the actual live worker count after a supervisor applies a
// proposal. Shrinks are acknowledged only after retiring workers finish their
// current task.
type CapacityAck struct {
	ExecutionGeneration uint64    `json:"execution_generation"`
	Epoch               uint64    `json:"epoch"`
	ProposedWorkers     int       `json:"proposed_workers"`
	ActualLiveWorkers   int       `json:"actual_live_workers"`
	RemainingInputCap   int       `json:"remaining_input_cap"`
	SupervisorState     string    `json:"supervisor_state"`
	AcknowledgedAt      time.Time `json:"acknowledged_at"`
}

// CapacitySubscription is a bounded, latest-value worker-capacity stream.
// Proposals and acknowledgements are both non-blocking and coalescing.
type CapacitySubscription interface {
	Proposals() <-chan CapacityProposal
	Acknowledge(CapacityAck)
	Close()
}

// CapacityAdvisor is the narrow bidirectional seam between a transport pool
// and the engine's request-worker supervisor.
type CapacityAdvisor interface {
	SubscribeCapacity(executionGeneration uint64) (CapacitySubscription, error)
}
