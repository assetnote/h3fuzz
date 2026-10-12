import type { ConfigOptions, Profile, ProfileID } from '../types/api';

/**
 * Flags inherited by a new web session before the user changes anything.
 *
 * `t` is intentionally absent. An explicit thread count disables the HTTP/3
 * worker supervisor, while an omitted `-t` lets the proven adaptive path size
 * workers from the negotiated connection and stream capacity.
 */
export const DEFAULT_WEB_EXPLICIT_FLAGS = ['D', 'e', 'mc', 'fc', 'H'] as const;

/** Baseline concurrency for regular HTTP scans created in the web console. */
export const DEFAULT_WEB_THREAD_COUNT = 1000;

/** The retained adaptive HTTP/3 cohorts start with eight owned connections. */
export const HTTP3_ADAPTIVE_INITIAL_CONNECTIONS = 8;

export interface HTTP3Capacity {
  adaptiveConnections: boolean;
  automaticWorkers: boolean;
  initialConnections: number;
  maximumConnections: number;
  streamsPerConnection: number;
  initialAdmission: number;
  maximumAdmission: number;
  initialWorkers: number;
  maximumWorkers: number;
}

export const defaultConfig: ConfigOptions = {
  filters: { mode: 'or', lines: '', regexp: '', size: '', status: '404', time: '', words: '' },
  matchers: {
    mode: 'or',
    lines: '',
    regexp: '',
    size: '',
    status: 'all',
    time: '',
    words: '',
  },
  general: {
    autocalibration: false,
    autocalibration_keyword: 'FUZZ',
    autocalibration_per_host: false,
    autocalibration_strategies: ['basic'],
    autocalibration_strings: [],
    colors: false,
    config_file: '',
    delay: '',
    json: false,
    maxtime: 0,
    maxtime_job: 0,
    noninteractive: true,
    quiet: false,
    rate: 0,
    scraperfile: '',
    // Response scrapers require bodies and therefore prevent the qualified
    // HEAD path used by the high-throughput HTTP/3 profile. Users can still
    // select scrapers explicitly from the form or an imported CLI command.
    scrapers: '',
    stop_on_403: false,
    stop_on_all: false,
    stop_on_errors: false,
    threads: DEFAULT_WEB_THREAD_COUNT,
    verbose: false,
  },
  http: {
    cookies: [],
    data: '',
    follow_redirects: false,
    headers: [
      'User-Agent: Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/111.0.0.0 Safari/537.36',
    ],
    http3: false,
    http1: false,
    no_http3: false,
    http3_fingerprint: false,
    no_response_optimization: false,
    http3_connections: 0,
    http3_max_connections: 64,
    http3_max_workers: 65536,
    http3_streams: 256,
    http3_probe_timeout: 3,
    ignore_body: false,
    method: 'GET',
    proxy_url: '',
    raw: false,
    recursion: false,
    recursion_depth: 0,
    recursion_strategy: 'default',
    replay_proxy_url: '',
    sni: '',
    timeout: 10,
    url: '',
    http2: false,
    'client-cert': '',
    'client-key': '',
    preflights: [],
    postflights: [],
    preflight_mode: 'per-request',
    preflight_error: 'abort',
    preflight_anyhost: false,
  },
  input: {
    dirsearch_compat: true,
    encoders: [],
    extensions: 'html',
    ignore_wordlist_comments: false,
    input_mode: 'pitchfork',
    input_num: 100,
    input_shell: '',
    input_commands: [],
    request_file: '',
    request_proto: 'https',
    wordlists: ['final.txt'],
  },
  output: {
    audit_log: '',
    debug_log: '',
    output_directory: '',
    output_file: '',
    output_format: 'json',
    output_skip_empty: false,
    run_telemetry: '',
  },
};

/**
 * Resolve the concurrency limits that the UI configuration will actually
 * expose. This mirrors the runner contract: a fixed `-t` wins, otherwise the
 * worker supervisor is bounded by both its worker limit and stream admission.
 */
export function http3Capacity(
  config: ConfigOptions,
  automaticWorkers: boolean,
): HTTP3Capacity {
  const streamsPerConnection = Math.max(1, config.http.http3_streams);
  const fixedConnections = Math.max(0, config.http.http3_connections);
  const maximumConfiguredConnections = Math.max(1, config.http.http3_max_connections);
  const adaptiveConnections = fixedConnections === 0;
  const maximumConnections = adaptiveConnections
    ? maximumConfiguredConnections
    : fixedConnections;
  const initialConnections = adaptiveConnections
    ? Math.min(HTTP3_ADAPTIVE_INITIAL_CONNECTIONS, maximumConnections)
    : fixedConnections;
  const initialAdmission = initialConnections * streamsPerConnection;
  const maximumAdmission = maximumConnections * streamsPerConnection;
  const configuredWorkerLimit = automaticWorkers
    ? Math.max(1, config.http.http3_max_workers)
    : Math.max(1, config.general.threads);

  return {
    adaptiveConnections,
    automaticWorkers,
    initialConnections,
    maximumConnections,
    streamsPerConnection,
    initialAdmission,
    maximumAdmission,
    initialWorkers: Math.min(initialAdmission, configuredWorkerLimit),
    maximumWorkers: Math.min(maximumAdmission, configuredWorkerLimit),
  };
}

export const bundledProfiles: Profile[] = [
  {
    id: 'aspm',
    name: 'ASP Minimal',
    description: 'Focused ASP.NET endpoints with common frontend assets.',
    extensions: ['aspx', 'htm', 'html', 'js'],
  },
  {
    id: 'asp',
    name: 'ASP / ASP.NET',
    description: 'Broad classic ASP and ASP.NET endpoint coverage.',
    extensions: ['aspx', 'asmx', 'html', 'htm', 'js', 'ashx', 'asp', 'svc'],
  },
  {
    id: 'php',
    name: 'PHP',
    description: 'PHP applications and common static assets.',
    extensions: ['php', 'html', 'htm', 'js'],
  },
  {
    id: 'jsp',
    name: 'JSP / Java',
    description: 'JSP, servlet actions, and common static assets.',
    extensions: ['jsp', 'htm', 'html', 'do', 'action', 'js'],
  },
  {
    id: 'dir',
    name: 'Directories',
    description: 'Directory-oriented discovery with HTML variants.',
    extensions: ['html'],
  },
];

export function cloneConfig(config: ConfigOptions): ConfigOptions {
  return JSON.parse(JSON.stringify(config)) as ConfigOptions;
}

export function mergeConfig(base: ConfigOptions, patch?: Partial<ConfigOptions>): ConfigOptions {
  if (!patch) return cloneConfig(base);
  return {
    general: { ...base.general, ...patch.general },
    http: { ...base.http, ...patch.http },
    input: { ...base.input, ...patch.input },
    matchers: { ...base.matchers, ...patch.matchers },
    filters: { ...base.filters, ...patch.filters },
    output: { ...base.output, ...patch.output },
  };
}

export function applyProfile(config: ConfigOptions, profile: Profile): ConfigOptions {
  const next = mergeConfig(config, profile.options);
  next.input.extensions = profile.extensions.join(',');
  next.input.dirsearch_compat = true;
  return next;
}

export function profileName(id: ProfileID, profiles: Profile[]): string {
  return profiles.find((profile) => profile.id === id)?.name ?? id.toUpperCase();
}
