package webserver

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/assetnote/h3fuzz/pkg/ffuf"
)

const (
	maxExplorerPageSize = 1000
	maxTerminalSearch   = 512
	maxSavedViewName    = 80
	maxPresetName       = 80
)

// SessionOrganization is intentionally stored separately from the execution
// record. Organizing a running scan must never rewrite its scanner config or
// race the hot progress-update path.
type SessionOrganization struct {
	Pinned       bool   `json:"pinned"`
	Archived     bool   `json:"archived"`
	GroupName    string `json:"group_name,omitempty"`
	ClonedFromID string `json:"cloned_from_id,omitempty"`
}

type PatchSessionOrganizationRequest struct {
	Pinned    *bool   `json:"pinned,omitempty"`
	Archived  *bool   `json:"archived,omitempty"`
	GroupName *string `json:"group_name,omitempty"`
}

type BulkSessionRequest struct {
	IDs    []string `json:"ids"`
	Action string   `json:"action"`
}

type BulkSessionResult struct {
	Succeeded []string          `json:"succeeded"`
	Failed    map[string]string `json:"failed"`
}

type CloneSessionRequest struct {
	Name          string              `json:"name,omitempty"`
	Target        *string             `json:"target,omitempty"`
	Wordlist      *string             `json:"wordlist,omitempty"`
	Profile       *string             `json:"profile,omitempty"`
	Options       *ffuf.ConfigOptions `json:"options,omitempty"`
	ExplicitFlags *[]string           `json:"explicit_flags,omitempty"`
	AutoStart     bool                `json:"auto_start"`
}

type NumericFacet struct {
	Min int64 `json:"min"`
	Max int64 `json:"max"`
}

type ResultFacets struct {
	Status      map[string]int64 `json:"status"`
	ContentType map[string]int64 `json:"content_type"`
	Size        NumericFacet     `json:"size"`
	Words       NumericFacet     `json:"words"`
	Lines       NumericFacet     `json:"lines"`
}

type ExploredResult struct {
	StoredResult
	DuplicateCount int64  `json:"duplicate_count,omitempty"`
	WildcardGroup  bool   `json:"wildcard_group,omitempty"`
	Signature      string `json:"signature,omitempty"`
}

type ResultExplorerPage struct {
	Results []ExploredResult `json:"results"`
	Total   int64            `json:"total"`
	Offset  int              `json:"offset"`
	Limit   int              `json:"limit"`
	HasMore bool             `json:"has_more"`
	Facets  ResultFacets     `json:"facets"`
}

type ResultExplorerQuery struct {
	Search          string
	Statuses        []int64
	ContentTypes    []string
	SizeMin         *int64
	SizeMax         *int64
	WordsMin        *int64
	WordsMax        *int64
	LinesMin        *int64
	LinesMax        *int64
	Sort            string
	Descending      bool
	Offset          int
	Limit           int
	GroupDuplicates bool
	// SnapshotID is used only by the lazy tree protocol. List exploration stays
	// live, while all pages of one expanded tree share a stable result boundary.
	SnapshotID *int64
}

type SavedResultView struct {
	ID        string          `json:"id"`
	SessionID string          `json:"session_id"`
	Name      string          `json:"name"`
	Query     json.RawMessage `json:"query"`
	CreatedAt time.Time       `json:"created_at"`
	UpdatedAt time.Time       `json:"updated_at"`
}

type SaveResultViewRequest struct {
	Name  string          `json:"name"`
	Query json.RawMessage `json:"query"`
}

// savedResultViewQuery is the persisted, UI-facing form of an explorer query.
// Keep this deliberately narrower than ResultExplorerQuery: pagination and the
// tree snapshot boundary describe a request, not a reusable filter view.
type savedResultViewQuery struct {
	Query           string   `json:"query,omitempty"`
	Statuses        []int64  `json:"statuses,omitempty"`
	ContentTypes    []string `json:"contentTypes,omitempty"`
	SizeMin         *int64   `json:"sizeMin,omitempty"`
	SizeMax         *int64   `json:"sizeMax,omitempty"`
	WordsMin        *int64   `json:"wordsMin,omitempty"`
	WordsMax        *int64   `json:"wordsMax,omitempty"`
	LinesMin        *int64   `json:"linesMin,omitempty"`
	LinesMax        *int64   `json:"linesMax,omitempty"`
	Sort            string   `json:"sort,omitempty"`
	Order           string   `json:"order,omitempty"`
	GroupDuplicates bool     `json:"groupDuplicates,omitempty"`
}

type TerminalQuery struct {
	Search     string
	Levels     []string
	Offset     int
	Limit      int
	Latest     bool
	BeforeID   *int64
	SnapshotID *int64
}

type TerminalSearchPage struct {
	Entries []TerminalEntry  `json:"entries"`
	Total   int64            `json:"total"`
	Offset  int              `json:"offset"`
	Limit   int              `json:"limit"`
	HasMore bool             `json:"has_more"`
	Levels  map[string]int64 `json:"levels"`
	// Cursor fields are populated for latest=true requests. SnapshotID freezes
	// the searchable history boundary, while NextBeforeID pages backwards
	// without paying SQLite's increasingly expensive OFFSET cost.
	SnapshotID   int64 `json:"snapshot_id,omitempty"`
	NextBeforeID int64 `json:"next_before_id,omitempty"`
	HasOlder     bool  `json:"has_older,omitempty"`
}

type Preset struct {
	ID            string             `json:"id"`
	Name          string             `json:"name"`
	Description   string             `json:"description,omitempty"`
	Wordlist      string             `json:"wordlist"`
	Profile       string             `json:"profile"`
	Options       ffuf.ConfigOptions `json:"options"`
	ExplicitFlags []string           `json:"explicit_flags"`
	CreatedAt     time.Time          `json:"created_at"`
	UpdatedAt     time.Time          `json:"updated_at"`
}

type WordlistInspection struct {
	Path                    string `json:"path"`
	Readable                bool   `json:"readable"`
	Entries                 int64  `json:"entries"`
	ExtensionMarkers        int64  `json:"extension_markers"`
	CommentEntries          int64  `json:"comment_entries"`
	CommentExtensionMarkers int64  `json:"comment_extension_markers"`
	SizeBytes               int64  `json:"size_bytes,omitempty"`
	Error                   string `json:"error,omitempty"`
}

type SessionComparison struct {
	LeftSessionID     string         `json:"left_session_id"`
	RightSessionID    string         `json:"right_session_id"`
	Added             []StoredResult `json:"added"`
	Removed           []StoredResult `json:"removed"`
	AddedTotal        int64          `json:"added_total"`
	RemovedTotal      int64          `json:"removed_total"`
	UnchangedCount    int64          `json:"unchanged_count"`
	AddedNextCursor   string         `json:"added_next_cursor,omitempty"`
	RemovedNextCursor string         `json:"removed_next_cursor,omitempty"`
	AddedHasMore      bool           `json:"added_has_more"`
	RemovedHasMore    bool           `json:"removed_has_more"`
	Limit             int            `json:"limit"`
}

type SessionComparisonQuery struct {
	Side   string
	Cursor string
	Limit  int
}

