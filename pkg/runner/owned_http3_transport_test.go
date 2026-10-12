package runner

import (
	"net/http"
	"testing"
)

func TestOwnedHTTP3TransportStaleReservationRollsBackAfterRetirement(t *testing.T) {
	transport := &ownedHTTP3Transport{}
	generation := &ownedHTTP3Generation{}
	generation.accepting.Store(true)
	// Model one already-admitted request so the synthetic generation is not
	// closed when the stale reservation rolls back.
	generation.inFlight.Store(1)
	transport.current.Store(generation)

	stalePublishedPointer := transport.current.Load()
	transport.retireGeneration(generation)
	if transport.reserveGeneration(stalePublishedPointer) {
		t.Fatal("reserver admitted work through a stale retired generation")
	}
	if got := generation.inFlight.Load(); got != 1 {
		t.Fatalf("stale reservation rollback left in-flight=%d, want original 1", got)
	}
	if current := transport.current.Load(); current != nil {
		t.Fatalf("retired generation remained current: %p", current)
	}
}

func TestOwnedHTTP3TransportNoBodyReleasesGenerationWithoutWrapper(t *testing.T) {
	transport := &ownedHTTP3Transport{}
	generation := &ownedHTTP3Generation{}
	generation.accepting.Store(true)
	generation.inFlight.Store(1)
	response := &http.Response{Body: http.NoBody}

	got := transport.trackResponse(generation, response)
	if got.Body != http.NoBody {
		t.Fatalf("bodyless response body = %T, want unchanged http.NoBody", got.Body)
	}
	if inFlight := generation.inFlight.Load(); inFlight != 0 {
		t.Fatalf("bodyless response left in-flight=%d, want 0", inFlight)
	}
}

func TestOwnedHTTP3RetryBudgetAllowsEachSafeReasonOnce(t *testing.T) {
	orders := []struct {
		name   string
		first  ownedHTTP3RetryReason
		second ownedHTTP3RetryReason
	}{
		{
			name:   "unusable then rejected",
			first:  ownedHTTP3RetryConnectionUnusable,
			second: ownedHTTP3RetryRequestRejected,
		},
		{
			name:   "rejected then unusable",
			first:  ownedHTTP3RetryRequestRejected,
			second: ownedHTTP3RetryConnectionUnusable,
		},
	}
	for _, order := range orders {
		t.Run(order.name, func(t *testing.T) {
			var budget ownedHTTP3RetryBudget
			if !budget.take(order.first) {
				t.Fatalf("first safe retry reason %d was rejected", order.first)
			}
			if budget.take(order.first) {
				t.Fatalf("duplicate safe retry reason %d was accepted", order.first)
			}
			if !budget.take(order.second) {
				t.Fatalf("independent safe retry reason %d was rejected", order.second)
			}
			if budget.take(order.second) {
				t.Fatalf("duplicate safe retry reason %d was accepted", order.second)
			}
			if budget.take(ownedHTTP3RetryNone) {
				t.Fatal("non-retryable reason was accepted")
			}
		})
	}
}
