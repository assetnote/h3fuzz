package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"time"
)

func ensureCertificate(certFile, keyFile string) error {
	certInfo, certErr := os.Stat(certFile)
	keyInfo, keyErr := os.Stat(keyFile)
	if certErr == nil && keyErr == nil {
		if certInfo.Mode().Perm() != 0o600 || keyInfo.Mode().Perm() != 0o600 {
			return errors.New("existing TLS certificate and key must have mode 0600")
		}
		if _, err := tls.LoadX509KeyPair(certFile, keyFile); err != nil {
			return fmt.Errorf("load existing TLS certificate: %w", err)
		}
		return nil
	}
	certMissing := errors.Is(certErr, os.ErrNotExist)
	keyMissing := errors.Is(keyErr, os.ErrNotExist)
	if !certMissing || !keyMissing {
		return fmt.Errorf("TLS certificate pair is incomplete or inaccessible: cert=%v key=%v", certErr, keyErr)
	}

	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return fmt.Errorf("generate TLS private key: %w", err)
	}
	serialLimit := new(big.Int).Lsh(big.NewInt(1), 128)
	serial, err := rand.Int(rand.Reader, serialLimit)
	if err != nil {
		return fmt.Errorf("generate certificate serial: %w", err)
	}
	now := time.Now().UTC()
	template := x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "h3fuzz instrumented origin"},
		NotBefore:    now.Add(-5 * time.Minute),
		NotAfter:     now.Add(365 * 24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{"localhost"},
		IPAddresses: []net.IP{
			net.ParseIP("127.0.0.1"),
			net.ParseIP("::1"),
		},
		BasicConstraintsValid: true,
	}
	certificateDER, err := x509.CreateCertificate(rand.Reader, &template, &template, &privateKey.PublicKey, privateKey)
	if err != nil {
		return fmt.Errorf("create TLS certificate: %w", err)
	}
	privateDER, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		return fmt.Errorf("marshal TLS private key: %w", err)
	}
	certificatePEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificateDER})
	privatePEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privateDER})

	if err := os.MkdirAll(filepath.Dir(certFile), 0o700); err != nil {
		return fmt.Errorf("create certificate directory: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(keyFile), 0o700); err != nil {
		return fmt.Errorf("create private-key directory: %w", err)
	}
	certTemporary, err := writeTemporarySibling(certFile, certificatePEM)
	if err != nil {
		return err
	}
	defer os.Remove(certTemporary)
	keyTemporary, err := writeTemporarySibling(keyFile, privatePEM)
	if err != nil {
		return err
	}
	defer os.Remove(keyTemporary)

	if err := os.Rename(keyTemporary, keyFile); err != nil {
		return fmt.Errorf("install TLS private key: %w", err)
	}
	if err := os.Rename(certTemporary, certFile); err != nil {
		_ = os.Remove(keyFile)
		return fmt.Errorf("install TLS certificate: %w", err)
	}
	return nil
}

func writeTemporarySibling(destination string, contents []byte) (string, error) {
	file, err := os.CreateTemp(filepath.Dir(destination), "."+filepath.Base(destination)+".*.tmp")
	if err != nil {
		return "", fmt.Errorf("create temporary %s: %w", destination, err)
	}
	name := file.Name()
	ok := false
	defer func() {
		_ = file.Close()
		if !ok {
			_ = os.Remove(name)
		}
	}()
	if err := file.Chmod(0o600); err != nil {
		return "", fmt.Errorf("chmod temporary %s: %w", destination, err)
	}
	if _, err := file.Write(contents); err != nil {
		return "", fmt.Errorf("write temporary %s: %w", destination, err)
	}
	if err := file.Sync(); err != nil {
		return "", fmt.Errorf("sync temporary %s: %w", destination, err)
	}
	if err := file.Close(); err != nil {
		return "", fmt.Errorf("close temporary %s: %w", destination, err)
	}
	ok = true
	return name, nil
}
