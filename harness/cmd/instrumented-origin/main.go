package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"
)

const maximumQUICStreamLimit int64 = 1 << 60

type commandConfig struct {
	DataAddress                  string
	AdminAddress                 string
	CertFile                     string
	KeyFile                      string
	FilesRoot                    string
	CountersEnabled              bool
	ConfiguredMaxIncomingStreams int64
	EffectiveMaxIncomingStreams  int64
}

func parseCommandConfig(arguments []string) (commandConfig, error) {
	flags := flag.NewFlagSet("instrumented-origin", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	config := commandConfig{}
	flags.StringVar(&config.DataAddress, "listen", ":9443", "TCP and UDP workload listen address")
	flags.StringVar(&config.AdminAddress, "admin-listen", "127.0.0.1:9090", "loopback-only admin listen address")
	flags.StringVar(&config.CertFile, "cert", "/var/lib/h3fuzz-origin/cert.pem", "TLS certificate path")
	flags.StringVar(&config.KeyFile, "key", "/var/lib/h3fuzz-origin/key.pem", "TLS private-key path")
	flags.StringVar(&config.FilesRoot, "files-root", "/srv", "benchmark fixture root")
	flags.BoolVar(&config.CountersEnabled, "counters", true, "enable phase-marker parsing and counters")
	flags.Int64Var(&config.ConfiguredMaxIncomingStreams, "max-incoming-streams", 256, "peer bidirectional stream limit")
	if err := flags.Parse(arguments); err != nil {
		return commandConfig{}, err
	}
	if flags.NArg() != 0 {
		return commandConfig{}, errors.New("instrumented-origin does not accept positional arguments")
	}
	if config.ConfiguredMaxIncomingStreams <= 0 {
		return commandConfig{}, errors.New("max-incoming-streams must be positive")
	}
	if err := validateLoopbackAddress(config.AdminAddress); err != nil {
		return commandConfig{}, err
	}
	config.EffectiveMaxIncomingStreams = config.ConfiguredMaxIncomingStreams
	if config.EffectiveMaxIncomingStreams > maximumQUICStreamLimit {
		config.EffectiveMaxIncomingStreams = maximumQUICStreamLimit
	}
	return config, nil
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		log.Fatalf("instrumented-origin: %v", err)
	}
}

func run(arguments []string) error {
	config, err := parseCommandConfig(arguments)
	if err != nil {
		return err
	}
	if err := ensureCertificate(config.CertFile, config.KeyFile); err != nil {
		return err
	}
	executable, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve executable: %w", err)
	}
	buildSHA256, err := fileSHA256(executable)
	if err != nil {
		return fmt.Errorf("hash executable: %w", err)
	}
	configSHA256, err := commandConfigSHA256(config)
	if err != nil {
		return err
	}
	originInstanceID, err := newUUID()
	if err != nil {
		return fmt.Errorf("generate origin instance UUID: %w", err)
	}
	origin, err := NewOrigin(OriginConfig{
		CountersEnabled:              config.CountersEnabled,
		OriginInstanceID:             originInstanceID,
		StartedAt:                    time.Now().UTC(),
		BuildSHA256:                  buildSHA256,
		ConfigSHA256:                 configSHA256,
		ConfiguredMaxIncomingStreams: config.ConfiguredMaxIncomingStreams,
		EffectiveMaxIncomingStreams:  config.EffectiveMaxIncomingStreams,
		FilesRoot:                    config.FilesRoot,
	})
	if err != nil {
		return fmt.Errorf("configure origin: %w", err)
	}
	servers, err := StartServers(origin, ServerConfig{
		DataAddress:  config.DataAddress,
		AdminAddress: config.AdminAddress,
		CertFile:     config.CertFile,
		KeyFile:      config.KeyFile,
	})
	if err != nil {
		return err
	}
	log.Printf("origin_instance_id=%s data=%s admin=%s counters=%t configured_max_incoming_streams=%d effective_max_incoming_streams=%d",
		originInstanceID,
		servers.DataAddress(),
		servers.AdminAddress(),
		config.CountersEnabled,
		config.ConfiguredMaxIncomingStreams,
		config.EffectiveMaxIncomingStreams,
	)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	waitResult := make(chan error, 1)
	go func() { waitResult <- servers.Wait() }()
	select {
	case err := <-waitResult:
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return errors.Join(err, servers.Close(shutdownCtx))
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := servers.Close(shutdownCtx); err != nil {
			return err
		}
		return <-waitResult
	}
}

func commandConfigSHA256(config commandConfig) (string, error) {
	certificateSHA256, err := fileSHA256(config.CertFile)
	if err != nil {
		return "", fmt.Errorf("hash TLS certificate: %w", err)
	}
	filesRoot, err := filepath.Abs(config.FilesRoot)
	if err != nil {
		return "", fmt.Errorf("resolve files root: %w", err)
	}
	canonical := struct {
		DataAddress                  string `json:"data_address"`
		AdminAddress                 string `json:"admin_address"`
		FilesRoot                    string `json:"files_root"`
		CountersEnabled              bool   `json:"counters_enabled"`
		ConfiguredMaxIncomingStreams int64  `json:"configured_max_incoming_streams"`
		EffectiveMaxIncomingStreams  int64  `json:"effective_max_incoming_streams"`
		CertificateSHA256            string `json:"certificate_sha256"`
	}{
		DataAddress:                  config.DataAddress,
		AdminAddress:                 config.AdminAddress,
		FilesRoot:                    filesRoot,
		CountersEnabled:              config.CountersEnabled,
		ConfiguredMaxIncomingStreams: config.ConfiguredMaxIncomingStreams,
		EffectiveMaxIncomingStreams:  config.EffectiveMaxIncomingStreams,
		CertificateSHA256:            certificateSHA256,
	}
	encoded, err := json.Marshal(canonical)
	if err != nil {
		return "", fmt.Errorf("encode origin configuration: %w", err)
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}

func fileSHA256(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}
