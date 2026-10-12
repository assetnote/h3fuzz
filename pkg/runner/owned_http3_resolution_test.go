package runner

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/assetnote/h3fuzz/harness/quicfault"
	"github.com/quic-go/quic-go"
)

func TestOwnedHTTP3TransportFallsBackAcrossResolvedAddresses(t *testing.T) {
	origin, transport := newOwnedRecoveryTestTransport(t)
	target, err := url.Parse(origin.URL(quicfault.PathOK))
	if err != nil {
		t.Fatal(err)
	}
	reachable, err := net.ResolveUDPAddr("udp4", target.Host)
	if err != nil {
		t.Fatal(err)
	}
	unreachable := &net.UDPAddr{IP: net.ParseIP("::1"), Port: reachable.Port}
	expectedAuthority := net.JoinHostPort("localhost", target.Port())
	transport.authority = expectedAuthority
	transport.resolveAddrs = func(_ context.Context, authority string) ([]*net.UDPAddr, error) {
		if authority != expectedAuthority {
			t.Fatalf("resolver authority = %q, want %q", authority, expectedAuthority)
		}
		return []*net.UDPAddr{unreachable, reachable}, nil
	}

	injected := errors.New("injected unreachable IPv6 candidate")
	realDial := transport.dialEarly
	var attemptsMu sync.Mutex
	var attempts []string
	transport.dialEarly = func(ctx context.Context, quicTransport *quic.Transport, addr net.Addr, tlsConfig *tls.Config, quicConfig *quic.Config) (*quic.Conn, error) {
		attemptsMu.Lock()
		attempts = append(attempts, addr.String())
		attemptsMu.Unlock()
		if udpAddr, ok := addr.(*net.UDPAddr); ok && udpAddr.IP.To4() == nil {
			return nil, injected
		}
		return realDial(ctx, quicTransport, addr, tlsConfig, quicConfig)
	}

	connection, err := recoveryRoundTripConnection(transport, recoveryRequest(t, target.String()))
	if err != nil {
		t.Fatalf("round trip after first address failed: %v", err)
	}
	if connection != 1 {
		t.Fatalf("connection = %d, want 1", connection)
	}
	attemptsMu.Lock()
	defer attemptsMu.Unlock()
	if len(attempts) != 2 {
		t.Fatalf("dial attempts = %v, want IPv6 failure followed by IPv4 success", attempts)
	}
	if attempts[0] != unreachable.String() || attempts[1] != reachable.String() {
		t.Fatalf("dial attempts = %v, want [%s %s]", attempts, unreachable, reachable)
	}
}

func TestResolveOwnedHTTP3AddrsAcceptsDefaultPortIPv6Literal(t *testing.T) {
	addresses, err := resolveOwnedHTTP3Addrs(context.Background(), "[::1]")
	if err != nil {
		t.Fatal(err)
	}
	if len(addresses) != 1 {
		t.Fatalf("addresses = %v, want one IPv6 loopback address", addresses)
	}
	if !addresses[0].IP.Equal(net.ParseIP("::1")) || addresses[0].Port != 443 {
		t.Fatalf("address = %v, want [::1]:443", addresses[0])
	}
}

func TestOwnedHTTP3UDPAddrsPrefersIPv4AndDeduplicates(t *testing.T) {
	addresses := ownedHTTP3UDPAddrs([]net.IPAddr{
		{IP: net.ParseIP("2001:db8::1")},
		{IP: net.ParseIP("192.0.2.1")},
		{IP: net.ParseIP("192.0.2.1")},
		{IP: net.ParseIP("2001:db8::2")},
	}, 8443)
	want := []string{"192.0.2.1:8443", "[2001:db8::1]:8443", "[2001:db8::2]:8443"}
	if len(addresses) != len(want) {
		t.Fatalf("addresses = %v, want %v", addresses, want)
	}
	for index, address := range addresses {
		if address.String() != want[index] {
			t.Fatalf("addresses = %v, want %v", addresses, want)
		}
	}
}

