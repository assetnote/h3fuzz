package webserver

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// BenchmarkExploreResultTreeLargeSession exercises the shape that previously
// made every expansion proportional to the entire scan. The selected directory
// contains only one percent of the rows, while the session has many siblings.
func BenchmarkExploreResultTreeLargeSession(b *testing.B) {
	store, err := OpenStore(":memory:")
	if err != nil {
		b.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	session := testSession("tree-benchmark")
	if err := store.CreateSession(ctx, &session); err != nil {
		b.Fatal(err)
	}
	const resultCount = 500_000
	now := time.Now().UTC()
	for start := 0; start < resultCount; start += 1_000 {
		batch := make([]StoredResult, 0, 1_000)
		for index := start; index < start+1_000 && index < resultCount; index++ {
			batch = append(batch, StoredResult{
				URL:        fmt.Sprintf("https://example.test/top/bucket-%03d/item-%06d", index/500, index),
				StatusCode: 200, ContentType: "text/html", CreatedAt: now,
			})
		}
		if _, _, err := store.InsertResultBatch(ctx, session.ID, batch, nil); err != nil {
			b.Fatal(err)
		}
	}
	var snapshot int64
	if err := store.db.QueryRow(`SELECT MAX(id) FROM results WHERE session_id=?`, session.ID).Scan(&snapshot); err != nil {
		b.Fatal(err)
	}
	query := ResultExplorerQuery{Limit: 1_000, SnapshotID: &snapshot}

	b.Run("root", func(b *testing.B) {
		for iteration := 0; iteration < b.N; iteration++ {
			if _, err := store.ExploreResultTree(ctx, session.ID, query, resultTreeScope{}); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("narrow-directory", func(b *testing.B) {
		parent := resultTreeScope{Origin: "https://example.test", Segments: []string{"top", "bucket-050"}}
		for iteration := 0; iteration < b.N; iteration++ {
			if _, err := store.ExploreResultTree(ctx, session.ID, query, parent); err != nil {
				b.Fatal(err)
			}
		}
	})
}
