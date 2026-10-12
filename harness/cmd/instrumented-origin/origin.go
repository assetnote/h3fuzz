package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/quic-go/quic-go"
)

const (
	harnessRunHeader  = "X-H3fuzz-Harness-Run"
	harnessRoleHeader = "X-H3fuzz-Harness-Role"

	phaseMain     = "main"
	phaseUnmarked = "unmarked"

	routeBench = "bench"
	routeLarge = "large"
	routeFiles = "files"
	routeOther = "other"

	protocolH1 = "h1"
	protocolH2 = "h2"
	protocolH3 = "h3"

	methodGET   = "GET"
	methodHEAD  = "HEAD"
	methodOther = "other"
)

var (
	noStoreHeaderValue = []string{"no-store"}
	h1HeaderValue      = []string{"HTTP/1.1"}
	h2HeaderValue      = []string{"HTTP/2.0"}
	h3HeaderValue      = []string{"HTTP/3.0"}
)

type requestRole string

const (
	roleInitial                         requestRole = "initial"
	roleOptimizationFallback            requestRole = "optimization_fallback"
	roleEngineRetry                     requestRole = "engine_retry"
	roleEngineRetryOptimizationFallback requestRole = "engine_retry_optimization_fallback"
)

// OriginConfig contains immutable identity and transport settings for one
// instrumented-origin process.
type OriginConfig struct {
	CountersEnabled              bool
	OriginInstanceID             string
	StartedAt                    time.Time
	BuildSHA256                  string
	ConfigSHA256                 string
	ConfiguredMaxIncomingStreams int64
	EffectiveMaxIncomingStreams  int64
	FilesRoot                    string

	clock         func() time.Time
	newSnapshotID func() (string, error)
}

// Origin serves the benchmark workload and owns its process-local counters.
// Its public surface deliberately exposes snapshots rather than the counter
// map, so callers cannot mutate accounting state.
type Origin struct {
	config  OriginConfig
	state   atomic.Pointer[counterEpoch]
	handler http.Handler
	admin   http.Handler
	quic    *quic.Config
	files   http.Handler
	large   string
}

type counterEpoch struct {
	number   uint64
	counters sync.Map // map[counterKey]*counterCell
}

type counterCell struct {
	handlerRequests     atomic.Uint64
	transportObserved   atomic.Bool
	h3Attempts          atomic.Uint64
	h3RequestRejections atomic.Uint64
}

type counterKey struct {
	runID    string
	phase    string
	route    string
	protocol string
	method   string
	role     string
}

// Snapshot is the versioned JSON representation exposed by the admin plane.
type Snapshot struct {
	SchemaVersion int            `json:"schema_version"`
	SnapshotID    string         `json:"snapshot_id"`
	CapturedAt    time.Time      `json:"captured_at"`
	Origin        OriginMetadata `json:"origin"`
	Counters      []CounterRow   `json:"counters"`
}

// OriginMetadata identifies the exact process and stream-limit configuration
// that produced a snapshot.
type OriginMetadata struct {
	OriginInstanceID             string    `json:"origin_instance_id"`
	StartedAt                    time.Time `json:"started_at"`
	BuildSHA256                  string    `json:"build_sha256"`
	ConfigSHA256                 string    `json:"config_sha256"`
	ConfiguredMaxIncomingStreams int64     `json:"configured_max_incoming_streams"`
	EffectiveMaxIncomingStreams  int64     `json:"effective_max_incoming_streams"`
	CounterEpoch                 uint64    `json:"counter_epoch,omitempty"`
}

// CounterRow is one exact run/phase/route/protocol/method/role tuple.
type CounterRow struct {
	RunID               *string `json:"run_id"`
	Phase               string  `json:"phase"`
	Route               string  `json:"route"`
	Protocol            string  `json:"protocol"`
	Method              string  `json:"method"`
	Role                *string `json:"role"`
	HandlerRequests     uint64  `json:"handler_requests"`
	H3Attempts          *uint64 `json:"h3_attempts"`
	H3RequestRejections *uint64 `json:"h3_request_rejections"`
}

