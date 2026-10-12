package filter

import (
	"encoding/json"
	"fmt"
	"sort"
	"sync"
	"sync/atomic"

	"github.com/assetnote/h3fuzz/pkg/ffuf"
)

// MatcherManager handles both filters and matchers. Writers serialize under mu,
// copy only the maps they change, then atomically publish a fully immutable
// snapshot. Matches reads one snapshot without locks or per-response map copies.
// The public map getters still return defensive copies because their callers are
// outside this package and may retain or modify the returned map.
type MatcherManager struct {
	mu       sync.Mutex
	snapshot atomic.Pointer[matcherSnapshot]
}

type matcherSnapshot struct {
	isCalibrated     bool
	matchers         map[string]ffuf.FilterProvider
	filters          map[string]ffuf.FilterProvider
	perDomainFilters map[string]*perDomainSnapshot
}

// perDomainSnapshot is deliberately distinct from the legacy, mutable
// PerDomainFilter value below. No caller can obtain one through the public API,
// so a published matcherSnapshot remains immutable for its entire lifetime.
type perDomainSnapshot struct {
	isCalibrated bool
	filters      map[string]ffuf.FilterProvider
}

func (p *perDomainSnapshot) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		IsCalibrated bool
		Filters      map[string]ffuf.FilterProvider
	}{IsCalibrated: p.isCalibrated, Filters: p.filters})
}

// PerDomainFilter is kept as a detached compatibility value for callers of
// NewPerDomainFilter. MatcherManager never publishes this mutable type.
type PerDomainFilter struct {
	IsCalibrated bool
	Filters      map[string]ffuf.FilterProvider
}

// NewPerDomainFilter copies the supplied (global) filter map. Storing the map by
// reference aliased the global Filters, so a per-domain filter write mutated the
// global filter set and leaked into every host.
func NewPerDomainFilter(globfilters map[string]ffuf.FilterProvider) *PerDomainFilter {
	return &PerDomainFilter{IsCalibrated: false, Filters: copyFilterMap(globfilters)}
}

func (p *PerDomainFilter) SetCalibrated(value bool) {
	p.IsCalibrated = value
}

