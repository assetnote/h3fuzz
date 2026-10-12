package webserver

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var unsafeFilename = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

func (s *Server) handleExport(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	session, err := s.store.GetSession(r.Context(), id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	format := strings.ToLower(defaultString(r.URL.Query().Get("format"), "json"))
	if r.URL.Query().Get("preview") == "true" {
		limit, parseErr := strconv.Atoi(defaultString(r.URL.Query().Get("limit"), "200"))
		if parseErr != nil || limit < 1 || limit > 1000 {
			writeAPIError(w, http.StatusBadRequest, "invalid_preview_limit", "preview limit must be between 1 and 1000")
			return
		}
		s.handleExportPreview(w, r, session, format, limit)
		return
	}
	filename := unsafeFilename.ReplaceAllString(session.Name, "_")
	if filename == "" {
		filename = "h3fuzz-results"
	}
	switch format {
	case "json":
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s.json"`, filename))
		_, _ = io.WriteString(w, `{"session":`)
		sessionJSON, _ := json.Marshal(session)
		_, _ = w.Write(sessionJSON)
		_, _ = io.WriteString(w, `,"results":[`)
		first := true
		err = s.store.ForEachResult(r.Context(), id, func(result StoredResult) error {
			if !first {
				if _, err := io.WriteString(w, ","); err != nil {
					return err
				}
			}
			first = false
			data, err := json.Marshal(result)
			if err == nil {
				_, err = w.Write(data)
			}
			return err
		})
		if err == nil {
			_, _ = io.WriteString(w, `]}`+"\n")
		}
	case "text":
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s.txt"`, filename))
		err = s.store.ForEachResult(r.Context(), id, func(result StoredResult) error {
			value := result.URL
			if parsed, parseErr := url.Parse(result.URL); parseErr == nil {
				value = parsed.EscapedPath()
				if value == "" {
					value = "/"
				}
				if parsed.RawQuery != "" {
					value += "?" + parsed.RawQuery
				}
			}
			_, writeErr := fmt.Fprintln(w, value)
			return writeErr
		})
	case "terminal":
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s-terminal.txt"`, filename))
		_, _ = fmt.Fprintf(w, "h3fuzz web session %s\nTarget: %s\nStatus: %s\nCreated: %s\n\n",
			session.ID, session.Target, session.Status, session.CreatedAt.Format(time.RFC3339))
		err = s.store.ForEachTerminalEntry(r.Context(), id, func(entry TerminalEntry) error {
			// Matched results are also persisted as level=result so the live
			// terminal can mirror ffuf output. The Results section below streams
			// the authoritative result table, so omit those rendering-only rows
			// here to avoid duplicating result lines.
			if entry.Level == "result" {
				return nil
			}
			_, writeErr := fmt.Fprintf(w, "[%s] [%s] %s\n", entry.At.Format(time.RFC3339Nano), strings.ToUpper(entry.Level), strings.TrimRight(entry.Message, "\n"))
			return writeErr
		})
		if err == nil {
			_, _ = io.WriteString(w, "\nResults:\n")
			err = s.store.ForEachResult(r.Context(), id, func(result StoredResult) error {
				_, writeErr := fmt.Fprintln(w, formatResultTerminal(result))
				return writeErr
			})
		}
	default:
		writeAPIError(w, http.StatusBadRequest, "invalid_export_format", "format must be json, text, or terminal")
		return
	}
	if err != nil {
		// The response may already be partially streamed. Ending it is safer than
		// appending a different wire format to the export.
		return
	}
}

func (s *Server) handleExportPreview(w http.ResponseWriter, r *http.Request, session Session, format string, limit int) {
	w.Header().Set("X-H3fuzz-Preview-Limit", strconv.Itoa(limit))
	setTruncated := func(truncated bool) {
		w.Header().Set("X-H3fuzz-Preview-Truncated", strconv.FormatBool(truncated))
	}
	switch format {
	case "json":
		page, err := s.store.Results(r.Context(), session.ID, 0, limit)
		if err != nil {
			writeInternalError(w, err)
			return
		}
		setTruncated(page.HasMore)
		writeJSON(w, http.StatusOK, struct {
			Session   Session        `json:"session"`
			Results   []StoredResult `json:"results"`
			Truncated bool           `json:"truncated"`
		}{Session: session, Results: page.Results, Truncated: page.HasMore})
	case "text":
		page, err := s.store.Results(r.Context(), session.ID, 0, limit)
		if err != nil {
			writeInternalError(w, err)
			return
		}
		setTruncated(page.HasMore)
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		for _, result := range page.Results {
			value := result.URL
			if parsed, parseErr := url.Parse(result.URL); parseErr == nil {
				value = parsed.EscapedPath()
				if value == "" {
					value = "/"
				}
				if parsed.RawQuery != "" {
					value += "?" + parsed.RawQuery
				}
			}
			_, _ = fmt.Fprintln(w, value)
		}
		if page.HasMore {
			_, _ = fmt.Fprintf(w, "... preview truncated after %d results ...\n", limit)
		}
	case "terminal":
		entries, terminalTruncated, err := s.terminalLifecyclePreview(r, session.ID, limit)
		if err != nil {
			writeInternalError(w, err)
			return
		}
		remaining := limit - len(entries)
		results := ResultsPage{Results: []StoredResult{}}
		if remaining > 0 && !terminalTruncated {
			results, err = s.store.Results(r.Context(), session.ID, 0, remaining)
			if err != nil {
				writeInternalError(w, err)
				return
			}
		}
		truncated := terminalTruncated || results.HasMore || int64(len(results.Results)) < session.ResultCount
		setTruncated(truncated)
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = fmt.Fprintf(w, "h3fuzz web session %s\nTarget: %s\nStatus: %s\nCreated: %s\n\n",
			session.ID, session.Target, session.Status, session.CreatedAt.Format(time.RFC3339))
		for _, entry := range entries {
			_, _ = fmt.Fprintf(w, "[%s] [%s] %s\n", entry.At.Format(time.RFC3339Nano), strings.ToUpper(entry.Level), strings.TrimRight(entry.Message, "\n"))
		}
		if len(results.Results) > 0 {
			_, _ = io.WriteString(w, "\nResults:\n")
			for _, result := range results.Results {
				_, _ = fmt.Fprintf(w, "%s [Status: %d, Size: %d, Words: %d, Lines: %d, Duration: %s]\n",
					result.URL, result.StatusCode, result.ContentLength, result.ContentWords, result.ContentLines, time.Duration(result.DurationNS))
			}
		}
		if truncated {
			_, _ = fmt.Fprintf(w, "... preview truncated after %d records ...\n", limit)
		}
	default:
		writeAPIError(w, http.StatusBadRequest, "invalid_export_format", "format must be json, text, or terminal")
	}
}

// terminalLifecyclePreview returns at most limit non-result terminal entries.
// Result-level entries exist solely for live terminal rendering and would
// otherwise duplicate the authoritative result rows included in the preview.
// It scans in bounded pages and retains no more than limit+1 entries.
func (s *Server) terminalLifecyclePreview(r *http.Request, sessionID string, limit int) ([]TerminalEntry, bool, error) {
	entries := make([]TerminalEntry, 0, limit)
	cursor := int64(0)
	for {
		page, err := s.store.Terminal(r.Context(), sessionID, cursor, 1000)
		if err != nil {
			return nil, false, err
		}
		for _, entry := range page.Entries {
			if entry.Level == "result" {
				continue
			}
			if len(entries) == limit {
				return entries, true, nil
			}
			entries = append(entries, entry)
		}
		if !page.HasMore {
			return entries, false, nil
		}
		cursor = page.NextCursor
	}
}
