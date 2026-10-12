package ffuf

import (
	"net/http"
	"testing"
)

var responseAllocationSink Response

func TestNewResponseLeavesScraperDataLazy(t *testing.T) {
	httpResponse := &http.Response{StatusCode: http.StatusOK, Header: make(http.Header)}
	request := &Request{}
	response := NewResponse(httpResponse, request)
	if response.ScraperData != nil {
		t.Fatal("NewResponse eagerly allocated ScraperData")
	}

	response.SetScraperData("links", []string{"/admin"})
	if got := response.ScraperData["links"]; len(got) != 1 || got[0] != "/admin" {
		t.Fatalf("lazy scraper assignment = %v, want [/admin]", got)
	}
	response.SetScraperData("tokens", []string{"secret"})
	if len(response.ScraperData) != 2 {
		t.Fatalf("second scraper assignment replaced the map: %v", response.ScraperData)
	}
}

func TestNewResponseDoesNotAllocateUnusedScraperMap(t *testing.T) {
	httpResponse := &http.Response{StatusCode: http.StatusOK, Header: make(http.Header)}
	request := &Request{}
	allocations := testing.AllocsPerRun(1_000, func() {
		responseAllocationSink = NewResponse(httpResponse, request)
	})
	if allocations != 0 {
		t.Fatalf("NewResponse allocated %.2f objects for an ordinary response, want 0", allocations)
	}
}

// BenchmarkNewResponseMetadata keeps the allocation-free response-construction
// boundary measurable independently of transport and matcher work. PERF-205's
// whole-program profile uses this boundary when ranking response construction.
func BenchmarkNewResponseMetadata(b *testing.B) {
	httpResponse := &http.Response{
		StatusCode: http.StatusNoContent,
		Header: http.Header{
			"Content-Type": {"text/plain"},
		},
	}
	request := &Request{}

	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		responseAllocationSink = NewResponse(httpResponse, request)
	}
}
