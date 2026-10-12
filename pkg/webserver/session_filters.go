package webserver

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/assetnote/h3fuzz/pkg/ffuf"
	ffuffilter "github.com/assetnote/h3fuzz/pkg/filter"
)

var ErrFilterConflict = errors.New("filter addition conflicts with the persisted scan")

type filterAddition struct {
	flag string
	name string
	old  *string
	add  string
}

// AddFilters atomically validates and adds filters to a session. Once a scan
// has started only OR-mode metadata filters can be added. This guarantees that
// an edit can hide more findings but can never promise to restore responses
// that the running engine already discarded.
func (m *Manager) AddFilters(ctx context.Context, id string, request AddSessionFiltersRequest) (Session, error) {
	state := m.sequenceFor(id)
	state.filterMu.Lock()
	defer state.filterMu.Unlock()

	session, err := m.store.GetSession(ctx, id)
	if err != nil {
		return Session{}, err
	}
	if session.Status == StatusStarting {
		return Session{}, fmt.Errorf("%w: session is starting; retry after it enters the running state", ErrFilterConflict)
	}

	options := cloneOptions(session.Options)
	resultFilters := session.ResultFilters
	if resultFilters.Mode == "" {
		resultFilters.Mode = "or"
	}
	mode := options.Filter.Mode
	if mode == "" {
		mode = "or"
	}
	if session.Status == StatusIdle {
		if request.Mode != "" {
			mode = request.Mode
		}
		if mode != "or" && mode != "and" {
			return Session{}, fmt.Errorf("filter mode must be either and or or")
		}
		options.Filter.Mode = mode
	} else {
		if request.Mode != "" && request.Mode != mode {
			return Session{}, fmt.Errorf("%w: filter mode cannot be changed after a scan has started", ErrFilterConflict)
		}
		if mode != "or" {
			return Session{}, fmt.Errorf("%w: filters can only be added after start when filter mode is or", ErrFilterConflict)
		}
		if request.Regexp != "" {
			return Session{}, fmt.Errorf("%w: regexp filters cannot be added after a scan has started because response bodies are not retained", ErrFilterConflict)
		}
	}

	additions := []filterAddition{
		{flag: "fc", name: "status", old: &options.Filter.Status, add: request.Status},
		{flag: "fs", name: "size", old: &options.Filter.Size, add: request.Size},
		{flag: "fw", name: "word", old: &options.Filter.Words, add: request.Words},
		{flag: "fl", name: "line", old: &options.Filter.Lines, add: request.Lines},
		{flag: "ft", name: "time", old: &options.Filter.Time, add: request.Time},
	}
	changed := make(map[string]string)
	explicit := append([]string(nil), session.ExplicitFlags...)
	for _, addition := range additions {
		if addition.add == "" {
			continue
		}
		merged, mergeErr := mergeFilterValue(addition.name, *addition.old, addition.add)
		if mergeErr != nil {
			return Session{}, mergeErr
		}
		if merged != *addition.old {
			*addition.old = merged
			changed[addition.name] = addition.add
			if session.Status != StatusIdle {
				resultValue := resultFilterValue(&resultFilters, addition.name)
				mergedResult, mergeErr := mergeFilterValue(addition.name, *resultValue, addition.add)
				if mergeErr != nil {
					return Session{}, mergeErr
				}
				*resultValue = mergedResult
			}
		}
		explicit = append(explicit, addition.flag)
	}
	if request.Regexp != "" {
		merged := mergeRegexpFilter(options.Filter.Regexp, request.Regexp)
		if _, err := ffuffilter.NewFilterByName("regexp", merged); err != nil {
			return Session{}, err
		}
		if merged != options.Filter.Regexp {
			options.Filter.Regexp = merged
			changed["regexp"] = request.Regexp
		}
		explicit = append(explicit, "fr")
	}
	if request.Mode != "" {
		explicit = append(explicit, "fmode")
	}
	explicit = uniqueStrings(explicit)

	// Validate the complete persisted snapshot before touching either the live
	// engine or SQLite. This keeps mixed valid/invalid additions all-or-nothing.
	if _, err := newStoredResultFilter(options.Filter); err != nil {
		return Session{}, err
	}
	resultFilter, err := newStoredResultFilter(resultFilters)
	if err != nil {
		return Session{}, err
	}
	if len(changed) == 0 && options.Filter.Mode == session.Options.Filter.Mode {
		return session, nil
	}

	if err := m.store.UpdateSessionFilters(ctx, id, options, resultFilters, explicit); err != nil {
		return Session{}, err
	}
	state.mu.Lock()
	state.resultFilter = resultFilter
	state.mu.Unlock()
	if session.Status == StatusRunning || session.Status == StatusPaused {
		run, activeErr := m.activeRun(id)
		if activeErr != nil {
			// The scan may have completed while the transactional retroactive
			// recount ran. In that case persistence is the complete operation and
			// there is no future engine work left to update.
			latest, loadErr := m.store.GetSession(ctx, id)
			if loadErr != nil {
				return Session{}, loadErr
			}
			if latest.Status == StatusRunning || latest.Status == StatusPaused {
				return Session{}, m.rollbackLiveFilterUpdate(id, session, activeErr)
			}
		} else {
			if err := run.addFiltersRuntime(changed); err != nil {
				return Session{}, m.rollbackLiveFilterUpdate(id, session, err)
			}
		}
	}
	m.emitCurrent(ctx, id, "session.updated")
	return m.store.GetSession(ctx, id)
}

