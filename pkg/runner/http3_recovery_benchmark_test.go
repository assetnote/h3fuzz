package runner

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/assetnote/h3fuzz/harness/quicfault"
	"github.com/quic-go/quic-go/http3"
)

func BenchmarkHTTP3HealthyTransport(b *testing.B) {
	origin, err := quicfault.Start()
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := origin.Close(ctx); err != nil {
			b.Errorf("close benchmark origin: %v", err)
		}
	})

	benchmark := func(b *testing.B, transport http.RoundTripper) {
		request, err := http.NewRequestWithContext(context.Background(), http.MethodHead, origin.URL(quicfault.PathOK), nil)
		if err != nil {
			b.Fatal(err)
		}
		b.ReportAllocs()
		b.ResetTimer()
		for range b.N {
			response, err := transport.RoundTrip(request)
			if err != nil {
				b.Fatal(err)
			}
			if _, err := io.Copy(io.Discard, response.Body); err != nil {
				b.Fatal(err)
			}
			if err := response.Body.Close(); err != nil {
				b.Fatal(err)
			}
		}
	}

	b.Run("quic-go-v0.63-stock", func(b *testing.B) {
		transport := &http3.Transport{
			TLSClientConfig:        origin.ClientTLSConfig(),
			QUICConfig:             http3QUICConfig(time.Second),
			EnableDatagrams:        true,
			DisableCompression:     true,
			MaxResponseHeaderBytes: 1 << 20,
		}
		b.Cleanup(func() {
			if err := transport.Close(); err != nil {
				b.Errorf("close stock transport: %v", err)
			}
		})
		benchmark(b, transport)
	})
	b.Run("explicit-generation-owned", func(b *testing.B) {
		transport := newOwnedHTTP3Transport(
			originAuthority(b, origin.URL("/")),
			origin.ClientTLSConfig(),
			http3QUICConfig(time.Second),
			newHTTP3ReconnectGate(1, nil),
		)
		b.Cleanup(func() {
			if err := transport.Close(); err != nil {
				b.Errorf("close owned transport: %v", err)
			}
		})
		benchmark(b, transport)
	})
}

func BenchmarkOwnedHTTP3HealthyGenerationReservation(b *testing.B) {
	transport := &ownedHTTP3Transport{}
	generation := &ownedHTTP3Generation{}
	generation.accepting.Store(true)
	generation.inFlight.Store(1)
	transport.current.Store(generation)
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if !transport.reserveGeneration(generation) {
			b.Fatal("healthy generation reservation failed")
		}
		transport.releaseGeneration(generation)
	}
}

func BenchmarkOwnedHTTP3HealthyGenerationReservationParallel(b *testing.B) {
	transport := &ownedHTTP3Transport{}
	generation := &ownedHTTP3Generation{}
	generation.accepting.Store(true)
	generation.inFlight.Store(1)
	transport.current.Store(generation)
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			if !transport.reserveGeneration(generation) {
				b.Fatal("healthy generation reservation failed")
			}
			transport.releaseGeneration(generation)
		}
	})
}

func originAuthority(tb testing.TB, rawURL string) string {
	tb.Helper()
	request, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		tb.Fatal(err)
	}
	if request.URL.Host == "" {
		tb.Fatal(fmt.Errorf("benchmark URL %q has no authority", rawURL))
	}
	return request.URL.Host
}
