package input

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"strings"
	"testing"

	"github.com/assetnote/h3fuzz/pkg/ffuf"
)

var (
	regularFileWordlistSink      *WordlistInput
	regularFileWordlistValueSink []byte
	legacyWordlistSink           [][]byte
)

func writeWordlistContents(t testing.TB, contents []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "wordlist.txt")
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func writeRegularFileWordlist(t testing.TB, entries int) string {
	t.Helper()

	var contents bytes.Buffer
	contents.Grow(entries * 16)
	for n := 0; n < entries; n++ {
		fmt.Fprintf(&contents, "word-%08d\n", n)
	}

	return writeWordlistContents(t, contents.Bytes())
}

func collectWordlistValues(input *WordlistInput) []string {
	values := make([]string, 0, input.Total())
	for input.Next() {
		values = append(values, string(input.Value()))
		input.IncrementPosition()
	}
	return values
}

func TestRegularFileWordlistPreservesLineAndCursorSemantics(t *testing.T) {
	for name, contents := range map[string]string{
		"LF with terminal newline":   "alpha\n\nbeta\n",
		"CRLF without final newline": "alpha\r\n\r\nbeta",
	} {
		t.Run(name, func(t *testing.T) {
			input := newRegularFileWordlist(t, writeWordlistContents(t, []byte(contents)))
			want := []string{"alpha", "", "beta"}
			if got := collectWordlistValues(input); !reflect.DeepEqual(got, want) {
				t.Fatalf("values = %#v, want %#v", got, want)
			}
			if input.Next() {
				t.Fatal("Next returned true after exhaustion")
			}
			if got := input.Position(); got != input.Total() {
				t.Fatalf("position after exhaustion = %d, total = %d", got, input.Total())
			}

			input.ResetPosition()
			if got := string(input.Value()); got != "alpha" {
				t.Fatalf("first value after reset = %q, want alpha", got)
			}
			input.SetPosition(2)
			if got := string(input.Value()); got != "beta" {
				t.Fatalf("value at position 2 = %q, want beta", got)
			}
		})
	}
}

func TestRegularFileWordlistValuesRemainIndependentlyAppendable(t *testing.T) {
	input := newRegularFileWordlist(t, writeWordlistContents(t, []byte("a\nb\n")))
	first := input.Value()
	first[0] = 'x'
	first = append(first, 'y')
	if got := string(first); got != "xy" {
		t.Fatalf("appended first value = %q, want xy", got)
	}
	input.SetPosition(1)
	if got := string(input.Value()); got != "b" {
		t.Fatalf("appending to first value changed second value to %q", got)
	}
	input.ResetPosition()
	if got := string(input.Value()); got != "x" {
		t.Fatalf("in-place mutation was not retained: got %q, want x", got)
	}
}

func TestRegularFileWordlistPreservesCommentSemantics(t *testing.T) {
	path := writeWordlistContents(t, []byte(" # spaced comment\n#comment\n\t#not-a-comment\nkeep # trailing\nhash#inside\n\n"))
	config := ffuf.NewConfig(context.Background(), func() {})
	config.IgnoreWordlistComments = true
	input, err := NewWordlistInput("FUZZ", path, &config)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"\t#not-a-comment", "keep", "hash#inside", ""}
	if got := collectWordlistValues(input); !reflect.DeepEqual(got, want) {
		t.Fatalf("values = %#v, want %#v", got, want)
	}
}

func TestRegularFileWordlistPreservesExtensionExpansionSemantics(t *testing.T) {
	t.Run("suffix expansion applies only to FUZZ", func(t *testing.T) {
		path := writeWordlistContents(t, []byte("admin\n"))
		for _, tc := range []struct {
			keyword string
			want    []string
		}{
			{keyword: "FUZZ", want: []string{"admin", "admin.php", "admin.bak"}},
			{keyword: "OTHER", want: []string{"admin"}},
		} {
			config := ffuf.NewConfig(context.Background(), func() {})
			config.Extensions = []string{".php", ".bak"}
			input, err := NewWordlistInput(tc.keyword, path, &config)
			if err != nil {
				t.Fatal(err)
			}
			if got := collectWordlistValues(input); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("keyword %s values = %#v, want %#v", tc.keyword, got, tc.want)
			}
		}
	})

	t.Run("dirsearch marker order and comment handling", func(t *testing.T) {
		path := writeWordlistContents(t, []byte("# comment\nplain # trailing\nindex.%ExT% # retained\n"))
		config := ffuf.NewConfig(context.Background(), func() {})
		config.DirSearchCompat = true
		config.IgnoreWordlistComments = true
		config.Extensions = []string{"php", "html"}
		input, err := NewWordlistInput("FUZZ", path, &config)
		if err != nil {
			t.Fatal(err)
		}
		want := []string{"plain", "index.php # retained", "index.html # retained"}
		if got := collectWordlistValues(input); !reflect.DeepEqual(got, want) {
			t.Fatalf("values = %#v, want %#v", got, want)
		}
	})
}

