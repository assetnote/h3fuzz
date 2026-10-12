package webserver

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
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
)

const generatedCertificateValidity = 5 * 365 * 24 * time.Hour

var managedCertificateMu sync.Mutex

type certificateIdentities struct {
	dnsNames    []string
	ipAddresses []net.IP
	required    []string
}

// DefaultTLSCertificatePaths locates the generated certificate beside the web
// database. This couples the persisted Basic Auth identity and TLS identity,
// and means selecting a different -web-db cannot unexpectedly reuse a key.
func DefaultTLSCertificatePaths(databasePath string) (certificatePath, privateKeyPath string, err error) {
	if databasePath == "" {
		databasePath, err = DefaultDatabasePath()
		if err != nil {
			return "", "", err
		}
	}
	if databasePath == ":memory:" {
		return "", "", nil
	}
	absolutePath, err := filepath.Abs(databasePath)
	if err != nil {
		return "", "", fmt.Errorf("resolve web database path for TLS: %w", err)
	}
	return absolutePath + ".cert.pem", absolutePath + ".key.pem", nil
}

func prepareServerTLS(config *Config) (*tls.Config, error) {
	providedCertificate := config.CertificatePath != ""
	providedKey := config.PrivateKeyPath != ""
	if config.PlainHTTP {
		if providedCertificate || providedKey {
			return nil, errors.New("-web-plain-http cannot be combined with -web-cert or -web-key")
		}
		return nil, nil
	}
	if providedCertificate != providedKey {
		return nil, errors.New("-web-cert and -web-key must be supplied together")
	}

	identities, err := identitiesForListenAddress(config.ListenAddress)
	if err != nil {
		return nil, err
	}
	managed := !providedCertificate
	if managed {
		config.CertificatePath, config.PrivateKeyPath, err = DefaultTLSCertificatePaths(config.DatabasePath)
		if err != nil {
			return nil, err
		}
	}

	var certificate tls.Certificate
	if config.CertificatePath == "" {
		certificate, err = generateSelfSignedCertificate(identities, time.Now())
	} else if managed {
		certificate, err = loadOrCreateManagedCertificate(config.CertificatePath, config.PrivateKeyPath, identities)
	} else {
		certificate, err = loadCertificatePair(config.CertificatePath, config.PrivateKeyPath)
		if err == nil {
			err = validateCertificate(certificate.Leaf, identities.required, time.Now())
		}
		if err == nil {
			err = validatePrivateKeyPermissions(config.PrivateKeyPath)
		}
	}
	if err != nil {
		if managed {
			return nil, fmt.Errorf("initialize generated web TLS certificate: %w", err)
		}
		return nil, fmt.Errorf("load web TLS certificate: %w", err)
	}

	return &tls.Config{
		Certificates: []tls.Certificate{certificate},
		MinVersion:   tls.VersionTLS12,
	}, nil
}

func loadOrCreateManagedCertificate(certificatePath, privateKeyPath string, identities certificateIdentities) (tls.Certificate, error) {
	managedCertificateMu.Lock()
	defer managedCertificateMu.Unlock()

	certificateExists, err := regularFileExists(certificatePath)
	if err != nil {
		return tls.Certificate{}, err
	}
	keyExists, err := regularFileExists(privateKeyPath)
	if err != nil {
		return tls.Certificate{}, err
	}
	if certificateExists != keyExists {
		return tls.Certificate{}, fmt.Errorf("managed certificate is incomplete: both %q and %q must exist (remove both files to regenerate)", certificatePath, privateKeyPath)
	}
	if !certificateExists {
		return generateAndPersistCertificate(certificatePath, privateKeyPath, identities)
	}

	certificate, err := loadCertificatePair(certificatePath, privateKeyPath)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("managed certificate is corrupt: %w (remove both %q and %q to regenerate)", err, certificatePath, privateKeyPath)
	}
	if err := os.Chmod(certificatePath, 0o600); err != nil {
		return tls.Certificate{}, fmt.Errorf("secure generated certificate permissions: %w", err)
	}
	if err := os.Chmod(privateKeyPath, 0o600); err != nil {
		return tls.Certificate{}, fmt.Errorf("secure generated private key permissions: %w", err)
	}
	if err := validateCertificate(certificate.Leaf, identities.generatedNames(), time.Now()); err != nil {
		// A listener address change or normal certificate expiry requires a new
		// identity. Syntactically corrupt or mismatched PEM files fail above rather
		// than being silently replaced.
		return generateAndPersistCertificate(certificatePath, privateKeyPath, identities)
	}
	return certificate, nil
}

func (identities certificateIdentities) generatedNames() []string {
	names := append([]string(nil), identities.dnsNames...)
	for _, ip := range identities.ipAddresses {
		names = append(names, ip.String())
	}
	return uniqueSortedStrings(names)
}

