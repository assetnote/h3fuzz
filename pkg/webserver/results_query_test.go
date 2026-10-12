package webserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
)

func getResultsPage(t *testing.T, server *Server, target string) ResultsPage {
	t.Helper()
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, authenticatedRequest(http.MethodGet, target, nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("results query = %d: %s", recorder.Code, recorder.Body.String())
	}
	var page ResultsPage
	if err := json.Unmarshal(recorder.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	return page
}

func TestResultsQueryAppliesStatusBeforeLatestWindow(t *testing.T) {
	server := testServer(t)
	session := testSession("result-window-status-filter")
	session.Status = StatusCompleted
	if err := server.store.CreateSession(context.Background(), &session); err != nil {
		t.Fatal(err)
	}
	results := []StoredResult{
		{URL: "https://example.test/early-success", StatusCode: 200, CreatedAt: time.Now().UTC()},
		{URL: "https://example.test/late-forbidden-1", StatusCode: 404, CreatedAt: time.Now().UTC()},
		{URL: "https://example.test/late-forbidden-2", StatusCode: 404, CreatedAt: time.Now().UTC()},
		{URL: "https://example.test/late-forbidden-3", StatusCode: 404, CreatedAt: time.Now().UTC()},
	}
	if _, _, err := server.store.InsertResultBatch(context.Background(), session.ID, results, nil); err != nil {
		t.Fatal(err)
	}

	page := getResultsPage(t, server,
		"/api/v1/sessions/"+session.ID+"/results?latest=true&limit=2&status=200")
	if len(page.Results) != 1 || page.Results[0].StatusCode != 200 || page.Results[0].URL != "https://example.test/early-success" {
		t.Fatalf("status filter must run before the latest-result window, got %#v", page.Results)
	}
	if page.Total != 1 || page.StatusCounts["200"] != 1 || page.StatusCounts["404"] != 3 {
		t.Fatalf("result summary = total %d, statuses %#v", page.Total, page.StatusCounts)
	}
}

func TestResultsQuerySearchesURLInputAndContentTypeCaseInsensitively(t *testing.T) {
	server := testServer(t)
	session := testSession("result-text-query")
	if err := server.store.CreateSession(context.Background(), &session); err != nil {
		t.Fatal(err)
	}
	results := []StoredResult{
		{URL: "https://example.test/Needle-URL", StatusCode: 200, CreatedAt: time.Now().UTC()},
		{URL: "https://example.test/input", Input: map[string][]byte{"FUZZ": []byte("needle-input")}, StatusCode: 201, CreatedAt: time.Now().UTC()},
		{URL: "https://example.test/type", ContentType: "application/needle+json", StatusCode: 202, CreatedAt: time.Now().UTC()},
		{URL: "https://example.test/no-match", StatusCode: 404, CreatedAt: time.Now().UTC()},
	}
	if _, _, err := server.store.InsertResultBatch(context.Background(), session.ID, results, nil); err != nil {
		t.Fatal(err)
	}

	page := getResultsPage(t, server, "/api/v1/sessions/"+session.ID+"/results?limit=10&q="+url.QueryEscape(" NeEdLe "))
	if page.Total != 3 || len(page.Results) != 3 {
		t.Fatalf("text query page = total %d, results %#v", page.Total, page.Results)
	}
	for index, status := range []int64{200, 201, 202} {
		if page.Results[index].StatusCode != status {
			t.Fatalf("text query result %d status = %d, want %d", index, page.Results[index].StatusCode, status)
		}
	}
}

func TestResultsQueryAcceptsCSVAndRepeatedStatusesWithoutNarrowingFacets(t *testing.T) {
	server := testServer(t)
	session := testSession("result-status-list")
	if err := server.store.CreateSession(context.Background(), &session); err != nil {
		t.Fatal(err)
	}
	results := []StoredResult{
		{URL: "https://example.test/ok", StatusCode: 200, CreatedAt: time.Now().UTC()},
		{URL: "https://example.test/created", StatusCode: 201, CreatedAt: time.Now().UTC()},
		{URL: "https://example.test/redirect", StatusCode: 302, CreatedAt: time.Now().UTC()},
		{URL: "https://example.test/missing", StatusCode: 404, CreatedAt: time.Now().UTC()},
	}
	if _, _, err := server.store.InsertResultBatch(context.Background(), session.ID, results, nil); err != nil {
		t.Fatal(err)
	}

	page := getResultsPage(t, server,
		"/api/v1/sessions/"+session.ID+"/results?limit=10&status=200,302&status=404&status=200")
	if page.Total != 3 || len(page.Results) != 3 {
		t.Fatalf("multi-status page = total %d, results %#v", page.Total, page.Results)
	}
	if len(page.StatusCounts) != 4 || page.StatusCounts["200"] != 1 || page.StatusCounts["201"] != 1 ||
		page.StatusCounts["302"] != 1 || page.StatusCounts["404"] != 1 {
		t.Fatalf("status facets were narrowed by the selected statuses: %#v", page.StatusCounts)
	}
}

func TestResultsQueryPagesBackwardAcrossAllMatches(t *testing.T) {
	server := testServer(t)
	session := testSession("result-backward-pages")
	if err := server.store.CreateSession(context.Background(), &session); err != nil {
		t.Fatal(err)
	}
	statuses := []int64{200, 404, 200, 404, 200, 404, 200}
	results := make([]StoredResult, len(statuses))
	for index, status := range statuses {
		results[index] = StoredResult{
			URL: "https://example.test/item-" + string(rune('1'+index)), StatusCode: status, CreatedAt: time.Now().UTC(),
		}
	}
	persisted, _, err := server.store.InsertResultBatch(context.Background(), session.ID, results, nil)
	if err != nil {
		t.Fatal(err)
	}

	latest := getResultsPage(t, server,
		"/api/v1/sessions/"+session.ID+"/results?latest=true&limit=2&status=200")
	if len(latest.Results) != 2 || latest.Results[0].ID != persisted[4].ID || latest.Results[1].ID != persisted[6].ID ||
		!latest.HasMore || !latest.WindowTruncated || latest.NextCursor != persisted[4].ID || latest.Total != 4 {
		t.Fatalf("latest filtered page = %#v", latest)
	}
	older := getResultsPage(t, server, "/api/v1/sessions/"+session.ID+"/results?before_id="+
		strconv.FormatInt(latest.NextCursor, 10)+"&limit=2&status=200")
	if len(older.Results) != 2 || older.Results[0].ID != persisted[0].ID || older.Results[1].ID != persisted[2].ID ||
		older.HasMore || older.NextCursor != persisted[0].ID || older.Total != 4 {
		t.Fatalf("older filtered page = %#v", older)
	}
}

func TestResultsQueryAppliesSavedLiveFiltersBeforeUIFiltersAndFacets(t *testing.T) {
	server := testServer(t)
	session := testSession("result-live-filter-order")
	session.Status = StatusCompleted
	if err := server.store.CreateSession(context.Background(), &session); err != nil {
		t.Fatal(err)
	}
	results := []StoredResult{
		{URL: "https://example.test/needle-hidden", StatusCode: 200, ContentWords: 10, CreatedAt: time.Now().UTC()},
		{URL: "https://example.test/needle-kept", StatusCode: 404, ContentWords: 20, CreatedAt: time.Now().UTC()},
		{URL: "https://example.test/needle-other", StatusCode: 201, ContentWords: 30, CreatedAt: time.Now().UTC()},
	}
	if _, _, err := server.store.InsertResultBatch(context.Background(), session.ID, results, nil); err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, authenticatedRequest(http.MethodPatch,
		"/api/v1/sessions/"+session.ID+"/filters", strings.NewReader(`{"words":"10"}`)))
	if recorder.Code != http.StatusOK {
		t.Fatalf("add saved filter = %d: %s", recorder.Code, recorder.Body.String())
	}

	page := getResultsPage(t, server,
		"/api/v1/sessions/"+session.ID+"/results?latest=true&limit=10&q=needle&status=404")
	if page.Total != 1 || len(page.Results) != 1 || page.Results[0].StatusCode != 404 {
		t.Fatalf("filtered query page = %#v", page)
	}
	if _, exists := page.StatusCounts["200"]; exists || page.StatusCounts["404"] != 1 || page.StatusCounts["201"] != 1 {
		t.Fatalf("saved-filtered status facets = %#v", page.StatusCounts)
	}
}

