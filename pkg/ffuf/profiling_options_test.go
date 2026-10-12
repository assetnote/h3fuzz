package ffuf

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pelletier/go-toml"
)

func TestReadConfigRejectsCLIOnlyProfilingPaths(t *testing.T) {
	tests := []struct {
		name string
		body string
		flag string
	}{
		{"cpu field name", "[output]\nprofilecpu = \"cpu.pprof\"\n", "profile-cpu"},
		{"heap snake case", "[output]\nprofile_heap = \"heap.pprof\"\n", "profile-heap"},
		{"mutex CLI spelling", "profile-mutex = \"mutex.pprof\"\n", "profile-mutex"},
		{"block CLI spelling", "[output]\nprofile-block = \"block.pprof\"\n", "profile-block"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "profile.toml")
			if err := os.WriteFile(path, []byte(test.body), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := ReadConfig(path)
			if err == nil || !strings.Contains(err.Error(), test.flag) {
				t.Fatalf("ReadConfig error = %v, want rejection naming -%s", err, test.flag)
			}
		})
	}
}

func TestProfilingPathsAreNeverSerializedIntoStoredConfiguration(t *testing.T) {
	opts := NewConfigOptions()
	opts.Output.ProfileCPU = "/secret/cpu.pprof"
	opts.Output.ProfileHeap = "/secret/heap.pprof"
	opts.Output.ProfileMutex = "/secret/mutex.pprof"
	opts.Output.ProfileBlock = "/secret/block.pprof"

	jsonData, err := json.Marshal(opts)
	if err != nil {
		t.Fatal(err)
	}
	tomlData, err := toml.Marshal(opts)
	if err != nil {
		t.Fatal(err)
	}
	for _, encoded := range [][]byte{jsonData, tomlData} {
		if strings.Contains(string(encoded), "pprof") || strings.Contains(string(encoded), "ProfileCPU") {
			t.Fatalf("CLI-only profiling path leaked into stored configuration: %s", encoded)
		}
	}
}
