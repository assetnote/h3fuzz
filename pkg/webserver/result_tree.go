package webserver

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/assetnote/h3fuzz/pkg/ffuf"
)

// ResultTreeNode is one immediate child in the server-authoritative directory
// hierarchy. Children are loaded lazily through Key, so a large scan never has
// to cross the API boundary as one unbounded response.
type ResultTreeNode struct {
	Key         string          `json:"key"`
	Name        string          `json:"name"`
	URL         string          `json:"url"`
	URI         string          `json:"uri"`
	Kind        string          `json:"kind"`
	Result      *ExploredResult `json:"result,omitempty"`
	ResultCount int64           `json:"result_count"`
	HasChildren bool            `json:"has_children"`
}

type ResultTreePage struct {
	Nodes      []ResultTreeNode `json:"nodes"`
	Total      int              `json:"total"`
	Offset     int              `json:"offset"`
	Limit      int              `json:"limit"`
	HasMore    bool             `json:"has_more"`
	SnapshotID int64            `json:"snapshot_id"`
}

type resultTreeScope struct {
	Origin   string   `json:"origin"`
	Segments []string `json:"segments,omitempty"`
	Suffix   string   `json:"suffix,omitempty"`
}

type resultTreeLocation struct {
	origin        string
	segments      []string
	suffix        string
	trailingSlash bool
}

func encodeResultTreeScope(scope resultTreeScope) string {
	encoded, _ := json.Marshal(scope)
	return base64.RawURLEncoding.EncodeToString(encoded)
}

func decodeResultTreeScope(encoded string) (resultTreeScope, error) {
	if encoded == "" {
		return resultTreeScope{}, nil
	}
	if len(encoded) > 8192 {
		return resultTreeScope{}, fmt.Errorf("parent is too long")
	}
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return resultTreeScope{}, fmt.Errorf("parent is not a valid tree key")
	}
	var scope resultTreeScope
	if err := json.Unmarshal(raw, &scope); err != nil || scope.Origin == "" {
		return resultTreeScope{}, fmt.Errorf("parent is not a valid tree key")
	}
	if len(scope.Segments) > 512 {
		return resultTreeScope{}, fmt.Errorf("parent is too deep")
	}
	return scope, nil
}

func treeLocation(result StoredResult) resultTreeLocation {
	raw := strings.TrimSpace(result.URL)
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		origin := strings.TrimSpace(result.Host)
		if origin == "" {
			origin = "Unknown host"
		}
		return resultTreeLocation{origin: origin, segments: []string{raw}}
	}
	escapedPath := parsed.EscapedPath()
	if escapedPath == "" {
		escapedPath = "/"
	}
	segments := strings.Split(strings.Trim(escapedPath, "/"), "/")
	if len(segments) == 1 && segments[0] == "" {
		segments[0] = "/"
	}
	suffix := ""
	if parsed.RawQuery != "" {
		suffix += "?" + parsed.RawQuery
	}
	if parsed.Fragment != "" {
		suffix += "#" + parsed.Fragment
	}
	return resultTreeLocation{
		origin:        parsed.Scheme + "://" + parsed.Host,
		segments:      segments,
		suffix:        suffix,
		trailingSlash: escapedPath != "/" && strings.HasSuffix(escapedPath, "/"),
	}
}

func displayTreeSegment(segment string) string {
	if segment == "/" {
		return "/"
	}
	decoded, err := url.PathUnescape(segment)
	if err != nil {
		return segment
	}
	return decoded
}

func treeURI(segments []string, directory bool, suffix string) string {
	if len(segments) == 1 && segments[0] == "/" {
		return "/" + suffix
	}
	path := "/" + strings.Join(segments, "/")
	if directory && !strings.HasSuffix(path, "/") {
		path += "/"
	}
	return path + suffix
}