func TestRegularFileWordlistPreservesScannerErrorAndPartialInput(t *testing.T) {
	contents := append([]byte("first\n"), bytes.Repeat([]byte("x"), maxWordlistLineSize+1)...)
	contents = append(contents, '\n')
	path := writeWordlistContents(t, contents)
	config := ffuf.NewConfig(context.Background(), func() {})
	input, err := NewWordlistInput("FUZZ", path, &config)
	if err == nil {
		t.Fatal("oversized wordlist entry was accepted")
	}
	if got := input.Total(); got != 1 {
		t.Fatalf("entries retained before scanner error = %d, want 1", got)
	}
	if got := string(input.Value()); got != "first" {
		t.Fatalf("retained value = %q, want first", got)
	}
}

func TestRegularFileWordlistAcceptsExactMaximumLogicalLine(t *testing.T) {
	payload := bytes.Repeat([]byte("x"), maxWordlistLineSize)
	for name, suffix := range map[string][]byte{
		"no final newline": nil,
		"LF":               {'\n'},
		"CRLF":             {'\r', '\n'},
	} {
		t.Run(name, func(t *testing.T) {
			contents := append(append([]byte(nil), payload...), suffix...)
			input := newRegularFileWordlist(t, writeWordlistContents(t, contents))
			if got := input.Total(); got != 1 {
				t.Fatalf("entries = %d, want 1", got)
			}
			if got := len(input.Value()); got != maxWordlistLineSize {
				t.Fatalf("value length = %d, want %d", got, maxWordlistLineSize)
			}
		})
	}
}

func TestRegularFileWordlistMarkerErrorDiscardsPartialInput(t *testing.T) {
	path := writeWordlistContents(t, []byte("first\nindex.%EXT%\n"))
	config := ffuf.NewConfig(context.Background(), func() {})
	config.Url = "https://example.test/FUZZ"
	input, err := NewWordlistInput("FUZZ", path, &config)
	if err == nil {
		t.Fatal("unexpanded marker was accepted")
	}
	if got := input.Total(); got != 0 {
		t.Fatalf("entries retained after marker error = %d, want 0", got)
	}
}

func TestStdinWordlistPreservesStreamSemantics(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(writer, "alpha\r\n\r\nbeta"); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	originalStdin := os.Stdin
	os.Stdin = reader
	t.Cleanup(func() { os.Stdin = originalStdin })

	config := ffuf.NewConfig(context.Background(), func() {})
	input, err := NewWordlistInput("FUZZ", "-", &config)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"alpha", "", "beta"}
	if got := collectWordlistValues(input); !reflect.DeepEqual(got, want) {
		t.Fatalf("values = %#v, want %#v", got, want)
	}
}