func (s *Store) migrateProductFeatures(ctx context.Context) error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS session_organization (
			session_id TEXT PRIMARY KEY REFERENCES sessions(id) ON DELETE CASCADE,
			pinned INTEGER NOT NULL DEFAULT 0,
			archived INTEGER NOT NULL DEFAULT 0,
			group_name TEXT NOT NULL DEFAULT '',
			cloned_from_id TEXT NOT NULL DEFAULT ''
		)`,
		`CREATE INDEX IF NOT EXISTS session_organization_listing
			ON session_organization(archived,pinned,group_name)`,
		`CREATE INDEX IF NOT EXISTS results_session_url_cursor
			ON results(session_id,url,id DESC)`,
		`CREATE TABLE IF NOT EXISTS result_views (
			id TEXT PRIMARY KEY,
			session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
			name TEXT NOT NULL,
			query_json BLOB NOT NULL,
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL,
			UNIQUE(session_id,name)
		)`,
		`CREATE INDEX IF NOT EXISTS result_views_session ON result_views(session_id,updated_at)`,
		`CREATE TABLE IF NOT EXISTS presets (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL UNIQUE,
			description TEXT NOT NULL DEFAULT '',
			wordlist TEXT NOT NULL,
			profile TEXT NOT NULL,
			options_json BLOB NOT NULL,
			explicit_flags_json BLOB NOT NULL,
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS results_explorer_metrics
			ON results(session_id,status,content_type,length,words,lines,id)`,
		`CREATE INDEX IF NOT EXISTS terminal_search_level
			ON terminal_log(session_id,level,id)`,
	}
	for _, statement := range statements {
		if _, err := s.db.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("initialize product feature storage: %w", err)
		}
	}
	return nil
}

func (s *Store) SessionOrganization(ctx context.Context, sessionID string) (SessionOrganization, error) {
	var organization SessionOrganization
	var pinned, archived int
	err := s.db.QueryRowContext(ctx, `SELECT pinned,archived,group_name,cloned_from_id
		FROM session_organization WHERE session_id=?`, sessionID).
		Scan(&pinned, &archived, &organization.GroupName, &organization.ClonedFromID)
	if errors.Is(err, sql.ErrNoRows) {
		var exists int
		if sessionErr := s.db.QueryRowContext(ctx, `SELECT 1 FROM sessions WHERE id=?`, sessionID).Scan(&exists); sessionErr != nil {
			if errors.Is(sessionErr, sql.ErrNoRows) {
				return SessionOrganization{}, ErrNotFound
			}
			return SessionOrganization{}, sessionErr
		}
		return organization, nil
	}
	organization.Pinned, organization.Archived = pinned != 0, archived != 0
	return organization, err
}

func (s *Store) enrichSessionOrganization(ctx context.Context, session *Session) error {
	var pinned, archived int
	err := s.db.QueryRowContext(ctx, `SELECT pinned,archived,group_name,cloned_from_id
		FROM session_organization WHERE session_id=?`, session.ID).
		Scan(&pinned, &archived, &session.GroupName, &session.ClonedFromID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	session.Pinned, session.Archived = pinned != 0, archived != 0
	return err
}

func (s *Store) enrichSessionOrganizations(ctx context.Context, sessions []Session) error {
	if len(sessions) == 0 {
		return nil
	}
	rows, err := s.db.QueryContext(ctx, `SELECT session_id,pinned,archived,group_name,cloned_from_id FROM session_organization`)
	if err != nil {
		return err
	}
	defer rows.Close()
	organizations := make(map[string]SessionOrganization)
	for rows.Next() {
		var sessionID string
		var organization SessionOrganization
		var pinned, archived int
		if err := rows.Scan(&sessionID, &pinned, &archived, &organization.GroupName, &organization.ClonedFromID); err != nil {
			return err
		}
		organization.Pinned, organization.Archived = pinned != 0, archived != 0
		organizations[sessionID] = organization
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for index := range sessions {
		organization := organizations[sessions[index].ID]
		sessions[index].Pinned, sessions[index].Archived = organization.Pinned, organization.Archived
		sessions[index].GroupName, sessions[index].ClonedFromID = organization.GroupName, organization.ClonedFromID
	}
	return nil
}

func (s *Store) PatchSessionOrganization(ctx context.Context, sessionID string, patch PatchSessionOrganizationRequest) (SessionOrganization, error) {
	if _, err := s.GetSession(ctx, sessionID); err != nil {
		return SessionOrganization{}, err
	}
	current, err := s.SessionOrganization(ctx, sessionID)
	if err != nil {
		return SessionOrganization{}, err
	}
	if patch.Pinned != nil {
		current.Pinned = *patch.Pinned
	}
	if patch.Archived != nil {
		current.Archived = *patch.Archived
	}
	if patch.GroupName != nil {
		current.GroupName = strings.TrimSpace(*patch.GroupName)
		if len(current.GroupName) > 120 {
			return SessionOrganization{}, fmt.Errorf("group name must be at most 120 characters")
		}
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO session_organization(session_id,pinned,archived,group_name,cloned_from_id)
		VALUES(?,?,?,?,?) ON CONFLICT(session_id) DO UPDATE SET
		pinned=excluded.pinned,archived=excluded.archived,group_name=excluded.group_name`,
		sessionID, boolInt(current.Pinned), boolInt(current.Archived), current.GroupName, current.ClonedFromID)
	return current, err
}

func (s *Store) SetClonedFrom(ctx context.Context, sessionID, sourceID string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO session_organization(session_id,cloned_from_id)
		VALUES(?,?) ON CONFLICT(session_id) DO UPDATE SET cloned_from_id=excluded.cloned_from_id`, sessionID, sourceID)
	return err
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func resultSignature(result StoredResult) (string, bool) {
	fingerprint := strings.TrimSpace(result.ResponseFingerprint)
	if fingerprint == "" {
		return "", false
	}
	contentType := strings.ToLower(strings.TrimSpace(strings.Split(result.ContentType, ";")[0]))
	return fmt.Sprintf("%d:%s:%s", result.StatusCode, contentType, fingerprint), true
}

// resultExplorerPredicate converts only row-selection filters into SQL. Sort,
// pagination, and duplicate presentation are deliberately handled by callers.
// Keeping one predicate builder prevents the list and directory-tree views from
// silently drifting apart as explorer filters evolve.
func resultExplorerPredicate(sessionID string, query ResultExplorerQuery) ([]string, []any) {
	where := []string{"session_id=?"}
	args := []any{sessionID}
	if query.Search != "" {
		where = append(where, `(LOWER(url) LIKE ? ESCAPE '\' OR LOWER(content_type) LIKE ? ESCAPE '\' OR LOWER(CAST(input_json AS TEXT)) LIKE ? ESCAPE '\' OR LOWER(CAST(scraper_json AS TEXT)) LIKE ? ESCAPE '\')`)
		like := "%" + escapeLike(strings.ToLower(query.Search)) + "%"
		args = append(args, like, like, like, like)
	}
	appendIntSet := func(column string, values []int64) {
		if len(values) == 0 {
			return
		}
		placeholders := make([]string, len(values))
		for index, value := range values {
			placeholders[index] = "?"
			args = append(args, value)
		}
		where = append(where, column+" IN ("+strings.Join(placeholders, ",")+")")
	}
	appendStringSet := func(column string, values []string) {
		if len(values) == 0 {
			return
		}
		placeholders := make([]string, len(values))
		for index, value := range values {
			placeholders[index] = "?"
			args = append(args, value)
		}
		where = append(where, column+" IN ("+strings.Join(placeholders, ",")+")")
	}
	appendRange := func(column string, minimum, maximum *int64) {
		if minimum != nil {
			where = append(where, column+">=?")
			args = append(args, *minimum)
		}
		if maximum != nil {
			where = append(where, column+"<=?")
			args = append(args, *maximum)
		}
	}
	appendIntSet("status", query.Statuses)
	appendStringSet("content_type", query.ContentTypes)
	appendRange("length", query.SizeMin, query.SizeMax)
	appendRange("words", query.WordsMin, query.WordsMax)
	appendRange("lines", query.LinesMin, query.LinesMax)
	return where, args
}

