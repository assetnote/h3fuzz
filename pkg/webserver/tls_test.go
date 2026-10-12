package webserver

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestGeneratedTLSCertificatePersistsAndMatchesListenHost(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "web.db")
	first, err := New(Config{ListenAddress: "127.0.0.1:0", DatabasePath: databasePath, Password: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	certificatePath, privateKeyPath := first.TLSCertificatePaths()
	if certificatePath != databasePath+".cert.pem" || privateKeyPath != databasePath+".key.pem" {
		t.Fatalf("generated TLS paths = %q, %q", certificatePath, privateKeyPath)
	}
	firstCertificate, err := os.ReadFile(certificatePath)
	if err != nil {
		t.Fatal(err)
	}
	firstKey, err := os.ReadFile(privateKeyPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := first.store.Close(); err != nil {
		t.Fatal(err)
	}

	for _, path := range []string{certificatePath, privateKeyPath} {
		info, statErr := os.Stat(path)
		if statErr != nil {
			t.Fatal(statErr)
		}
		if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
			t.Fatalf("generated TLS file %s permissions = %04o, want 0600", path, info.Mode().Perm())
		}
	}
	block, _ := pem.Decode(firstCertificate)
	if block == nil {
		t.Fatal("generated certificate is not PEM")
	}
	leaf, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	if err := leaf.VerifyHostname("127.0.0.1"); err != nil {
		t.Fatalf("generated certificate SAN does not cover listener: %v", err)
	}

	second, err := New(Config{ListenAddress: "127.0.0.1:0", DatabasePath: databasePath, Password: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = second.store.Close() })
	secondCertificate, err := os.ReadFile(certificatePath)
	if err != nil {
		t.Fatal(err)
	}
	secondKey, err := os.ReadFile(privateKeyPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(secondCertificate) != string(firstCertificate) || string(secondKey) != string(firstKey) {
		t.Fatal("generated TLS identity changed across restart")
	}
}

func TestGeneratedTLSCertificateCorruptionFailsClosed(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "web.db")
	server, err := New(Config{ListenAddress: "127.0.0.1:0", DatabasePath: databasePath, Password: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	certificatePath, _ := server.TLSCertificatePaths()
	if err := server.store.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(certificatePath, []byte("not a certificate\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	corrupt, err := New(Config{ListenAddress: "127.0.0.1:0", DatabasePath: databasePath, Password: "secret"})
	if corrupt != nil {
		_ = corrupt.store.Close()
		t.Fatal("New returned a server with a corrupt managed certificate")
	}
	if err == nil || !strings.Contains(err.Error(), "managed certificate is corrupt") {
		t.Fatalf("New error = %v, want managed certificate corruption error", err)
	}
}

func TestGeneratedTLSCertificateRotatesForChangedListenHost(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "web.db")
	first, err := New(Config{ListenAddress: "127.0.0.1:0", DatabasePath: databasePath, Password: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	certificatePath, _ := first.TLSCertificatePaths()
	before, err := os.ReadFile(certificatePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := first.store.Close(); err != nil {
		t.Fatal(err)
	}

	second, err := New(Config{ListenAddress: "localhost:0", DatabasePath: databasePath, Password: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = second.store.Close() })
	after, err := os.ReadFile(certificatePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) == string(after) {
		t.Fatal("managed certificate was not rotated after the listen host changed")
	}
	if err := second.tlsConfig.Certificates[0].Leaf.VerifyHostname("localhost"); err != nil {
		t.Fatalf("rotated certificate does not cover localhost: %v", err)
	}
}

func TestTLSConfigurationRequiresCompleteNonConflictingFlags(t *testing.T) {
	tests := []struct {
		name   string
		config Config
		want   string
	}{
		{name: "certificate only", config: Config{CertificatePath: "cert.pem"}, want: "must be supplied together"},
		{name: "key only", config: Config{PrivateKeyPath: "key.pem"}, want: "must be supplied together"},
		{name: "plain HTTP with certificate", config: Config{PlainHTTP: true, CertificatePath: "cert.pem", PrivateKeyPath: "key.pem"}, want: "cannot be combined"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			test.config.ListenAddress = "127.0.0.1:0"
			test.config.DatabasePath = filepath.Join(t.TempDir(), "web.db")
			test.config.Password = "secret"
			server, err := New(test.config)
			if server != nil {
				_ = server.store.Close()
				t.Fatal("New returned a server for an invalid TLS configuration")
			}
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("New error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func TestOperatorCertificateMustMatchListenHostAndUseProtectedKey(t *testing.T) {
	source, err := New(Config{
		ListenAddress: "127.0.0.1:0",
		DatabasePath:  filepath.Join(t.TempDir(), "source.db"),
		Password:      "secret",
	})
	if err != nil {
		t.Fatal(err)
	}
	certificatePath, privateKeyPath := source.TLSCertificatePaths()
	if err := source.store.Close(); err != nil {
		t.Fatal(err)
	}

	configured, err := New(Config{
		ListenAddress:   "127.0.0.1:0",
		DatabasePath:    filepath.Join(t.TempDir(), "configured.db"),
		Password:        "secret",
		CertificatePath: certificatePath,
		PrivateKeyPath:  privateKeyPath,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := configured.store.Close(); err != nil {
		t.Fatal(err)
	}

	mismatch, err := New(Config{
		ListenAddress:   "localhost:0",
		DatabasePath:    filepath.Join(t.TempDir(), "mismatch.db"),
		Password:        "secret",
		CertificatePath: certificatePath,
		PrivateKeyPath:  privateKeyPath,
	})
	if mismatch != nil {
		_ = mismatch.store.Close()
		t.Fatal("New accepted an operator certificate for the wrong listen host")
	}
	if err == nil || !strings.Contains(err.Error(), "does not cover web listen host") {
		t.Fatalf("New error = %v, want listen-host SAN error", err)
	}

	if runtime.GOOS != "windows" {
		if err := os.Chmod(privateKeyPath, 0o644); err != nil {
			t.Fatal(err)
		}
		insecure, err := New(Config{
			ListenAddress:   "127.0.0.1:0",
			DatabasePath:    filepath.Join(t.TempDir(), "insecure.db"),
			Password:        "secret",
			CertificatePath: certificatePath,
			PrivateKeyPath:  privateKeyPath,
		})
		if insecure != nil {
			_ = insecure.store.Close()
			t.Fatal("New accepted a group/world-readable operator private key")
		}
		if err == nil || !strings.Contains(err.Error(), "remove group/world access") {
			t.Fatalf("New error = %v, want private-key permissions error", err)
		}
	}
}

func TestServeListenerDefaultsToTLS(t *testing.T) {
	server, err := New(Config{
		ListenAddress: "127.0.0.1:0",
		DatabasePath:  filepath.Join(t.TempDir(), "web.db"),
		Username:      "h3fuzz",
		Password:      "secret",
	})
	if err != nil {
		t.Fatal(err)
	}
	listener, err := server.Listen()
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- server.ServeListener(listener) }()

	certificatePath, _ := server.TLSCertificatePaths()
	certificatePEM, err := os.ReadFile(certificatePath)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(certificatePEM) {
		t.Fatal("could not trust generated certificate")
	}
	client := &http.Client{
		Timeout: 3 * time.Second,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{
			MinVersion: tls.VersionTLS12,
			RootCAs:    roots,
		}},
	}
	request, err := http.NewRequest(http.MethodGet, server.ConsoleURL(listener)+"/api/v1/health", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.SetBasicAuth("h3fuzz", "secret")
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, response.Body)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("HTTPS health response = %d", response.StatusCode)
	}
	if !strings.HasPrefix(server.ConsoleURL(listener), "https://") {
		t.Fatalf("console URL = %q, want https", server.ConsoleURL(listener))
	}

	shutdownServer(t, server, done)
}

func TestPlainHTTPRequiresExplicitOptIn(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "web.db")
	server, err := New(Config{
		ListenAddress: "127.0.0.1:0",
		DatabasePath:  databasePath,
		Username:      "h3fuzz",
		Password:      "secret",
		PlainHTTP:     true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if certificatePath, privateKeyPath := server.TLSCertificatePaths(); certificatePath != "" || privateKeyPath != "" {
		t.Fatalf("plain HTTP generated TLS paths %q, %q", certificatePath, privateKeyPath)
	}
	listener, err := server.Listen()
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- server.ServeListener(listener) }()

	request, err := http.NewRequest(http.MethodGet, server.ConsoleURL(listener)+"/api/v1/health", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.SetBasicAuth("h3fuzz", "secret")
	response, err := (&http.Client{Timeout: 3 * time.Second}).Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("HTTP health response = %d", response.StatusCode)
	}
	if !strings.HasPrefix(server.ConsoleURL(listener), "http://") {
		t.Fatalf("console URL = %q, want http", server.ConsoleURL(listener))
	}
	if _, err := os.Stat(databasePath + ".key.pem"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("plain HTTP generated a private key: %v", err)
	}

	shutdownServer(t, server, done)
}

func shutdownServer(t *testing.T, server *Server, done <-chan error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := server.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("ServeListener returned %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("ServeListener did not stop after shutdown")
	}
}
