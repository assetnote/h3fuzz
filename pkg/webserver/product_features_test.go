package webserver

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSessionOrganizationPersistsWithoutChangingExecutionState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "web.db")
	store, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	session := testSession("organized")
	if err := store.CreateSession(context.Background(), &session); err != nil {
		t.Fatal(err)
	}
	pinned, archived, group := true, true, "Production"
	organization, err := store.PatchSessionOrganization(context.Background(), session.ID,
		PatchSessionOrganizationRequest{Pinned: &pinned, Archived: &archived, GroupName: &group})
	if err != nil {
		t.Fatal(err)
	}
	if !organization.Pinned || !organization.Archived || organization.GroupName != group {
		t.Fatalf("organization = %#v", organization)
	}
	loaded, err := store.GetSession(context.Background(), session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Status != StatusIdle || !loaded.Pinned || !loaded.Archived || loaded.GroupName != group {
		t.Fatalf("organized session = %#v", loaded)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	loaded, err = store.GetSession(context.Background(), session.ID)
	if err != nil || !loaded.Pinned || loaded.GroupName != group {
		t.Fatalf("reopened organized session = %#v, err=%v", loaded, err)
	}
}

func TestExploreResultsFiltersSortsFacetsAndGroupsResponseShapes(t *testing.T) {
	store, err := OpenStore(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	session := testSession("explorer")
	if err := store.CreateSession(context.Background(), &session); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	results := []StoredResult{
		{URL: "https://example.test/a", StatusCode: 200, ContentLength: 100, ContentWords: 10, ContentLines: 2, ContentType: "text/html", ResponseFingerprint: "sha256:shared", CreatedAt: now},
		{URL: "https://example.test/b", StatusCode: 200, ContentLength: 100, ContentWords: 10, ContentLines: 2, ContentType: "text/html", ResponseFingerprint: "sha256:shared", CreatedAt: now},
		{URL: "https://example.test/c", StatusCode: 200, ContentLength: 100, ContentWords: 10, ContentLines: 2, ContentType: "text/html", ResponseFingerprint: "sha256:shared", CreatedAt: now},
		{URL: "https://example.test/data", StatusCode: 201, ContentLength: 42, ContentWords: 3, ContentLines: 1, ContentType: "application/json", ResponseFingerprint: "sha256:data", CreatedAt: now},
		{URL: "https://example.test/missing", StatusCode: 404, ContentLength: 9, ContentWords: 1, ContentLines: 1, ContentType: "text/plain", ResponseFingerprint: "sha256:missing", CreatedAt: now},
	}
	if _, _, err := store.InsertResultBatch(context.Background(), session.ID, results, nil); err != nil {
		t.Fatal(err)
	}
	minimum := int64(40)
	page, err := store.ExploreResults(context.Background(), session.ID, ResultExplorerQuery{
		Search: "example.test", Statuses: []int64{200, 201}, SizeMin: &minimum,
		Sort: "size", Descending: true, Limit: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 4 || len(page.Results) != 4 || page.Results[0].ContentLength != 100 {
		t.Fatalf("filtered explorer page = %#v", page)
	}
	if page.Facets.Status["200"] != 3 || page.Facets.ContentType["application/json"] != 1 || page.Facets.Size.Min != 42 {
		t.Fatalf("facets = %#v", page.Facets)
	}
	grouped, err := store.ExploreResults(context.Background(), session.ID, ResultExplorerQuery{
		GroupDuplicates: true, Sort: "created", Limit: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if grouped.Total != 3 || len(grouped.Results) != 3 || grouped.Results[0].DuplicateCount != 3 || grouped.Results[0].WildcardGroup {
		t.Fatalf("grouped explorer page = %#v", grouped)
	}
}

func TestExploreResultsGroupsOnlyPersistedExactCapturedFingerprints(t *testing.T) {
	store, err := OpenStore(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	session := testSession("exact-duplicate-evidence")
	if err := store.CreateSession(context.Background(), &session); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	results := []StoredResult{
		{URL: "https://example.test/same-shape-a", StatusCode: 200, ContentLength: 100, ContentWords: 10, ContentLines: 2, ContentType: "text/html", ResponseFingerprint: "sha256:a", CreatedAt: now},
		{URL: "https://example.test/same-shape-b", StatusCode: 200, ContentLength: 100, ContentWords: 10, ContentLines: 2, ContentType: "text/html", ResponseFingerprint: "sha256:b", CreatedAt: now},
		{URL: "https://example.test/bodyless-a", StatusCode: 204, ContentType: "text/plain", CreatedAt: now},
		{URL: "https://example.test/bodyless-b", StatusCode: 204, ContentType: "text/plain", CreatedAt: now},
	}
	if _, _, err := store.InsertResultBatch(context.Background(), session.ID, results, nil); err != nil {
		t.Fatal(err)
	}
	page, err := store.ExploreResults(context.Background(), session.ID, ResultExplorerQuery{
		GroupDuplicates: true, Limit: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 4 || len(page.Results) != 4 {
		t.Fatalf("same-shape or bodyless responses were falsely grouped: %#v", page)
	}
	for _, result := range page.Results {
		if result.DuplicateCount != 1 || result.WildcardGroup {
			t.Fatalf("result carried unsupported duplicate evidence: %#v", result)
		}
	}
}

func TestExploreResultTreeAggregatesEntireFilteredSetWithBoundedPages(t *testing.T) {
	store, err := OpenStore(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	session := testSession("authoritative-tree")
	if err := store.CreateSession(context.Background(), &session); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	results := make([]StoredResult, 0, 1007)
	for index := 0; index < 1005; index++ {
		results = append(results, StoredResult{
			URL:        fmt.Sprintf("https://example.test/dir/item-%04d", index),
			StatusCode: 200, ContentType: "text/html", CreatedAt: now,
		})
	}
	results = append(results,
		StoredResult{URL: "https://example.test/dir/hidden-a", StatusCode: 404, CreatedAt: now},
		StoredResult{URL: "https://example.test/dir/hidden-b", StatusCode: 404, CreatedAt: now},
	)
	if _, _, err := store.InsertResultBatch(context.Background(), session.ID, results, nil); err != nil {
		t.Fatal(err)
	}
	query := ResultExplorerQuery{Statuses: []int64{200}, Limit: 1000}
	root, err := store.ExploreResultTree(context.Background(), session.ID, query, resultTreeScope{})
	if err != nil || root.SnapshotID == 0 || len(root.Nodes) != 1 || root.Nodes[0].ResultCount != 1005 {
		t.Fatalf("root = %#v, err=%v", root, err)
	}
	query.SnapshotID = &root.SnapshotID
	if _, _, err := store.InsertResultBatch(context.Background(), session.ID, []StoredResult{{
		URL:         "https://example.test/dir/late-arrival",
		StatusCode:  200,
		ContentType: "text/html",
		CreatedAt:   now.Add(time.Second),
	}}, nil); err != nil {
		t.Fatal(err)
	}
	originScope, err := decodeResultTreeScope(root.Nodes[0].Key)
	if err != nil {
		t.Fatal(err)
	}
	directories, err := store.ExploreResultTree(context.Background(), session.ID, query, originScope)
	if err != nil || len(directories.Nodes) != 1 || directories.Nodes[0].Name != "dir" ||
		directories.Nodes[0].ResultCount != 1005 || !directories.Nodes[0].HasChildren {
		t.Fatalf("directories = %#v, err=%v", directories, err)
	}
	directoryScope, err := decodeResultTreeScope(directories.Nodes[0].Key)
	if err != nil {
		t.Fatal(err)
	}
	first, err := store.ExploreResultTree(context.Background(), session.ID, query, directoryScope)
	if err != nil || first.Total != 1005 || len(first.Nodes) != 1000 || !first.HasMore {
		t.Fatalf("first child page = total %d, rows %d, more %t, err=%v", first.Total, len(first.Nodes), first.HasMore, err)
	}
	query.Offset = 1000
	second, err := store.ExploreResultTree(context.Background(), session.ID, query, directoryScope)
	if err != nil || second.Total != 1005 || len(second.Nodes) != 5 || second.HasMore {
		t.Fatalf("second child page = total %d, rows %d, more %t, err=%v", second.Total, len(second.Nodes), second.HasMore, err)
	}
	for _, node := range append(first.Nodes, second.Nodes...) {
		if node.Result == nil || node.Result.StatusCode != 200 || strings.Contains(node.URL, "hidden") {
			t.Fatalf("tree included a row outside the server-side filter: %#v", node)
		}
	}
}

func TestExploreResultsPaginationAndFacetsRemainAuthoritativePastPageLimit(t *testing.T) {
	store, err := OpenStore(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	session := testSession("authoritative-result-pages")
	if err := store.CreateSession(context.Background(), &session); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	results := make([]StoredResult, 1006)
	for index := range results {
		status := int64(200)
		if index == len(results)-1 {
			status = 404
		}
		results[index] = StoredResult{
			URL: fmt.Sprintf("https://example.test/item-%04d", index), StatusCode: status,
			ContentLength: int64(index + 1), ContentType: "text/html", CreatedAt: now,
		}
	}
	if _, _, err := store.InsertResultBatch(context.Background(), session.ID, results, nil); err != nil {
		t.Fatal(err)
	}
	first, err := store.ExploreResults(context.Background(), session.ID, ResultExplorerQuery{Limit: 1000})
	if err != nil || first.Total != 1006 || len(first.Results) != 1000 || !first.HasMore ||
		first.Facets.Status["200"] != 1005 || first.Facets.Status["404"] != 1 || first.Facets.Size.Max != 1006 {
		t.Fatalf("first page is not server-authoritative: %#v, err=%v", first, err)
	}
	second, err := store.ExploreResults(context.Background(), session.ID, ResultExplorerQuery{Offset: 1000, Limit: 1000})
	if err != nil || second.Total != 1006 || len(second.Results) != 6 || second.HasMore || second.Facets.Status["200"] != 1005 {
		t.Fatalf("second page is not server-authoritative: %#v, err=%v", second, err)
	}
}

func TestTerminalSearchProvidesUnlimitedPagingSearchAndSeverityFacets(t *testing.T) {
	store, err := OpenStore(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	session := testSession("terminal-search")
	if err := store.CreateSession(context.Background(), &session); err != nil {
		t.Fatal(err)
	}
	logs := []TerminalEntry{
		{At: time.Now().UTC(), Level: "info", Message: "scanner started"},
		{At: time.Now().UTC(), Level: "warning", Message: "request timeout one"},
		{At: time.Now().UTC(), Level: "error", Message: "request timeout two"},
	}
	if _, _, err := store.InsertResultBatch(context.Background(), session.ID, nil, logs); err != nil {
		t.Fatal(err)
	}
	page, err := store.SearchTerminal(context.Background(), session.ID, TerminalQuery{
		Search: "timeout", Levels: []string{"error"}, Limit: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 1 || len(page.Entries) != 1 || page.Entries[0].Level != "error" || page.Levels["warning"] != 1 {
		t.Fatalf("terminal search = %#v", page)
	}
}

func TestTerminalSearchLatestPagesBackwardWithinStableSnapshot(t *testing.T) {
	store, err := OpenStore(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	session := testSession("terminal-cursor")
	if err := store.CreateSession(context.Background(), &session); err != nil {
		t.Fatal(err)
	}
	logs := make([]TerminalEntry, 2505)
	for index := range logs {
		logs[index] = TerminalEntry{At: time.Now().UTC(), Level: "info", Message: fmt.Sprintf("line %04d", index)}
	}
	if _, _, err := store.InsertResultBatch(context.Background(), session.ID, nil, logs); err != nil {
		t.Fatal(err)
	}
	first, err := store.SearchTerminal(context.Background(), session.ID, TerminalQuery{Latest: true, Limit: 1000})
	if err != nil {
		t.Fatal(err)
	}
	if first.Total != 2505 || len(first.Entries) != 1000 || !first.HasOlder || first.SnapshotID == 0 ||
		first.Entries[0].ID != first.NextBeforeID {
		t.Fatalf("latest terminal page = %#v", first)
	}
	if _, _, err := store.InsertResultBatch(context.Background(), session.ID, nil, []TerminalEntry{{
		At: time.Now().UTC(), Level: "error", Message: "arrived after snapshot",
	}}); err != nil {
		t.Fatal(err)
	}
	before, snapshot := first.NextBeforeID, first.SnapshotID
	second, err := store.SearchTerminal(context.Background(), session.ID, TerminalQuery{
		Latest: true, BeforeID: &before, SnapshotID: &snapshot, Limit: 1000,
	})
	if err != nil {
		t.Fatal(err)
	}
	if second.Total != 2505 || len(second.Entries) != 1000 || !second.HasOlder ||
		second.Entries[len(second.Entries)-1].ID >= first.Entries[0].ID || second.Levels["error"] != 0 {
		t.Fatalf("second terminal page escaped its snapshot: %#v", second)
	}
	before = second.NextBeforeID
	third, err := store.SearchTerminal(context.Background(), session.ID, TerminalQuery{
		Latest: true, BeforeID: &before, SnapshotID: &snapshot, Limit: 1000,
	})
	if err != nil || len(third.Entries) != 505 || third.HasOlder {
		t.Fatalf("oldest terminal page = %#v, err=%v", third, err)
	}
}

func TestCompareSessionsIsBoundedAndCursorPageableWithExactTotals(t *testing.T) {
	store, err := OpenStore(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	left, right := testSession("comparison-left"), testSession("comparison-right")
	if err := store.CreateSession(context.Background(), &left); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateSession(context.Background(), &right); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	leftResults, rightResults := make([]StoredResult, 0, 505), make([]StoredResult, 0, 505)
	for index := 0; index < 230; index++ {
		location := fmt.Sprintf("https://example.test/common-%04d", index)
		leftResults = append(leftResults, StoredResult{URL: location, StatusCode: 200, CreatedAt: now})
		rightResults = append(rightResults, StoredResult{URL: location, StatusCode: 200, CreatedAt: now})
	}
	for index := 0; index < 275; index++ {
		leftResults = append(leftResults, StoredResult{URL: fmt.Sprintf("https://example.test/removed-%04d", index), StatusCode: 404, CreatedAt: now})
		rightResults = append(rightResults, StoredResult{URL: fmt.Sprintf("https://example.test/added-%04d", index), StatusCode: 200, CreatedAt: now})
	}
	if _, _, err := store.InsertResultBatch(context.Background(), left.ID, leftResults, nil); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.InsertResultBatch(context.Background(), right.ID, rightResults, nil); err != nil {
		t.Fatal(err)
	}
	first, err := store.CompareSessionsPage(context.Background(), left.ID, right.ID, SessionComparisonQuery{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if first.AddedTotal != 275 || first.RemovedTotal != 275 || first.UnchangedCount != 230 ||
		len(first.Added) != 100 || len(first.Removed) != 100 || !first.AddedHasMore || !first.RemovedHasMore ||
		first.AddedNextCursor == "" || first.RemovedNextCursor == "" {
		t.Fatalf("first comparison page = %#v", first)
	}
	second, err := store.CompareSessionsPage(context.Background(), left.ID, right.ID, SessionComparisonQuery{
		Side: "added", Cursor: first.AddedNextCursor, Limit: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	if second.AddedTotal != 275 || second.RemovedTotal != 275 || second.UnchangedCount != 230 ||
		len(second.Added) != 100 || len(second.Removed) != 0 || !second.AddedHasMore ||
		second.Added[0].URL <= first.Added[len(first.Added)-1].URL {
		t.Fatalf("second comparison page = %#v", second)
	}
}

func TestPresetsAndSavedViewsRoundTrip(t *testing.T) {
	store, err := OpenStore(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	session := testSession("views")
	if err := store.CreateSession(context.Background(), &session); err != nil {
		t.Fatal(err)
	}
	preset, err := store.PutPreset(context.Background(), Preset{Name: "Authenticated API", Wordlist: "final.txt",
		Profile: "php", Options: session.Options, ExplicitFlags: []string{"H", "e"}})
	if err != nil {
		t.Fatal(err)
	}
	loadedPreset, err := store.Preset(context.Background(), preset.ID)
	if err != nil || loadedPreset.Name != preset.Name || len(loadedPreset.ExplicitFlags) != 2 {
		t.Fatalf("preset = %#v, err=%v", loadedPreset, err)
	}
	query, _ := json.Marshal(map[string]any{"statuses": []int{200}, "sort": "url"})
	view, err := store.SaveResultView(context.Background(), session.ID, SaveResultViewRequest{Name: "Success", Query: query})
	if err != nil {
		t.Fatal(err)
	}
	views, err := store.ResultViews(context.Background(), session.ID)
	if err != nil || len(views) != 1 || views[0].ID != view.ID {
		t.Fatalf("views = %#v, err=%v", views, err)
	}
}

func TestSavedResultViewsRejectQueriesTheExplorerCannotReplay(t *testing.T) {
	store, err := OpenStore(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	session := testSession("invalid-views")
	if err := store.CreateSession(context.Background(), &session); err != nil {
		t.Fatal(err)
	}

	invalid := []json.RawMessage{
		json.RawMessage(`null`),
		json.RawMessage(`{"unknown":true}`),
		json.RawMessage(`{"sort":"hash"}`),
		json.RawMessage(`{"order":"sideways"}`),
		json.RawMessage(`{"sizeMin":20,"sizeMax":10}`),
		json.RawMessage(`{"statuses":[999]}`),
	}
	for _, query := range invalid {
		if _, err := store.SaveResultView(context.Background(), session.ID, SaveResultViewRequest{
			Name: "invalid", Query: query,
		}); err == nil {
			t.Errorf("SaveResultView(%s) succeeded, want validation error", query)
		}
	}

	valid := json.RawMessage(`{"query":"admin","statuses":[200,204],"contentTypes":["text/html"],"sizeMin":0,"sort":"url","order":"asc","groupDuplicates":true}`)
	if _, err := store.SaveResultView(context.Background(), session.ID, SaveResultViewRequest{Name: "valid", Query: valid}); err != nil {
		t.Fatalf("SaveResultView(valid) = %v", err)
	}
}

func TestProductFeatureRoutesValidateAndServeExplorer(t *testing.T) {
	server := testServer(t)
	session := testSession("feature-routes")
	if err := server.store.CreateSession(context.Background(), &session); err != nil {
		t.Fatal(err)
	}
	if _, _, err := server.store.InsertResultBatch(context.Background(), session.ID, []StoredResult{{
		URL: "https://example.test/admin", StatusCode: 200, ContentType: "text/html", CreatedAt: time.Now().UTC(),
	}}, nil); err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, authenticatedRequest(http.MethodGet,
		"/api/v1/sessions/feature-routes/results/explore?status=200&sort=url", nil))
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"total":1`) {
		t.Fatalf("explorer route = %d %s", recorder.Code, recorder.Body.String())
	}
	recorder = httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, authenticatedRequest(http.MethodGet,
		"/api/v1/sessions/feature-routes/results/explore?size_min=-1", nil))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("invalid explorer query = %d %s", recorder.Code, recorder.Body.String())
	}
	recorder = httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, authenticatedRequest(http.MethodGet,
		"/api/v1/sessions/feature-routes/results/tree?limit=1000", nil))
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"result_count":1`) ||
		!strings.Contains(recorder.Body.String(), `"snapshot_id":`) {
		t.Fatalf("tree route = %d %s", recorder.Code, recorder.Body.String())
	}
	recorder = httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, authenticatedRequest(http.MethodGet,
		"/api/v1/sessions/feature-routes/results/tree?snapshot_id=-1", nil))
	if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), `"code":"invalid_tree_snapshot"`) {
		t.Fatalf("invalid tree snapshot = %d %s", recorder.Code, recorder.Body.String())
	}
}
