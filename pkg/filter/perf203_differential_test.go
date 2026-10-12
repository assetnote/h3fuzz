package filter

import (
	"testing"

	"github.com/assetnote/h3fuzz/pkg/ffuf"
)

// perf203ReferenceMatches is an intentionally simple characterization oracle.
// It reads the public manager view and evaluates providers independently of the
// production snapshot loop. This freezes the semantics a future compiled
// status/size plan would have to preserve.
func perf203ReferenceMatches(manager ffuf.MatcherManager, resp *ffuf.Response, perHost bool, matcherMode, filterMode string) bool {
	matchers := manager.GetMatchers()
	filters := manager.GetFilters()
	if perHost && resp != nil && resp.Request != nil {
		filters = manager.FiltersForDomain(ffuf.HostURLFromRequest(*resp.Request))
	}

	matched := false
	for _, matcher := range matchers {
		value, err := matcher.Filter(resp)
		if err != nil {
			continue
		}
		if value {
			matched = true
		} else if matcherMode == "and" {
			return false
		}
	}
	if !matched {
		return false
	}

	for _, filter := range filters {
		value, err := filter.Filter(resp)
		if err != nil {
			continue
		}
		if value && filterMode == "or" {
			return false
		}
		if !value && filterMode == "and" {
			return true
		}
	}
	return len(filters) == 0 || filterMode != "and"
}

func perf203MustAddMatcher(t *testing.T, manager ffuf.MatcherManager, name, value string) {
	t.Helper()
	if err := manager.AddMatcher(name, value); err != nil {
		t.Fatalf("AddMatcher(%q, %q): %v", name, value, err)
	}
}

func perf203MustAddFilter(t *testing.T, manager ffuf.MatcherManager, name, value string) {
	t.Helper()
	if err := manager.AddFilter(name, value, false); err != nil {
		t.Fatalf("AddFilter(%q, %q): %v", name, value, err)
	}
}

func perf203MustAddDomainFilter(t *testing.T, manager ffuf.MatcherManager, domain, name, value string) {
	t.Helper()
	if err := manager.AddPerDomainFilter(domain, name, value); err != nil {
		t.Fatalf("AddPerDomainFilter(%q, %q, %q): %v", domain, name, value, err)
	}
}

func TestPERF203A42CeilingDecisionDifferential(t *testing.T) {
	manager := NewMatcherManager()
	perf203MustAddMatcher(t, manager, "status", perf203DefaultStatusMatcher)
	perf203MustAddFilter(t, manager, "status", "204")

	// Exercise every three-digit status plus out-of-range boundary values. Size
	// is varied to prove that the exact A42 status-only decision ignores it.
	statuses := []int64{-1, 0, 99, 100, 199, 600, 999, 1000}
	for status := int64(100); status <= 599; status++ {
		statuses = append(statuses, status)
	}
	for _, status := range statuses {
		for _, size := range []int64{-1, 0, 1, 32, 1 << 20} {
			resp := &ffuf.Response{StatusCode: status, ContentLength: size}
			got := manager.Matches(resp, false, "or", "or")
			want := perf203A42CeilingDecision(status)
			if got != want {
				t.Fatalf("status=%d size=%d: Matches=%t scalar oracle=%t", status, size, got, want)
			}
		}
	}
}

func TestPERF203StatusSizeManagerDifferential(t *testing.T) {
	type setupCase struct {
		name  string
		setup func(*testing.T, ffuf.MatcherManager)
	}
	setups := []setupCase{
		{
			name: "empty",
			setup: func(_ *testing.T, _ ffuf.MatcherManager) {
			},
		},
		{
			name: "status-only-a42",
			setup: func(t *testing.T, manager ffuf.MatcherManager) {
				perf203MustAddMatcher(t, manager, "status", perf203DefaultStatusMatcher)
				perf203MustAddFilter(t, manager, "status", "204")
			},
		},
		{
			name: "status-size-global",
			setup: func(t *testing.T, manager ffuf.MatcherManager) {
				perf203MustAddMatcher(t, manager, "status", "200-299,404")
				perf203MustAddMatcher(t, manager, "size", "0,16-64")
				perf203MustAddFilter(t, manager, "status", "204,500")
				perf203MustAddFilter(t, manager, "size", "32,128-256")
			},
		},
		{
			name: "status-size-per-domain",
			setup: func(t *testing.T, manager ffuf.MatcherManager) {
				perf203MustAddMatcher(t, manager, "status", "all")
				perf203MustAddFilter(t, manager, "size", "0")
				perf203MustAddDomainFilter(t, manager, "example.test/bench", "status", "204")
				perf203MustAddDomainFilter(t, manager, "example.test/bench", "size", "64")
			},
		},
		{
			name: "unsupported-word-line-fallback",
			setup: func(t *testing.T, manager ffuf.MatcherManager) {
				perf203MustAddMatcher(t, manager, "status", "all")
				perf203MustAddMatcher(t, manager, "word", "1-3")
				perf203MustAddFilter(t, manager, "line", "2")
			},
		},
	}

	requests := []*ffuf.Request{
		nil,
		{Host: "example.test", Url: "https://example.test/bench/item"},
		{Host: "other.test", Url: "https://other.test/else/item"},
	}
	for _, setup := range setups {
		t.Run(setup.name, func(t *testing.T) {
			manager := NewMatcherManager()
			setup.setup(t, manager)
			for _, matcherMode := range []string{"or", "and"} {
				for _, filterMode := range []string{"or", "and"} {
					for _, perHost := range []bool{false, true} {
						for _, request := range requests {
							for _, status := range []int64{199, 200, 204, 299, 404, 500} {
								for _, size := range []int64{0, 1, 16, 32, 64, 128, 257} {
									resp := &ffuf.Response{StatusCode: status, ContentLength: size, Request: request}
									got := manager.Matches(resp, perHost, matcherMode, filterMode)
									want := perf203ReferenceMatches(manager, resp, perHost, matcherMode, filterMode)
									if got != want {
										t.Fatalf("matcher=%s filter=%s perHost=%t request=%v status=%d size=%d: Matches=%t reference=%t", matcherMode, filterMode, perHost, request, status, size, got, want)
									}
								}
							}
						}
					}
				}
			}
		})
	}
}
