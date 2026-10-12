package webserver

import (
	"os"
	"testing"
)

func TestOpenStoreAcceptsDotRelativePath(t *testing.T) {
	t.Chdir(t.TempDir())

	store, err := OpenStore("./h3fuzz-web.db")
	if err != nil {
		t.Fatalf("OpenStore with dot-relative path: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	if _, err := os.Stat("h3fuzz-web.db"); err != nil {
		t.Fatalf("stat relative database: %v", err)
	}
}