func (s *Store) ExploreResults(ctx context.Context, sessionID string, query ResultExplorerQuery) (ResultExplorerPage, error) {
	if query.Limit <= 0 {
		query.Limit = 200
	}
	if query.Limit > maxExplorerPageSize {
		query.Limit = maxExplorerPageSize
	}
	if query.Offset < 0 {
		query.Offset = 0
	}
	resultFilter, err := s.resultFilter(ctx, sessionID)
	if err != nil {
		return ResultExplorerPage{}, err
	}

	where, args := resultExplorerPredicate(sessionID, query)

	orderColumns := map[string]string{
		"created": "id", "url": "url", "status": "status", "content_type": "content_type",
		"size": "length", "words": "words", "lines": "lines", "duration": "duration_ns",
	}
	orderColumn := orderColumns[query.Sort]
	if orderColumn == "" {
		orderColumn = "id"
	}
	direction := "ASC"
	if query.Descending {
		direction = "DESC"
	}
	if len(resultFilter.providers) == 0 && !query.GroupDuplicates {
		return s.exploreResultsIndexed(ctx, sessionID, query, where, args, orderColumn, direction)
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id,input_json,position,status,length,words,lines,content_type,
		redirect_location,url,duration_ns,scraper_json,host,response_fingerprint,created_at FROM results WHERE `+
		strings.Join(where, " AND ")+` ORDER BY `+orderColumn+` `+direction+`,id `+direction, args...)
	if err != nil {
		return ResultExplorerPage{}, err
	}
	defer rows.Close()

	page := ResultExplorerPage{
		Results: []ExploredResult{}, Offset: query.Offset, Limit: query.Limit,
		Facets: ResultFacets{Status: map[string]int64{}, ContentType: map[string]int64{}},
	}
	grouped := make(map[string]int)
	groupedResults := make([]ExploredResult, 0)
	seenVisible := 0
	firstFacet := true
	for rows.Next() {
		result, scanErr := scanStoredResult(rows, sessionID)
		if scanErr != nil {
			return ResultExplorerPage{}, scanErr
		}
		keep, filterErr := resultFilter.keep(result)
		if filterErr != nil {
			return ResultExplorerPage{}, filterErr
		}
		if !keep {
			continue
		}
		page.Facets.Status[strconv.FormatInt(result.StatusCode, 10)]++
		page.Facets.ContentType[result.ContentType]++
		if firstFacet {
			page.Facets.Size = NumericFacet{Min: result.ContentLength, Max: result.ContentLength}
			page.Facets.Words = NumericFacet{Min: result.ContentWords, Max: result.ContentWords}
			page.Facets.Lines = NumericFacet{Min: result.ContentLines, Max: result.ContentLines}
			firstFacet = false
		} else {
			page.Facets.Size.Min = min64(page.Facets.Size.Min, result.ContentLength)
			page.Facets.Size.Max = max64(page.Facets.Size.Max, result.ContentLength)
			page.Facets.Words.Min = min64(page.Facets.Words.Min, result.ContentWords)
			page.Facets.Words.Max = max64(page.Facets.Words.Max, result.ContentWords)
			page.Facets.Lines.Min = min64(page.Facets.Lines.Min, result.ContentLines)
			page.Facets.Lines.Max = max64(page.Facets.Lines.Max, result.ContentLines)
		}

		signature, hasFingerprint := resultSignature(result)
		if query.GroupDuplicates {
			if index, exists := grouped[signature]; hasFingerprint && exists {
				groupedResults[index].DuplicateCount++
				continue
			}
			if hasFingerprint {
				grouped[signature] = len(groupedResults)
			}
			groupedResults = append(groupedResults, ExploredResult{
				StoredResult: result, DuplicateCount: 1, Signature: signature,
			})
			continue
		}
		page.Total++
		if seenVisible < query.Offset {
			seenVisible++
			continue
		}
		if len(page.Results) >= query.Limit {
			continue
		}
		entry := ExploredResult{StoredResult: result, DuplicateCount: 1, Signature: signature}
		page.Results = append(page.Results, entry)
		seenVisible++
	}
	if err := rows.Err(); err != nil {
		return ResultExplorerPage{}, err
	}
	if query.GroupDuplicates {
		page.Total = int64(len(groupedResults))
		start := min(query.Offset, len(groupedResults))
		end := min(start+query.Limit, len(groupedResults))
		page.Results = append(page.Results, groupedResults[start:end]...)
	}
	page.HasMore = int64(query.Offset+len(page.Results)) < page.Total
	return page, nil
}

func (s *Store) exploreResultsIndexed(ctx context.Context, sessionID string, query ResultExplorerQuery,
	where []string, args []any, orderColumn, direction string) (ResultExplorerPage, error) {
	clause := strings.Join(where, " AND ")
	page := ResultExplorerPage{
		Results: []ExploredResult{}, Offset: query.Offset, Limit: query.Limit,
		Facets: ResultFacets{Status: map[string]int64{}, ContentType: map[string]int64{}},
	}
	var sizeMin, sizeMax, wordsMin, wordsMax, linesMin, linesMax sql.NullInt64
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*),MIN(length),MAX(length),MIN(words),MAX(words),MIN(lines),MAX(lines)
		FROM results WHERE `+clause, args...).Scan(&page.Total, &sizeMin, &sizeMax, &wordsMin, &wordsMax, &linesMin, &linesMax); err != nil {
		return ResultExplorerPage{}, err
	}
	if sizeMin.Valid {
		page.Facets.Size = NumericFacet{Min: sizeMin.Int64, Max: sizeMax.Int64}
		page.Facets.Words = NumericFacet{Min: wordsMin.Int64, Max: wordsMax.Int64}
		page.Facets.Lines = NumericFacet{Min: linesMin.Int64, Max: linesMax.Int64}
	}
	statusRows, err := s.db.QueryContext(ctx, `SELECT status,COUNT(*) FROM results WHERE `+clause+` GROUP BY status`, args...)
	if err != nil {
		return ResultExplorerPage{}, err
	}
	for statusRows.Next() {
		var status, count int64
		if err := statusRows.Scan(&status, &count); err != nil {
			statusRows.Close()
			return ResultExplorerPage{}, err
		}
		page.Facets.Status[strconv.FormatInt(status, 10)] = count
	}
	if err := statusRows.Close(); err != nil {
		return ResultExplorerPage{}, err
	}
	typeRows, err := s.db.QueryContext(ctx, `SELECT content_type,COUNT(*) FROM results WHERE `+clause+` GROUP BY content_type`, args...)
	if err != nil {
		return ResultExplorerPage{}, err
	}
	for typeRows.Next() {
		var contentType string
		var count int64
		if err := typeRows.Scan(&contentType, &count); err != nil {
			typeRows.Close()
			return ResultExplorerPage{}, err
		}
		page.Facets.ContentType[contentType] = count
	}
	if err := typeRows.Close(); err != nil {
		return ResultExplorerPage{}, err
	}
	rowArgs := append(append([]any{}, args...), query.Limit, query.Offset)
	rows, err := s.db.QueryContext(ctx, `SELECT id,input_json,position,status,length,words,lines,content_type,
		redirect_location,url,duration_ns,scraper_json,host,response_fingerprint,created_at FROM results WHERE `+clause+
		` ORDER BY `+orderColumn+` `+direction+`,id `+direction+` LIMIT ? OFFSET ?`, rowArgs...)
	if err != nil {
		return ResultExplorerPage{}, err
	}
	defer rows.Close()
	for rows.Next() {
		result, err := scanStoredResult(rows, sessionID)
		if err != nil {
			return ResultExplorerPage{}, err
		}
		signature, _ := resultSignature(result)
		page.Results = append(page.Results, ExploredResult{
			StoredResult: result, DuplicateCount: 1, Signature: signature,
		})
	}
	if err := rows.Err(); err != nil {
		return ResultExplorerPage{}, err
	}
	page.HasMore = int64(query.Offset+len(page.Results)) < page.Total
	return page, nil
}

func min64(left, right int64) int64 {
	if left < right {
		return left
	}
	return right
}

func max64(left, right int64) int64 {
	if left > right {
		return left
	}
	return right
}

func escapeLike(value string) string {
	replacer := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return replacer.Replace(value)
}

func validateSavedResultViewQuery(raw json.RawMessage) error {
	if len(raw) == 0 || !json.Valid(raw) {
		return fmt.Errorf("view query must be valid JSON")
	}
	if trimmed := strings.TrimSpace(string(raw)); trimmed == "" || trimmed[0] != '{' {
		return fmt.Errorf("view query must be a JSON object")
	}
	var query savedResultViewQuery
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&query); err != nil {
		return fmt.Errorf("view query is invalid: %w", err)
	}

	values := url.Values{}
	if query.Query != "" {
		values.Set("q", query.Query)
	}
	for _, status := range query.Statuses {
		values.Add("status", strconv.FormatInt(status, 10))
	}
	for _, contentType := range query.ContentTypes {
		values.Add("content_type", contentType)
	}
	for key, value := range map[string]*int64{
		"size_min": query.SizeMin, "size_max": query.SizeMax,
		"words_min": query.WordsMin, "words_max": query.WordsMax,
		"lines_min": query.LinesMin, "lines_max": query.LinesMax,
	} {
		if value != nil {
			values.Set(key, strconv.FormatInt(*value, 10))
		}
	}
	if query.Sort != "" {
		values.Set("sort", query.Sort)
	}
	if query.Order != "" {
		values.Set("order", query.Order)
	}
	if query.GroupDuplicates {
		values.Set("group_duplicates", "true")
	}
	if _, err := parseExplorerQuery(values); err != nil {
		return fmt.Errorf("view query is invalid: %w", err)
	}
	return nil
}

func (s *Store) SaveResultView(ctx context.Context, sessionID string, request SaveResultViewRequest) (SavedResultView, error) {
	if _, err := s.GetSession(ctx, sessionID); err != nil {
		return SavedResultView{}, err
	}
	request.Name = strings.TrimSpace(request.Name)
	if request.Name == "" || len(request.Name) > maxSavedViewName {
		return SavedResultView{}, fmt.Errorf("view name must be between 1 and %d characters", maxSavedViewName)
	}
	if err := validateSavedResultViewQuery(request.Query); err != nil {
		return SavedResultView{}, err
	}
	id, err := newID()
	if err != nil {
		return SavedResultView{}, err
	}
	now := time.Now().UTC()
	_, err = s.db.ExecContext(ctx, `INSERT INTO result_views(id,session_id,name,query_json,created_at,updated_at)
		VALUES(?,?,?,?,?,?) ON CONFLICT(session_id,name) DO UPDATE SET query_json=excluded.query_json,updated_at=excluded.updated_at`,
		id, sessionID, request.Name, []byte(request.Query), now.UnixNano(), now.UnixNano())
	if err != nil {
		return SavedResultView{}, err
	}
	return s.ResultViewByName(ctx, sessionID, request.Name)
}

