import type {
  BulkSessionResult,
  CreateSessionRequest,
  OptionsMetadata,
  Preset,
  Profile,
  Result,
  ResultExplorerPage,
  ResultFilterViewQuery,
  ResultTreePage,
  SavedResultView,
  Session,
  SessionComparison,
  SessionFilterPatch,
  SessionOrganization,
  Settings,
  StorageStats,
  TerminalEntry,
  TerminalSearchPage,
  WordlistInspection,
} from '../types/api';
import {
  normalizeOptionsMetadata,
  normalizeProfile,
  normalizeResult,
  normalizeSession,
  normalizeSettings,
  normalizeTerminalPayload,
} from './normalize';

const API_BASE = '/api/v1';
export const EXPORT_PREVIEW_LIMIT = 256 * 1024;
const EXPORT_PREVIEW_RECORD_LIMIT = 200;

export class APIError extends Error {
  readonly status: number;
  readonly code?: string;

  constructor(message: string, status: number, code?: string) {
    super(message);
    this.name = 'APIError';
    this.status = status;
    this.code = code;
  }
}

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const response = await fetch(`${API_BASE}${path}`, {
    ...init,
    headers: {
      Accept: 'application/json',
      ...(init?.body ? { 'Content-Type': 'application/json' } : {}),
      ...init?.headers,
    },
  });

  if (!response.ok) {
    let message = `Request failed with status ${response.status}`;
    let code: string | undefined;
    try {
      const body = (await response.json()) as { error?: { code?: string; message?: string } };
      message = body.error?.message ?? message;
      code = body.error?.code;
    } catch {
      // Preserve the HTTP fallback when an intermediary returns non-JSON.
    }
    throw new APIError(message, response.status, code);
  }

  if (response.status === 204) return undefined as T;
  return (await response.json()) as T;
}

export interface ResultsPage {
  results: Result[];
  next_cursor: number;
  has_more: boolean;
  window_truncated?: boolean;
  total?: number;
  status_counts?: Record<string, number>;
}

export interface ResultQueryOptions {
  afterID?: number;
  beforeID?: number;
  limit?: number;
  latest?: boolean;
  query?: string;
  statuses?: number[];
}

export interface ResultExplorerOptions extends ResultFilterViewQuery {
  offset?: number;
  limit?: number;
}

export interface ResultTreeOptions extends ResultExplorerOptions {
  parent?: string;
  snapshotID?: number;
}

export interface TerminalSearchOptions {
  query?: string;
  levels?: string[];
  offset?: number;
  limit?: number;
  latest?: boolean;
  beforeID?: number;
  snapshotID?: number;
}

export interface SessionComparisonOptions {
  side?: 'both' | 'added' | 'removed';
  cursor?: string;
  limit?: number;
}

export interface TerminalPage {
  entries: TerminalEntry[];
  next_cursor: number;
  has_more: boolean;
}

export interface ExportPreview {
  content: string;
  captured_bytes: number;
  total_bytes?: number;
  truncated: boolean;
}

async function exportPreview(
  id: string,
  format: 'json' | 'text' | 'terminal',
  maxBytes = EXPORT_PREVIEW_LIMIT,
  signal?: AbortSignal,
): Promise<ExportPreview> {
  const query = new URLSearchParams({
    format,
    preview: 'true',
    limit: String(EXPORT_PREVIEW_RECORD_LIMIT),
  });
  const url = `${API_BASE}/sessions/${encodeURIComponent(id)}/export?${query}`;
  const response = await fetch(url, {
    credentials: 'same-origin',
    signal,
    headers: { Accept: 'text/plain, application/json' },
  });
  if (!response.ok) {
    throw new APIError(`Export preview failed with status ${response.status}`, response.status);
  }
  if (!response.body) {
    throw new APIError('Streaming preview is not supported by this browser. Use Download instead.', 0);
  }

  const reader = response.body.getReader();
  const chunks: Uint8Array[] = [];
  let captured = 0;
  let finished = false;
  while (captured < maxBytes) {
    const { done, value } = await reader.read();
    if (done) {
      finished = true;
      break;
    }
    if (!value?.byteLength) continue;
    const remaining = maxBytes - captured;
    const chunk = value.byteLength > remaining ? value.subarray(0, remaining) : value;
    chunks.push(chunk);
    captured += chunk.byteLength;
    if (chunk.byteLength < value.byteLength) break;
  }
  if (!finished) await reader.cancel();

  const bytes = new Uint8Array(captured);
  let offset = 0;
  chunks.forEach((chunk) => {
    bytes.set(chunk, offset);
    offset += chunk.byteLength;
  });
  const contentRange = response.headers.get('content-range');
  const rangeTotal = contentRange?.match(/\/(\d+)$/)?.[1];
  const contentLength = response.headers.get('content-length');
  const serverTruncated = response.headers.get('x-h3fuzz-preview-truncated') === 'true';
  const totalBytes = serverTruncated ? undefined : Number(rangeTotal ?? contentLength) || undefined;
  return {
    content: new TextDecoder().decode(bytes),
    captured_bytes: captured,
    total_bytes: totalBytes,
    truncated: serverTruncated || (totalBytes !== undefined ? totalBytes > captured : !finished),
  };
}

