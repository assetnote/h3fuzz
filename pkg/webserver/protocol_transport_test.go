package webserver

import (
	"testing"

	"github.com/assetnote/h3fuzz/pkg/ffuf"
)

func TestEffectiveRuntimeTransportUsesNegotiatedProtocol(t *testing.T) {
	tests := []struct {
		name string
		conf ffuf.Config
		want string
	}{
		{name: "automatic HTTP1 fallback", conf: ffuf.Config{}, want: RuntimeTransportHTTP1},
		{name: "requested HTTP2 fell back to HTTP1", conf: ffuf.Config{Http2: true}, want: RuntimeTransportHTTP1},
		{name: "automatic HTTP2 negotiated", conf: ffuf.Config{Http2Selected: true}, want: RuntimeTransportHTTP2},
		{name: "HTTP3 takes precedence", conf: ffuf.Config{Http2Selected: true, Http3Selected: true}, want: RuntimeTransportHTTP3},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := effectiveRuntimeTransport(&test.conf); got != test.want {
				t.Fatalf("effectiveRuntimeTransport() = %q, want %q", got, test.want)
			}
		})
	}
}
