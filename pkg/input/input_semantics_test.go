package input

import (
	"reflect"
	"testing"

	"github.com/assetnote/h3fuzz/pkg/ffuf"
	"github.com/ffuf/pencode/pkg/pencode"
)

type finiteInputProvider struct {
	keyword  string
	values   [][]byte
	position int
	active   bool
}

func (p *finiteInputProvider) Keyword() string     { return p.keyword }
func (p *finiteInputProvider) Next() bool          { return p.position < len(p.values) }
func (p *finiteInputProvider) Position() int       { return p.position }
func (p *finiteInputProvider) SetPosition(pos int) { p.position = pos }
func (p *finiteInputProvider) ResetPosition()      { p.position = 0 }
func (p *finiteInputProvider) IncrementPosition()  { p.position++ }
func (p *finiteInputProvider) Value() []byte       { return p.values[p.position] }
func (p *finiteInputProvider) Total() int          { return len(p.values) }
func (p *finiteInputProvider) Active() bool        { return p.active }
func (p *finiteInputProvider) Enable()             { p.active = true }
func (p *finiteInputProvider) Disable()            { p.active = false }

func newFiniteMainInputProvider(mode string, providers ...*finiteInputProvider) *MainInputProvider {
	internal := make([]ffuf.InternalInputProvider, len(providers))
	for index, provider := range providers {
		internal[index] = provider
	}
	return &MainInputProvider{
		Config:    &ffuf.Config{InputMode: mode},
		Providers: internal,
		Encoders:  make(map[string]*pencode.Chain),
	}
}

func collectInputValues(provider *MainInputProvider) []map[string]string {
	values := make([]map[string]string, 0, provider.Total())
	for provider.Next() {
		current := make(map[string]string)
		for keyword, value := range provider.Value() {
			current[keyword] = string(value)
		}
		values = append(values, current)
	}
	return values
}

func TestMainInputProviderClusterbombAndSniperIteration(t *testing.T) {
	want := []map[string]string{
		{"FUZZ": "a", "FUZ2Z": "1"},
		{"FUZZ": "b", "FUZ2Z": "1"},
		{"FUZZ": "a", "FUZ2Z": "2"},
		{"FUZZ": "b", "FUZ2Z": "2"},
		{"FUZZ": "a", "FUZ2Z": "3"},
		{"FUZZ": "b", "FUZ2Z": "3"},
	}
	for _, mode := range []string{"clusterbomb", "sniper"} {
		t.Run(mode, func(t *testing.T) {
			provider := newFiniteMainInputProvider(mode,
				&finiteInputProvider{keyword: "FUZZ", values: [][]byte{[]byte("a"), []byte("b")}, active: true},
				&finiteInputProvider{keyword: "FUZ2Z", values: [][]byte{[]byte("1"), []byte("2"), []byte("3")}, active: true},
			)
			if got := collectInputValues(provider); !reflect.DeepEqual(got, want) {
				t.Fatalf("%s values = %#v, want %#v", mode, got, want)
			}
			if provider.Next() {
				t.Fatalf("Next returned true after %s input exhaustion", mode)
			}
			if got, want := provider.Position(), provider.Total(); got != want {
				t.Fatalf("Position after %s exhaustion = %d, want %d", mode, got, want)
			}
		})
	}
}

func TestMainInputProviderPitchforkIterationAndExhaustion(t *testing.T) {
	provider := newFiniteMainInputProvider("pitchfork",
		&finiteInputProvider{keyword: "FUZZ", values: [][]byte{[]byte("a"), []byte("b")}, active: true},
		&finiteInputProvider{keyword: "FUZ2Z", values: [][]byte{[]byte("1"), []byte("2"), []byte("3")}, active: true},
	)
	want := []map[string]string{
		{"FUZZ": "a", "FUZ2Z": "1"},
		{"FUZZ": "b", "FUZ2Z": "2"},
		{"FUZZ": "a", "FUZ2Z": "3"},
	}
	if got := collectInputValues(provider); !reflect.DeepEqual(got, want) {
		t.Fatalf("pitchfork values = %#v, want %#v", got, want)
	}
	if provider.Next() {
		t.Fatal("Next returned true after pitchfork input exhaustion")
	}
	if got, want := provider.Position(), provider.Total(); got != want {
		t.Fatalf("Position after exhaustion = %d, want %d", got, want)
	}
}