function queryResults(id: string, options: ResultQueryOptions = {}): Promise<ResultsPage> {
  const query = new URLSearchParams();
  if (options.afterID !== undefined) query.set('after_id', String(options.afterID));
  if (options.beforeID !== undefined) query.set('before_id', String(options.beforeID));
  if (options.limit !== undefined) query.set('limit', String(options.limit));
  if (options.latest) query.set('latest', 'true');
  if (options.query) query.set('q', options.query);
  options.statuses?.forEach((status) => query.append('status', String(status)));
  return request<ResultsPage>(`/sessions/${encodeURIComponent(id)}/results?${query}`).then((page) => ({
    ...page,
    results: (page.results ?? []).map(normalizeResult),
  }));
}

function resultExplorerQuery(options: ResultExplorerOptions): URLSearchParams {
  const query = new URLSearchParams();
  if (options.query) query.set('q', options.query);
  options.statuses?.forEach((status) => query.append('status', String(status)));
  options.contentTypes?.forEach((contentType) => query.append('content_type', contentType));
  if (options.sizeMin !== undefined) query.set('size_min', String(options.sizeMin));
  if (options.sizeMax !== undefined) query.set('size_max', String(options.sizeMax));
  if (options.wordsMin !== undefined) query.set('words_min', String(options.wordsMin));
  if (options.wordsMax !== undefined) query.set('words_max', String(options.wordsMax));
  if (options.linesMin !== undefined) query.set('lines_min', String(options.linesMin));
  if (options.linesMax !== undefined) query.set('lines_max', String(options.linesMax));
  if (options.sort) query.set('sort', options.sort);
  if (options.order) query.set('order', options.order);
  if (options.groupDuplicates) query.set('group_duplicates', 'true');
  if (options.offset !== undefined) query.set('offset', String(options.offset));
  if (options.limit !== undefined) query.set('limit', String(options.limit));
  return query;
}

function exploreResults(id: string, options: ResultExplorerOptions = {}): Promise<ResultExplorerPage> {
  const query = resultExplorerQuery(options);
  return request<ResultExplorerPage>(`/sessions/${encodeURIComponent(id)}/results/explore?${query}`).then((page) => ({
    ...page,
    results: (page.results ?? []).map(normalizeResult),
  }));
}

function exploreResultTree(id: string, options: ResultTreeOptions = {}): Promise<ResultTreePage> {
  const query = resultExplorerQuery(options);
  if (options.parent) query.set('parent', options.parent);
  if (options.snapshotID !== undefined) query.set('snapshot_id', String(options.snapshotID));
  return request<ResultTreePage>(`/sessions/${encodeURIComponent(id)}/results/tree?${query}`).then((page) => ({
    ...page,
    nodes: (page.nodes ?? []).map((node) => ({
      ...node,
      result: node.result ? normalizeResult(node.result) : undefined,
    })),
  }));
}

function searchTerminal(id: string, options: TerminalSearchOptions = {}): Promise<TerminalSearchPage> {
  const query = new URLSearchParams();
  if (options.query) query.set('q', options.query);
  options.levels?.forEach((level) => query.append('level', level));
  if (options.offset !== undefined) query.set('offset', String(options.offset));
  if (options.limit !== undefined) query.set('limit', String(options.limit));
  if (options.latest !== undefined) query.set('latest', String(options.latest));
  if (options.beforeID !== undefined) query.set('before_id', String(options.beforeID));
  if (options.snapshotID !== undefined) query.set('snapshot_id', String(options.snapshotID));
  return request<TerminalSearchPage>(`/sessions/${encodeURIComponent(id)}/terminal/search?${query}`).then((page) => ({
    ...page,
    entries: normalizeTerminalPayload(page),
  }));
}

