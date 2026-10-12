package runner

import (
	"errors"

	"github.com/assetnote/h3fuzz/pkg/ffuf"
)

// SubscribeCapacity forwards the engine's narrow capacity subscription to the
// selected HTTP/3 pool. Non-HTTP/3 and replay runners remain fixed-capacity.
func (r *SimpleRunner) SubscribeCapacity(executionGeneration uint64) (ffuf.CapacitySubscription, error) {
	if r == nil || r.replay || r.h3Pool == nil {
		return nil, errors.New("adaptive HTTP/3 worker capacity is unavailable")
	}
	return r.h3Pool.SubscribeCapacity(executionGeneration)
}
