package filter

import (
	"testing"

	"github.com/assetnote/h3fuzz/pkg/ffuf"
)

var matchesAllocationSink bool

// resp builds a minimal Response for the non-per-host match path (Request is only
// dereferenced when perHost is true).
func resp(status, size int64) *ffuf.Response {
	return &ffuf.Response{StatusCode: status, ContentLength: size}
}

// TestMatches_StatusMatcher covers the basic matcher path now that the decision
// lives on MatcherManager instead of Job.isMatch.
func TestMatches_StatusMatcher(t *testing.T) {
	mm := NewMatcherManager()
	if err := mm.AddMatcher("status", "200,204"); err != nil {
		t.Fatalf("AddMatcher: %v", err)
	}
	if !mm.Matches(resp(200, 10), false, "or", "or") {
		t.Error("200 should match status 200,204")
	}
	if mm.Matches(resp(404, 10), false, "or", "or") {
		t.Error("404 should not match status 200,204")
	}
}

// TestMatches_MatcherModeAnd checks the and-mode combination: every matcher must
// pass.
func TestMatches_MatcherModeAnd(t *testing.T) {
	mm := NewMatcherManager()
	_ = mm.AddMatcher("status", "200")
	_ = mm.AddMatcher("size", "10")
	if !mm.Matches(resp(200, 10), false, "and", "or") {
		t.Error("200 + size 10 should match in and-mode")
	}
	if mm.Matches(resp(200, 99), false, "and", "or") {
		t.Error("200 + size 99 should NOT match in and-mode (size fails)")
	}
}

// TestMatches_FilterDrops checks that a filter removes an otherwise-matched
// response.
func TestMatches_FilterDrops(t *testing.T) {
	mm := NewMatcherManager()
	_ = mm.AddMatcher("status", "all")
	_ = mm.AddFilter("size", "10", false)
	if mm.Matches(resp(200, 10), false, "or", "or") {
		t.Error("a size-10 response should be filtered out")
	}
	if !mm.Matches(resp(200, 20), false, "or", "or") {
		t.Error("a size-20 response should survive the size-10 filter")
	}
}

func TestMatches_HotPathDoesNotAllocate(t *testing.T) {
	mm := NewMatcherManager()
	if err := mm.AddMatcher("status", "all"); err != nil {
		t.Fatalf("AddMatcher: %v", err)
	}
	if err := mm.AddFilter("size", "10", false); err != nil {
		t.Fatalf("AddFilter: %v", err)
	}
	response := resp(200, 20)
	if !mm.Matches(response, false, "or", "or") {
		t.Fatal("test response should pass before measuring allocations")
	}

	allocations := testing.AllocsPerRun(1_000, func() {
		matchesAllocationSink = mm.Matches(response, false, "or", "or")
	})
	if allocations != 0 {
		t.Fatalf("Matches allocated %.2f objects per response, want 0", allocations)
	}
}
