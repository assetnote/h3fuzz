package filter

import (
	"sync"
	"testing"
	"time"

	"github.com/assetnote/h3fuzz/pkg/ffuf"
)

// TestMatcherManager_ConcurrentReadWrite regresses C1: the reader methods took
// no lock while the writers did, so the write-side mutex protected writers only
// from each other. With -ac at default threads a worker ranging GetFilters()
// raced autocalibration's AddFilter, which the Go runtime aborts with
// "concurrent map read and map write". Run under -race; before the fix this
// crashes.
func TestMatcherManager_ConcurrentReadWrite(t *testing.T) {
	mm := NewMatcherManager()
	if err := mm.AddMatcher("status", "all"); err != nil {
		t.Fatalf("AddMatcher: %v", err)
	}

	var wg sync.WaitGroup
	stop := make(chan struct{})
	response := &ffuf.Response{
		StatusCode: 200,
		Request:    &ffuf.Request{Url: "https://host.example/asset"},
	}

	// writers: churn the maps like autocalibration and the interactive fc/fs
	// commands do.
	for w := 0; w < 4; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					_ = mm.AddFilter("size", "1337", false)
					mm.RemoveFilter("size")
					_ = mm.AddPerDomainFilter("host.example", "word", "9")
					mm.SetCalibratedForHost("host.example", true)
					mm.SetCalibrated(true)
				}
			}
		}()
	}
	// readers: exactly what every worker's isMatch does.
	for r := 0; r < 8; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					for range mm.GetFilters() {
					}
					for range mm.GetMatchers() {
					}
					for range mm.FiltersForDomain("host.example") {
					}
					_ = mm.Matches(response, true, "or", "or")
					_ = mm.Calibrated()
					_ = mm.CalibratedForDomain("host.example")
				}
			}
		}()
	}

	time.Sleep(200 * time.Millisecond)
	close(stop)
	wg.Wait()
}

func TestMatcherManager_CopyOnWritePreservesPublishedSnapshots(t *testing.T) {
	manager := NewMatcherManager().(*MatcherManager)
	initial := manager.loadSnapshot()
	if err := manager.AddMatcher("status", "all"); err != nil {
		t.Fatalf("AddMatcher: %v", err)
	}
	if err := manager.AddFilter("size", "100", false); err != nil {
		t.Fatalf("AddFilter: %v", err)
	}
	if err := manager.AddPerDomainFilter("host.example", "word", "5"); err != nil {
		t.Fatalf("AddPerDomainFilter: %v", err)
	}

	published := manager.loadSnapshot()
	if len(initial.matchers) != 0 || len(initial.filters) != 0 || len(initial.perDomainFilters) != 0 {
		t.Fatal("a writer mutated a previously published snapshot")
	}
	if published.matchers["status"] == nil || published.filters["size"] == nil {
		t.Fatal("new snapshot is missing global matcher/filter updates")
	}
	if domain := published.perDomainFilters["host.example"]; domain == nil || domain.filters["word"] == nil || domain.filters["size"] == nil {
		t.Fatal("new snapshot is missing the inherited per-domain filter set")
	}

	manager.RemoveFilter("size")
	if published.filters["size"] == nil {
		t.Fatal("removal mutated the snapshot that preceded it")
	}
	if manager.loadSnapshot().filters["size"] != nil {
		t.Fatal("removed filter remains in the current snapshot")
	}
}

func TestMutablePerDomainFilterIsDetachedFromPublishedSnapshot(t *testing.T) {
	manager := NewMatcherManager()
	if err := manager.AddFilter("size", "100", false); err != nil {
		t.Fatal(err)
	}
	if err := manager.AddPerDomainFilter("host.example", "word", "5"); err != nil {
		t.Fatal(err)
	}

	detached := NewPerDomainFilter(manager.FiltersForDomain("host.example"))
	detached.SetCalibrated(true)
	delete(detached.Filters, "size")
	detached.Filters["line"] = &LineFilter{}

	if manager.CalibratedForDomain("host.example") {
		t.Fatal("mutating a detached PerDomainFilter changed published calibration state")
	}
	published := manager.FiltersForDomain("host.example")
	if published["size"] == nil || published["line"] != nil {
		t.Fatalf("mutating a detached PerDomainFilter changed published rules: %v", published)
	}
}

// TestMatcherManager_PerDomainDoesNotAliasGlobal regresses F1: NewPerDomainFilter
// stored the global filter map by reference, so a per-domain filter write leaked
// into the global filter set (and thus applied to every host).
func TestMatcherManager_PerDomainDoesNotAliasGlobal(t *testing.T) {
	mm := NewMatcherManager()
	if err := mm.AddFilter("size", "100", false); err != nil {
		t.Fatalf("AddFilter global: %v", err)
	}
	if err := mm.AddPerDomainFilter("host.example", "word", "5"); err != nil {
		t.Fatalf("AddPerDomainFilter: %v", err)
	}

	if _, leaked := mm.GetFilters()["word"]; leaked {
		t.Error("per-domain 'word' filter leaked into the global filter set (F1 aliasing)")
	}
	perDomain := mm.FiltersForDomain("host.example")
	if _, ok := perDomain["word"]; !ok {
		t.Error("per-domain filter set should contain its own 'word' filter")
	}
	if _, ok := perDomain["size"]; !ok {
		t.Error("per-domain filter set should inherit the global 'size' filter")
	}
}
