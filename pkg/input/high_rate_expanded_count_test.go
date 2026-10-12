package input

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/assetnote/h3fuzz/pkg/ffuf"
)

func TestHighRateSizingUsesPostExtensionInputTotal(t *testing.T) {
	wordlist := filepath.Join(t.TempDir(), "words.txt")
	if err := os.WriteFile(wordlist, []byte("admin\nlogin\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	conf := ffuf.NewConfig(context.Background(), func() {})
	conf.Url = "https://example.test/FUZZ"
	conf.Extensions = []string{".php", ".bak"}
	provider, err := NewWordlistInput("FUZZ", wordlist, &conf)
	if err != nil {
		t.Fatal(err)
	}
	if provider.Total() != 6 {
		t.Fatalf("post-extension total = %d, want 6", provider.Total())
	}

	conf.Threads = 256
	conf.Http3Connections = 1
	conf.Http3MaxConnections = 64
	conf.Http3Streams = 256
	conf.Http3MaxWorkers = ffuf.MaxHTTP3Workers
	conf.HighRatePolicy = &ffuf.HighRatePolicyReport{
		Enabled: true, TargetRPS: 1, LatencyMS: 1, LatencySource: "explicit", Headroom: 1.25,
		ConnectionMode: "fixed", ConnectionCapacity: 1, StreamsPerConnection: 256,
		HTTP3MaxWorkers: ffuf.MaxHTTP3Workers, HardWorkerLimit: ffuf.MaxHTTP3Workers,
	}
	if err := ffuf.FinalizeHighRatePolicy(&conf, provider.Total()); err != nil {
		t.Fatal(err)
	}
	if conf.HighRatePolicy.ExpandedInputs != 6 || conf.Threads != 6 {
		t.Fatalf("expanded-input sizing = threads %d policy %+v", conf.Threads, conf.HighRatePolicy)
	}
}