// H3TransportAttempt represents optional transport instrumentation before
// route-handler dispatch. The stock quic-go HTTP/3 server does not expose this
// request-level hook, so these fields remain null unless an explicit hook or a
// fault-injection fixture calls RecordH3TransportAttempt.
type H3TransportAttempt struct {
	RunID    string
	Route    string
	Method   string
	Role     string
	Rejected bool
}

// NewOrigin creates one instrumented origin. Identity fields are intentionally
// fixed at construction so every snapshot from the process is comparable.
func NewOrigin(config OriginConfig) (*Origin, error) {
	if !validUUID(config.OriginInstanceID) {
		return nil, errors.New("origin instance ID must be a UUID")
	}
	if config.StartedAt.IsZero() {
		return nil, errors.New("started_at must be set")
	}
	if !validSHA256(config.BuildSHA256) || !validSHA256(config.ConfigSHA256) {
		return nil, errors.New("build and config SHA-256 values must be 64 hexadecimal characters")
	}
	if config.ConfiguredMaxIncomingStreams <= 0 || config.EffectiveMaxIncomingStreams <= 0 {
		return nil, errors.New("incoming stream limits must be positive")
	}
	if config.EffectiveMaxIncomingStreams > config.ConfiguredMaxIncomingStreams {
		return nil, errors.New("effective incoming stream limit cannot exceed configured value")
	}
	if config.EffectiveMaxIncomingStreams > maximumQUICStreamLimit {
		return nil, errors.New("effective incoming stream limit exceeds QUIC maximum")
	}
	if config.FilesRoot == "" {
		config.FilesRoot = "."
	}
	filesRoot, err := filepath.Abs(config.FilesRoot)
	if err != nil {
		return nil, fmt.Errorf("resolve files root: %w", err)
	}
	rootInfo, err := os.Stat(filesRoot)
	if err != nil {
		return nil, fmt.Errorf("inspect files root: %w", err)
	}
	if !rootInfo.IsDir() {
		return nil, errors.New("files root must be a directory")
	}
	if config.clock == nil {
		config.clock = time.Now
	}
	if config.newSnapshotID == nil {
		config.newSnapshotID = newUUID
	}

	origin := &Origin{
		config: config,
		quic: &quic.Config{
			MaxIncomingStreams: config.EffectiveMaxIncomingStreams,
		},
		files: http.StripPrefix("/files", http.FileServer(http.Dir(filesRoot))),
		large: filepath.Join(filesRoot, "known-large.bin"),
	}
	origin.state.Store(&counterEpoch{number: 1})
	dataHandler := http.HandlerFunc(origin.serveData)
	if config.CountersEnabled {
		origin.handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin.recordHandlerRequest(r)
			dataHandler.ServeHTTP(w, r)
		})
	} else {
		origin.handler = dataHandler
	}
	adminMux := http.NewServeMux()
	adminMux.HandleFunc("/admin/v1", origin.serveMetadata)
	adminMux.HandleFunc("/admin/v1/snapshot", origin.serveSnapshot)
	adminMux.HandleFunc("/admin/v1/reset", origin.serveReset)
	origin.admin = adminMux
	return origin, nil
}

// Handler returns the data-plane handler. The counter-disabled handler does
// not parse marker headers or touch counter state.
func (o *Origin) Handler() http.Handler {
	return o.handler
}

// AdminHandler returns the isolated snapshot and lifecycle API. Callers must
// bind this handler only to a validated loopback listener.
func (o *Origin) AdminHandler() http.Handler {
	return o.admin
}

// QUICConfig returns a clone of the configuration used by the HTTP/3 server.
func (o *Origin) QUICConfig() *quic.Config {
	return o.quic.Clone()
}

// RecordH3TransportAttempt records the optional pre-handler ledger without
// implying that the route handler ran.
func (o *Origin) RecordH3TransportAttempt(attempt H3TransportAttempt) error {
	if !o.config.CountersEnabled {
		return nil
	}
	if !validUUID(attempt.RunID) {
		return errors.New("transport attempt run ID must be a UUID")
	}
	if attempt.Route != routeBench && attempt.Route != routeFiles && attempt.Route != routeLarge {
		return errors.New("transport attempt route must be bench, files, or large")
	}
	if attempt.Method != methodGET && attempt.Method != methodHEAD && attempt.Method != methodOther {
		return errors.New("transport attempt method must be GET, HEAD, or other")
	}
	role := requestRole(attempt.Role)
	switch role {
	case roleInitial, roleOptimizationFallback, roleEngineRetry, roleEngineRetryOptimizationFallback:
	default:
		return errors.New("transport attempt role is invalid")
	}
	key := counterKey{
		runID:    strings.ToLower(attempt.RunID),
		phase:    phaseMain,
		route:    attempt.Route,
		protocol: protocolH3,
		method:   attempt.Method,
		role:     attempt.Role,
	}
	state := o.state.Load()
	cell := counterFor(state, key)
	cell.transportObserved.Store(true)
	cell.h3Attempts.Add(1)
	if attempt.Rejected {
		cell.h3RequestRejections.Add(1)
	}
	return nil
}