func regularFileExists(path string) (bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("inspect TLS file %q: %w", path, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return false, fmt.Errorf("TLS file %q must be a regular file, not a symlink", path)
	}
	return true, nil
}

func loadCertificatePair(certificatePath, privateKeyPath string) (tls.Certificate, error) {
	certificate, err := tls.LoadX509KeyPair(certificatePath, privateKeyPath)
	if err != nil {
		return tls.Certificate{}, err
	}
	if len(certificate.Certificate) == 0 {
		return tls.Certificate{}, errors.New("certificate PEM contains no certificates")
	}
	leaf, err := x509.ParseCertificate(certificate.Certificate[0])
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("parse leaf certificate: %w", err)
	}
	certificate.Leaf = leaf
	return certificate, nil
}

func validateCertificate(certificate *x509.Certificate, requiredNames []string, now time.Time) error {
	if certificate == nil {
		return errors.New("certificate has no parsed leaf")
	}
	if now.Before(certificate.NotBefore) {
		return fmt.Errorf("certificate is not valid before %s", certificate.NotBefore.Format(time.RFC3339))
	}
	if !now.Before(certificate.NotAfter) {
		return fmt.Errorf("certificate expired at %s", certificate.NotAfter.Format(time.RFC3339))
	}
	for _, name := range requiredNames {
		if err := certificate.VerifyHostname(name); err != nil {
			return fmt.Errorf("certificate does not cover web listen host %q: %w", name, err)
		}
	}
	return nil
}

func validatePrivateKeyPermissions(path string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if permissions := info.Mode().Perm(); permissions&0o077 != 0 {
		return fmt.Errorf("private key %q permissions are %04o; remove group/world access (for example chmod 600)", path, permissions)
	}
	return nil
}

func generateAndPersistCertificate(certificatePath, privateKeyPath string, identities certificateIdentities) (tls.Certificate, error) {
	certificate, certificatePEM, privateKeyPEM, err := newSelfSignedCertificate(identities, time.Now())
	if err != nil {
		return tls.Certificate{}, err
	}
	if err := writeCertificatePair(certificatePath, privateKeyPath, certificatePEM, privateKeyPEM); err != nil {
		return tls.Certificate{}, err
	}
	return certificate, nil
}

func generateSelfSignedCertificate(identities certificateIdentities, now time.Time) (tls.Certificate, error) {
	certificate, _, _, err := newSelfSignedCertificate(identities, now)
	return certificate, err
}

func newSelfSignedCertificate(identities certificateIdentities, now time.Time) (tls.Certificate, []byte, []byte, error) {
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, nil, nil, fmt.Errorf("generate private key: %w", err)
	}
	serialLimit := new(big.Int).Lsh(big.NewInt(1), 128)
	serial, err := rand.Int(rand.Reader, serialLimit)
	if err != nil {
		return tls.Certificate{}, nil, nil, fmt.Errorf("generate certificate serial: %w", err)
	}
	commonName := "h3fuzz local web console"
	if len(identities.dnsNames) > 0 {
		commonName = identities.dnsNames[0]
	} else if len(identities.ipAddresses) > 0 {
		commonName = identities.ipAddresses[0].String()
	}
	template := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			Organization: []string{"h3fuzz"},
			CommonName:   commonName,
		},
		NotBefore:             now.Add(-5 * time.Minute),
		NotAfter:              now.Add(generatedCertificateValidity),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              append([]string(nil), identities.dnsNames...),
		IPAddresses:           append([]net.IP(nil), identities.ipAddresses...),
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &privateKey.PublicKey, privateKey)
	if err != nil {
		return tls.Certificate{}, nil, nil, fmt.Errorf("create self-signed certificate: %w", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(privateKey)
	if err != nil {
		return tls.Certificate{}, nil, nil, fmt.Errorf("marshal private key: %w", err)
	}
	certificatePEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	privateKeyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	certificate, err := tls.X509KeyPair(certificatePEM, privateKeyPEM)
	if err != nil {
		return tls.Certificate{}, nil, nil, fmt.Errorf("load generated certificate: %w", err)
	}
	certificate.Leaf, err = x509.ParseCertificate(der)
	if err != nil {
		return tls.Certificate{}, nil, nil, fmt.Errorf("parse generated certificate: %w", err)
	}
	return certificate, certificatePEM, privateKeyPEM, nil
}

func writeCertificatePair(certificatePath, privateKeyPath string, certificatePEM, privateKeyPEM []byte) error {
	if certificatePath == privateKeyPath {
		return errors.New("web certificate and private key paths must be different")
	}
	for _, directory := range []string{filepath.Dir(certificatePath), filepath.Dir(privateKeyPath)} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			return fmt.Errorf("create TLS directory %q: %w", directory, err)
		}
	}
	keyTemporary, err := writeTemporaryPEM(privateKeyPath, privateKeyPEM)
	if err != nil {
		return err
	}
	defer os.Remove(keyTemporary)
	certificateTemporary, err := writeTemporaryPEM(certificatePath, certificatePEM)
	if err != nil {
		return err
	}
	defer os.Remove(certificateTemporary)

	if err := os.Rename(keyTemporary, privateKeyPath); err != nil {
		return fmt.Errorf("install generated private key: %w", err)
	}
	if err := os.Rename(certificateTemporary, certificatePath); err != nil {
		return fmt.Errorf("install generated certificate: %w", err)
	}
	if err := os.Chmod(privateKeyPath, 0o600); err != nil {
		return fmt.Errorf("secure generated private key permissions: %w", err)
	}
	if err := os.Chmod(certificatePath, 0o600); err != nil {
		return fmt.Errorf("secure generated certificate permissions: %w", err)
	}
	return nil
}

