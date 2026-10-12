package filter

import (
	"net/http"
	"runtime"
	"testing"

	"github.com/assetnote/h3fuzz/pkg/ffuf"
)

var optimizerAuditBool bool
var optimizerAuditResponse ffuf.Response

const perf203DefaultStatusMatcher = "200-299,301,302,307,401,403,405,500"

func optimizerAuditManager(b *testing.B) ffuf.MatcherManager {
	b.Helper()
	m := NewMatcherManager()
	if err := m.AddMatcher("status", perf203DefaultStatusMatcher); err != nil {
		b.Fatal(err)
	}
	if err := m.AddFilter("status", "404", false); err != nil {
		b.Fatal(err)
	}
	return m
}

func optimizerAuditStatusSizeManager(b *testing.B) ffuf.MatcherManager {
	b.Helper()
	m := NewMatcherManager()
	if err := m.AddMatcher("status", perf203DefaultStatusMatcher); err != nil {
		b.Fatal(err)
	}
	if err := m.AddMatcher("size", "0-1024"); err != nil {
		b.Fatal(err)
	}
	if err := m.AddFilter("status", "204", false); err != nil {
		b.Fatal(err)
	}
	if err := m.AddFilter("size", "0", false); err != nil {
		b.Fatal(err)
	}
	return m
}

// perf203A42CeilingDecision is the scalar lower bound for the exact matcher and
// filter configuration used by the retained A42 ceiling workload: ffuf's
// default status matcher plus -fc 204. It deliberately remains test-only. The
// retained CPU profile, not this synthetic lower bound, decides whether a
// production fast path is warranted.
//
//go:noinline
func perf203A42CeilingDecision(status int64) bool {
	matched := status >= 200 && status <= 299 ||
		status == 301 || status == 302 || status == 307 ||
		status == 401 || status == 403 || status == 405 || status == 500
	return matched && status != 204
}

func BenchmarkPERF203A42CeilingDecision(b *testing.B) {
	b.Run("manager-filtered-204", func(b *testing.B) {
		m := optimizerAuditManager(b)
		resp := &ffuf.Response{StatusCode: 204}
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			optimizerAuditBool = m.Matches(resp, false, "or", "or")
		}
	})

	b.Run("manager-passed-200", func(b *testing.B) {
		m := optimizerAuditManager(b)
		resp := &ffuf.Response{StatusCode: 200}
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			optimizerAuditBool = m.Matches(resp, false, "or", "or")
		}
	})

	b.Run("manager-alternating", func(b *testing.B) {
		m := optimizerAuditManager(b)
		responses := [...]ffuf.Response{{StatusCode: 204}, {StatusCode: 200}}
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			optimizerAuditBool = m.Matches(&responses[i&1], false, "or", "or")
		}
	})

	b.Run("manager-status-size-passed", func(b *testing.B) {
		m := optimizerAuditStatusSizeManager(b)
		resp := &ffuf.Response{StatusCode: 200, ContentLength: 128}
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			optimizerAuditBool = m.Matches(resp, false, "and", "or")
		}
	})

	b.Run("scalar-lower-bound-alternating", func(b *testing.B) {
		statuses := [...]int64{204, 200}
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			optimizerAuditBool = perf203A42CeilingDecision(statuses[i&1])
		}
	})

	b.Run("manager-parallel-filtered-204", func(b *testing.B) {
		m := optimizerAuditManager(b)
		b.ReportAllocs()
		b.ResetTimer()
		b.RunParallel(func(pb *testing.PB) {
			resp := &ffuf.Response{StatusCode: 204}
			matched := false
			for pb.Next() {
				matched = m.Matches(resp, false, "or", "or")
			}
			runtime.KeepAlive(matched)
		})
	})
}

func BenchmarkOptimizerAuditMatchesSerial(b *testing.B) {
	m := optimizerAuditManager(b)
	resp := &ffuf.Response{StatusCode: 200}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		optimizerAuditBool = m.Matches(resp, false, "or", "or")
	}
}

func BenchmarkOptimizerAuditMatchesParallel(b *testing.B) {
	m := optimizerAuditManager(b)
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		resp := &ffuf.Response{StatusCode: 200}
		for pb.Next() {
			optimizerAuditBool = m.Matches(resp, false, "or", "or")
		}
	})
}

func BenchmarkOptimizerAuditNewResponse(b *testing.B) {
	httpResp := &http.Response{StatusCode: 200, Header: make(http.Header)}
	req := &ffuf.Request{}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		optimizerAuditResponse = ffuf.NewResponse(httpResp, req)
	}
}

func BenchmarkOptimizerAuditResponseWithoutMap(b *testing.B) {
	httpResp := &http.Response{StatusCode: 200, Header: make(http.Header)}
	req := &ffuf.Request{}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		optimizerAuditResponse = ffuf.Response{
			Request: req, StatusCode: int64(httpResp.StatusCode),
			ContentType: httpResp.Header.Get("Content-Type"), Headers: httpResp.Header,
		}
	}
}