// resultTreeMetadata turns the parsed URL hierarchy into a delimiter-safe key.
// PathEscape is deliberately applied to each already-escaped URL segment. It
// means '/' can be used as the hierarchy delimiter without ambiguity, even for
// encoded slashes, empty segments, and malformed URLs handled by treeLocation.
func resultTreeMetadata(rawURL, host string) (origin, pathKey, suffix string, trailingSlash bool) {
	location := treeLocation(StoredResult{URL: rawURL, Host: host})
	encoded := make([]string, len(location.segments))
	for index, segment := range location.segments {
		encoded[index] = url.PathEscape(segment)
	}
	return location.origin, strings.Join(encoded, "/"), location.suffix, location.trailingSlash
}

func resultTreePathKey(segments []string) string {
	encoded := make([]string, len(segments))
	for index, segment := range segments {
		encoded[index] = url.PathEscape(segment)
	}
	return strings.Join(encoded, "/")
}

func decodeResultTreePathSegment(encoded string) string {
	segment, err := url.PathUnescape(encoded)
	if err != nil {
		return encoded
	}
	return segment
}

// storedResultFilterSQL compiles the metric-only persisted ffuf filters to the
// exact keep predicate used by engine.ResultPassesFilters. A filter predicate
// means "discard this row", so OR mode keeps rows matching none of them and
// AND mode keeps rows unless every predicate matches.
func storedResultFilterSQL(options ffuf.FilterOptions) (string, []any, error) {
	type metricFilter struct {
		column string
		value  string
		all    bool
	}
	filters := []metricFilter{
		{column: "status", value: options.Status, all: true},
		{column: "length", value: options.Size},
		{column: "words", value: options.Words},
		{column: "lines", value: options.Lines},
	}
	matches := make([]string, 0, 5)
	args := make([]any, 0, 10)
	for _, filter := range filters {
		if filter.value == "" {
			continue
		}
		ranges := make([]string, 0)
		for _, raw := range strings.Split(filter.value, ",") {
			if filter.all && raw == "all" {
				ranges = append(ranges, "1")
				continue
			}
			valueRange, err := ffuf.ValueRangeFromString(raw)
			if err != nil {
				return "", nil, err
			}
			ranges = append(ranges, filter.column+" BETWEEN ? AND ?")
			args = append(args, valueRange.Min, valueRange.Max)
		}
		matches = append(matches, "("+strings.Join(ranges, " OR ")+")")
	}
	if options.Time != "" {
		if len(options.Time) < 2 || (options.Time[0] != '>' && options.Time[0] != '<') {
			return "", nil, fmt.Errorf("invalid time filter %q", options.Time)
		}
		milliseconds, err := strconv.ParseInt(options.Time[1:], 10, 64)
		if err != nil {
			return "", nil, fmt.Errorf("invalid time filter %q", options.Time)
		}
		matches = append(matches, "((duration_ns / 1000000) "+string(options.Time[0])+" ?)")
		args = append(args, milliseconds)
	}
	if len(matches) == 0 {
		return "", nil, nil
	}
	mode := strings.ToLower(strings.TrimSpace(options.Mode))
	if mode == "" {
		mode = "or"
	}
	separator := " OR "
	if mode == "and" {
		separator = " AND "
	} else if mode != "or" {
		return "", nil, fmt.Errorf("invalid filter mode %q", mode)
	}
	return "NOT (" + strings.Join(matches, separator) + ")", args, nil
}