func TestOwnedHTTP3DialCandidatesOuterCancellationCauseWins(t *testing.T) {
	outer, cancel := context.WithCancelCause(context.Background())
	cause := errors.New("transport closed while dialing")
	staleDialErr := errors.New("stale candidate error")
	transport := candidateTestTransport(func(context.Context, *quic.Transport, net.Addr, *tls.Config, *quic.Config) (*quic.Conn, error) {
		cancel(cause)
		return nil, staleDialErr
	})

	_, err := transport.dialResolvedCandidates(outer, nil, nil, candidateTestAddresses())
	if !errors.Is(err, cause) {
		t.Fatalf("dial error = %v, want outer cancellation cause %v", err, cause)
	}
	if errors.Is(err, staleDialErr) {
		t.Fatalf("dial error = %v, stale candidate error should not mask outer cause", err)
	}
}

func TestOwnedHTTP3DialCandidatesSharesDeadlineAndTriesNextAddress(t *testing.T) {
	outer, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	outerDeadline, _ := outer.Deadline()
	secondErr := errors.New("second candidate failed")
	var attempts int
	transport := candidateTestTransport(func(ctx context.Context, _ *quic.Transport, _ net.Addr, _ *tls.Config, _ *quic.Config) (*quic.Conn, error) {
		attempts++
		if attempts == 1 {
			attemptDeadline, hasDeadline := ctx.Deadline()
			if !hasDeadline || !attemptDeadline.Before(outerDeadline) {
				t.Fatalf("first candidate deadline = %v, want before outer deadline %v", attemptDeadline, outerDeadline)
			}
			<-ctx.Done()
			return nil, context.Cause(ctx)
		}
		if ctx != outer {
			t.Fatal("final candidate did not receive the outer context")
		}
		return nil, secondErr
	})

	_, err := transport.dialResolvedCandidates(outer, nil, nil, candidateTestAddresses())
	if attempts != 2 {
		t.Fatalf("dial attempts = %d, want 2", attempts)
	}
	if !errors.Is(err, context.DeadlineExceeded) || !errors.Is(err, secondErr) {
		t.Fatalf("dial error = %v, want both candidate failures", err)
	}
}

func TestOwnedHTTP3DialCandidatesReportsEveryAttemptedAddress(t *testing.T) {
	addresses := candidateTestAddresses()
	firstErr := errors.New("first candidate failed")
	secondErr := errors.New("second candidate failed")
	transport := candidateTestTransport(func(_ context.Context, _ *quic.Transport, addr net.Addr, _ *tls.Config, _ *quic.Config) (*quic.Conn, error) {
		if addr.String() == addresses[0].String() {
			return nil, firstErr
		}
		return nil, secondErr
	})

	_, err := transport.dialResolvedCandidates(context.Background(), nil, nil, addresses)
	if !errors.Is(err, firstErr) || !errors.Is(err, secondErr) {
		t.Fatalf("dial error = %v, want both underlying failures", err)
	}
	for _, address := range addresses {
		if !strings.Contains(err.Error(), address.String()) {
			t.Fatalf("dial error = %q, want attempted address %q", err, address)
		}
	}
}

func candidateTestTransport(dial func(context.Context, *quic.Transport, net.Addr, *tls.Config, *quic.Config) (*quic.Conn, error)) *ownedHTTP3Transport {
	return &ownedHTTP3Transport{
		authority: "example.test:443",
		tlsConfig: &tls.Config{MinVersion: tls.VersionTLS13},
		quicConfig: &quic.Config{
			HandshakeIdleTimeout: time.Second,
		},
		dialEarly: dial,
	}
}

func candidateTestAddresses() []*net.UDPAddr {
	return []*net.UDPAddr{
		{IP: net.ParseIP("192.0.2.1"), Port: 443},
		{IP: net.ParseIP("2001:db8::1"), Port: 443},
	}
}