// ResetCounters atomically advances to an empty epoch. Requests racing the
// reset are accounted wholly in either the old or new epoch; a snapshot never
// combines rows from both.
func (o *Origin) ResetCounters() uint64 {
	for {
		current := o.state.Load()
		next := &counterEpoch{number: current.number + 1}
		if o.state.CompareAndSwap(current, next) {
			return next.number
		}
	}
}

// Snapshot takes a deterministic view of the current epoch. Callers should
// capture before/after evidence while the measured workload is quiescent.
func (o *Origin) Snapshot() Snapshot {
	state := o.state.Load()
	rows := make([]CounterRow, 0)
	state.counters.Range(func(rawKey, rawValue any) bool {
		key := rawKey.(counterKey)
		cell := rawValue.(*counterCell)
		row := CounterRow{
			Phase:           key.phase,
			Route:           key.route,
			Protocol:        key.protocol,
			Method:          key.method,
			HandlerRequests: cell.handlerRequests.Load(),
		}
		if cell.transportObserved.Load() {
			attempts := cell.h3Attempts.Load()
			rejections := cell.h3RequestRejections.Load()
			row.H3Attempts = &attempts
			row.H3RequestRejections = &rejections
		}
		if key.runID != "" {
			runID := key.runID
			row.RunID = &runID
		}
		if key.role != "" {
			role := key.role
			row.Role = &role
		}
		rows = append(rows, row)
		return true
	})
	sort.Slice(rows, func(i, j int) bool {
		return counterRowSortKey(rows[i]) < counterRowSortKey(rows[j])
	})

	snapshotID, err := o.config.newSnapshotID()
	if err != nil {
		// A cryptographic RNG failure is unrecoverable for the process identity
		// contract. Panic rather than emitting a malformed or reused ID.
		panic(fmt.Sprintf("generate snapshot UUID: %v", err))
	}
	return Snapshot{
		SchemaVersion: 1,
		SnapshotID:    snapshotID,
		CapturedAt:    o.config.clock().UTC(),
		Origin:        o.metadata(state.number),
		Counters:      rows,
	}
}

func (o *Origin) metadata(epoch uint64) OriginMetadata {
	return OriginMetadata{
		OriginInstanceID:             strings.ToLower(o.config.OriginInstanceID),
		StartedAt:                    o.config.StartedAt.UTC(),
		BuildSHA256:                  strings.ToLower(o.config.BuildSHA256),
		ConfigSHA256:                 strings.ToLower(o.config.ConfigSHA256),
		ConfiguredMaxIncomingStreams: o.config.ConfiguredMaxIncomingStreams,
		EffectiveMaxIncomingStreams:  o.config.EffectiveMaxIncomingStreams,
		CounterEpoch:                 epoch,
	}
}

func (o *Origin) recordHandlerRequest(r *http.Request) {
	key := counterKey{
		phase:    phaseUnmarked,
		route:    classifyRoute(r.URL.Path),
		protocol: classifyProtocol(r.ProtoMajor),
		method:   classifyMethod(r.Method),
	}
	if runID, role, ok := markedRequest(r, key.route); ok {
		key.runID = runID
		key.phase = phaseMain
		key.role = string(role)
	}
	state := o.state.Load()
	counterFor(state, key).handlerRequests.Add(1)
}

func counterFor(state *counterEpoch, key counterKey) *counterCell {
	if cellValue, ok := state.counters.Load(key); ok {
		return cellValue.(*counterCell)
	}
	cellValue, _ := state.counters.LoadOrStore(key, &counterCell{})
	return cellValue.(*counterCell)
}

