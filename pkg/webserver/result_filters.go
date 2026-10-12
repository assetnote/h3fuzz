package webserver

import (
	"time"

	"github.com/assetnote/h3fuzz/pkg/engine"
	"github.com/assetnote/h3fuzz/pkg/ffuf"
	ffuffilter "github.com/assetnote/h3fuzz/pkg/filter"
)

type storedResultFilter struct {
	mode      string
	providers map[string]ffuf.FilterProvider
}

func newStoredResultFilter(options ffuf.FilterOptions) (*storedResultFilter, error) {
	mode := options.Mode
	if mode == "" {
		mode = "or"
	}
	providers := make(map[string]ffuf.FilterProvider)
	values := []struct {
		name  string
		value string
	}{
		{name: "status", value: options.Status},
		{name: "size", value: options.Size},
		{name: "word", value: options.Words},
		{name: "line", value: options.Lines},
		{name: "time", value: options.Time},
	}
	for _, candidate := range values {
		if candidate.value == "" {
			continue
		}
		provider, err := ffuffilter.NewFilterByName(candidate.name, candidate.value)
		if err != nil {
			return nil, err
		}
		providers[candidate.name] = provider
	}
	// Validate mode through the shared evaluator even when there are no metric
	// filters. Regexp is validated at the API boundary but is never included
	// here because response bodies are intentionally not persisted.
	if _, err := engine.ResultPassesFilters(ffuf.Result{}, providers, mode); err != nil {
		return nil, err
	}
	return &storedResultFilter{mode: mode, providers: providers}, nil
}

func (f *storedResultFilter) keep(result StoredResult) (bool, error) {
	if f == nil || len(f.providers) == 0 {
		return true, nil
	}
	return engine.ResultPassesFilters(ffuf.Result{
		Input:            result.Input,
		Position:         result.Position,
		StatusCode:       result.StatusCode,
		ContentLength:    result.ContentLength,
		ContentWords:     result.ContentWords,
		ContentLines:     result.ContentLines,
		ContentType:      result.ContentType,
		RedirectLocation: result.RedirectLocation,
		Url:              result.URL,
		Duration:         time.Duration(result.DurationNS),
		ScraperData:      result.ScraperData,
		Host:             result.Host,
	}, f.providers, f.mode)
}