func TestMainInputProviderEncodesOnlyConfiguredKeyword(t *testing.T) {
	provider := newFiniteMainInputProvider("pitchfork",
		&finiteInputProvider{keyword: "FUZZ", values: [][]byte{[]byte("payload")}, active: true},
		&finiteInputProvider{keyword: "PLAIN", values: [][]byte{[]byte("unchanged")}, active: true},
	)
	chain := pencode.NewChain()
	if err := chain.Initialize([]string{"b64encode"}); err != nil {
		t.Fatalf("initialize encoder: %v", err)
	}
	provider.Encoders["FUZZ"] = chain

	if !provider.Next() {
		t.Fatal("Next returned false for non-empty encoded input")
	}
	got := provider.Value()
	if string(got["FUZZ"]) != "cGF5bG9hZA==" {
		t.Fatalf("encoded FUZZ = %q, want base64 payload", got["FUZZ"])
	}
	if string(got["PLAIN"]) != "unchanged" {
		t.Fatalf("unconfigured PLAIN = %q, want unchanged", got["PLAIN"])
	}
}

func TestMainInputProviderSkipsDisabledProviders(t *testing.T) {
	provider := newFiniteMainInputProvider("clusterbomb",
		&finiteInputProvider{keyword: "FUZZ", values: [][]byte{[]byte("a"), []byte("b")}, active: true},
		&finiteInputProvider{keyword: "DISABLED", values: [][]byte{[]byte("x"), []byte("y"), []byte("z")}, active: true},
	)
	provider.ActivateKeywords([]string{"FUZZ"})
	want := []map[string]string{{"FUZZ": "a"}, {"FUZZ": "b"}}
	if got := collectInputValues(provider); !reflect.DeepEqual(got, want) {
		t.Fatalf("values with disabled provider = %#v, want %#v", got, want)
	}
}

func TestMainInputProviderResetAndSetPosition(t *testing.T) {
	for _, mode := range []string{"clusterbomb", "pitchfork", "sniper"} {
		t.Run(mode, func(t *testing.T) {
			provider := newFiniteMainInputProvider(mode,
				&finiteInputProvider{keyword: "FUZZ", values: [][]byte{[]byte("a"), []byte("b")}, active: true},
				&finiteInputProvider{keyword: "FUZ2Z", values: [][]byte{[]byte("1"), []byte("2"), []byte("3")}, active: true},
			)
			first := collectInputValues(provider)[0]
			provider.Reset()
			if got := provider.Position(); got != 0 {
				t.Fatalf("Position after Reset = %d, want 0", got)
			}
			if !provider.Next() {
				t.Fatal("Next returned false after Reset")
			}
			resetValue := make(map[string]string)
			for keyword, value := range provider.Value() {
				resetValue[keyword] = string(value)
			}
			if !reflect.DeepEqual(resetValue, first) {
				t.Fatalf("first value after Reset = %#v, want %#v", resetValue, first)
			}

			position := 3
			wantPositioned := map[string]string{"FUZZ": "a", "FUZ2Z": "2"}
			if mode == "pitchfork" {
				position = 1
				wantPositioned = map[string]string{"FUZZ": "b", "FUZ2Z": "2"}
			}
			provider.SetPosition(position)
			positioned := provider.Value()
			if got, want := string(positioned["FUZZ"]), wantPositioned["FUZZ"]; got != want {
				t.Fatalf("FUZZ at position %d = %q, want %q", position, got, want)
			}
			if got, want := string(positioned["FUZ2Z"]), wantPositioned["FUZ2Z"]; got != want {
				t.Fatalf("FUZ2Z at position %d = %q, want %q", position, got, want)
			}
		})
	}
}

func TestMainInputProviderSingleValueAllocationBudget(t *testing.T) {
	provider := &MainInputProvider{
		Config: &ffuf.Config{InputMode: "clusterbomb"},
		Providers: []ffuf.InternalInputProvider{
			&benchmarkInputProvider{keyword: "FUZZ", value: []byte("payload"), active: true},
		},
	}
	allocs := testing.AllocsPerRun(1000, func() {
		benchmarkInputValueSink = provider.Value()
	})
	if allocs > 2 {
		t.Fatalf("Value allocations = %.0f, want at most 2", allocs)
	}
}
