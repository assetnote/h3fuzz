package main

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"github.com/quic-go/quic-go/http3"
)

// ServerConfig describes the two isolated origin listeners. DataAddress is
// bound on both TCP (H1/H2) and UDP (H3); AdminAddress must be loopback.
type ServerConfig struct {
	DataAddress  string
	AdminAddress string
	CertFile     string
	KeyFile      string
}

// RunningServers is a started origin whose listeners are already bound.
type RunningServers struct {
	dataAddress  string
	adminAddress string
	tcp          *http.Server
	h3           *http3.Server
	admin        *http.Server
	errors       chan error
	wait         sync.WaitGroup
	closeOnce    sync.Once
	closeErr     error
}

// StartServers starts H1, H2, and H3 on one data-plane port plus an isolated
// cleartext admin listener. It returns only after all listeners are bound.
func StartServers(origin *Origin, config ServerConfig) (*RunningServers, error) {
	if origin == nil {
		return nil, errors.New("origin is required")
	}
	if config.DataAddress == "" {
		return nil, errors.New("data address is required")
	}
	if err := validateLoopbackAddress(config.AdminAddress); err != nil {
		return nil, err
	}
	certificate, err := tls.LoadX509KeyPair(config.CertFile, config.KeyFile)
	if err != nil {
		return nil, fmt.Errorf("load TLS certificate: %w", err)
	}

	tcpListener, err := net.Listen("tcp", config.DataAddress)
	if err != nil {
		return nil, fmt.Errorf("listen TCP data plane: %w", err)
	}
	udpAddress, err := matchingUDPAddress(config.DataAddress, tcpListener.Addr())
	if err != nil {
		_ = tcpListener.Close()
		return nil, err
	}
	udpListener, err := net.ListenPacket("udp", udpAddress)
	if err != nil {
		_ = tcpListener.Close()
		return nil, fmt.Errorf("listen UDP data plane: %w", err)
	}
	adminListener, err := net.Listen("tcp", config.AdminAddress)
	if err != nil {
		_ = udpListener.Close()
		_ = tcpListener.Close()
		return nil, fmt.Errorf("listen admin plane: %w", err)
	}

	tcpTLS := &tls.Config{
		Certificates: []tls.Certificate{certificate},
		MinVersion:   tls.VersionTLS12,
		NextProtos:   []string{"h2", "http/1.1"},
	}
	h3TLS := http3.ConfigureTLSConfig(&tls.Config{
		Certificates: []tls.Certificate{certificate},
		MinVersion:   tls.VersionTLS13,
	})
	tcpProtocols := &http.Protocols{}
	tcpProtocols.SetHTTP1(true)
	tcpProtocols.SetHTTP2(true)
	running := &RunningServers{
		dataAddress:  tcpListener.Addr().String(),
		adminAddress: adminListener.Addr().String(),
		errors:       make(chan error, 3),
		tcp: &http.Server{
			Handler:   origin.Handler(),
			TLSConfig: tcpTLS,
			Protocols: tcpProtocols,
		},
		h3: &http3.Server{
			Addr:       tcpListener.Addr().String(),
			Handler:    origin.Handler(),
			TLSConfig:  h3TLS,
			QUICConfig: origin.QUICConfig(),
		},
		admin: &http.Server{Handler: origin.AdminHandler()},
	}
	running.wait.Add(3)
	go running.serve("TCP data plane", func() error {
		return running.tcp.ServeTLS(tcpListener, "", "")
	})
	go running.serve("UDP HTTP/3 data plane", func() error {
		return running.h3.Serve(udpListener)
	})
	go running.serve("admin plane", func() error {
		return running.admin.Serve(adminListener)
	})
	go func() {
		running.wait.Wait()
		close(running.errors)
	}()
	return running, nil
}

// DataAddress is the shared TCP/UDP address selected for workload traffic.
func (s *RunningServers) DataAddress() string {
	return s.dataAddress
}

// AdminAddress is the selected loopback address for snapshot collection.
func (s *RunningServers) AdminAddress() string {
	return s.adminAddress
}

// Wait returns the first unexpected serving error, or nil after shutdown.
func (s *RunningServers) Wait() error {
	err, ok := <-s.errors
	if !ok {
		return nil
	}
	return err
}

// Close gracefully drains all protocol servers once.
func (s *RunningServers) Close(ctx context.Context) error {
	s.closeOnce.Do(func() {
		var closeErrors []error
		if err := s.admin.Shutdown(ctx); err != nil {
			closeErrors = append(closeErrors, fmt.Errorf("shutdown admin server: %w", err))
		}
		if err := s.tcp.Shutdown(ctx); err != nil {
			closeErrors = append(closeErrors, fmt.Errorf("shutdown TCP server: %w", err))
		}
		if err := s.h3.Shutdown(ctx); err != nil {
			closeErrors = append(closeErrors, fmt.Errorf("shutdown HTTP/3 server: %w", err))
		}
		s.wait.Wait()
		s.closeErr = errors.Join(closeErrors...)
	})
	return s.closeErr
}

func (s *RunningServers) serve(name string, serve func() error) {
	defer s.wait.Done()
	if err := serve(); err != nil && !errors.Is(err, http.ErrServerClosed) && !errors.Is(err, net.ErrClosed) {
		s.errors <- fmt.Errorf("%s: %w", name, err)
	}
}

func validateLoopbackAddress(address string) error {
	if address == "" {
		return errors.New("admin address is required")
	}
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("parse admin address: %w", err)
	}
	host = strings.Trim(host, "[]")
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("admin address %q must use a loopback IP literal", address)
	}
	return nil
}

func matchingUDPAddress(configured string, actual net.Addr) (string, error) {
	host, _, err := net.SplitHostPort(configured)
	if err != nil {
		return "", fmt.Errorf("parse data address: %w", err)
	}
	_, actualPort, err := net.SplitHostPort(actual.String())
	if err != nil {
		return "", fmt.Errorf("parse bound TCP address: %w", err)
	}
	if _, err := strconv.Atoi(actualPort); err != nil {
		return "", fmt.Errorf("parse bound TCP port: %w", err)
	}
	return net.JoinHostPort(host, actualPort), nil
}
