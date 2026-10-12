package main

import (
	"crypto/tls"
	"os"
	"path/filepath"
	"testing"
)

func TestParseCommandConfigClipsReportedEffectiveStreamLimit(t *testing.T) {
	t.Parallel()

	config, err := parseCommandConfig([]string{
		"-listen", "127.0.0.1:9443",
		"-admin-listen", "127.0.0.1:9090",
		"-max-incoming-streams", "2305843009213693952",
	})
	if err != nil {
		t.Fatalf("parseCommandConfig: %v", err)
	}
	if config.ConfiguredMaxIncomingStreams != 1<<61 {
		t.Fatalf("configured limit = %d, want %d", config.ConfiguredMaxIncomingStreams, int64(1<<61))
	}
	if config.EffectiveMaxIncomingStreams != 1<<60 {
		t.Fatalf("effective limit = %d, want %d", config.EffectiveMaxIncomingStreams, int64(1<<60))
	}
}

func TestEnsureCertificateCreatesAnOwnerOnlyUsablePair(t *testing.T) {
	t.Parallel()

	directory := filepath.Join(t.TempDir(), "tls")
	certFile := filepath.Join(directory, "cert.pem")
	keyFile := filepath.Join(directory, "key.pem")
	if err := ensureCertificate(certFile, keyFile); err != nil {
		t.Fatalf("ensureCertificate: %v", err)
	}
	if _, err := tls.LoadX509KeyPair(certFile, keyFile); err != nil {
		t.Fatalf("load generated pair: %v", err)
	}
	for _, path := range []string{certFile, keyFile} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat %s: %v", path, err)
		}
		if got := info.Mode().Perm(); got != 0o600 {
			t.Fatalf("%s mode = %o, want 600", path, got)
		}
	}
}
