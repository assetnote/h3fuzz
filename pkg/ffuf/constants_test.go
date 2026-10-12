package ffuf

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRuntimeStateUsesH3fuzzNamespace(t *testing.T) {
	if got := filepath.Base(CONFIGDIR); got != "h3fuzz" {
		t.Fatalf("CONFIGDIR namespace = %q, want h3fuzz", got)
	}

	for name, dir := range map[string]string{
		"history":         HISTORYDIR,
		"scraper":         SCRAPERDIR,
		"autocalibration": AUTOCALIBDIR,
	} {
		if got := filepath.Dir(dir); got != CONFIGDIR {
			t.Errorf("%s directory parent = %q, want %q", name, got, CONFIGDIR)
		}
	}
}

func TestReadDefaultConfigKeepsDotFfufrcCompatibility(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	if err := os.MkdirAll(home, 0o750); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)

	originalConfigDir := CONFIGDIR
	originalHistoryDir := HISTORYDIR
	originalScraperDir := SCRAPERDIR
	originalAutocalibDir := AUTOCALIBDIR
	t.Cleanup(func() {
		CONFIGDIR = originalConfigDir
		HISTORYDIR = originalHistoryDir
		SCRAPERDIR = originalScraperDir
		AUTOCALIBDIR = originalAutocalibDir
	})

	stateRoot := filepath.Join(root, "xdg")
	CONFIGDIR = filepath.Join(stateRoot, "h3fuzz")
	HISTORYDIR = filepath.Join(CONFIGDIR, "history")
	SCRAPERDIR = filepath.Join(CONFIGDIR, "scraper")
	AUTOCALIBDIR = filepath.Join(CONFIGDIR, "autocalibration")

	legacyConfig := []byte("[general]\nthreads = 731\n")
	if err := os.WriteFile(filepath.Join(home, ".ffufrc"), legacyConfig, 0o600); err != nil {
		t.Fatal(err)
	}

	options, err := ReadDefaultConfig()
	if err != nil {
		t.Fatalf("ReadDefaultConfig() error = %v", err)
	}
	if got := options.General.Threads; got != 731 {
		t.Fatalf("threads loaded from ~/.ffufrc = %d, want 731", got)
	}
	if _, err := os.Stat(filepath.Join(stateRoot, "ffuf")); !os.IsNotExist(err) {
		t.Fatalf("legacy ffuf state directory was created: err = %v", err)
	}
}
