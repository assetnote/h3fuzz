package webserver

import (
	"bytes"
	"context"
	"crypto/subtle"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/adrg/xdg"
	"github.com/assetnote/h3fuzz/pkg/ffuf"
)

const maxRequestBody = 2 << 20
const maxResultSearchLength = 512

type Config struct {
	ListenAddress   string
	DatabasePath    string
	Username        string
	Password        string
	CertificatePath string
	PrivateKeyPath  string
	PlainHTTP       bool
	AllowRemote     bool
	AllowInputCmd   bool
	Assets          fs.FS
}

type Server struct {
	config    Config
	store     *Store
	hub       *Hub
	manager   *Manager
	http      *http.Server
	tlsConfig *tls.Config
}

func DefaultDatabasePath() (string, error) { return xdg.DataFile("h3fuzz/web.db") }

func New(config Config) (*Server, error) {
	if config.ListenAddress == "" {
		config.ListenAddress = "127.0.0.1:8787"
	}
	if config.Username == "" {
		config.Username = "h3fuzz"
	}
	if err := validateListenAddress(config.ListenAddress, config.AllowRemote); err != nil {
		return nil, err
	}
	if config.DatabasePath == "" {
		var err error
		config.DatabasePath, err = DefaultDatabasePath()
		if err != nil {
			return nil, err
		}
	}
	store, err := OpenStore(config.DatabasePath)
	if err != nil {
		return nil, err
	}
	if config.Password == "" {
		config.Password, err = store.persistentWebPassword(context.Background())
		if err != nil {
			_ = store.Close()
			return nil, fmt.Errorf("initialize web authentication: %w", err)
		}
	}
	tlsConfig, err := prepareServerTLS(&config)
	if err != nil {
		_ = store.Close()
		return nil, err
	}
	hub := NewHub()
	manager, err := NewManager(store, hub, config.AllowInputCmd)
	if err != nil {
		_ = store.Close()
		return nil, err
	}
	server := &Server{config: config, store: store, hub: hub, manager: manager, tlsConfig: tlsConfig}
	mux := http.NewServeMux()
	server.routes(mux)
	server.http = &http.Server{
		Addr: config.ListenAddress, Handler: server.securityHeaders(server.authenticate(server.protectOrigin(server.protectStorageMaintenance(mux)))),
		ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second,
		WriteTimeout: 0, IdleTimeout: 90 * time.Second, TLSConfig: tlsConfig,
	}
	return server, nil
}

func (s *Server) Serve() error {
	listener, err := s.Listen()
	if err != nil {
		return err
	}
	return s.ServeListener(listener)
}

func (s *Server) Listen() (net.Listener, error) {
	return net.Listen("tcp", s.http.Addr)
}

func (s *Server) ServeListener(listener net.Listener) error {
	var err error
	if s.config.PlainHTTP {
		err = s.http.Serve(listener)
	} else {
		// Certificates are loaded and validated during New. Empty filenames make
		// net/http use the in-memory certificate on s.http.TLSConfig.
		err = s.http.ServeTLS(listener, "", "")
	}
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func (s *Server) Shutdown(ctx context.Context) error {
	// Reject new starts before closing the listener, disconnect upgraded
	// WebSockets, and let in-flight HTTP handlers leave the store before the
	// managed scan jobs and database are torn down.
	s.manager.beginShutdown()
	s.hub.Close()
	httpErr := s.http.Shutdown(ctx)
	managerErr := s.manager.Shutdown(ctx)
	storeErr := s.store.Close()
	return errors.Join(httpErr, managerErr, storeErr)
}

func (s *Server) Handler() http.Handler { return s.http.Handler }

// BasicAuthPassword returns the credential selected during initialization so
// the CLI can print it. When Config.Password is empty, this is the generated
// password persisted in the selected web database.
func (s *Server) BasicAuthPassword() string { return s.config.Password }

// Scheme reports the transport selected for the web listener.
func (s *Server) Scheme() string {
	if s.config.PlainHTTP {
		return "http"
	}
	return "https"
}

// ConsoleURL returns a browser URL for listener. It retains a configured DNS
// name (rather than replacing it with the resolved IP) so it continues to match
// the certificate SAN, while taking the actual port from the bound listener.
func (s *Server) ConsoleURL(listener net.Listener) string {
	host, _, err := net.SplitHostPort(s.config.ListenAddress)
	if err != nil {
		return s.Scheme() + "://" + listener.Addr().String()
	}
	_, port, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		return s.Scheme() + "://" + listener.Addr().String()
	}
	if host == "" {
		host = "localhost"
	}
	return s.Scheme() + "://" + net.JoinHostPort(host, port)
}

