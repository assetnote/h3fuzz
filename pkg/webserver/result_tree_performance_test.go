package webserver

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/assetnote/h3fuzz/pkg/ffuf"
)

func TestExploreResultTreeSQLFilterMatchesRuntimeSemantics(t *testing.T) {
	for _, mode := range []string{"or", "and"} {
		t.Run(mode, func(t *testing.T) {
			store, err := OpenStore(":memory:")
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			session := testSession("tree-filter-" + mode)
			session.ResultFilters = ffuf.FilterOptions{Mode: mode, Status: "404", Words: "7-9", Time: ">100"}
			if err := store.CreateSession(context.Background(), &session); err != nil {
				t.Fatal(err)
			}
			results := []StoredResult{
				{URL: "https://example.test/all", StatusCode: 404, ContentWords: 8, DurationNS: int64(150 * time.Millisecond), CreatedAt: time.Now().UTC()},
				{URL: "https://example.test/status", StatusCode: 404, ContentWords: 1, DurationNS: int64(50 * time.Millisecond), CreatedAt: time.Now().UTC()},
				{URL: "https://example.test/words", StatusCode: 200, ContentWords: 8, DurationNS: int64(50 * time.Millisecond), CreatedAt: time.Now().UTC()},
				{URL: "https://example.test/none", StatusCode: 200, ContentWords: 1, DurationNS: int64(50 * time.Millisecond), CreatedAt: time.Now().UTC()},
			}
			if _, _, err := store.InsertResultBatch(context.Background(), session.ID, results, nil); err != nil {
				t.Fatal(err)
			}
			page, err := store.ExploreResultTree(context.Background(), session.ID, ResultExplorerQuery{Limit: 100}, resultTreeScope{})
			if err != nil {
				t.Fatal(err)
			}
			want := int64(1)
			if mode == "and" {
				want = 3
			}
			if len(page.Nodes) != 1 || page.Nodes[0].ResultCount != want {
				t.Fatalf("%s tree count = %#v, want %d", mode, page.Nodes, want)
			}
		})
	}
}

func TestResultTreePathMetadataPreservesEscapedAndEmptySegments(t *testing.T) {
	store, err := OpenStore(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	session := testSession("tree-path-encoding")
	if err := store.CreateSession(context.Background(), &session); err != nil {
		t.Fatal(err)
	}
	results := []StoredResult{
		{URL: "https://example.test/a%2Fb/item?x=1", StatusCode: 200, CreatedAt: time.Now().UTC()},
		{URL: "https://example.test/a//leaf", StatusCode: 200, CreatedAt: time.Now().UTC()},
		{URL: "https://example.test/dir/", StatusCode: 200, CreatedAt: time.Now().UTC()},
	}
	if _, _, err := store.InsertResultBatch(context.Background(), session.ID, results, nil); err != nil {
		t.Fatal(err)
	}
	query := ResultExplorerQuery{Limit: 100}
	root, err := store.ExploreResultTree(context.Background(), session.ID, query, resultTreeScope{})
	if err != nil || len(root.Nodes) != 1 {
		t.Fatalf("root = %#v, err=%v", root, err)
	}
	query.SnapshotID = &root.SnapshotID
	origin, _ := decodeResultTreeScope(root.Nodes[0].Key)
	children, err := store.ExploreResultTree(context.Background(), session.ID, query, origin)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(children.Nodes))
	for _, node := range children.Nodes {
		names = append(names, node.Name)
	}
	if strings.Join(names, ",") != "a,a/b,dir" {
		t.Fatalf("top-level names = %q", names)
	}
	var encodedScope resultTreeScope
	for _, node := range children.Nodes {
		if node.Name == "a/b" {
			encodedScope, err = decodeResultTreeScope(node.Key)
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	encodedChildren, err := store.ExploreResultTree(context.Background(), session.ID, query, encodedScope)
	if err != nil || len(encodedChildren.Nodes) != 1 || encodedChildren.Nodes[0].Name != "item?x=1" {
		t.Fatalf("encoded children = %#v, err=%v", encodedChildren, err)
	}
}

func TestResultTreeDeepScopeUsesPrefixIndex(t *testing.T) {
	store, err := OpenStore(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	rows, err := store.db.Query(`EXPLAIN QUERY PLAN SELECT id FROM results
		WHERE session_id=? AND tree_origin=? AND tree_path_key>=? AND tree_path_key<? AND id<=?`,
		"session", "https://example.test", "top/narrow/", "top/narrow0", 1000)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	plan := ""
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		plan += detail + "\n"
	}
	if !strings.Contains(plan, "results_tree_scope") || !strings.Contains(plan, "tree_path_key>?") {
		t.Fatalf("deep tree query does not use bounded prefix index:\n%s", plan)
	}
}

func TestResultTreeMetadataBackfillIsIdempotent(t *testing.T) {
	store, err := OpenStore(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	session := testSession("tree-backfill")
	if err := store.CreateSession(context.Background(), &session); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.InsertResultBatch(context.Background(), session.ID, []StoredResult{{
		URL: "https://example.test/legacy/item", StatusCode: 200, CreatedAt: time.Now().UTC(),
	}}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE results SET tree_origin='',tree_path_key='',tree_suffix='',tree_trailing_slash=0 WHERE session_id=?`, session.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.backfillResultTreeMetadata(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := store.backfillResultTreeMetadata(context.Background()); err != nil {
		t.Fatal(err)
	}
	var origin, pathKey string
	if err := store.db.QueryRow(`SELECT tree_origin,tree_path_key FROM results WHERE session_id=?`, session.ID).Scan(&origin, &pathKey); err != nil {
		t.Fatal(err)
	}
	if origin != "https://example.test" || pathKey != "legacy/item" {
		t.Fatalf("backfilled metadata = %q %q", origin, pathKey)
	}
	if page, err := store.ExploreResultTree(context.Background(), session.ID, ResultExplorerQuery{Limit: 100}, resultTreeScope{}); err != nil || page.Total != 1 {
		t.Fatalf("backfilled tree = %#v, err=%v", page, err)
	}
}

func TestExploreResultTreeRetainsTotalBeyondLastPage(t *testing.T) {
	store, err := OpenStore(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	session := testSession("tree-empty-page-total")
	if err := store.CreateSession(context.Background(), &session); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.InsertResultBatch(context.Background(), session.ID, []StoredResult{{
		URL: "https://example.test/dir/item", StatusCode: 200, CreatedAt: time.Now().UTC(),
	}}, nil); err != nil {
		t.Fatal(err)
	}
	query := ResultExplorerQuery{Limit: 10, Offset: 99}
	root, err := store.ExploreResultTree(context.Background(), session.ID, query, resultTreeScope{})
	if err != nil || root.Total != 1 || len(root.Nodes) != 0 {
		t.Fatalf("root page = %#v, err=%v", root, err)
	}
	query.SnapshotID = &root.SnapshotID
	children, err := store.ExploreResultTree(context.Background(), session.ID, query,
		resultTreeScope{Origin: "https://example.test", Segments: []string{"dir"}})
	if err != nil || children.Total != 1 || len(children.Nodes) != 0 {
		t.Fatalf("child page = %#v, err=%v", children, err)
	}
}

func Example_resultTreePrefixRange() {
	prefix := resultTreePathKey([]string{"top", "narrow"}) + "/"
	fmt.Println(prefix, strings.TrimSuffix(prefix, "/")+"0")
	// Output: top/narrow/ top/narrow0
}