func (s *Store) ResultViewByName(ctx context.Context, sessionID, name string) (SavedResultView, error) {
	var view SavedResultView
	var created, updated int64
	err := s.db.QueryRowContext(ctx, `SELECT id,session_id,name,query_json,created_at,updated_at
		FROM result_views WHERE session_id=? AND name=?`, sessionID, name).
		Scan(&view.ID, &view.SessionID, &view.Name, &view.Query, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return SavedResultView{}, ErrNotFound
	}
	view.CreatedAt, view.UpdatedAt = time.Unix(0, created).UTC(), time.Unix(0, updated).UTC()
	return view, err
}

func (s *Store) ResultViews(ctx context.Context, sessionID string) ([]SavedResultView, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,session_id,name,query_json,created_at,updated_at
		FROM result_views WHERE session_id=? ORDER BY name COLLATE NOCASE`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	views := []SavedResultView{}
	for rows.Next() {
		var view SavedResultView
		var created, updated int64
		if err := rows.Scan(&view.ID, &view.SessionID, &view.Name, &view.Query, &created, &updated); err != nil {
			return nil, err
		}
		view.CreatedAt, view.UpdatedAt = time.Unix(0, created).UTC(), time.Unix(0, updated).UTC()
		views = append(views, view)
	}
	return views, rows.Err()
}

func (s *Store) DeleteResultView(ctx context.Context, sessionID, viewID string) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM result_views WHERE id=? AND session_id=?`, viewID, sessionID)
	if err != nil {
		return err
	}
	if count, _ := result.RowsAffected(); count == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) SearchTerminal(ctx context.Context, sessionID string, query TerminalQuery) (TerminalSearchPage, error) {
	if _, err := s.GetSession(ctx, sessionID); err != nil {
		return TerminalSearchPage{}, err
	}
	if query.Limit <= 0 {
		query.Limit = 500
	}
	if query.Limit > 1000 {
		query.Limit = 1000
	}
	where := []string{"1=1"}
	args := terminalEventQueryArgs(sessionID)
	var snapshotID int64
	if query.Latest {
		if query.SnapshotID != nil {
			snapshotID = *query.SnapshotID
		} else if err := s.db.QueryRowContext(ctx,
			`SELECT terminal_sequence FROM sessions WHERE id=?`, sessionID).Scan(&snapshotID); err != nil {
			return TerminalSearchPage{}, err
		}
		if query.Search == "" {
			return s.searchTerminalLatestIndexed(ctx, sessionID, query, snapshotID)
		}
		where = append(where, "event_id<=?")
		args = append(args, snapshotID)
	}
	if query.Search != "" {
		where = append(where, `LOWER(message) LIKE ? ESCAPE '\'`)
		args = append(args, "%"+escapeLike(strings.ToLower(query.Search))+"%")
	}
	if len(query.Levels) > 0 {
		placeholders := make([]string, len(query.Levels))
		for index, level := range query.Levels {
			placeholders[index] = "?"
			args = append(args, level)
		}
		where = append(where, "level IN ("+strings.Join(placeholders, ",")+")")
	}
	clause := strings.Join(where, " AND ")
	page := TerminalSearchPage{
		Entries: []TerminalEntry{}, Offset: query.Offset, Limit: query.Limit,
		Levels: map[string]int64{}, SnapshotID: snapshotID,
	}
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM (`+terminalEventsSQL+`) WHERE `+clause, args...).Scan(&page.Total); err != nil {
		return TerminalSearchPage{}, err
	}
	facetWhere := "1=1"
	facetArgs := terminalEventQueryArgs(sessionID)
	if query.Latest {
		facetWhere += " AND event_id<=?"
		facetArgs = append(facetArgs, snapshotID)
	}
	facetRows, err := s.db.QueryContext(ctx, `SELECT level,COUNT(*) FROM (`+terminalEventsSQL+`)
		WHERE `+facetWhere+` GROUP BY level`, facetArgs...)
	if err != nil {
		return TerminalSearchPage{}, err
	}
	for facetRows.Next() {
		var level string
		var count int64
		if err := facetRows.Scan(&level, &count); err != nil {
			facetRows.Close()
			return TerminalSearchPage{}, err
		}
		page.Levels[level] = count
	}
	if err := facetRows.Close(); err != nil {
		return TerminalSearchPage{}, err
	}
	rowWhere := append([]string{}, where...)
	rowArgs := append([]any{}, args...)
	order := "ASC"
	rowLimit := query.Limit
	if query.Latest {
		order = "DESC"
		rowLimit++
		if query.BeforeID != nil {
			rowWhere = append(rowWhere, "event_id<?")
			rowArgs = append(rowArgs, *query.BeforeID)
		}
		rowArgs = append(rowArgs, rowLimit)
	} else {
		rowArgs = append(rowArgs, rowLimit, query.Offset)
	}
	rowSQL := `SELECT event_id,at,level,message FROM (` + terminalEventsSQL + `) WHERE ` +
		strings.Join(rowWhere, " AND ") + ` ORDER BY event_id ` + order + ` LIMIT ?`
	if !query.Latest {
		rowSQL += ` OFFSET ?`
	}
	rows, err := s.db.QueryContext(ctx, rowSQL, rowArgs...)
	if err != nil {
		return TerminalSearchPage{}, err
	}
	defer rows.Close()
	for rows.Next() {
		entry, err := scanTerminalEntry(rows, sessionID)
		if err != nil {
			return TerminalSearchPage{}, err
		}
		page.Entries = append(page.Entries, entry)
	}
	if query.Latest {
		if len(page.Entries) > query.Limit {
			page.Entries = page.Entries[:query.Limit]
			page.HasOlder = true
		}
		for left, right := 0, len(page.Entries)-1; left < right; left, right = left+1, right-1 {
			page.Entries[left], page.Entries[right] = page.Entries[right], page.Entries[left]
		}
		if len(page.Entries) > 0 {
			page.NextBeforeID = page.Entries[0].ID
		}
		page.HasMore = page.HasOlder
		// Offset remains useful to legacy renderers as an ordinal for the first
		// line, but it is derived rather than used to fetch the page.
		page.Offset = max(0, int(page.Total)-len(page.Entries))
	} else {
		page.HasMore = int64(query.Offset+len(page.Entries)) < page.Total
	}
	return page, rows.Err()
}

// searchTerminalLatestIndexed serves the live terminal's common unsearched
// path without materializing and formatting the complete terminal union. New
// result events are represented by compact ranges, so it reads only enough
// range metadata and result rows to fill the requested page, then merges those
// rows with the three compatibility/lifecycle streams by their shared cursor.
func (s *Store) searchTerminalLatestIndexed(ctx context.Context, sessionID string, query TerminalQuery,
	snapshotID int64) (TerminalSearchPage, error) {
	page := TerminalSearchPage{
		Entries: []TerminalEntry{}, Offset: query.Offset, Limit: query.Limit,
		Levels: map[string]int64{}, SnapshotID: snapshotID,
	}
	levels, err := s.terminalLevelCounts(ctx, sessionID, snapshotID)
	if err != nil {
		return TerminalSearchPage{}, err
	}
	page.Levels = levels
	if len(query.Levels) == 0 {
		for _, count := range levels {
			page.Total += count
		}
	} else {
		for _, level := range query.Levels {
			page.Total += levels[level]
		}
	}

	upperBound := snapshotID
	if query.BeforeID != nil && *query.BeforeID-1 < upperBound {
		upperBound = *query.BeforeID - 1
	}
	wanted := query.Limit + 1
	sources := make([][]TerminalEntry, 0, 4)
	if terminalLevelSelected(query.Levels, "result") {
		entries, rangeErr := s.latestRangeTerminalResults(ctx, sessionID, upperBound, wanted)
		if rangeErr != nil {
			return TerminalSearchPage{}, rangeErr
		}
		sources = append(sources, entries)
		entries, transitionalErr := s.latestTransitionalTerminalResults(ctx, sessionID, upperBound, wanted)
		if transitionalErr != nil {
			return TerminalSearchPage{}, transitionalErr
		}
		sources = append(sources, entries)
	}
	legacy, err := s.latestTerminalLogRows(ctx, sessionID, upperBound, wanted, query.Levels, false)
	if err != nil {
		return TerminalSearchPage{}, err
	}
	current, err := s.latestTerminalLogRows(ctx, sessionID, upperBound, wanted, query.Levels, true)
	if err != nil {
		return TerminalSearchPage{}, err
	}
	sources = append(sources, legacy, current)

	positions := make([]int, len(sources))
	descending := make([]TerminalEntry, 0, wanted)
	for len(descending) < wanted {
		selected := -1
		for index := range sources {
			if positions[index] >= len(sources[index]) {
				continue
			}
			if selected == -1 || sources[index][positions[index]].ID > sources[selected][positions[selected]].ID {
				selected = index
			}
		}
		if selected == -1 {
			break
		}
		descending = append(descending, sources[selected][positions[selected]])
		positions[selected]++
	}
	if len(descending) > query.Limit {
		descending = descending[:query.Limit]
		page.HasOlder = true
	}
	for left, right := 0, len(descending)-1; left < right; left, right = left+1, right-1 {
		descending[left], descending[right] = descending[right], descending[left]
	}
	page.Entries = descending
	if len(page.Entries) > 0 {
		page.NextBeforeID = page.Entries[0].ID
	}
	page.HasMore = page.HasOlder
	page.Offset = max(0, int(page.Total)-len(page.Entries))
	return page, nil
}

func terminalLevelSelected(levels []string, wanted string) bool {
	if len(levels) == 0 {
		return true
	}
	for _, level := range levels {
		if level == wanted {
			return true
		}
	}
	return false
}

func (s *Store) terminalLevelCounts(ctx context.Context, sessionID string, snapshotID int64) (map[string]int64, error) {
	levels := map[string]int64{}
	var rangeResults, transitionalResults int64
	if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(SUM(
		CASE WHEN last_terminal_sequence<=? THEN last_terminal_sequence-first_terminal_sequence+1
		ELSE ?-first_terminal_sequence+1 END),0)
		FROM terminal_result_ranges WHERE session_id=? AND first_terminal_sequence<=?`,
		snapshotID, snapshotID, sessionID, snapshotID).Scan(&rangeResults); err != nil {
		return nil, err
	}
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM results
		WHERE session_id=? AND terminal_sequence>0 AND terminal_sequence<=?`, sessionID, snapshotID).
		Scan(&transitionalResults); err != nil {
		return nil, err
	}
	if count := rangeResults + transitionalResults; count > 0 {
		levels["result"] = count
	}
	rows, err := s.db.QueryContext(ctx, `SELECT level,COUNT(*) FROM terminal_log
		WHERE session_id=? AND ((terminal_sequence=0 AND id<=?) OR
		(terminal_sequence>0 AND terminal_sequence<=?)) GROUP BY level`, sessionID, snapshotID, snapshotID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var level string
		var count int64
		if err := rows.Scan(&level, &count); err != nil {
			return nil, err
		}
		levels[level] += count
	}
	return levels, rows.Err()
}

func (s *Store) latestRangeTerminalResults(ctx context.Context, sessionID string, upperBound int64,
	limit int) ([]TerminalEntry, error) {
	if upperBound < 1 || limit < 1 {
		return []TerminalEntry{}, nil
	}
	ranges, err := s.db.QueryContext(ctx, `SELECT first_result_id,last_result_id,
		first_terminal_sequence,last_terminal_sequence FROM terminal_result_ranges
		WHERE session_id=? AND first_terminal_sequence<=?
		ORDER BY first_terminal_sequence DESC`, sessionID, upperBound)
	if err != nil {
		return nil, err
	}
	type resultRange struct {
		firstResult, lastResult, firstSequence, lastSequence int64
	}
	selectedRanges := make([]resultRange, 0, 2)
	remaining := limit
	for ranges.Next() && remaining > 0 {
		var item resultRange
		if err := ranges.Scan(&item.firstResult, &item.lastResult, &item.firstSequence, &item.lastSequence); err != nil {
			ranges.Close()
			return nil, err
		}
		if item.lastSequence > upperBound {
			item.lastResult -= item.lastSequence - upperBound
			item.lastSequence = upperBound
		}
		if item.lastSequence < item.firstSequence {
			continue
		}
		count := int(item.lastSequence - item.firstSequence + 1)
		if count > remaining {
			item.firstResult = item.lastResult - int64(remaining) + 1
			item.firstSequence = item.lastSequence - int64(remaining) + 1
			count = remaining
		}
		selectedRanges = append(selectedRanges, item)
		remaining -= count
	}
	if err := ranges.Close(); err != nil {
		return nil, err
	}
	entries := make([]TerminalEntry, 0, limit-remaining)
	for _, item := range selectedRanges {
		rows, err := s.db.QueryContext(ctx, `SELECT id,created_at,url,status,length,words,lines,duration_ns
			FROM results WHERE session_id=? AND id BETWEEN ? AND ? ORDER BY id DESC`,
			sessionID, item.firstResult, item.lastResult)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var resultID, at, status, size, words, lines, duration int64
			var resultURL string
			if err := rows.Scan(&resultID, &at, &resultURL, &status, &size, &words, &lines, &duration); err != nil {
				rows.Close()
				return nil, err
			}
			entries = append(entries, TerminalEntry{
				ID: item.firstSequence + (resultID - item.firstResult), SessionID: sessionID,
				At: time.Unix(0, at).UTC(), Level: "result",
				Message: formatResultTerminalFields(resultURL, status, size, words, lines, duration),
			})
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
	}
	return entries, nil
}

func (s *Store) latestTransitionalTerminalResults(ctx context.Context, sessionID string, upperBound int64,
	limit int) ([]TerminalEntry, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT terminal_sequence,terminal_at,url,status,length,words,lines,duration_ns
		FROM results WHERE session_id=? AND terminal_sequence>0 AND terminal_sequence<=?
		ORDER BY terminal_sequence DESC LIMIT ?`, sessionID, upperBound, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	entries := make([]TerminalEntry, 0, limit)
	for rows.Next() {
		var entry TerminalEntry
		var at, status, size, words, lines, duration int64
		var resultURL string
		if err := rows.Scan(&entry.ID, &at, &resultURL, &status, &size, &words, &lines, &duration); err != nil {
			return nil, err
		}
		entry.SessionID, entry.At, entry.Level = sessionID, time.Unix(0, at).UTC(), "result"
		entry.Message = formatResultTerminalFields(resultURL, status, size, words, lines, duration)
		entries = append(entries, entry)
	}
	return entries, rows.Err()
}