func TestResultsQueryRejectsInvalidFiltersAndCursors(t *testing.T) {
	server := testServer(t)
	session := testSession("result-query-validation")
	if err := server.store.CreateSession(context.Background(), &session); err != nil {
		t.Fatal(err)
	}
	longQuery := strings.Repeat("a", maxResultSearchLength+1)
	tests := []struct {
		name  string
		query string
		code  string
	}{
		{name: "non numeric status", query: "status=ok", code: "invalid_status"},
		{name: "status below HTTP range", query: "status=99", code: "invalid_status"},
		{name: "status above HTTP range", query: "status=600", code: "invalid_status"},
		{name: "empty CSV status", query: "status=200,,404", code: "invalid_status"},
		{name: "negative before cursor", query: "before_id=-1", code: "invalid_cursor"},
		{name: "repeated before cursor", query: "before_id=1&before_id=2", code: "invalid_cursor"},
		{name: "repeated after cursor", query: "after_id=1&after_id=2", code: "invalid_cursor"},
		{name: "both cursors", query: "after_id=1&before_id=2", code: "invalid_cursor"},
		{name: "latest and after cursor", query: "latest=true&after_id=0", code: "invalid_cursor"},
		{name: "latest and before cursor", query: "latest=true&before_id=0", code: "invalid_cursor"},
		{name: "query too long", query: "q=" + longQuery, code: "invalid_query"},
		{name: "query invalid UTF-8", query: "q=%FF", code: "invalid_query"},
		{name: "repeated query", query: "q=one&q=two", code: "invalid_query"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			server.Handler().ServeHTTP(recorder, authenticatedRequest(http.MethodGet,
				"/api/v1/sessions/"+session.ID+"/results?"+test.query, nil))
			if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), `"code":"`+test.code+`"`) {
				t.Fatalf("invalid query response = %d: %s", recorder.Code, recorder.Body.String())
			}
		})
	}
}

