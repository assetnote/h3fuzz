export type SessionStatus =
  | 'idle'
  | 'starting'
  | 'running'
  | 'paused'
  | 'stopping'
  | 'completed'
  | 'stopped'
  | 'failed';

export type ProfileID = string;

export type ErrorCategory =
  | 'dns'
  | 'tls'
  | 'quic_handshake'
  | 'timeout'
  | 'connection_reset'
  | 'rate_limited'
  | 'persistence_backpressure'
  | 'other';

export interface ErrorDiagnostic {
  category: ErrorCategory | string;
  count: number;
  samples: string[];
  suggestion: string;
}

export interface PreflightCheck {
  attempted: boolean;
  reliable: boolean;
  requests: number;
  elapsed_ms: number;
  /** Legacy probe fields accepted from older servers. */
  supported?: boolean;
  status?: number;
  response_bytes?: number;
  duration_ms?: number;
  reason?: string;
}

export interface HTTP3Fingerprint {
  target: string;
  supported: boolean;
  high_rate_ready: boolean;
  reason?: string;
  alpn?: string;
  quic_version?: string;
  http_version?: string;
  status_code?: number;
  connection_reused: boolean;
  concurrent_streams_completed: number;
  concurrent_streams_attempted: number;
  quic_datagrams: boolean;
  http3_datagrams: boolean;
  remote_addr?: string;
  elapsed_ms: number;
}

export interface PreflightTimings {
  advertisement: number;
  http3: number;
  baseline: number;
  head: number;
  range: number;
  total: number;
}

export interface ProtocolPreflightReport {
  state: 'not_run' | 'running' | 'complete' | 'completed' | 'failed' | string;
  ran_at?: string;
  /** Legacy timestamp accepted from older servers. */
  started_at?: string;
  completed_at?: string;
  http3_advertised: boolean;
  http3_negotiated: boolean;
  http3_advertisement?: string;
  advertisement_check: PreflightCheck;
  negotiation_check: PreflightCheck;
  fingerprint: HTTP3Fingerprint;
  head_check: PreflightCheck;
  range_check: PreflightCheck;
  /** Legacy check names accepted from older servers. */
  head?: PreflightCheck;
  range?: PreflightCheck;
  selected_protocol: string;
  selected_strategy: string;
  strategy_reason?: string;
  effective_workers: number;
  effective_connections: number;
  effective_streams: number;
  streams_per_connection: number;
  downgrade_reason?: string;
  timings_ms: PreflightTimings;
  error?: string;
}

export interface Progress {
  started_at?: string;
  req_count: number;
  req_total: number;
  req_sec: number;
  queue_pos: number;
  queue_total: number;
  error_count: number;
  results_queued?: number;
  persistence_backpressure?: boolean;
  last_batch_duration_ms?: number;
  stall_reason?: string;
  stalled_for_ms?: number;
  request_count?: number;
  completed_count?: number;
  response_count?: number;
  current_rate?: number;
  average_30s?: number;
  overall_average?: number;
  peak_rate?: number;
  eta_seconds?: number | null;
}

export interface Session {
  id: string;
  name: string;
  target: string;
  status: SessionStatus;
  profile: ProfileID;
  wordlist: string;
  options: ConfigOptions;
  explicit_flags: string[];
  progress: Progress;
  result_count: number;
  sequence?: number;
  runtime_transport?: 'http1' | 'http2' | 'http3' | '';
  runtime_transport_reason?: string;
  response_optimization_strategy?: 'HEAD' | 'RANGE' | 'GET' | string;
  response_optimization_reason?: string;
  preflight_report?: ProtocolPreflightReport;
  error_diagnostics?: ErrorDiagnostic[];
  pinned?: boolean;
  archived?: boolean;
  group_name?: string;
  cloned_from_id?: string;
  error?: string;
  created_at: string;
  updated_at: string;
  started_at?: string;
  completed_at?: string;
}