func (s *Store) latestTerminalLogRows(ctx context.Context, sessionID string, upperBound int64, limit int,
	levels []string, current bool) ([]TerminalEntry, error) {
	sequenceColumn := "id"
	where := []string{"session_id=?", "terminal_sequence=0", "id<=?"}
	if current {
		sequenceColumn = "terminal_sequence"
		where = []string{"session_id=?", "terminal_sequence>0", "terminal_sequence<=?"}
	}
	args := []any{sessionID, upperBound}
	if len(levels) > 0 {
		placeholders := make([]string, len(levels))
		for index, level := range levels {
			placeholders[index] = "?"
			args = append(args, level)
		}
		where = append(where, "level IN ("+strings.Join(placeholders, ",")+")")
	}
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, `SELECT `+sequenceColumn+`,at,level,message FROM terminal_log
		WHERE `+strings.Join(where, " AND ")+` ORDER BY `+sequenceColumn+` DESC LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	entries := make([]TerminalEntry, 0, limit)
	for rows.Next() {
		entry, err := scanTerminalEntry(rows, sessionID)
		if err != nil {
			return nil, err
		}
		entries = append(entries, entry)
	}
	return entries, rows.Err()
}

func (s *Store) PutPreset(ctx context.Context, preset Preset) (Preset, error) {
	preset.Name = strings.TrimSpace(preset.Name)
	if preset.Name == "" || len(preset.Name) > maxPresetName {
		return Preset{}, fmt.Errorf("preset name must be between 1 and %d characters", maxPresetName)
	}
	if strings.TrimSpace(preset.Wordlist) == "" {
		return Preset{}, fmt.Errorf("preset wordlist is required")
	}
	if _, ok := profileByID(preset.Profile); !ok {
		return Preset{}, fmt.Errorf("unknown profile %q", preset.Profile)
	}
	if preset.ID == "" {
		var err error
		preset.ID, err = newID()
		if err != nil {
			return Preset{}, err
		}
	}
	options, err := json.Marshal(preset.Options)
	if err != nil {
		return Preset{}, err
	}
	explicit, err := json.Marshal(uniqueStrings(preset.ExplicitFlags))
	if err != nil {
		return Preset{}, err
	}
	now := time.Now().UTC()
	if preset.CreatedAt.IsZero() {
		preset.CreatedAt = now
	}
	preset.UpdatedAt = now
	_, err = s.db.ExecContext(ctx, `INSERT INTO presets(id,name,description,wordlist,profile,options_json,explicit_flags_json,created_at,updated_at)
		VALUES(?,?,?,?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET name=excluded.name,description=excluded.description,
		wordlist=excluded.wordlist,profile=excluded.profile,options_json=excluded.options_json,
		explicit_flags_json=excluded.explicit_flags_json,updated_at=excluded.updated_at`, preset.ID, preset.Name,
		preset.Description, preset.Wordlist, preset.Profile, options, explicit, preset.CreatedAt.UnixNano(), now.UnixNano())
	return preset, err
}

func scanPreset(row scanner) (Preset, error) {
	var preset Preset
	var options, explicit []byte
	var created, updated int64
	if err := row.Scan(&preset.ID, &preset.Name, &preset.Description, &preset.Wordlist, &preset.Profile,
		&options, &explicit, &created, &updated); err != nil {
		return Preset{}, err
	}
	if err := json.Unmarshal(options, &preset.Options); err != nil {
		return Preset{}, err
	}
	if err := json.Unmarshal(explicit, &preset.ExplicitFlags); err != nil {
		return Preset{}, err
	}
	preset.CreatedAt, preset.UpdatedAt = time.Unix(0, created).UTC(), time.Unix(0, updated).UTC()
	return preset, nil
}

func (s *Store) Presets(ctx context.Context) ([]Preset, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,name,description,wordlist,profile,options_json,explicit_flags_json,created_at,updated_at
		FROM presets ORDER BY name COLLATE NOCASE`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	presets := []Preset{}
	for rows.Next() {
		preset, err := scanPreset(rows)
		if err != nil {
			return nil, err
		}
		presets = append(presets, preset)
	}
	return presets, rows.Err()
}

func (s *Store) Preset(ctx context.Context, id string) (Preset, error) {
	preset, err := scanPreset(s.db.QueryRowContext(ctx, `SELECT id,name,description,wordlist,profile,options_json,explicit_flags_json,created_at,updated_at
		FROM presets WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Preset{}, ErrNotFound
	}
	return preset, err
}

func (s *Store) DeletePreset(ctx context.Context, id string) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM presets WHERE id=?`, id)
	if err != nil {
		return err
	}
	if count, _ := result.RowsAffected(); count == 0 {
		return ErrNotFound
	}
	return nil
}

const comparisonCursorVersion = "v1."

func encodeComparisonCursor(location string) string {
	return comparisonCursorVersion + base64.RawURLEncoding.EncodeToString([]byte(location))
}

func decodeComparisonCursor(cursor string) (string, error) {
	if cursor == "" {
		return "", nil
	}
	if !strings.HasPrefix(cursor, comparisonCursorVersion) {
		return "", fmt.Errorf("unsupported comparison cursor")
	}
	decoded, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(cursor, comparisonCursorVersion))
	if err != nil || !utf8.Valid(decoded) {
		return "", fmt.Errorf("invalid comparison cursor")
	}
	return string(decoded), nil
}

func scanComparisonResult(row *sql.Rows) (StoredResult, error) {
	var result StoredResult
	var input, scraper []byte
	var created int64
	if err := row.Scan(&result.ID, &result.SessionID, &input, &result.Position, &result.StatusCode,
		&result.ContentLength, &result.ContentWords, &result.ContentLines, &result.ContentType,
		&result.RedirectLocation, &result.URL, &result.DurationNS, &scraper, &result.Host,
		&result.ResponseFingerprint, &created); err != nil {
		return StoredResult{}, err
	}
	_ = json.Unmarshal(input, &result.Input)
	_ = json.Unmarshal(scraper, &result.ScraperData)
	result.CreatedAt = time.Unix(0, created).UTC()
	return result, nil
}

// CompareSessionsPage performs a merge over URL-ordered result rows. Unlike the
// original map-based implementation, memory is bounded by the requested page
// size. The entire comparison is read through one SQLite cursor, giving totals
// and both change lists one consistent database snapshot while retaining the
// exact semantics of retroactive session filters and duplicate URLs.
func (s *Store) CompareSessionsPage(ctx context.Context, leftID, rightID string, query SessionComparisonQuery) (SessionComparison, error) {
	leftFilter, err := s.resultFilter(ctx, leftID)
	if err != nil {
		return SessionComparison{}, err
	}
	rightFilter, err := s.resultFilter(ctx, rightID)
	if err != nil {
		return SessionComparison{}, err
	}
	if query.Limit <= 0 {
		query.Limit = 200
	}
	if query.Limit > maxExplorerPageSize {
		query.Limit = maxExplorerPageSize
	}
	if query.Side == "" {
		query.Side = "both"
	}
	if query.Side != "both" && query.Side != "added" && query.Side != "removed" {
		return SessionComparison{}, fmt.Errorf("side must be both, added, or removed")
	}
	afterURL, err := decodeComparisonCursor(query.Cursor)
	if err != nil {
		return SessionComparison{}, err
	}

	rows, err := s.db.QueryContext(ctx, `SELECT id,session_id,input_json,position,status,length,words,lines,content_type,
		redirect_location,url,duration_ns,scraper_json,host,response_fingerprint,created_at FROM results
		WHERE session_id IN (?,?) ORDER BY url ASC,session_id ASC,id DESC`, leftID, rightID)
	if err != nil {
		return SessionComparison{}, err
	}
	defer rows.Close()

	comparison := SessionComparison{
		LeftSessionID: leftID, RightSessionID: rightID, Added: []StoredResult{},
		Removed: []StoredResult{}, Limit: query.Limit,
	}
	var currentURL string
	var haveGroup bool
	var leftResult, rightResult *StoredResult
	appendChange := func(side string, result StoredResult) {
		if side == "added" {
			comparison.AddedTotal++
			if query.Side != "both" && query.Side != "added" || result.URL <= afterURL {
				return
			}
			if len(comparison.Added) < query.Limit {
				comparison.Added = append(comparison.Added, result)
				return
			}
			comparison.AddedHasMore = true
			return
		}
		comparison.RemovedTotal++
		if query.Side != "both" && query.Side != "removed" || result.URL <= afterURL {
			return
		}
		if len(comparison.Removed) < query.Limit {
			comparison.Removed = append(comparison.Removed, result)
			return
		}
		comparison.RemovedHasMore = true
	}
	flushGroup := func() {
		switch {
		case leftResult == nil && rightResult != nil:
			appendChange("added", *rightResult)
		case leftResult != nil && rightResult == nil:
			appendChange("removed", *leftResult)
		case leftResult != nil && rightResult != nil:
			comparison.UnchangedCount++
		}
	}

	for rows.Next() {
		result, scanErr := scanComparisonResult(rows)
		if scanErr != nil {
			return SessionComparison{}, scanErr
		}
		if !haveGroup || result.URL != currentURL {
			if haveGroup {
				flushGroup()
			}
			currentURL, haveGroup = result.URL, true
			leftResult, rightResult = nil, nil
		}
		if result.SessionID == leftID && leftResult == nil {
			keep, filterErr := leftFilter.keep(result)
			if filterErr != nil {
				return SessionComparison{}, filterErr
			}
			if keep {
				candidate := result
				leftResult = &candidate
			}
		}
		if result.SessionID == rightID && rightResult == nil {
			keep, filterErr := rightFilter.keep(result)
			if filterErr != nil {
				return SessionComparison{}, filterErr
			}
			if keep {
				candidate := result
				rightResult = &candidate
			}
		}
	}
	if err := rows.Err(); err != nil {
		return SessionComparison{}, err
	}
	if haveGroup {
		flushGroup()
	}
	if comparison.AddedHasMore && len(comparison.Added) > 0 {
		comparison.AddedNextCursor = encodeComparisonCursor(comparison.Added[len(comparison.Added)-1].URL)
	}
	if comparison.RemovedHasMore && len(comparison.Removed) > 0 {
		comparison.RemovedNextCursor = encodeComparisonCursor(comparison.Removed[len(comparison.Removed)-1].URL)
	}
	return comparison, nil
}

// CompareSessions retains the original store API while making its response
// bounded. API consumers can request subsequent pages through the query-aware
// endpoint.
func (s *Store) CompareSessions(ctx context.Context, leftID, rightID string) (SessionComparison, error) {
	return s.CompareSessionsPage(ctx, leftID, rightID, SessionComparisonQuery{})
}

func (s *Server) productFeatureRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/sessions/{id}/results/explore", s.handleExploreResults)
	mux.HandleFunc("GET /api/v1/sessions/{id}/results/tree", s.handleExploreResultTree)
	mux.HandleFunc("GET /api/v1/sessions/{id}/result-views", s.handleListResultViews)
	mux.HandleFunc("POST /api/v1/sessions/{id}/result-views", s.handleSaveResultView)
	mux.HandleFunc("DELETE /api/v1/sessions/{id}/result-views/{view_id}", s.handleDeleteResultView)
	mux.HandleFunc("GET /api/v1/sessions/{id}/terminal/search", s.handleSearchTerminal)
	mux.HandleFunc("GET /api/v1/sessions/{id}/organization", s.handleGetSessionOrganization)
	mux.HandleFunc("PATCH /api/v1/sessions/{id}/organization", s.handlePatchSessionOrganization)
	mux.HandleFunc("POST /api/v1/sessions/{id}/clone", s.handleCloneSession)
	mux.HandleFunc("GET /api/v1/sessions/{id}/compare/{other_id}", s.handleCompareSessions)
	mux.HandleFunc("POST /api/v1/sessions/bulk", s.handleBulkSessions)
	mux.HandleFunc("GET /api/v1/presets", s.handleListPresets)
	mux.HandleFunc("POST /api/v1/presets", s.handleCreatePreset)
	mux.HandleFunc("PUT /api/v1/presets/{id}", s.handleUpdatePreset)
	mux.HandleFunc("DELETE /api/v1/presets/{id}", s.handleDeletePreset)
	mux.HandleFunc("POST /api/v1/wordlists/inspect", s.handleInspectWordlist)
}