// TLSCertificatePaths returns the selected PEM paths. They are empty for
// explicit plain HTTP and for intentionally ephemeral in-memory databases.
func (s *Server) TLSCertificatePaths() (certificatePath, privateKeyPath string) {
	return s.config.CertificatePath, s.config.PrivateKeyPath
}

func validateListenAddress(address string, allowRemote bool) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("invalid -web-listen address %q: expected host:port", address)
	}
	loopback := strings.EqualFold(host, "localhost")
	if ip := net.ParseIP(host); ip != nil {
		loopback = ip.IsLoopback()
	}
	if !loopback && !allowRemote {
		return fmt.Errorf("refusing non-loopback web listener %q without -web-allow-remote", address)
	}
	return nil
}

func (s *Server) routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/health", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("GET /api/v1/options", s.handleOptions)
	mux.HandleFunc("GET /api/v1/profiles", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, Profiles())
	})
	mux.HandleFunc("GET /api/v1/settings", s.handleGetSettings)
	mux.HandleFunc("PUT /api/v1/settings", s.handlePutSettings)
	mux.HandleFunc("GET /api/v1/storage", s.handleStorageStats)
	mux.HandleFunc("POST /api/v1/storage/compact", s.handleCompactStorage)
	mux.HandleFunc("GET /api/v1/sessions", s.handleListSessions)
	mux.HandleFunc("POST /api/v1/sessions", s.handleCreateSession)
	mux.HandleFunc("GET /api/v1/sessions/{id}", s.handleGetSession)
	mux.HandleFunc("PATCH /api/v1/sessions/{id}/filters", s.handleAddSessionFilters)
	mux.HandleFunc("PATCH /api/v1/sessions/{id}", s.handlePatchSession)
	mux.HandleFunc("DELETE /api/v1/sessions/{id}", s.handleDeleteSession)
	mux.HandleFunc("POST /api/v1/sessions/{id}/{action}", s.handleAction)
	mux.HandleFunc("GET /api/v1/sessions/{id}/results", s.handleResults)
	mux.HandleFunc("GET /api/v1/sessions/{id}/terminal", s.handleTerminal)
	mux.HandleFunc("GET /api/v1/sessions/{id}/export", s.handleExport)
	mux.HandleFunc("GET /api/v1/ws", s.handleWebSocket)
	s.productFeatureRoutes(mux)
	mux.HandleFunc("/api/", func(w http.ResponseWriter, _ *http.Request) {
		writeAPIError(w, http.StatusNotFound, "api_not_found", "API endpoint was not found")
	})
	mux.Handle("/", s.staticHandler())
}

