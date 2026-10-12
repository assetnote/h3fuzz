package input

import (
	"testing"

	"github.com/assetnote/h3fuzz/pkg/ffuf"
)

type benchmarkInputProvider struct {
	keyword string
	value   []byte
	active  bool
}

func (p *benchmarkInputProvider) Keyword() string    { return p.keyword }
func (p *benchmarkInputProvider) Next() bool         { return true }
func (p *benchmarkInputProvider) Position() int      { return 0 }
func (p *benchmarkInputProvider) SetPosition(int)    {}
func (p *benchmarkInputProvider) ResetPosition()     {}
func (p *benchmarkInputProvider) IncrementPosition() {}
func (p *benchmarkInputProvider) Value() []byte      { return p.value }
func (p *benchmarkInputProvider) Total() int         { return 1 }
func (p *benchmarkInputProvider) Active() bool       { return p.active }
func (p *benchmarkInputProvider) Enable()            { p.active = true }
func (p *benchmarkInputProvider) Disable()           { p.active = false }

var benchmarkInputValueSink map[string][]byte

func BenchmarkMainInputProviderValue(b *testing.B) {
	for _, mode := range []string{"clusterbomb", "pitchfork", "sniper"} {
		b.Run(mode+"/one-provider", func(b *testing.B) {
			provider := &MainInputProvider{
				Config: &ffuf.Config{InputMode: mode},
				Providers: []ffuf.InternalInputProvider{
					&benchmarkInputProvider{keyword: "FUZZ", value: []byte("payload"), active: true},
				},
			}
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				benchmarkInputValueSink = provider.Value()
			}
		})

		b.Run(mode+"/two-providers", func(b *testing.B) {
			provider := &MainInputProvider{
				Config: &ffuf.Config{InputMode: mode},
				Providers: []ffuf.InternalInputProvider{
					&benchmarkInputProvider{keyword: "FUZZ", value: []byte("payload"), active: true},
					&benchmarkInputProvider{keyword: "FUZ2Z", value: []byte("second"), active: true},
				},
			}
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				benchmarkInputValueSink = provider.Value()
			}
		})
	}
}