func singleQueryValue(values url.Values, key string) (string, error) {
	raw, present := values[key]
	if !present {
		return "", nil
	}
	if len(raw) != 1 {
		return "", fmt.Errorf("%s must be provided once", key)
	}
	return raw[0], nil
}

func optionalNonNegative(values url.Values, key string) (*int64, error) {
	raw, err := singleQueryValue(values, key)
	if err != nil || raw == "" {
		return nil, err
	}
	parsed, parseErr := strconv.ParseInt(raw, 10, 64)
	if parseErr != nil || parsed < 0 {
		return nil, fmt.Errorf("%s must be a non-negative integer", key)
	}
	return &parsed, nil
}

func parseExplorerQuery(values url.Values) (ResultExplorerQuery, error) {
	query := ResultExplorerQuery{Sort: values.Get("sort"), Descending: strings.EqualFold(values.Get("order"), "desc")}
	if query.Sort != "" {
		validSort := map[string]bool{"created": true, "url": true, "status": true, "content_type": true,
			"size": true, "words": true, "lines": true, "duration": true}
		if !validSort[query.Sort] {
			return query, fmt.Errorf("sort must be created, url, status, content_type, size, words, lines, or duration")
		}
	}
	if order := values.Get("order"); order != "" && !strings.EqualFold(order, "asc") && !strings.EqualFold(order, "desc") {
		return query, fmt.Errorf("order must be asc or desc")
	}
	query.Search = strings.TrimSpace(values.Get("q"))
	if !utf8.ValidString(query.Search) || len(query.Search) > maxResultSearchLength {
		return query, fmt.Errorf("q must be valid UTF-8 and at most %d bytes", maxResultSearchLength)
	}
	var err error
	if query.Statuses, err = parseResultStatuses(values["status"]); err != nil {
		return query, err
	}
	seenTypes := map[string]struct{}{}
	for _, raw := range values["content_type"] {
		for _, value := range strings.Split(raw, ",") {
			value = strings.TrimSpace(value)
			if value == "" {
				return query, fmt.Errorf("content_type values must not be empty")
			}
			if len(value) > 255 || len(query.ContentTypes) >= 100 {
				return query, fmt.Errorf("content_type accepts at most 100 values of 255 bytes each")
			}
			if _, exists := seenTypes[value]; !exists {
				query.ContentTypes = append(query.ContentTypes, value)
				seenTypes[value] = struct{}{}
			}
		}
	}
	if query.SizeMin, err = optionalNonNegative(values, "size_min"); err != nil {
		return query, err
	}
	if query.SizeMax, err = optionalNonNegative(values, "size_max"); err != nil {
		return query, err
	}
	if query.WordsMin, err = optionalNonNegative(values, "words_min"); err != nil {
		return query, err
	}
	if query.WordsMax, err = optionalNonNegative(values, "words_max"); err != nil {
		return query, err
	}
	if query.LinesMin, err = optionalNonNegative(values, "lines_min"); err != nil {
		return query, err
	}
	if query.LinesMax, err = optionalNonNegative(values, "lines_max"); err != nil {
		return query, err
	}
	for _, bounds := range []struct {
		name    string
		minimum *int64
		maximum *int64
	}{{"size", query.SizeMin, query.SizeMax}, {"words", query.WordsMin, query.WordsMax}, {"lines", query.LinesMin, query.LinesMax}} {
		if bounds.minimum != nil && bounds.maximum != nil && *bounds.minimum > *bounds.maximum {
			return query, fmt.Errorf("%s_min must not exceed %s_max", bounds.name, bounds.name)
		}
	}
	query.Offset, err = strconv.Atoi(defaultString(values.Get("offset"), "0"))
	if err != nil || query.Offset < 0 {
		return query, fmt.Errorf("offset must be a non-negative integer")
	}
	query.Limit, err = strconv.Atoi(defaultString(values.Get("limit"), "200"))
	if err != nil || query.Limit < 1 || query.Limit > maxExplorerPageSize {
		return query, fmt.Errorf("limit must be between 1 and %d", maxExplorerPageSize)
	}
	if raw := values.Get("group_duplicates"); raw != "" && raw != "true" && raw != "false" {
		return query, fmt.Errorf("group_duplicates must be true or false")
	}
	query.GroupDuplicates = values.Get("group_duplicates") == "true"
	return query, nil
}