// copyFilterMap returns a shallow copy of a filter map. The FilterProvider values
// are safe to share: they are replaced, never mutated, after creation.
func copyFilterMap(in map[string]ffuf.FilterProvider) map[string]ffuf.FilterProvider {
	out := make(map[string]ffuf.FilterProvider, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func copyPerDomainFilters(in map[string]*perDomainSnapshot) map[string]*perDomainSnapshot {
	out := make(map[string]*perDomainSnapshot, len(in))
	for host, filters := range in {
		out[host] = filters
	}
	return out
}

var emptyMatcherSnapshot = &matcherSnapshot{
	matchers:         map[string]ffuf.FilterProvider{},
	filters:          map[string]ffuf.FilterProvider{},
	perDomainFilters: map[string]*perDomainSnapshot{},
}

func (f *MatcherManager) loadSnapshot() *matcherSnapshot {
	if snapshot := f.snapshot.Load(); snapshot != nil {
		return snapshot
	}
	return emptyMatcherSnapshot
}

func NewMatcherManager() ffuf.MatcherManager {
	manager := &MatcherManager{}
	manager.snapshot.Store(emptyMatcherSnapshot)
	return manager
}

// MarshalJSON preserves MatcherManager's historical exported JSON shape even
// though its mutable maps now live behind an unexported atomic snapshot.
func (f *MatcherManager) MarshalJSON() ([]byte, error) {
	snapshot := f.loadSnapshot()
	return json.Marshal(struct {
		IsCalibrated     bool
		Matchers         map[string]ffuf.FilterProvider
		Filters          map[string]ffuf.FilterProvider
		PerDomainFilters map[string]*perDomainSnapshot
	}{
		IsCalibrated:     snapshot.isCalibrated,
		Matchers:         snapshot.matchers,
		Filters:          snapshot.filters,
		PerDomainFilters: snapshot.perDomainFilters,
	})
}

func (f *MatcherManager) SetCalibrated(value bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	current := f.loadSnapshot()
	if current.isCalibrated == value {
		return
	}
	next := *current
	next.isCalibrated = value
	f.snapshot.Store(&next)
}

func (f *MatcherManager) SetCalibratedForHost(host string, value bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	current := f.loadSnapshot()
	existing := current.perDomainFilters[host]
	if existing != nil && existing.isCalibrated == value {
		return
	}
	perDomain := copyPerDomainFilters(current.perDomainFilters)
	if existing != nil {
		perDomain[host] = &perDomainSnapshot{isCalibrated: value, filters: existing.filters}
	} else {
		perDomain[host] = &perDomainSnapshot{isCalibrated: value, filters: copyFilterMap(current.filters)}
	}
	next := *current
	next.perDomainFilters = perDomain
	f.snapshot.Store(&next)
}

func NewFilterByName(name string, value string) (ffuf.FilterProvider, error) {
	if name == "status" {
		return NewStatusFilter(value)
	}
	if name == "size" {
		return NewSizeFilter(value)
	}
	if name == "word" {
		return NewWordFilter(value)
	}
	if name == "line" {
		return NewLineFilter(value)
	}
	if name == "regexp" {
		return NewRegexpFilter(value)
	}
	if name == "time" {
		return NewTimeFilter(value)
	}
	return nil, fmt.Errorf("Could not create filter with name %s", name)
}

// AddFilter adds a new filter to MatcherManager
func (f *MatcherManager) AddFilter(name string, option string, replace bool) error {
	newFilter, err := NewFilterByName(name, option)
	if err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	current := f.loadSnapshot()
	filters := copyFilterMap(current.filters)
	if existing := current.filters[name]; existing != nil && !replace {
		newFilter, err = appendFilterValue(name, existing, option)
		if err != nil {
			return err
		}
	}
	filters[name] = newFilter
	next := *current
	next.filters = filters
	f.snapshot.Store(&next)
	return nil
}

// appendFilterValue constructs the union of an existing filter and one new
// value. Most ffuf metric filters encode unions as comma-separated ranges. The
// time filter accepts one inequality, so its representable monotonic union is
// the less restrictive threshold in the same direction.
func appendFilterValue(name string, existing ffuf.FilterProvider, option string) (ffuf.FilterProvider, error) {
	if name == "time" {
		added, err := NewTimeFilter(option)
		if err != nil {
			return nil, err
		}
		current, ok := existing.(*TimeFilter)
		if !ok {
			return nil, fmt.Errorf("existing time filter has unexpected type %T", existing)
		}
		next := added.(*TimeFilter)
		if current.gt != next.gt || current.lt != next.lt {
			return nil, fmt.Errorf("cannot add %q to existing time filter %q because their union is not representable by h3fuzz -ft", option, existing.Repr())
		}
		if current.gt && next.ms < current.ms || current.lt && next.ms > current.ms {
			return next, nil
		}
		return current, nil
	}
	if name == "regexp" {
		return NewRegexpFilter("(?:" + existing.Repr() + ")|(?:" + option + ")")
	}
	return NewFilterByName(name, existing.Repr()+","+option)
}

// AddFilters appends a batch of metric filters and publishes the complete
// update with one atomic snapshot store. This is intentionally narrower than
// AddFilter: live web scans retain only response metrics, not response bodies,
// so regexp filters cannot be applied consistently to results seen before the
// update. Callers use this method for monotonic post-start filtering.
//
// Existing values of the same type are retained. For the normal "or" filter
// mode this can only remove additional findings; it can never make a previously
// filtered response visible. Per-domain autocalibration snapshots receive the
// same additions so -ach does not bypass a filter added while a scan is live.
func (f *MatcherManager) AddFilters(options map[string]string) error {
	if len(options) == 0 {
		return nil
	}

	names := make([]string, 0, len(options))
	for name := range options {
		switch name {
		case "status", "size", "word", "line", "time":
			names = append(names, name)
		case "regexp":
			return fmt.Errorf("regexp filters cannot be added after a scan starts because historical response bodies are not retained")
		default:
			return fmt.Errorf("unsupported live filter %q", name)
		}
	}
	sort.Strings(names)

	f.mu.Lock()
	defer f.mu.Unlock()
	current := f.loadSnapshot()
	filters := copyFilterMap(current.filters)
	for _, name := range names {
		option := options[name]
		provider, err := NewFilterByName(name, option)
		if existing := filters[name]; existing != nil {
			provider, err = appendFilterValue(name, existing, option)
		}
		if err != nil {
			return err
		}
		filters[name] = provider
	}

	perDomain := copyPerDomainFilters(current.perDomainFilters)
	for host, existingDomain := range current.perDomainFilters {
		domainFilters := copyFilterMap(existingDomain.filters)
		for _, name := range names {
			option := options[name]
			provider, err := NewFilterByName(name, option)
			if existing := domainFilters[name]; existing != nil {
				provider, err = appendFilterValue(name, existing, option)
			}
			if err != nil {
				return err
			}
			domainFilters[name] = provider
		}
		perDomain[host] = &perDomainSnapshot{
			isCalibrated: existingDomain.isCalibrated,
			filters:      domainFilters,
		}
	}

	next := *current
	next.filters = filters
	next.perDomainFilters = perDomain
	f.snapshot.Store(&next)
	return nil
}

// AddPerDomainFilter adds a new filter to PerDomainFilter configuration
func (f *MatcherManager) AddPerDomainFilter(domain string, name string, option string) error {
	newFilter, err := NewFilterByName(name, option)
	if err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	current := f.loadSnapshot()
	existingDomain := current.perDomainFilters[domain]
	var filters map[string]ffuf.FilterProvider
	calibrated := false
	if existingDomain != nil {
		filters = copyFilterMap(existingDomain.filters)
		calibrated = existingDomain.isCalibrated
	} else {
		filters = copyFilterMap(current.filters)
	}
	if existing := filters[name]; existing != nil {
		newFilter, err = NewFilterByName(name, existing.Repr()+","+option)
		if err != nil {
			return err
		}
	}
	filters[name] = newFilter
	perDomain := copyPerDomainFilters(current.perDomainFilters)
	perDomain[domain] = &perDomainSnapshot{isCalibrated: calibrated, filters: filters}
	next := *current
	next.perDomainFilters = perDomain
	f.snapshot.Store(&next)
	return nil
}

// RemoveFilter removes a filter of a given type
func (f *MatcherManager) RemoveFilter(name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	current := f.loadSnapshot()
	if _, exists := current.filters[name]; !exists {
		return
	}
	filters := copyFilterMap(current.filters)
	delete(filters, name)
	next := *current
	next.filters = filters
	f.snapshot.Store(&next)
}

// AddMatcher adds a new matcher to Config
func (f *MatcherManager) AddMatcher(name string, option string) error {
	newMatcher, err := NewFilterByName(name, option)
	if err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	current := f.loadSnapshot()
	matchers := copyFilterMap(current.matchers)
	if existing := current.matchers[name]; existing != nil {
		newMatcher, err = NewFilterByName(name, existing.Repr()+","+option)
		if err != nil {
			return err
		}
	}
	matchers[name] = newMatcher
	next := *current
	next.matchers = matchers
	f.snapshot.Store(&next)
	return nil
}

func (f *MatcherManager) GetFilters() map[string]ffuf.FilterProvider {
	return copyFilterMap(f.loadSnapshot().filters)
}

func (f *MatcherManager) GetMatchers() map[string]ffuf.FilterProvider {
	return copyFilterMap(f.loadSnapshot().matchers)
}

func (f *MatcherManager) FiltersForDomain(domain string) map[string]ffuf.FilterProvider {
	snapshot := f.loadSnapshot()
	if snapshot.perDomainFilters[domain] == nil {
		return copyFilterMap(snapshot.filters)
	}
	return copyFilterMap(snapshot.perDomainFilters[domain].filters)
}

func (f *MatcherManager) CalibratedForDomain(domain string) bool {
	if domainFilters := f.loadSnapshot().perDomainFilters[domain]; domainFilters != nil {
		return domainFilters.isCalibrated
	}
	return false
}

func (f *MatcherManager) Calibrated() bool {
	return f.loadSnapshot().isCalibrated
}

// Matches reports whether resp passes the configured matchers and filters. It is
// the match decision that used to live in Job.isMatch, moved here so it sits with
// the rule data it decides over and is unit-testable without an engine. perHost
// selects the per-domain filter set (autocalibration -ach); matcherMode/filterMode
// are the and/or combination modes. One atomic load pins all rule maps for the
// decision; writers never mutate maps reachable from a published snapshot.
func (f *MatcherManager) Matches(resp *ffuf.Response, perHost bool, matcherMode string, filterMode string) bool {
	matched := false
	snapshot := f.loadSnapshot()
	filters := snapshot.filters
	if perHost && resp != nil && resp.Request != nil {
		if domainFilters := snapshot.perDomainFilters[ffuf.HostURLFromRequest(*resp.Request)]; domainFilters != nil {
			filters = domainFilters.filters
		}
	}
	for _, m := range snapshot.matchers {
		match, err := m.Filter(resp)
		if err != nil {
			continue
		}
		if match {
			matched = true
		} else if matcherMode == "and" {
			// we already know this isn't an "and" match
			return false
		}
	}
	// The response was not matched, return before running filters
	if !matched {
		return false
	}
	for _, flt := range filters {
		fv, err := flt.Filter(resp)
		if err != nil {
			continue
		}
		if fv {
			if filterMode == "or" {
				// return early, as a filter matched
				return false
			}
		} else {
			if filterMode == "and" {
				// return early, as not all filters matched in "and" mode
				return true
			}
		}
	}
	if len(filters) > 0 && filterMode == "and" {
		// we did not return early, so all filters were matched
		return false
	}
	return true
}