func writeTemporaryPEM(destination string, contents []byte) (string, error) {
	file, err := os.CreateTemp(filepath.Dir(destination), ".h3fuzz-tls-*")
	if err != nil {
		return "", fmt.Errorf("create temporary TLS file: %w", err)
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
		return "", fmt.Errorf("secure temporary TLS file: %w", err)
	}
	if _, err := file.Write(contents); err != nil {
		return "", fmt.Errorf("write temporary TLS file: %w", err)
	}
	if err := file.Sync(); err != nil {
		return "", fmt.Errorf("sync temporary TLS file: %w", err)
	}
	if err := file.Close(); err != nil {
		return "", fmt.Errorf("close temporary TLS file: %w", err)
	}
	ok = true
	return name, nil
}

func identitiesForListenAddress(address string) (certificateIdentities, error) {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return certificateIdentities{}, fmt.Errorf("invalid -web-listen address %q: expected host:port", address)
	}
	host = strings.TrimSuffix(strings.TrimSpace(host), ".")
	ipHost := host
	if zone := strings.LastIndexByte(ipHost, '%'); zone >= 0 {
		ipHost = ipHost[:zone]
	}
	identities := certificateIdentities{}
	addDNS := func(name string) {
		name = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(name), "."))
		if name != "" {
			identities.dnsNames = append(identities.dnsNames, name)
		}
	}
	addIP := func(ip net.IP) {
		if ip != nil {
			identities.ipAddresses = append(identities.ipAddresses, append(net.IP(nil), ip...))
		}
	}

	if ip := net.ParseIP(ipHost); ip != nil {
		addIP(ip)
		if !ip.IsUnspecified() {
			identities.required = append(identities.required, ip.String())
		} else {
			addInterfaceIdentities(addDNS, addIP)
		}
	} else if host != "" {
		addDNS(host)
		identities.required = append(identities.required, strings.ToLower(host))
		if strings.EqualFold(host, "localhost") {
			addIP(net.ParseIP("127.0.0.1"))
			addIP(net.ParseIP("::1"))
		}
	} else {
		addInterfaceIdentities(addDNS, addIP)
	}
	if len(identities.dnsNames) == 0 && len(identities.ipAddresses) == 0 {
		addDNS("localhost")
		addIP(net.ParseIP("127.0.0.1"))
		addIP(net.ParseIP("::1"))
	}
	identities.dnsNames = uniqueSortedStrings(identities.dnsNames)
	identities.ipAddresses = uniqueSortedIPs(identities.ipAddresses)
	identities.required = uniqueSortedStrings(identities.required)
	return identities, nil
}

func addInterfaceIdentities(addDNS func(string), addIP func(net.IP)) {
	addDNS("localhost")
	addIP(net.ParseIP("127.0.0.1"))
	addIP(net.ParseIP("::1"))
	if hostname, err := os.Hostname(); err == nil {
		addDNS(hostname)
	}
	if addresses, err := net.InterfaceAddrs(); err == nil {
		for _, address := range addresses {
			host := address.String()
			if slash := strings.IndexByte(host, '/'); slash >= 0 {
				host = host[:slash]
			}
			if zone := strings.LastIndexByte(host, '%'); zone >= 0 {
				host = host[:zone]
			}
			addIP(net.ParseIP(host))
		}
	}
}

func uniqueSortedStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func uniqueSortedIPs(values []net.IP) []net.IP {
	byString := make(map[string]net.IP, len(values))
	for _, value := range values {
		if value != nil {
			byString[value.String()] = value
		}
	}
	keys := make([]string, 0, len(byString))
	for key := range byString {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]net.IP, 0, len(keys))
	for _, key := range keys {
		result = append(result, byString[key])
	}
	return result
}