func (s *Server) handleExploreResults(w http.ResponseWriter, r *http.Request) {
	query, err := parseExplorerQuery(r.URL.Query())
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_result_query", err.Error())
		return
	}
	page, err := s.store.ExploreResults(r.Context(), r.PathValue("id"), query)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, page)
}

func (s *Server) handleListResultViews(w http.ResponseWriter, r *http.Request) {
	views, err := s.store.ResultViews(r.Context(), r.PathValue("id"))
	if err != nil {
		writeInternalError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, views)
}

func (s *Server) handleSaveResultView(w http.ResponseWriter, r *http.Request) {
	var request SaveResultViewRequest
	if !decodeJSON(w, r, &request) {
		return
	}
	view, err := s.store.SaveResultView(r.Context(), r.PathValue("id"), request)
	if err != nil {
		writeAPIError(w, http.StatusUnprocessableEntity, "invalid_result_view", err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, view)
}

func (s *Server) handleDeleteResultView(w http.ResponseWriter, r *http.Request) {
	if err := s.store.DeleteResultView(r.Context(), r.PathValue("id"), r.PathValue("view_id")); err != nil {
		writeStoreError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleSearchTerminal(w http.ResponseWriter, r *http.Request) {
	values := r.URL.Query()
	query := TerminalQuery{Search: strings.TrimSpace(values.Get("q")), Levels: values["level"]}
	if !utf8.ValidString(query.Search) || len(query.Search) > maxTerminalSearch {
		writeAPIError(w, http.StatusBadRequest, "invalid_terminal_query", "q must be valid UTF-8 and at most 512 bytes")
		return
	}
	var err error
	query.Offset, err = strconv.Atoi(defaultString(values.Get("offset"), "0"))
	if err != nil || query.Offset < 0 {
		writeAPIError(w, http.StatusBadRequest, "invalid_terminal_offset", "offset must be a non-negative integer")
		return
	}
	query.Limit, err = strconv.Atoi(defaultString(values.Get("limit"), "500"))
	if err != nil || query.Limit < 1 || query.Limit > 1000 {
		writeAPIError(w, http.StatusBadRequest, "invalid_terminal_limit", "limit must be between 1 and 1000")
		return
	}
	if values.Has("latest") {
		query.Latest, err = strconv.ParseBool(values.Get("latest"))
		if err != nil {
			writeAPIError(w, http.StatusBadRequest, "invalid_terminal_latest", "latest must be true or false")
			return
		}
	}
	if raw := values.Get("before_id"); raw != "" {
		beforeID, parseErr := strconv.ParseInt(raw, 10, 64)
		if parseErr != nil || beforeID <= 0 {
			writeAPIError(w, http.StatusBadRequest, "invalid_terminal_before_id", "before_id must be a positive integer")
			return
		}
		query.BeforeID = &beforeID
	}
	if raw := values.Get("snapshot_id"); raw != "" {
		snapshotID, parseErr := strconv.ParseInt(raw, 10, 64)
		if parseErr != nil || snapshotID < 0 {
			writeAPIError(w, http.StatusBadRequest, "invalid_terminal_snapshot_id", "snapshot_id must be a non-negative integer")
			return
		}
		query.SnapshotID = &snapshotID
	}
	if (query.BeforeID != nil || query.SnapshotID != nil) && !query.Latest {
		writeAPIError(w, http.StatusBadRequest, "invalid_terminal_cursor", "before_id and snapshot_id require latest=true")
		return
	}
	if query.BeforeID != nil && query.SnapshotID == nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_terminal_cursor", "snapshot_id is required when before_id is set")
		return
	}
	for _, level := range query.Levels {
		if level != "debug" && level != "info" && level != "warning" && level != "error" {
			writeAPIError(w, http.StatusBadRequest, "invalid_terminal_level", "level must be debug, info, warning, or error")
			return
		}
	}
	page, err := s.store.SearchTerminal(r.Context(), r.PathValue("id"), query)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, page)
}

func (s *Server) handleGetSessionOrganization(w http.ResponseWriter, r *http.Request) {
	organization, err := s.store.SessionOrganization(r.Context(), r.PathValue("id"))
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, organization)
}