// rollbackLiveFilterUpdate restores the exact pre-request snapshot after the
// live engine rejects an additive filter. Engine.AddFilters guarantees a
// rejected batch publishes no matcher changes, while the store retains raw
// results, so recomputing the old visible set is lossless and leaves both sides
// honest for a corrected retry.
func (m *Manager) rollbackLiveFilterUpdate(id string, previous Session, applyErr error) error {
	resultFilter, filterErr := newStoredResultFilter(previous.ResultFilters)
	if filterErr != nil {
		return fmt.Errorf("live filter engine update failed (%v), and previous persisted filters are invalid: %w", applyErr, filterErr)
	}
	rollbackErr := m.store.UpdateSessionFilters(context.Background(), id, previous.Options,
		previous.ResultFilters, previous.ExplicitFlags)
	if rollbackErr != nil {
		return fmt.Errorf("live filter engine update failed (%v), and persisted filters could not be rolled back: %w", applyErr, rollbackErr)
	}
	state := m.sequenceFor(id)
	state.mu.Lock()
	state.resultFilter = resultFilter
	state.mu.Unlock()
	m.emitCurrent(context.Background(), id, "session.updated")
	return fmt.Errorf("live filter engine update failed; persisted filters were rolled back: %w", applyErr)
}

func resultFilterValue(options *ffuf.FilterOptions, name string) *string {
	switch name {
	case "status":
		return &options.Status
	case "size":
		return &options.Size
	case "word":
		return &options.Words
	case "line":
		return &options.Lines
	case "time":
		return &options.Time
	default:
		panic("unsupported stored result filter: " + name)
	}
}

func mergeFilterValue(name, existing, addition string) (string, error) {
	if _, err := ffuffilter.NewFilterByName(name, addition); err != nil {
		return "", err
	}
	if existing == "" {
		provider, _ := ffuffilter.NewFilterByName(name, addition)
		return provider.Repr(), nil
	}
	if name == "time" {
		return mergeTimeFilter(existing, addition)
	}
	values := uniqueCSV(append(strings.Split(existing, ","), strings.Split(addition, ",")...))
	provider, err := ffuffilter.NewFilterByName(name, strings.Join(values, ","))
	if err != nil {
		return "", err
	}
	return provider.Repr(), nil
}

func uniqueCSV(values []string) []string {
	seen := make(map[string]bool, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		if !seen[value] {
			seen[value] = true
			out = append(out, value)
		}
	}
	return out
}

func mergeTimeFilter(existing, addition string) (string, error) {
	if existing == addition {
		return existing, nil
	}
	if len(existing) < 2 || len(addition) < 2 {
		return "", fmt.Errorf("invalid time filter")
	}
	oldValue, oldErr := strconv.ParseInt(existing[1:], 10, 64)
	newValue, newErr := strconv.ParseInt(addition[1:], 10, 64)
	if oldErr != nil || newErr != nil {
		return "", fmt.Errorf("invalid time filter")
	}
	if existing[0] != addition[0] {
		return "", fmt.Errorf("%w: cannot add %q to existing time filter %q because their union is not representable by h3fuzz -ft", ErrFilterConflict, addition, existing)
	}
	if existing[0] == '>' && newValue < oldValue || existing[0] == '<' && newValue > oldValue {
		return addition, nil
	}
	return existing, nil
}

func mergeRegexpFilter(existing, addition string) string {
	if existing == "" || existing == addition {
		return addition
	}
	wrapped := "(?:" + addition + ")"
	if strings.Contains(existing, wrapped) {
		return existing
	}
	return "(?:" + existing + ")|" + wrapped
}