func (s *Server) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		username, password, ok := r.BasicAuth()
		userMatch := subtle.ConstantTimeCompare([]byte(username), []byte(s.config.Username)) == 1
		passwordMatch := subtle.ConstantTimeCompare([]byte(password), []byte(s.config.Password)) == 1
		if !ok || !userMatch || !passwordMatch {
			w.Header().Set("WWW-Authenticate", `Basic realm="h3fuzz", charset="UTF-8"`)
			writeAPIError(w, http.StatusUnauthorized, "authentication_required", "valid Basic authentication is required")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) protectOrigin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodOptions {
			if origin := r.Header.Get("Origin"); origin != "" && !sameOrigin(r, origin) {
				writeAPIError(w, http.StatusForbidden, "cross_origin_request", "cross-origin state changes are not allowed")
				return
			}
			if strings.EqualFold(r.Header.Get("Sec-Fetch-Site"), "cross-site") {
				writeAPIError(w, http.StatusForbidden, "cross_site_request", "cross-site state changes are not allowed")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func sameOrigin(r *http.Request, rawOrigin string) bool {
	origin, err := url.Parse(rawOrigin)
	if err != nil || origin.Host == "" {
		return false
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return strings.EqualFold(origin.Scheme, scheme) && strings.EqualFold(origin.Host, r.Host)
}

func (s *Server) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; connect-src 'self' ws: wss:; img-src 'self' data:; script-src 'self'; style-src 'self' 'unsafe-inline'; base-uri 'none'; frame-ancestors 'none'")
		if strings.HasPrefix(r.URL.Path, "/api/") {
			w.Header().Set("Cache-Control", "no-store")
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) handleOptions(w http.ResponseWriter, r *http.Request) {
	settings, err := s.store.GetSettings(r.Context())
	if err != nil {
		writeInternalError(w, err)
		return
	}
	flags := ffuf.DescribeFlags(&settings.Defaults.Options)
	if !s.config.AllowInputCmd {
		for i := range flags {
			if flags[i].Name == "input-cmd" {
				flags[i].Available = false
				flags[i].UnavailableReason = "disabled by server policy; restart with -web-allow-input-cmd to enable it"
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"defaults": settings.Defaults.Options, "flags": flags})
}

func (s *Server) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	settings, err := s.store.GetSettings(r.Context())
	if err != nil {
		writeInternalError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, settings)
}

func (s *Server) handlePutSettings(w http.ResponseWriter, r *http.Request) {
	var settings Settings
	if !decodeJSON(w, r, &settings) {
		return
	}
	if _, ok := profileByID(settings.Defaults.Profile); !ok {
		writeAPIError(w, http.StatusUnprocessableEntity, "invalid_profile", "settings contain an unknown profile")
		return
	}
	if strings.TrimSpace(settings.Defaults.Wordlist) == "" {
		writeAPIError(w, http.StatusUnprocessableEntity, "invalid_wordlist", "default wordlist is required")
		return
	}
	if !s.config.AllowInputCmd && len(settings.Defaults.Options.Input.Inputcommands) > 0 {
		writeAPIError(w, http.StatusForbidden, "input_command_disabled", "input-cmd is disabled by server policy")
		return
	}
	settings.Defaults.Options.General.Noninteractive = true
	if err := sanitizeWebOptions(&settings.Defaults.Options); err != nil {
		writeAPIError(w, http.StatusUnprocessableEntity, "invalid_settings", err.Error())
		return
	}
	settings.Defaults.ExplicitFlags = uniqueStrings(settings.Defaults.ExplicitFlags)
	if err := rejectWebProfilingFlags(settings.Defaults.ExplicitFlags); err != nil {
		writeAPIError(w, http.StatusUnprocessableEntity, "invalid_settings", err.Error())
		return
	}
	if err := s.store.PutSettings(r.Context(), settings); err != nil {
		writeInternalError(w, err)
		return
	}
	settings, _ = s.store.GetSettings(r.Context())
	writeJSON(w, http.StatusOK, settings)
}

func (s *Server) handleListSessions(w http.ResponseWriter, r *http.Request) {
	sessions, err := s.store.ListSessions(r.Context())
	if err != nil {
		writeInternalError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, sessions)
}

func (s *Server) handleCreateSession(w http.ResponseWriter, r *http.Request) {
	var request CreateSessionRequest
	if !decodeJSON(w, r, &request) {
		return
	}
	session, err := s.manager.Create(r.Context(), request)
	if err != nil {
		if errors.Is(err, ErrMissingFuzzKeyword) {
			writeAPIError(w, http.StatusBadRequest, "missing_fuzz_keyword", err.Error())
			return
		}
		writeAPIError(w, http.StatusUnprocessableEntity, "invalid_session", err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, session)
}

func (s *Server) handleGetSession(w http.ResponseWriter, r *http.Request) {
	session, err := s.store.GetSession(r.Context(), r.PathValue("id"))
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, session)
}

func (s *Server) handlePatchSession(w http.ResponseWriter, r *http.Request) {
	var request PatchSessionRequest
	if !decodeJSON(w, r, &request) {
		return
	}
	id := r.PathValue("id")
	existing, err := s.store.GetSession(r.Context(), id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if existing.Status != StatusIdle {
		writeAPIError(w, http.StatusConflict, "session_not_editable", "session can only be edited while idle")
		return
	}
	session, err := s.manager.Update(r.Context(), id, request)
	if err != nil {
		if errors.Is(err, ErrMissingFuzzKeyword) {
			writeAPIError(w, http.StatusBadRequest, "missing_fuzz_keyword", err.Error())
			return
		}
		writeAPIError(w, http.StatusUnprocessableEntity, "invalid_session", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, session)
}

func (s *Server) handleAddSessionFilters(w http.ResponseWriter, r *http.Request) {
	var request AddSessionFiltersRequest
	if !decodeJSON(w, r, &request) {
		return
	}
	session, err := s.manager.AddFilters(r.Context(), r.PathValue("id"), request)
	if err != nil {
		if isNotFound(err) {
			writeStoreError(w, err)
			return
		}
		if errors.Is(err, ErrFilterConflict) {
			writeAPIError(w, http.StatusConflict, "filters_not_additive", err.Error())
			return
		}
		writeAPIError(w, http.StatusUnprocessableEntity, "invalid_filters", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, session)
}

func (s *Server) handleDeleteSession(w http.ResponseWriter, r *http.Request) {
	if err := s.manager.Delete(r.Context(), r.PathValue("id")); err != nil {
		if isNotFound(err) {
			writeStoreError(w, err)
		} else {
			writeAPIError(w, http.StatusConflict, "session_active", err.Error())
		}
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleAction(w http.ResponseWriter, r *http.Request) {
	id, action := r.PathValue("id"), r.PathValue("action")
	if _, err := s.store.GetSession(r.Context(), id); err != nil {
		writeStoreError(w, err)
		return
	}
	var err error
	var actionSession *Session
	switch action {
	case "start":
		err = s.manager.Start(r.Context(), id)
	case "pause":
		err = s.manager.Pause(r.Context(), id)
	case "resume":
		err = s.manager.Resume(r.Context(), id)
	case "stop":
		var stopped Session
		stopped, err = s.manager.StopSnapshot(r.Context(), id)
		if err == nil {
			actionSession = &stopped
		}
	case "preflight":
		var preflighted Session
		preflighted, err = s.manager.RerunPreflight(r.Context(), id)
		if err == nil {
			actionSession = &preflighted
		}
	default:
		writeAPIError(w, http.StatusNotFound, "unknown_action", "unknown session action")
		return
	}
	if err != nil {
		if errors.Is(err, ErrMissingFuzzKeyword) {
			writeAPIError(w, http.StatusBadRequest, "missing_fuzz_keyword", err.Error())
			return
		}
		writeAPIError(w, http.StatusConflict, "invalid_session_state", err.Error())
		return
	}
	if actionSession != nil {
		writeJSON(w, http.StatusAccepted, *actionSession)
		return
	}
	session, err := s.store.GetSession(r.Context(), id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, session)
}

func (s *Server) handleResults(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, err := s.store.GetSession(r.Context(), id); err != nil {
		writeStoreError(w, err)
		return
	}
	values := r.URL.Query()
	if raw, present := values["after_id"]; present && len(raw) != 1 {
		writeAPIError(w, http.StatusBadRequest, "invalid_cursor", "after_id must be provided once")
		return
	}
	after, err := strconv.ParseInt(defaultString(values.Get("after_id"), "0"), 10, 64)
	if err != nil || after < 0 {
		writeAPIError(w, http.StatusBadRequest, "invalid_cursor", "after_id must be a non-negative integer")
		return
	}
	latest := values.Get("latest") == "true"
	var before *int64
	if raw, present := values["before_id"]; present {
		if len(raw) != 1 {
			writeAPIError(w, http.StatusBadRequest, "invalid_cursor", "before_id must be provided once")
			return
		}
		parsed, parseErr := strconv.ParseInt(raw[0], 10, 64)
		if parseErr != nil || parsed < 0 {
			writeAPIError(w, http.StatusBadRequest, "invalid_cursor", "before_id must be a non-negative integer")
			return
		}
		before = &parsed
	}
	_, afterPresent := values["after_id"]
	if (latest && (before != nil || afterPresent)) || (before != nil && afterPresent) {
		writeAPIError(w, http.StatusBadRequest, "invalid_cursor", "latest, before_id, and after_id are mutually exclusive")
		return
	}
	maxLimit := 1000
	if latest {
		maxLimit = 25000
	}
	limit, err := strconv.Atoi(defaultString(values.Get("limit"), "200"))
	if err != nil || limit <= 0 || limit > maxLimit {
		writeAPIError(w, http.StatusBadRequest, "invalid_limit", fmt.Sprintf("limit must be between 1 and %d", maxLimit))
		return
	}
	if raw, present := values["q"]; present && len(raw) != 1 {
		writeAPIError(w, http.StatusBadRequest, "invalid_query", "q must be provided once")
		return
	}
	search := strings.TrimSpace(values.Get("q"))
	if !utf8.ValidString(search) || len(search) > maxResultSearchLength {
		writeAPIError(w, http.StatusBadRequest, "invalid_query", fmt.Sprintf("q must be valid UTF-8 and at most %d bytes", maxResultSearchLength))
		return
	}
	statuses, err := parseResultStatuses(values["status"])
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_status", err.Error())
		return
	}
	page, err := s.store.QueryResults(r.Context(), id, ResultQuery{
		AfterID: after, BeforeID: before, Limit: limit, Latest: latest, Search: search, Statuses: statuses,
	})
	if err != nil {
		writeInternalError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, page)
}

func parseResultStatuses(values []string) ([]int64, error) {
	seen := make(map[int64]struct{})
	statuses := make([]int64, 0, len(values))
	for _, value := range values {
		for _, token := range strings.Split(value, ",") {
			token = strings.TrimSpace(token)
			if token == "" {
				return nil, errors.New("status values must not be empty")
			}
			status, err := strconv.ParseInt(token, 10, 64)
			if err != nil || status < 100 || status > 599 {
				return nil, fmt.Errorf("status %q must be an integer between 100 and 599", token)
			}
			if _, exists := seen[status]; exists {
				continue
			}
			seen[status] = struct{}{}
			statuses = append(statuses, status)
		}
	}
	return statuses, nil
}

func (s *Server) handleTerminal(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, err := s.store.GetSession(r.Context(), id); err != nil {
		writeStoreError(w, err)
		return
	}
	after, err := strconv.ParseInt(defaultString(r.URL.Query().Get("after_id"), "0"), 10, 64)
	if err != nil || after < 0 {
		writeAPIError(w, http.StatusBadRequest, "invalid_cursor", "after_id must be a non-negative integer")
		return
	}
	limit, err := strconv.Atoi(defaultString(r.URL.Query().Get("limit"), "200"))
	if err != nil || limit <= 0 || limit > 1000 {
		writeAPIError(w, http.StatusBadRequest, "invalid_limit", "limit must be between 1 and 1000")
		return
	}
	page, err := s.store.Terminal(r.Context(), id, after, limit)
	if err != nil {
		writeInternalError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, page)
}

func decodeJSON(w http.ResponseWriter, r *http.Request, target interface{}) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBody)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		var maxBytesError *http.MaxBytesError
		if errors.As(err, &maxBytesError) {
			writeAPIError(w, http.StatusRequestEntityTooLarge, "request_too_large", "request body exceeds 2 MiB")
			return false
		}
		writeAPIError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return false
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		writeAPIError(w, http.StatusBadRequest, "invalid_json", "request body must contain one JSON value")
		return false
	}
	return true
}

func (s *Server) staticHandler() http.Handler {
	if s.config.Assets == nil {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			writeAPIError(w, http.StatusNotFound, "ui_not_built", "web interface assets are not embedded in this build")
		})
	}
	files := http.FileServer(http.FS(s.config.Assets))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assetPath := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if assetPath == "." || assetPath == "" {
			assetPath = "index.html"
		}
		if assetPath == "index.html" {
			index, err := fs.ReadFile(s.config.Assets, "index.html")
			if err != nil {
				http.Error(w, "web interface index is unavailable", http.StatusInternalServerError)
				return
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Cache-Control", "no-store")
			http.ServeContent(w, r, "index.html", time.Time{}, bytes.NewReader(index))
			return
		}
		if _, err := fs.Stat(s.config.Assets, assetPath); err != nil {
			index, readErr := fs.ReadFile(s.config.Assets, "index.html")
			if readErr != nil {
				http.Error(w, "web interface index is unavailable", http.StatusInternalServerError)
				return
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Cache-Control", "no-store")
			http.ServeContent(w, r, "index.html", time.Time{}, bytes.NewReader(index))
			return
		}
		r.URL.Path = "/" + assetPath
		files.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, value interface{}) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeAPIError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, APIError{Error: APIErrorBody{Code: code, Message: message}})
}

func writeInternalError(w http.ResponseWriter, err error) {
	writeAPIError(w, http.StatusInternalServerError, "internal_error", err.Error())
}

func writeStoreError(w http.ResponseWriter, err error) {
	if isNotFound(err) {
		writeAPIError(w, http.StatusNotFound, "session_not_found", "session was not found")
		return
	}
	writeInternalError(w, err)
}

func defaultString(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}