func TestResultsStatusCursorIndexIsInstalled(t *testing.T) {
	server := testServer(t)
	rows, err := server.store.db.Query(`PRAGMA index_info(results_session_status_cursor)`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var columns []string
	for rows.Next() {
		var sequence, columnID int
		var name string
		if err := rows.Scan(&sequence, &columnID, &name); err != nil {
			t.Fatal(err)
		}
		columns = append(columns, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if strings.Join(columns, ",") != "session_id,status,id" {
		t.Fatalf("result status cursor index columns = %v", columns)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}

	planRows, err := server.store.db.Query(`EXPLAIN QUERY PLAN
		SELECT id,input_json,position,status,length,words,lines,content_type,
		redirect_location,url,duration_ns,scraper_json,host,response_fingerprint,created_at
		FROM results WHERE session_id=? AND status IN (?) ORDER BY id DESC LIMIT ?`, "session", 200, 26)
	if err != nil {
		t.Fatal(err)
	}
	defer planRows.Close()
	var plan []string
	for planRows.Next() {
		var id, parent, unused int
		var detail string
		if err := planRows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		plan = append(plan, detail)
	}
	if err := planRows.Err(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(plan, "\n"), "results_session_status_cursor") {
		t.Fatalf("status-filtered result query does not use status cursor index: %v", plan)
	}
}

func BenchmarkResultsQueryStatusLargeSet(b *testing.B) {
	for _, indexed := range []bool{true, false} {
		name := "without_status_index"
		if indexed {
			name = "with_status_index"
		}
		b.Run(name, func(b *testing.B) {
			store, err := OpenStore(":memory:")
			if err != nil {
				b.Fatal(err)
			}
			defer store.Close()
			if !indexed {
				if _, err := store.db.Exec(`DROP INDEX results_session_status_cursor`); err != nil {
					b.Fatal(err)
				}
			}
			session := testSession("result-query-benchmark")
			if err := store.CreateSession(context.Background(), &session); err != nil {
				b.Fatal(err)
			}
			const resultCount = 50_000
			results := make([]StoredResult, resultCount)
			for index := range results {
				status := int64(404)
				if index%1000 == 0 {
					status = 200
				}
				results[index] = StoredResult{
					URL:        "https://example.test/item",
					StatusCode: status,
					CreatedAt:  time.Now().UTC(),
				}
			}
			if _, _, err := store.InsertResultBatch(context.Background(), session.ID, results, nil); err != nil {
				b.Fatal(err)
			}

			b.ResetTimer()
			for range b.N {
				page, err := store.QueryResults(context.Background(), session.ID, ResultQuery{
					Latest: true, Limit: 25, Statuses: []int64{200},
				})
				if err != nil {
					b.Fatal(err)
				}
				if page.Total != 50 || len(page.Results) != 25 {
					b.Fatalf("unexpected benchmark result page: %#v", page)
				}
			}
		})
	}
}