func TestRegularFileWordlistsIntegrateWithModesAndEncoders(t *testing.T) {
	firstPath := writeWordlistContents(t, []byte("a\nb\n"))
	secondPath := writeWordlistContents(t, []byte("1\n2\n3\n"))
	tests := []struct {
		mode     string
		encoder  string
		expected []map[string]string
	}{
		{
			mode:    "pitchfork",
			encoder: "b64encode",
			expected: []map[string]string{
				{"FUZZ": "YQ==", "FUZ2Z": "1"},
				{"FUZZ": "Yg==", "FUZ2Z": "2"},
				{"FUZZ": "YQ==", "FUZ2Z": "3"},
			},
		},
		{
			mode: "clusterbomb",
			expected: []map[string]string{
				{"FUZZ": "a", "FUZ2Z": "1"},
				{"FUZZ": "b", "FUZ2Z": "1"},
				{"FUZZ": "a", "FUZ2Z": "2"},
				{"FUZZ": "b", "FUZ2Z": "2"},
				{"FUZZ": "a", "FUZ2Z": "3"},
				{"FUZZ": "b", "FUZ2Z": "3"},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.mode, func(t *testing.T) {
			config := ffuf.NewConfig(context.Background(), func() {})
			config.InputMode = tc.mode
			config.InputProviders = []ffuf.InputProviderConfig{
				{Name: "wordlist", Keyword: "FUZZ", Value: firstPath, Encoders: tc.encoder},
				{Name: "wordlist", Keyword: "FUZ2Z", Value: secondPath},
			}
			provider, errs := NewInputProvider(&config)
			if err := errs.ErrorOrNil(); err != nil {
				t.Fatal(err)
			}
			got := make([]map[string]string, 0, provider.Total())
			for provider.Next() {
				current := make(map[string]string)
				for keyword, value := range provider.Value() {
					current[keyword] = string(value)
				}
				got = append(got, current)
			}
			if !reflect.DeepEqual(got, tc.expected) {
				t.Fatalf("values = %#v, want %#v", got, tc.expected)
			}
			provider.Reset()
			if !provider.Next() {
				t.Fatal("Next returned false after Reset")
			}
			if got := provider.Value(); got["FUZ2Z"][0] != '1' {
				t.Fatalf("first value after Reset = %#v", got)
			}
		})
	}
}

func BenchmarkRegularFileWordlistLoad(b *testing.B) {
	const entries = 128 * 1024
	path := writeRegularFileWordlist(b, entries)
	b.ReportAllocs()
	b.ReportMetric(entries, "entries/op")
	b.ResetTimer()
	for n := 0; n < b.N; n++ {
		regularFileWordlistSink = newRegularFileWordlist(b, path)
	}
}

func BenchmarkLegacyObjectPerLineWordlistLoad(b *testing.B) {
	const entries = 128 * 1024
	path := writeRegularFileWordlist(b, entries)
	b.ReportAllocs()
	b.ReportMetric(entries, "entries/op")
	b.ResetTimer()
	for n := 0; n < b.N; n++ {
		legacyWordlistSink = loadLegacyObjectPerLineWordlist(b, path)
	}
}

// loadLegacyObjectPerLineWordlist mirrors the pre-arena regular-file path for
// a plain wordlist. Keeping it test-only makes the allocation comparison
// reproducible without retaining a second production implementation.
func loadLegacyObjectPerLineWordlist(t testing.TB, path string) [][]byte {
	t.Helper()
	config := ffuf.NewConfig(context.Background(), func() {})
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
	validationFile, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := validationFile.Close(); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()

	reader := bufio.NewScanner(file)
	reader.Buffer(nil, maxWordlistLineSize)
	extensionMarker := regexp.MustCompile(`(?i)%ext%`)
	var values [][]byte
	for reader.Scan() {
		if text := reader.Text(); extensionMarker.MatchString(text) && strings.Contains(config.Url, "FUZZ") {
			t.Fatalf("unexpected extension marker in generated benchmark wordlist")
		}
		values = append(values, []byte(reader.Text()))
	}
	if err := reader.Err(); err != nil {
		t.Fatal(err)
	}
	return values
}

func BenchmarkRegularFileWordlistValue(b *testing.B) {
	const entries = 128 * 1024
	input := newRegularFileWordlist(b, writeRegularFileWordlist(b, entries))
	b.ReportAllocs()
	b.ResetTimer()
	for n := 0; n < b.N; n++ {
		input.SetPosition(n % entries)
		regularFileWordlistValueSink = input.Value()
	}
}

func newRegularFileWordlist(t testing.TB, path string) *WordlistInput {
	t.Helper()
	config := ffuf.NewConfig(context.Background(), func() {})
	input, err := NewWordlistInput("FUZZ", path, &config)
	if err != nil {
		t.Fatalf("load regular-file wordlist: %v", err)
	}
	return input
}

func TestRegularFileWordlistLoadAllocationBudget(t *testing.T) {
	if raceDetectorEnabled {
		t.Skip("race instrumentation changes allocation accounting")
	}
	const entries = 32 * 1024
	path := writeRegularFileWordlist(t, entries)

	allocs := testing.AllocsPerRun(1, func() {
		regularFileWordlistSink = newRegularFileWordlist(t, path)
	})
	if allocs > 256 {
		t.Fatalf("regular-file load allocated %.0f objects for %d entries, want <= 256", allocs, entries)
	}
	if got := regularFileWordlistSink.Total(); got != entries {
		t.Fatalf("loaded entries = %d, want %d", got, entries)
	}
}

func TestRegularFileWordlistRetainedHeapBudget(t *testing.T) {
	if raceDetectorEnabled {
		t.Skip("race instrumentation changes retained-heap accounting")
	}
	const entries = 128 * 1024
	path := writeRegularFileWordlist(t, entries)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	runtime.GC()
	var before runtime.MemStats
	runtime.ReadMemStats(&before)
	input := newRegularFileWordlist(t, path)
	runtime.GC()
	var after runtime.MemStats
	runtime.ReadMemStats(&after)
	runtime.KeepAlive(input)

	retainedObjects := int64(after.HeapObjects) - int64(before.HeapObjects)
	retainedBytes := int64(after.HeapAlloc) - int64(before.HeapAlloc)
	if retainedObjects > 512 {
		t.Fatalf("retained heap objects = %d for %d entries, want <= 512", retainedObjects, entries)
	}
	maxRetainedBytes := 2*info.Size() + 1024*1024
	if retainedBytes > maxRetainedBytes {
		t.Fatalf("retained heap bytes = %d for a %d-byte wordlist, want <= %d", retainedBytes, info.Size(), maxRetainedBytes)
	}
	t.Logf("retained heap: %d objects, %d bytes for %d entries (%d file bytes)", retainedObjects, retainedBytes, entries, info.Size())
}