func (o *Origin) serveData(w http.ResponseWriter, r *http.Request) {
	headers := w.Header()
	headers["Cache-Control"] = noStoreHeaderValue
	switch r.ProtoMajor {
	case 3:
		headers["X-H3-Harness-Protocol"] = h3HeaderValue
	case 2:
		headers["X-H3-Harness-Protocol"] = h2HeaderValue
	default:
		headers["X-H3-Harness-Protocol"] = h1HeaderValue
	}
	if r.URL.Path == "/healthz" {
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprintln(w, r.Proto)
		return
	}
	switch classifyRoute(r.URL.Path) {
	case routeBench:
		w.WriteHeader(http.StatusNoContent)
	case routeFiles:
		o.files.ServeHTTP(w, r)
	case routeLarge:
		http.ServeFile(w, r, o.large)
	default:
		http.NotFound(w, r)
	}
}

func (o *Origin) serveSnapshot(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(o.Snapshot()); err != nil {
		panic(fmt.Sprintf("encode origin snapshot: %v", err))
	}
}

func (o *Origin) serveMetadata(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	metadata := struct {
		SchemaVersion int            `json:"schema_version"`
		Origin        OriginMetadata `json:"origin"`
	}{
		SchemaVersion: 1,
		Origin:        o.metadata(o.state.Load().number),
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(metadata); err != nil {
		panic(fmt.Sprintf("encode origin metadata: %v", err))
	}
}

func (o *Origin) serveReset(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	result := struct {
		SchemaVersion    int    `json:"schema_version"`
		OriginInstanceID string `json:"origin_instance_id"`
		CounterEpoch     uint64 `json:"counter_epoch"`
	}{
		SchemaVersion:    1,
		OriginInstanceID: strings.ToLower(o.config.OriginInstanceID),
		CounterEpoch:     o.ResetCounters(),
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(result); err != nil {
		panic(fmt.Sprintf("encode origin reset result: %v", err))
	}
}

func markedRequest(r *http.Request, route string) (string, requestRole, bool) {
	if route != routeBench && route != routeFiles && route != routeLarge {
		return "", "", false
	}
	runID := strings.ToLower(r.Header.Get(harnessRunHeader))
	if !validUUID(runID) {
		return "", "", false
	}
	role := requestRole(r.Header.Get(harnessRoleHeader))
	switch role {
	case roleInitial, roleOptimizationFallback, roleEngineRetry, roleEngineRetryOptimizationFallback:
		return runID, role, true
	default:
		return "", "", false
	}
}

func classifyRoute(path string) string {
	switch {
	case strings.HasPrefix(path, "/bench/"):
		return routeBench
	case strings.HasPrefix(path, "/large/"):
		return routeLarge
	case strings.HasPrefix(path, "/files/"):
		return routeFiles
	default:
		return routeOther
	}
}

func classifyProtocol(major int) string {
	switch major {
	case 3:
		return protocolH3
	case 2:
		return protocolH2
	default:
		return protocolH1
	}
}

func classifyMethod(method string) string {
	switch method {
	case http.MethodGet:
		return methodGET
	case http.MethodHead:
		return methodHEAD
	default:
		return methodOther
	}
}

func counterRowSortKey(row CounterRow) string {
	runID, role := "", ""
	if row.RunID != nil {
		runID = *row.RunID
	}
	if row.Role != nil {
		role = *row.Role
	}
	return strings.Join([]string{runID, row.Phase, row.Route, row.Protocol, row.Method, role}, "\x00")
}

func validSHA256(value string) bool {
	if len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func validUUID(value string) bool {
	if len(value) != 36 || value[8] != '-' || value[13] != '-' || value[18] != '-' || value[23] != '-' {
		return false
	}
	for index := range value {
		if index == 8 || index == 13 || index == 18 || index == 23 {
			continue
		}
		character := value[index]
		if !((character >= '0' && character <= '9') ||
			(character >= 'a' && character <= 'f') ||
			(character >= 'A' && character <= 'F')) {
			return false
		}
	}
	return true
}

func newUUID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	value[6] = (value[6] & 0x0f) | 0x40
	value[8] = (value[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		value[0:4], value[4:6], value[6:8], value[8:10], value[10:16]), nil
}
