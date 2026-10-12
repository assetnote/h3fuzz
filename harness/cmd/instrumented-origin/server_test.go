package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"
)

func TestRunningOriginSeparatesRealH1H2AndH3Requests(t *testing.T) {
	t.Parallel()

	origin := newTestOrigin(t, true)
	certFile, keyFile := writeTestCertificate(t)
	running, err := StartServers(origin, ServerConfig{
		DataAddress:  "127.0.0.1:0",
		AdminAddress: "127.0.0.1:0",
		CertFile:     certFile,
		KeyFile:      keyFile,
	})
	if err != nil {
		t.Fatalf("StartServers: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := running.Close(ctx); err != nil {
			t.Errorf("close servers: %v", err)
		}
	})

	tlsConfig := insecureLocalTLSConfig() // test-only certificate
	h1Transport := &http.Transport{
		TLSClientConfig: tlsConfig.Clone(),
		TLSNextProto:    map[string]func(string, *tls.Conn) http.RoundTripper{},
	}
	h2Protocols := &http.Protocols{}
	h2Protocols.SetHTTP2(true)
	h2Transport := &http.Transport{
		TLSClientConfig: tlsConfig.Clone(),
		Protocols:       h2Protocols,
	}
	h3Transport := &http3.Transport{TLSClientConfig: tlsConfig.Clone()}
	t.Cleanup(func() {
		h1Transport.CloseIdleConnections()
		h2Transport.CloseIdleConnections()
		_ = h3Transport.Close()
	})

	clients := []*http.Client{
		{Transport: h1Transport},
		{Transport: h2Transport},
		{Transport: h3Transport},
	}
	for index, client := range clients {
		req, err := http.NewRequest(http.MethodGet, "https://"+running.DataAddress()+"/bench/protocol", nil)
		if err != nil {
			t.Fatalf("new request: %v", err)
		}
		req.Header.Set(harnessRunHeader, testRunID)
		req.Header.Set(harnessRoleHeader, string(roleInitial))
		response, err := client.Do(req)
		if err != nil {
			t.Fatalf("protocol request %d: %v", index+1, err)
		}
		_ = response.Body.Close()
		if response.StatusCode != http.StatusNoContent {
			t.Fatalf("protocol request %d status = %d", index+1, response.StatusCode)
		}
	}

	counts := map[string]uint64{}
	for _, row := range origin.Snapshot().Counters {
		counts[row.Protocol] += row.HandlerRequests
	}
	for _, protocol := range []string{protocolH1, protocolH2, protocolH3} {
		if counts[protocol] != 1 {
			t.Fatalf("protocol counts = %+v, want one %s request", counts, protocol)
		}
	}
}

func TestRunningOriginEnforcesConfiguredPeerStreamLimit(t *testing.T) {
	t.Parallel()

	origin, err := NewOrigin(OriginConfig{
		CountersEnabled:              true,
		OriginInstanceID:             "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee",
		StartedAt:                    time.Date(2026, 10, 11, 1, 2, 3, 0, time.UTC),
		BuildSHA256:                  repeatHex("a"),
		ConfigSHA256:                 repeatHex("b"),
		ConfiguredMaxIncomingStreams: 1,
		EffectiveMaxIncomingStreams:  1,
		FilesRoot:                    ".",
	})
	if err != nil {
		t.Fatalf("NewOrigin: %v", err)
	}
	certFile, keyFile := writeTestCertificate(t)
	running, err := StartServers(origin, ServerConfig{
		DataAddress:  "127.0.0.1:0",
		AdminAddress: "127.0.0.1:0",
		CertFile:     certFile,
		KeyFile:      keyFile,
	})
	if err != nil {
		t.Fatalf("StartServers: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := running.Close(ctx); err != nil {
			t.Errorf("close servers: %v", err)
		}
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	tlsConfig := insecureLocalTLSConfig()
	tlsConfig.NextProtos = []string{http3.NextProtoH3}
	connection, err := quic.DialAddr(ctx, running.DataAddress(), tlsConfig, nil)
	if err != nil {
		t.Fatalf("dial QUIC origin: %v", err)
	}
	t.Cleanup(func() { _ = connection.CloseWithError(0, "test complete") })
	first, err := connection.OpenStream()
	if err != nil {
		t.Fatalf("open first stream: %v", err)
	}
	t.Cleanup(func() { _ = first.Close() })
	if _, err := connection.OpenStream(); !errors.Is(err, &quic.StreamLimitReachedError{}) {
		t.Fatalf("second stream error = %v, want StreamLimitReachedError", err)
	}
	if got := origin.Snapshot().Origin.EffectiveMaxIncomingStreams; got != 1 {
		t.Fatalf("reported effective stream limit = %d, want 1", got)
	}
}

func writeTestCertificate(t *testing.T) (string, string) {
	t.Helper()
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate private key: %v", err)
	}
	now := time.Now()
	template := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "localhost"},
		NotBefore:    now.Add(-time.Minute),
		NotAfter:     now.Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}
	certificateDER, err := x509.CreateCertificate(rand.Reader, &template, &template, &privateKey.PublicKey, privateKey)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}
	privateDER, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		t.Fatalf("marshal private key: %v", err)
	}
	directory := t.TempDir()
	certFile := filepath.Join(directory, "cert.pem")
	keyFile := filepath.Join(directory, "key.pem")
	if err := os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificateDER}), 0o600); err != nil {
		t.Fatalf("write certificate: %v", err)
	}
	if err := os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privateDER}), 0o600); err != nil {
		t.Fatalf("write private key: %v", err)
	}
	return certFile, keyFile
}

func insecureLocalTLSConfig() *tls.Config {
	return &tls.Config{ // #nosec G402 -- local self-signed test server only.
		InsecureSkipVerify: true,
		ServerName:         "localhost",
		MinVersion:         tls.VersionTLS13,
	}
}