export const api = {
  sessions: async () => {
    const payload = await request<Session[] | { sessions: Session[] }>('/sessions');
    return (Array.isArray(payload) ? payload : payload.sessions).map(normalizeSession);
  },
  session: async (id: string) => normalizeSession(await request<Session>(`/sessions/${encodeURIComponent(id)}`)),
  createSession: async (body: CreateSessionRequest) =>
    normalizeSession(await request<Session>('/sessions', { method: 'POST', body: JSON.stringify(body) })),
  updateSession: async (id: string, body: Partial<Pick<Session, 'name'>>) =>
    normalizeSession(await request<Session>(`/sessions/${encodeURIComponent(id)}`, {
      method: 'PATCH',
      body: JSON.stringify(body),
    })),
  deleteSession: (id: string) =>
    request<void>(`/sessions/${encodeURIComponent(id)}`, { method: 'DELETE' }),
  action: async (id: string, action: 'start' | 'pause' | 'resume' | 'stop' | 'preflight') =>
    normalizeSession(await request<Session>(`/sessions/${encodeURIComponent(id)}/${action}`, { method: 'POST' })),
  addSessionFilters: async (id: string, filters: SessionFilterPatch) =>
    normalizeSession(await request<Session>(`/sessions/${encodeURIComponent(id)}/filters`, {
      method: 'PATCH',
      body: JSON.stringify(filters),
    })),
  queryResults,
  results: (id: string, afterID = 0, limit = 500) =>
    queryResults(id, { afterID, limit }),
  latestResults: (id: string, limit = 25_000) =>
    queryResults(id, { latest: true, limit }),
  exploreResults,
  exploreResultTree,
  resultViews: (id: string) =>
    request<SavedResultView[]>(`/sessions/${encodeURIComponent(id)}/result-views`),
  saveResultView: (id: string, name: string, query: ResultFilterViewQuery) =>
    request<SavedResultView>(`/sessions/${encodeURIComponent(id)}/result-views`, {
      method: 'POST',
      body: JSON.stringify({ name, query }),
    }),
  deleteResultView: (id: string, viewID: string) =>
    request<void>(`/sessions/${encodeURIComponent(id)}/result-views/${encodeURIComponent(viewID)}`, {
      method: 'DELETE',
    }),
  terminal: (id: string, afterID = 0, limit = 500) => {
    const query = new URLSearchParams({ after_id: String(afterID), limit: String(limit) });
    return request<TerminalPage | TerminalEntry[] | TerminalEntry>(
      `/sessions/${encodeURIComponent(id)}/terminal?${query}`,
    ).then((payload) => {
      const entries = normalizeTerminalPayload(payload);
      if (Array.isArray(payload) || !('entries' in payload)) {
        return {
          entries,
          next_cursor: entries.at(-1)?.id ?? afterID,
          has_more: false,
        };
      }
      return {
        ...payload,
        entries,
        next_cursor: payload.next_cursor ?? entries.at(-1)?.id ?? afterID,
        has_more: payload.has_more ?? false,
      };
    });
  },
  searchTerminal,
  sessionOrganization: (id: string) =>
    request<SessionOrganization>(`/sessions/${encodeURIComponent(id)}/organization`),
  updateSessionOrganization: (id: string, body: Partial<Pick<SessionOrganization, 'pinned' | 'archived' | 'group_name'>>) =>
    request<SessionOrganization>(`/sessions/${encodeURIComponent(id)}/organization`, {
      method: 'PATCH', body: JSON.stringify(body),
    }),
  bulkSessions: (ids: string[], action: 'stop' | 'delete' | 'archive') =>
    request<BulkSessionResult>('/sessions/bulk', { method: 'POST', body: JSON.stringify({ ids, action }) }),
  cloneSession: async (id: string, body: Partial<CreateSessionRequest> = {}) =>
    normalizeSession(await request<Session>(`/sessions/${encodeURIComponent(id)}/clone`, {
      method: 'POST', body: JSON.stringify(body),
    })),
  compareSessions: (leftID: string, rightID: string, options: SessionComparisonOptions = {}) => {
    const query = new URLSearchParams();
    if (options.side) query.set('side', options.side);
    if (options.cursor) query.set('cursor', options.cursor);
    if (options.limit !== undefined) query.set('limit', String(options.limit));
    const suffix = query.size ? `?${query}` : '';
    return request<SessionComparison>(`/sessions/${encodeURIComponent(leftID)}/compare/${encodeURIComponent(rightID)}${suffix}`)
      .then((comparison) => ({
        ...comparison,
        added: (comparison.added ?? []).map(normalizeResult),
        removed: (comparison.removed ?? []).map(normalizeResult),
      }));
  },
  presets: () => request<Preset[]>('/presets'),
  createPreset: (preset: Omit<Preset, 'id' | 'created_at' | 'updated_at'>) =>
    request<Preset>('/presets', { method: 'POST', body: JSON.stringify(preset) }),
  updatePreset: (preset: Preset) =>
    request<Preset>(`/presets/${encodeURIComponent(preset.id)}`, { method: 'PUT', body: JSON.stringify(preset) }),
  deletePreset: (id: string) => request<void>(`/presets/${encodeURIComponent(id)}`, { method: 'DELETE' }),
  inspectWordlist: (path: string) =>
    request<WordlistInspection>('/wordlists/inspect', { method: 'POST', body: JSON.stringify({ path }) }),
  settings: async () => normalizeSettings(await request<Settings>('/settings')),
  saveSettings: async (settings: Settings) =>
    normalizeSettings(await request<Settings>('/settings', { method: 'PUT', body: JSON.stringify(settings) })),
  storage: () => request<StorageStats>('/storage'),
  compactStorage: () => request<StorageStats>('/storage/compact', {
    method: 'POST',
    body: JSON.stringify({ confirm: true }),
  }),
  profiles: async () => {
    const payload = await request<Profile[] | { profiles: Profile[] }>('/profiles');
    return (Array.isArray(payload) ? payload : payload.profiles).map(normalizeProfile);
  },
  options: async () => normalizeOptionsMetadata(await request<OptionsMetadata>('/options')),
  exportPreview,
  exportURL: (id: string, format: 'json' | 'text' | 'terminal') =>
    `${API_BASE}/sessions/${encodeURIComponent(id)}/export?format=${format}`,
};

export function websocketURL(): string {
  const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
  return `${protocol}//${window.location.host}${API_BASE}/ws`;
}