// backfillResultTreeMetadata upgrades existing databases in bounded
// transactions. The empty origin is a safe migration marker because every
// parsed or fallback tree location has a non-empty origin.
func (s *Store) backfillResultTreeMetadata(ctx context.Context) error {
	const batchSize = 2_000
	for {
		rows, err := s.db.QueryContext(ctx, `SELECT id,url,host FROM results
			WHERE tree_origin='' ORDER BY id LIMIT ?`, batchSize)
		if err != nil {
			return err
		}
		type pendingMetadata struct {
			id                      int64
			origin, pathKey, suffix string
			trailingSlash           bool
		}
		pending := make([]pendingMetadata, 0, batchSize)
		for rows.Next() {
			var id int64
			var rawURL, host string
			if err := rows.Scan(&id, &rawURL, &host); err != nil {
				_ = rows.Close()
				return err
			}
			origin, pathKey, suffix, trailingSlash := resultTreeMetadata(rawURL, host)
			pending = append(pending, pendingMetadata{id: id, origin: origin, pathKey: pathKey, suffix: suffix, trailingSlash: trailingSlash})
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return err
		}
		if err := rows.Close(); err != nil {
			return err
		}
		if len(pending) == 0 {
			return nil
		}
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		statement, err := tx.PrepareContext(ctx, `UPDATE results SET
			tree_origin=?,tree_path_key=?,tree_suffix=?,tree_trailing_slash=? WHERE id=?`)
		if err != nil {
			_ = tx.Rollback()
			return err
		}
		for _, metadata := range pending {
			if _, err := statement.ExecContext(ctx, metadata.origin, metadata.pathKey, metadata.suffix,
				boolInt(metadata.trailingSlash), metadata.id); err != nil {
				_ = statement.Close()
				_ = tx.Rollback()
				return err
			}
		}
		if err := statement.Close(); err != nil {
			_ = tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
}

// ExploreResultTree aggregates one hierarchy level in SQLite. The path index
// makes a deep expansion proportional to the selected subtree instead of the
// entire scan, while SQL grouping avoids allocating and decoding every result
// in Go. The id boundary keeps all lazy pages on one stable snapshot.
func (s *Store) ExploreResultTree(ctx context.Context, sessionID string, query ResultExplorerQuery, parent resultTreeScope) (ResultTreePage, error) {
	if query.Limit <= 0 {
		query.Limit = 200
	}
	if query.Limit > maxExplorerPageSize {
		query.Limit = maxExplorerPageSize
	}
	if query.Offset < 0 {
		query.Offset = 0
	}
	session, err := s.GetSession(ctx, sessionID)
	if err != nil {
		return ResultTreePage{}, err
	}
	where, args := resultExplorerPredicate(sessionID, query)
	filterPredicate, filterArgs, err := storedResultFilterSQL(session.ResultFilters)
	if err != nil {
		return ResultTreePage{}, err
	}
	if filterPredicate != "" {
		where = append(where, filterPredicate)
		args = append(args, filterArgs...)
	}
	snapshotID := int64(0)
	if query.SnapshotID != nil {
		snapshotID = *query.SnapshotID
	} else if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(id),0) FROM results WHERE session_id=?`, sessionID).Scan(&snapshotID); err != nil {
		return ResultTreePage{}, err
	}
	where = append(where, "id<=?")
	args = append(args, snapshotID)
	if parent.Origin == "" {
		return s.exploreResultTreeOrigins(ctx, query, where, args, snapshotID)
	}
	return s.exploreResultTreeChildren(ctx, sessionID, query, parent, where, args, snapshotID)
}

func (s *Store) exploreResultTreeOrigins(ctx context.Context, query ResultExplorerQuery, where []string, args []any, snapshotID int64) (ResultTreePage, error) {
	queryArgs := append([]any{}, args...)
	queryArgs = append(queryArgs, query.Limit, query.Offset)
	rows, err := s.db.QueryContext(ctx, `WITH children AS (
		SELECT tree_origin,COUNT(*) AS result_count
		FROM results WHERE `+strings.Join(where, " AND ")+`
		GROUP BY tree_origin
	) SELECT tree_origin,result_count,COUNT(*) OVER()
	FROM children ORDER BY LOWER(tree_origin),tree_origin LIMIT ? OFFSET ?`, queryArgs...)
	if err != nil {
		return ResultTreePage{}, err
	}
	defer rows.Close()
	page := ResultTreePage{Nodes: []ResultTreeNode{}, Offset: query.Offset, Limit: query.Limit, SnapshotID: snapshotID}
	for rows.Next() {
		var origin string
		var count, total int64
		if err := rows.Scan(&origin, &count, &total); err != nil {
			return ResultTreePage{}, err
		}
		page.Total = int(total)
		page.Nodes = append(page.Nodes, ResultTreeNode{
			Key: encodeResultTreeScope(resultTreeScope{Origin: origin}), Name: origin,
			URL: origin + "/", URI: "/", Kind: "origin", ResultCount: count, HasChildren: true,
		})
	}
	if err := rows.Err(); err != nil {
		return ResultTreePage{}, err
	}
	if err := rows.Close(); err != nil {
		return ResultTreePage{}, err
	}
	if len(page.Nodes) == 0 && query.Offset > 0 {
		if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM (
			SELECT 1 FROM results WHERE `+strings.Join(where, " AND ")+` GROUP BY tree_origin
		)`, args...).Scan(&page.Total); err != nil {
			return ResultTreePage{}, err
		}
	}
	page.HasMore = query.Offset+len(page.Nodes) < page.Total
	return page, nil
}

