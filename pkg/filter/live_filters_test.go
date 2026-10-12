package filter

import (
	"sync"
	"testing"
	"time"

	"github.com/assetnote/h3fuzz/pkg/ffuf"
)

func TestAddFiltersRejectsWholeBatchWhenOneValueIsInvalid(t *testing.T) {
	manager := NewMatcherManager().(*MatcherManager)
	if err := manager.AddFilter("status", "404", false); err != nil {
		t.Fatal(err)
	}

	err := manager.AddFilters(map[string]string{
		"size": "123",
		"word": "not-a-count",
	})
	if err == nil {
		t.Fatal("AddFilters accepted an invalid word filter")
	}

	filters := manager.GetFilters()
	if _, exists := filters["size"]; exists {
		t.Fatal("valid member of rejected batch was partially published")
	}
	if got := filters["status"].Repr(); got != "404" {
		t.Fatalf("existing status filter changed to %q", got)
	}
}

func TestAddFiltersPublishesCompleteAdditiveBatch(t *testing.T) {
	manager := NewMatcherManager().(*MatcherManager)
	if err := manager.AddFilter("status", "404", false); err != nil {
		t.Fatal(err)
	}
	if err := manager.AddFilters(map[string]string{
		"status": "403",
		"size":   "123",
		"word":   "7",
	}); err != nil {
		t.Fatal(err)
	}

	filters := manager.GetFilters()
	if got := filters["status"].Repr(); got != "404,403" {
		t.Fatalf("status filter = %q, want additive 404,403", got)
	}
	if got := filters["size"].Repr(); got != "123" {
		t.Fatalf("size filter = %q, want 123", got)
	}
	if got := filters["word"].Repr(); got != "7" {
		t.Fatalf("word filter = %q, want 7", got)
	}
}

func TestAddFiltersMergesRepresentableTimeUnion(t *testing.T) {
	manager := NewMatcherManager().(*MatcherManager)
	if err := manager.AddFilter("time", ">100", false); err != nil {
		t.Fatal(err)
	}
	if err := manager.AddFilters(map[string]string{"time": ">50"}); err != nil {
		t.Fatal(err)
	}
	if got := manager.GetFilters()["time"].Repr(); got != ">50" {
		t.Fatalf("time filter = %q, want monotonic union >50", got)
	}

	if err := manager.AddFilters(map[string]string{"time": "<25"}); err == nil {
		t.Fatal("opposing time inequality was accepted even though ffuf -ft cannot represent its union")
	}
	if got := manager.GetFilters()["time"].Repr(); got != ">50" {
		t.Fatalf("rejected time update changed filter to %q", got)
	}
}

func TestWordAndLineFiltersUseObservedMetricsWithoutBody(t *testing.T) {
	word, err := NewWordFilter("7")
	if err != nil {
		t.Fatal(err)
	}
	line, err := NewLineFilter("3")
	if err != nil {
		t.Fatal(err)
	}
	response := &ffuf.Response{Data: nil, ContentWords: 7, ContentLines: 3}

	if matched, err := word.Filter(response); err != nil || !matched {
		t.Fatalf("word filter against observed ContentWords = %v, %v; want true, nil", matched, err)
	}
	if matched, err := line.Filter(response); err != nil || !matched {
		t.Fatalf("line filter against observed ContentLines = %v, %v; want true, nil", matched, err)
	}
}

func TestAddFiltersPublishesOneSnapshotToConcurrentReaders(t *testing.T) {
	manager := NewMatcherManager().(*MatcherManager)
	stop := make(chan struct{})
	partial := make(chan int, 1)
	var readers sync.WaitGroup
	for i := 0; i < 8; i++ {
		readers.Add(1)
		go func() {
			defer readers.Done()
			for {
				select {
				case <-stop:
					return
				default:
					count := len(manager.GetFilters())
					if count != 0 && count != 5 {
						select {
						case partial <- count:
						default:
						}
						return
					}
				}
			}
		}()
	}

	time.Sleep(time.Millisecond)
	if err := manager.AddFilters(map[string]string{
		"status": "404",
		"size":   "123",
		"word":   "7",
		"line":   "3",
		"time":   ">100",
	}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Millisecond)
	close(stop)
	readers.Wait()
	select {
	case count := <-partial:
		t.Fatalf("reader observed partially published filter set of size %d", count)
	default:
	}
}

// Matches loads one immutable snapshot at the start of each decision. In AND
// mode either single member of this two-filter batch would incorrectly filter
// one probe, while both together (and neither before publication) keep both.
// That makes a partially visible batch observable rather than merely relying
// on the race detector to notice unsafe memory access.
func TestAddFiltersConcurrentMatchSeesOldOrCompleteBatch(t *testing.T) {
	manager := NewMatcherManager().(*MatcherManager)
	if err := manager.AddMatcher("status", "all"); err != nil {
		t.Fatal(err)
	}
	statusOnly := &ffuf.Response{StatusCode: 201, ContentLength: 99}
	sizeOnly := &ffuf.Response{StatusCode: 200, ContentLength: 10}
	stop := make(chan struct{})
	partial := make(chan struct{}, 1)
	var readers sync.WaitGroup
	for i := 0; i < 8; i++ {
		readers.Add(1)
		go func() {
			defer readers.Done()
			for {
				select {
				case <-stop:
					return
				default:
					if !manager.Matches(statusOnly, false, "or", "and") ||
						!manager.Matches(sizeOnly, false, "or", "and") {
						select {
						case partial <- struct{}{}:
						default:
						}
						return
					}
				}
			}
		}()
	}

	time.Sleep(time.Millisecond)
	if err := manager.AddFilters(map[string]string{"status": "201", "size": "10"}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Millisecond)
	close(stop)
	readers.Wait()
	select {
	case <-partial:
		t.Fatal("a concurrent match observed only part of the filter batch")
	default:
	}
}

func TestAddFiltersPropagatesToExistingPerDomainCalibration(t *testing.T) {
	manager := NewMatcherManager().(*MatcherManager)
	if err := manager.AddPerDomainFilter("host.example", "size", "100"); err != nil {
		t.Fatal(err)
	}
	if err := manager.AddFilters(map[string]string{"word": "7"}); err != nil {
		t.Fatal(err)
	}

	domain := manager.FiltersForDomain("host.example")
	if got := domain["word"].Repr(); got != "7" {
		t.Fatalf("per-domain word filter = %q, want live value 7", got)
	}
	if got := domain["size"].Repr(); got != "100" {
		t.Fatalf("per-domain calibration size = %q, want preserved 100", got)
	}
}