export interface SessionOrganization {
  pinned: boolean;
  archived: boolean;
  group_name?: string;
  cloned_from_id?: string;
}

export interface BulkSessionResult {
  succeeded: string[];
  failed: Record<string, string>;
}

export interface Result {
  id: number;
  session_id: string;
  input: Record<string, string> | string;
  position: number;
  status: number;
  length: number;
  words: number;
  lines: number;
  content_type: string;
  redirect_location?: string;
  url: string;
  duration_ns: number;
  scraper?: Record<string, string[]>;
  host: string;
  created_at: string;
  duplicate_count?: number;
  wildcard_group?: boolean;
  signature?: string;
  response_fingerprint?: string;
}

export interface TerminalEntry {
  id?: number;
  session_id?: string;
  at: string;
  level: 'debug' | 'info' | 'warning' | 'error' | string;
  message: string;
}

export interface Profile {
  id: ProfileID;
  name: string;
  description?: string;
  extensions: string[];
  options?: Partial<ConfigOptions>;
}

export interface FlightVariable {
  name: string;
  regex: string;
  source?: '' | 'auto' | 'form' | 'meta' | 'cookie' | 'header';
  key?: string;
}

export interface FlightConfig {
  request_file: string;
  vars: FlightVariable[];
}

export interface ConfigOptions {
  general: {
    autocalibration: boolean;
    autocalibration_keyword: string;
    autocalibration_per_host: boolean;
    autocalibration_strategies: string[];
    autocalibration_strings: string[];
    colors: boolean;
    config_file: string;
    delay: string;
    json: boolean;
    maxtime: number;
    maxtime_job: number;
    noninteractive: boolean;
    quiet: boolean;
    rate: number;
    scraperfile: string;
    scrapers: string;
    stop_on_403: boolean;
    stop_on_all: boolean;
    stop_on_errors: boolean;
    threads: number;
    verbose: boolean;
  };
  http: {
    cookies: string[];
    data: string;
    follow_redirects: boolean;
    headers: string[];
    http3: boolean;
    http1: boolean;
    no_http3: boolean;
    http3_fingerprint: boolean;
    no_response_optimization: boolean;
    http3_connections: number;
    http3_max_connections: number;
    http3_max_workers: number;
    http3_streams: number;
    http3_probe_timeout: number;
    ignore_body: boolean;
    method: string;
    proxy_url: string;
    raw: boolean;
    recursion: boolean;
    recursion_depth: number;
    recursion_strategy: string;
    replay_proxy_url: string;
    sni: string;
    timeout: number;
    url: string;
    http2: boolean;
    'client-cert': string;
    'client-key': string;
    preflights: FlightConfig[];
    postflights: FlightConfig[];
    preflight_mode: string;
    preflight_error: string;
    preflight_anyhost: boolean;
  };
  input: {
    dirsearch_compat: boolean;
    encoders: string[];
    extensions: string;
    ignore_wordlist_comments: boolean;
    input_mode: string;
    input_num: number;
    input_shell: string;
    input_commands: string[];
    request_file: string;
    request_proto: string;
    wordlists: string[];
  };
  matchers: ConditionOptions;
  filters: ConditionOptions;
  output: {
    audit_log: string;
    debug_log: string;
    output_directory: string;
    output_file: string;
    output_format: string;
    output_skip_empty: boolean;
    run_telemetry: string;
  };
}

export interface ConditionOptions {
  mode: string;
  lines: string;
  regexp: string;
  size: string;
  status: string;
  time: string;
  words: string;
}

export interface Settings {
  defaults: {
    wordlist: string;
    profile: ProfileID;
    options: ConfigOptions;
    explicit_flags: string[];
  };
  updated_at?: string;
}

export interface StorageStats {
  database_bytes: number;
  wal_bytes: number;
  total_bytes: number;
  reclaimable_bytes: number;
  warning_threshold_bytes: number;
  warning: boolean;
  response_bodies_stored: boolean;
}