type resultTreeChildAggregate struct {
	segmentKey             string
	suffix                 string
	resultCount            int64
	directory, hasChildren bool
	resultID               int64
}

func (s *Store) exploreResultTreeChildren(ctx context.Context, sessionID string, query ResultExplorerQuery, parent resultTreeScope, where []string, args []any, snapshotID int64) (ResultTreePage, error) {
	where = append(where, "tree_origin=?")
	args = append(args, parent.Origin)
	startAt := 1
	if len(parent.Segments) > 0 {
		prefix := resultTreePathKey(parent.Segments) + "/"
		// '/' is immediately followed by '0' in the ASCII/BINARY collation
		// used by the index, so this half-open range contains exactly prefix.*.
		where = append(where, "tree_path_key>=?", "tree_path_key<?")
		args = append(args, prefix, strings.TrimSuffix(prefix, "/")+"0")
		startAt = len(prefix) + 1 // SQLite substr is one-indexed.
	}
	queryArgs := make([]any, 0, len(args)+3)
	queryArgs = append(queryArgs, startAt)
	queryArgs = append(queryArgs, args...)
	queryArgs = append(queryArgs, query.Limit, query.Offset)
	rows, err := s.db.QueryContext(ctx, `WITH scoped AS (
		SELECT id,tree_suffix,tree_trailing_slash,substr(tree_path_key,?) AS remainder
		FROM results WHERE `+strings.Join(where, " AND ")+`
	), children AS (
		SELECT
			CASE WHEN instr(remainder,'/')=0 THEN remainder ELSE substr(remainder,1,instr(remainder,'/')-1) END AS segment_key,
			CASE WHEN instr(remainder,'/')=0 THEN tree_suffix ELSE '' END AS child_suffix,
			COUNT(*) AS result_count,
			MAX(CASE WHEN instr(remainder,'/')>0 OR tree_trailing_slash=1 THEN 1 ELSE 0 END) AS directory,
			MAX(CASE WHEN instr(remainder,'/')>0 THEN 1 ELSE 0 END) AS has_children,
			MAX(CASE WHEN instr(remainder,'/')=0 THEN id ELSE 0 END) AS result_id
		FROM scoped
		GROUP BY segment_key,child_suffix
	) SELECT segment_key,child_suffix,result_count,directory,has_children,result_id,COUNT(*) OVER()
	FROM children
	ORDER BY directory DESC,LOWER(segment_key || child_suffix),segment_key,child_suffix
	LIMIT ? OFFSET ?`, queryArgs...)
	if err != nil {
		return ResultTreePage{}, err
	}
	aggregates := make([]resultTreeChildAggregate, 0, query.Limit)
	total := 0
	for rows.Next() {
		var aggregate resultTreeChildAggregate
		var directory, hasChildren int
		var rowTotal int64
		if err := rows.Scan(&aggregate.segmentKey, &aggregate.suffix, &aggregate.resultCount,
			&directory, &hasChildren, &aggregate.resultID, &rowTotal); err != nil {
			_ = rows.Close()
			return ResultTreePage{}, err
		}
		aggregate.directory, aggregate.hasChildren = directory != 0, hasChildren != 0
		total = int(rowTotal)
		aggregates = append(aggregates, aggregate)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return ResultTreePage{}, err
	}
	if err := rows.Close(); err != nil {
		return ResultTreePage{}, err
	}
	if len(aggregates) == 0 && query.Offset > 0 {
		countArgs := make([]any, 0, len(args)+1)
		countArgs = append(countArgs, startAt)
		countArgs = append(countArgs, args...)
		if err := s.db.QueryRowContext(ctx, `WITH scoped AS (
			SELECT tree_suffix,substr(tree_path_key,?) AS remainder
			FROM results WHERE `+strings.Join(where, " AND ")+`
		) SELECT COUNT(*) FROM (
			SELECT 1 FROM scoped GROUP BY
				CASE WHEN instr(remainder,'/')=0 THEN remainder ELSE substr(remainder,1,instr(remainder,'/')-1) END,
				CASE WHEN instr(remainder,'/')=0 THEN tree_suffix ELSE '' END
		)`, countArgs...).Scan(&total); err != nil {
			return ResultTreePage{}, err
		}
	}

	resultIDs := make([]int64, 0, len(aggregates))
	for _, aggregate := range aggregates {
		if aggregate.resultID > 0 {
			resultIDs = append(resultIDs, aggregate.resultID)
		}
	}
	results, err := s.resultsByID(ctx, sessionID, resultIDs)
	if err != nil {
		return ResultTreePage{}, err
	}
	page := ResultTreePage{
		Nodes: []ResultTreeNode{}, Total: total, Offset: query.Offset, Limit: query.Limit, SnapshotID: snapshotID,
	}
	for _, aggregate := range aggregates {
		segment := decodeResultTreePathSegment(aggregate.segmentKey)
		childSegments := append(append([]string{}, parent.Segments...), segment)
		kind := "resource"
		if aggregate.directory {
			kind = "directory"
		}
		uri := treeURI(childSegments, aggregate.directory, aggregate.suffix)
		node := ResultTreeNode{
			Key:  encodeResultTreeScope(resultTreeScope{Origin: parent.Origin, Segments: childSegments, Suffix: aggregate.suffix}),
			Name: displayTreeSegment(segment) + aggregate.suffix,
			URL:  parent.Origin + uri, URI: uri, Kind: kind, ResultCount: aggregate.resultCount,
			HasChildren: aggregate.hasChildren,
		}
		if result, exists := results[aggregate.resultID]; exists {
			signature, _ := resultSignature(result)
			explored := ExploredResult{StoredResult: result, DuplicateCount: 1, Signature: signature}
			node.Result = &explored
			node.URL = result.URL
		}
		page.Nodes = append(page.Nodes, node)
	}
	page.HasMore = query.Offset+len(page.Nodes) < page.Total
	return page, nil
}

