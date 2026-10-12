package input

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/assetnote/h3fuzz/pkg/ffuf"
)

func TestStripCommentsIgnoresCommentLines(t *testing.T) {
	text, _ := stripComments("# text")

	if text != "" {
		t.Errorf("Returned text was not a blank string")
	}
}

func TestStripCommentsStripsCommentAfterText(t *testing.T) {
	text, _ := stripComments("text # comment")

	if text != "text" {
		t.Errorf("Comment was not stripped or pre-comment text was not returned")
	}
}

func TestWordlistAcceptsLinesLargerThanScannerDefault(t *testing.T) {
	// Issue #567 reports a real 448 KiB single-entry wordlist.
	payload := strings.Repeat("a", 448*1024)
	wordlist := filepath.Join(t.TempDir(), "large-wordlist.txt")
	if err := os.WriteFile(wordlist, []byte(payload+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	config := ffuf.NewConfig(ctx, cancel)
	input, err := NewWordlistInput("FUZZ", wordlist, &config)
	if err != nil {
		t.Fatalf("reading large wordlist entry: %v", err)
	}
	if input.Total() != 1 {
		t.Fatalf("got %d entries, want 1", input.Total())
	}
	if got := string(input.Value()); got != payload {
		t.Fatalf("payload length = %d, want %d", len(got), len(payload))
	}
}

func TestWordlistRejectsUnexpandedExtensionMarker(t *testing.T) {
	wordlist := filepath.Join(t.TempDir(), "markers.txt")
	if err := os.WriteFile(wordlist, []byte("admin\nindex.%EXT%\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	config := ffuf.NewConfig(ctx, cancel)
	config.Url = "https://example.test/FUZZ"
	_, err := NewWordlistInput("FUZZ", wordlist, &config)
	if err == nil {
		t.Fatalf("wordlist containing %%EXT%% was accepted without expansion settings")
	}
	if !strings.Contains(err.Error(), "-D -e") {
		t.Fatalf("error %q does not explain how to expand %%EXT%%", err)
	}
}

func TestWordlistExpandsExtensionMarkerInDirSearchMode(t *testing.T) {
	wordlist := filepath.Join(t.TempDir(), "markers.txt")
	if err := os.WriteFile(wordlist, []byte("index.%EXT%\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	config := ffuf.NewConfig(ctx, cancel)
	config.DirSearchCompat = true
	config.Extensions = []string{"php", "html"}
	input, err := NewWordlistInput("FUZZ", wordlist, &config)
	if err != nil {
		t.Fatalf("reading DirSearch wordlist: %v", err)
	}
	if input.Total() != 2 {
		t.Fatalf("got %d entries, want 2", input.Total())
	}
	if got := string(input.Value()); got != "index.php" {
		t.Fatalf("first expansion = %q, want index.php", got)
	}
	input.IncrementPosition()
	if got := string(input.Value()); got != "index.html" {
		t.Fatalf("second expansion = %q, want index.html", got)
	}
}
