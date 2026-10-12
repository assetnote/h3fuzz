package engine_test

import (
	"testing"
	"time"

	"github.com/assetnote/h3fuzz/pkg/engine"
	"github.com/assetnote/h3fuzz/pkg/ffuf"
	"github.com/assetnote/h3fuzz/pkg/filter"
)

func TestJobAddFiltersAppliesToFutureResponses(t *testing.T) {
	manager := filter.NewMatcherManager()
	if err := manager.AddMatcher("status", "all"); err != nil {
		t.Fatal(err)
	}
	conf := &ffuf.Config{FilterMode: "or", MatcherMode: "or", MatcherManager: manager}
	job := engine.NewJob(conf)

	if err := job.AddFilters(map[string]string{"word": "2"}); err != nil {
		t.Fatal(err)
	}
	if manager.Matches(&ffuf.Response{StatusCode: 200, Data: []byte("one two")}, false, "or", "or") {
		t.Fatal("new word filter did not suppress a future matching response")
	}
	if !manager.Matches(&ffuf.Response{StatusCode: 200, Data: []byte("one two three")}, false, "or", "or") {
		t.Fatal("new word filter suppressed a future non-matching response")
	}
}

func TestJobAddFiltersRequiresMonotonicOrMode(t *testing.T) {
	manager := filter.NewMatcherManager()
	conf := &ffuf.Config{FilterMode: "and", MatcherManager: manager}
	job := engine.NewJob(conf)

	if err := job.AddFilters(map[string]string{"word": "2"}); err == nil {
		t.Fatal("AddFilters accepted a live update in and mode")
	}
	if len(manager.GetFilters()) != 0 {
		t.Fatal("rejected and-mode update mutated active filters")
	}
}

func TestResultPassesFiltersUsesStoredMetrics(t *testing.T) {
	word, err := filter.NewFilterByName("word", "7")
	if err != nil {
		t.Fatal(err)
	}
	timing, err := filter.NewFilterByName("time", ">100")
	if err != nil {
		t.Fatal(err)
	}
	filters := map[string]ffuf.FilterProvider{"word": word, "time": timing}

	keep, err := engine.ResultPassesFilters(ffuf.Result{ContentWords: 7, Duration: 50 * time.Millisecond}, filters, "or")
	if err != nil {
		t.Fatal(err)
	}
	if keep {
		t.Fatal("stored result matching word filter was kept")
	}

	keep, err = engine.ResultPassesFilters(ffuf.Result{ContentWords: 8, Duration: 50 * time.Millisecond}, filters, "or")
	if err != nil {
		t.Fatal(err)
	}
	if !keep {
		t.Fatal("stored result matching neither filter was removed")
	}
}

func TestResultPassesFiltersRejectsRegexpWithoutStoredBody(t *testing.T) {
	regexpFilter, err := filter.NewFilterByName("regexp", "secret")
	if err != nil {
		t.Fatal(err)
	}
	_, err = engine.ResultPassesFilters(ffuf.Result{}, map[string]ffuf.FilterProvider{"regexp": regexpFilter}, "or")
	if err == nil {
		t.Fatal("metadata-only result filtering accepted regexp")
	}
}