func (s *Store) resultsByID(ctx context.Context, sessionID string, ids []int64) (map[int64]StoredResult, error) {
	results := make(map[int64]StoredResult, len(ids))
	if len(ids) == 0 {
		return results, nil
	}
	placeholders := make([]string, len(ids))
	args := make([]any, 0, len(ids)+1)
	args = append(args, sessionID)
	for index, id := range ids {
		placeholders[index] = "?"
		args = append(args, id)
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id,input_json,position,status,length,words,lines,content_type,
		redirect_location,url,duration_ns,scraper_json,host,response_fingerprint,created_at
		FROM results WHERE session_id=? AND id IN (`+strings.Join(placeholders, ",")+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		result, err := scanStoredResult(rows, sessionID)
		if err != nil {
			return nil, err
		}
		results[result.ID] = result
	}
	return results, rows.Err()
}

func (s *Server) handleExploreResultTree(w http.ResponseWriter, r *http.Request) {
	query, err := parseExplorerQuery(r.URL.Query())
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_result_query", err.Error())
		return
	}
	parent, err := decodeResultTreeScope(r.URL.Query().Get("parent"))
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_tree_parent", err.Error())
		return
	}
	if raw := r.URL.Query().Get("snapshot_id"); raw != "" {
		if len(r.URL.Query()["snapshot_id"]) != 1 {
			writeAPIError(w, http.StatusBadRequest, "invalid_tree_snapshot", "snapshot_id may be provided once")
			return
		}
		parsed, parseErr := strconv.ParseInt(raw, 10, 64)
		if parseErr != nil || parsed < 0 {
			writeAPIError(w, http.StatusBadRequest, "invalid_tree_snapshot", "snapshot_id must be a non-negative integer")
			return
		}
		query.SnapshotID = &parsed
	}
	page, err := s.store.ExploreResultTree(r.Context(), r.PathValue("id"), query, parent)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, page)
}