func (s *Server) handlePatchSessionOrganization(w http.ResponseWriter, r *http.Request) {
	var request PatchSessionOrganizationRequest
	if !decodeJSON(w, r, &request) {
		return
	}
	organization, err := s.store.PatchSessionOrganization(r.Context(), r.PathValue("id"), request)
	if err != nil {
		writeAPIError(w, http.StatusUnprocessableEntity, "invalid_organization", err.Error())
		return
	}
	s.manager.emitCurrent(r.Context(), r.PathValue("id"), "session.updated")
	writeJSON(w, http.StatusOK, organization)
}

func (s *Server) handleCloneSession(w http.ResponseWriter, r *http.Request) {
	var request CloneSessionRequest
	if !decodeJSON(w, r, &request) {
		return
	}
	source, err := s.store.GetSession(r.Context(), r.PathValue("id"))
	if err != nil {
		writeStoreError(w, err)
		return
	}
	name := strings.TrimSpace(request.Name)
	if name == "" {
		name = source.Name + " copy"
	}
	create := CreateSessionRequest{Name: name, Target: source.Target,
		Wordlist: source.Wordlist, Profile: source.Profile, Options: &source.Options,
		ExplicitFlags: append([]string(nil), source.ExplicitFlags...), AutoStart: request.AutoStart}
	if request.Target != nil {
		create.Target = *request.Target
	}
	if request.Wordlist != nil {
		create.Wordlist = *request.Wordlist
	}
	if request.Profile != nil {
		create.Profile = *request.Profile
	}
	if request.Options != nil {
		create.Options = request.Options
	}
	if request.ExplicitFlags != nil {
		create.ExplicitFlags = append([]string(nil), (*request.ExplicitFlags)...)
	}
	autoStart := create.AutoStart
	// Persist clone provenance before runtime initialization. This guarantees
	// every lifecycle event, including an auto-start failure, identifies the
	// source session and prevents a failed POST retry from creating mystery
	// duplicates.
	create.AutoStart = false
	clone, err := s.manager.Create(r.Context(), create)
	if err != nil {
		writeAPIError(w, http.StatusUnprocessableEntity, "clone_failed", err.Error())
		return
	}
	if err := s.store.SetClonedFrom(r.Context(), clone.ID, source.ID); err != nil {
		// The clone is still idle here, so remove it if provenance cannot be
		// recorded rather than leaving an orphan the caller cannot identify.
		_ = s.manager.Delete(context.Background(), clone.ID)
		writeInternalError(w, err)
		return
	}
	s.manager.emitCurrent(r.Context(), clone.ID, "session.updated")
	clone, err = s.store.GetSession(r.Context(), clone.ID)
	if err != nil {
		writeInternalError(w, err)
		return
	}
	if autoStart {
		clone, err = s.manager.startCreatedSession(r.Context(), clone.ID)
		if err != nil {
			writeInternalError(w, err)
			return
		}
	}
	writeJSON(w, http.StatusCreated, clone)
}

func (s *Server) handleCompareSessions(w http.ResponseWriter, r *http.Request) {
	values := r.URL.Query()
	query := SessionComparisonQuery{Side: values.Get("side"), Cursor: values.Get("cursor")}
	var err error
	query.Limit, err = strconv.Atoi(defaultString(values.Get("limit"), "200"))
	if err != nil || query.Limit < 1 || query.Limit > maxExplorerPageSize {
		writeAPIError(w, http.StatusBadRequest, "invalid_comparison_limit", "limit must be between 1 and 1000")
		return
	}
	comparison, err := s.store.CompareSessionsPage(r.Context(), r.PathValue("id"), r.PathValue("other_id"), query)
	if err != nil {
		if strings.Contains(err.Error(), "comparison cursor") || strings.HasPrefix(err.Error(), "side must") {
			writeAPIError(w, http.StatusBadRequest, "invalid_comparison_query", err.Error())
			return
		}
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, comparison)
}

func (s *Server) handleBulkSessions(w http.ResponseWriter, r *http.Request) {
	var request BulkSessionRequest
	if !decodeJSON(w, r, &request) {
		return
	}
	if request.Action != "stop" && request.Action != "delete" && request.Action != "archive" {
		writeAPIError(w, http.StatusBadRequest, "invalid_bulk_action", "action must be stop, delete, or archive")
		return
	}
	if len(request.IDs) == 0 || len(request.IDs) > 500 {
		writeAPIError(w, http.StatusBadRequest, "invalid_bulk_sessions", "ids must contain between 1 and 500 sessions")
		return
	}
	result := BulkSessionResult{Succeeded: []string{}, Failed: map[string]string{}}
	for _, id := range uniqueStrings(request.IDs) {
		var err error
		switch request.Action {
		case "stop":
			_, err = s.manager.StopSnapshot(r.Context(), id)
		case "delete":
			err = s.manager.Delete(r.Context(), id)
		case "archive":
			value := true
			_, err = s.store.PatchSessionOrganization(r.Context(), id, PatchSessionOrganizationRequest{Archived: &value})
			if err == nil {
				s.manager.emitCurrent(r.Context(), id, "session.updated")
			}
		}
		if err != nil {
			result.Failed[id] = err.Error()
			continue
		}
		result.Succeeded = append(result.Succeeded, id)
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleListPresets(w http.ResponseWriter, r *http.Request) {
	presets, err := s.store.Presets(r.Context())
	if err != nil {
		writeInternalError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, presets)
}

func (s *Server) handleCreatePreset(w http.ResponseWriter, r *http.Request) {
	var preset Preset
	if !decodeJSON(w, r, &preset) {
		return
	}
	if !s.validatePreset(w, &preset) {
		return
	}
	preset.ID = ""
	created, err := s.store.PutPreset(r.Context(), preset)
	if err != nil {
		writeAPIError(w, http.StatusUnprocessableEntity, "invalid_preset", err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, created)
}

func (s *Server) handleUpdatePreset(w http.ResponseWriter, r *http.Request) {
	var preset Preset
	if !decodeJSON(w, r, &preset) {
		return
	}
	if !s.validatePreset(w, &preset) {
		return
	}
	existing, err := s.store.Preset(r.Context(), r.PathValue("id"))
	if err != nil {
		writeStoreError(w, err)
		return
	}
	preset.ID, preset.CreatedAt = existing.ID, existing.CreatedAt
	updated, err := s.store.PutPreset(r.Context(), preset)
	if err != nil {
		writeAPIError(w, http.StatusUnprocessableEntity, "invalid_preset", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

func (s *Server) validatePreset(w http.ResponseWriter, preset *Preset) bool {
	if !s.config.AllowInputCmd && len(preset.Options.Input.Inputcommands) > 0 {
		writeAPIError(w, http.StatusForbidden, "input_command_disabled", "input-cmd is disabled by server policy")
		return false
	}
	preset.Options.General.Noninteractive = true
	if err := sanitizeWebOptions(&preset.Options); err != nil {
		writeAPIError(w, http.StatusUnprocessableEntity, "invalid_preset", err.Error())
		return false
	}
	preset.ExplicitFlags = uniqueStrings(preset.ExplicitFlags)
	return true
}

func (s *Server) handleDeletePreset(w http.ResponseWriter, r *http.Request) {
	if err := s.store.DeletePreset(r.Context(), r.PathValue("id")); err != nil {
		writeStoreError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleInspectWordlist(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Path string `json:"path"`
	}
	if !decodeJSON(w, r, &request) {
		return
	}
	request.Path = strings.TrimSpace(request.Path)
	inspection := WordlistInspection{Path: request.Path}
	file, err := os.Open(request.Path)
	if err != nil {
		inspection.Error = err.Error()
		writeJSON(w, http.StatusOK, inspection)
		return
	}
	defer file.Close()
	stat, err := file.Stat()
	if err != nil || !stat.Mode().IsRegular() {
		if err != nil {
			inspection.Error = err.Error()
		} else {
			inspection.Error = "wordlist must be a regular file"
		}
		writeJSON(w, http.StatusOK, inspection)
		return
	}
	inspection.Readable, inspection.SizeBytes = true, stat.Size()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		inspection.Entries++
		line := scanner.Text()
		hasExtensionMarker := strings.Contains(strings.ToLower(line), "%ext%")
		if hasExtensionMarker {
			inspection.ExtensionMarkers++
		}
		if strings.HasPrefix(strings.TrimLeft(line, " "), "#") {
			inspection.CommentEntries++
			if hasExtensionMarker {
				inspection.CommentExtensionMarkers++
			}
		}
	}
	if err := scanner.Err(); err != nil {
		inspection.Readable = false
		inspection.Error = err.Error()
	}
	writeJSON(w, http.StatusOK, inspection)
}