export interface Preset {
  id: string;
  name: string;
  description?: string;
  wordlist: string;
  profile: ProfileID;
  options: ConfigOptions;
  explicit_flags: string[];
  created_at: string;
  updated_at: string;
}

export interface NumericFacet {
  min: number;
  max: number;
}

export interface ResultFacets {
  status: Record<string, number>;
  content_type: Record<string, number>;
  size: NumericFacet;
  words: NumericFacet;
  lines: NumericFacet;
}

export interface ResultExplorerPage {
  results: Result[];
  total: number;
  offset: number;
  limit: number;
  has_more: boolean;
  facets: ResultFacets;
}

export interface ResultTreeNode {
  key: string;
  name: string;
  url: string;
  uri: string;
  kind: 'origin' | 'directory' | 'resource';
  result?: Result;
  result_count: number;
  has_children: boolean;
}

export interface ResultTreePage {
  nodes: ResultTreeNode[];
  total: number;
  offset: number;
  limit: number;
  has_more: boolean;
  snapshot_id: number;
}

export interface ResultFilterViewQuery {
  query?: string;
  statuses?: number[];
  contentTypes?: string[];
  sizeMin?: number;
  sizeMax?: number;
  wordsMin?: number;
  wordsMax?: number;
  linesMin?: number;
  linesMax?: number;
  sort?: string;
  order?: 'asc' | 'desc';
  groupDuplicates?: boolean;
}

export interface SavedResultView {
  id: string;
  session_id: string;
  name: string;
  query: ResultFilterViewQuery;
  created_at: string;
  updated_at: string;
}

export interface SessionComparison {
  left_session_id: string;
  right_session_id: string;
  added: Result[];
  removed: Result[];
  added_total: number;
  removed_total: number;
  unchanged_count: number;
  added_next_cursor?: string;
  removed_next_cursor?: string;
  added_has_more: boolean;
  removed_has_more: boolean;
  limit: number;
}

export interface WordlistInspection {
  path: string;
  readable: boolean;
  entries: number;
  extension_markers?: number;
  comment_entries?: number;
  comment_extension_markers?: number;
  size_bytes?: number;
  error?: string;
}

export interface TerminalSearchPage {
  entries: TerminalEntry[];
  total: number;
  offset: number;
  limit: number;
  has_more: boolean;
  levels: Record<string, number>;
  snapshot_id?: number;
  next_before_id?: number;
  has_older?: boolean;
}

export interface CreateSessionRequest {
  name?: string;
  target: string;
  wordlist?: string;
  profile: ProfileID;
  options?: ConfigOptions;
  explicit_flags?: string[];
  auto_start?: boolean;
}

export interface SessionFilterPatch {
  status?: string;
  size?: string;
  words?: string;
  lines?: string;
  time?: string;
  regexp?: string;
  mode?: string;
}

export interface WebSocketEnvelope<T = unknown> {
  type:
    | 'snapshot'
    | 'session.created'
    | 'session.updated'
    | 'session.deleted'
    | 'progress'
    | 'result'
    | 'terminal'
    | 'resync.required';
  session_id?: string;
  sequence: number;
  data: T;
  at: string;
}

export interface OptionMetadata {
  name: string;
  json_path?: string;
  section: 'general' | 'http' | 'input' | 'matcher' | 'filter' | 'output' | string;
  type: 'boolean' | 'integer' | 'string' | 'string_array' | 'object_array';
  default: unknown;
  usage: string;
  aliases: string[];
  repeatable: boolean;
  hidden: boolean;
  available?: boolean;
  unavailable_reason?: string;
  choices?: string[];
  min?: number;
  max?: number;
}

export interface OptionsMetadata {
  defaults: ConfigOptions;
  flags: OptionMetadata[];
}

export type ConnectionState = 'connecting' | 'connected' | 'reconnecting' | 'offline';
