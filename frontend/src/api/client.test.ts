import { afterEach, describe, expect, it, vi } from 'vitest';
import { api } from './client';

afterEach(() => {
  vi.unstubAllGlobals();
});

describe('terminal REST hydration', () => {
  it('loads and normalizes persisted terminal entries with a cursor', async () => {
    const fetchMock = vi.fn().mockResolvedValue({
      ok: true,
      status: 200,
      json: async () => ({
        entries: [
          {
            id: 19,
            session_id: 'session/one',
            at: null,
            level: '',
            message: 'persisted output',
          },
        ],
        next_cursor: 19,
        has_more: false,
      }),
    });
    vi.stubGlobal('fetch', fetchMock);

    const page = await api.terminal('session/one', 8, 250);

    expect(fetchMock).toHaveBeenCalledWith(
      '/api/v1/sessions/session%2Fone/terminal?after_id=8&limit=250',
      expect.objectContaining({ headers: expect.objectContaining({ Accept: 'application/json' }) }),
    );
    expect(page).toEqual({
      entries: [
        {
          id: 19,
          session_id: 'session/one',
          at: '',
          level: 'info',
          message: 'persisted output',
        },
      ],
      next_cursor: 19,
      has_more: false,
    });
  });

  it('requests a stable backwards search page without an OFFSET', async () => {
    const fetchMock = vi.fn().mockResolvedValue({
      ok: true,
      status: 200,
      json: async () => ({
        entries: [], total: 30_000, offset: 29_000, limit: 1_000, has_more: true,
        levels: { info: 30_000 }, snapshot_id: 31_000, next_before_id: 30_001, has_older: true,
      }),
    });
    vi.stubGlobal('fetch', fetchMock);

    await api.searchTerminal('session/one', {
      query: 'timeout', levels: ['error'], latest: true, beforeID: 30_001, snapshotID: 31_000, limit: 1_000,
    });

    expect(fetchMock).toHaveBeenCalledWith(
      '/api/v1/sessions/session%2Fone/terminal/search?q=timeout&level=error&limit=1000&latest=true&before_id=30001&snapshot_id=31000',
      expect.any(Object),
    );
  });
});

describe('SQLite storage management', () => {
  it('loads storage statistics and sends explicit compaction confirmation', async () => {
    const stats = {
      database_bytes: 10,
      wal_bytes: 2,
      total_bytes: 12,
      reclaimable_bytes: 4,
      warning_threshold_bytes: 1_073_741_824,
      warning: false,
      response_bodies_stored: false,
    };
    const fetchMock = vi.fn()
      .mockResolvedValueOnce({ ok: true, status: 200, json: async () => stats })
      .mockResolvedValueOnce({ ok: true, status: 200, json: async () => stats });
    vi.stubGlobal('fetch', fetchMock);

    await expect(api.storage()).resolves.toEqual(stats);
    await expect(api.compactStorage()).resolves.toEqual(stats);
    expect(fetchMock).toHaveBeenNthCalledWith(1, '/api/v1/storage', expect.objectContaining({
      headers: expect.objectContaining({ Accept: 'application/json' }),
    }));
    expect(fetchMock).toHaveBeenNthCalledWith(2, '/api/v1/storage/compact', expect.objectContaining({
      method: 'POST',
      body: JSON.stringify({ confirm: true }),
    }));
  });
});

describe('bounded session comparison', () => {
  it('sends side and cursor paging options and normalizes change rows', async () => {
    const fetchMock = vi.fn().mockResolvedValue({
      ok: true,
      status: 200,
      json: async () => ({
        left_session_id: 'left', right_session_id: 'right',
        added: [{ id: 3, session_id: 'right', status: 200, url: 'https://target.example/new' }],
        removed: [], added_total: 301, removed_total: 0, unchanged_count: 8,
        added_has_more: true, removed_has_more: false, added_next_cursor: 'v1.next', limit: 200,
      }),
    });
    vi.stubGlobal('fetch', fetchMock);

    const page = await api.compareSessions('left/id', 'right/id', { side: 'added', cursor: 'v1.start', limit: 200 });

    expect(fetchMock).toHaveBeenCalledWith(
      '/api/v1/sessions/left%2Fid/compare/right%2Fid?side=added&cursor=v1.start&limit=200',
      expect.any(Object),
    );
    expect(page).toMatchObject({ added_total: 301, added: [{ id: 3, status: 200 }] });
  });
});

describe('server-side result browsing', () => {
  it('sends search, status, and backward cursor filters without losing result metadata', async () => {
    const fetchMock = vi.fn().mockResolvedValue({
      ok: true,
      status: 200,
      json: async () => ({
        results: [{
          id: 1,
          session_id: 'session/one',
          input: { FUZZ: 'admin' },
          status: 200,
          url: 'https://target.example/admin',
        }],
        next_cursor: 1,
        has_more: false,
        total: 1,
        status_counts: { '200': 1, '404': 30_000 },
      }),
    });
    vi.stubGlobal('fetch', fetchMock);

    const page = await api.queryResults('session/one', {
      beforeID: 30_001,
      limit: 1000,
      query: 'admin',
      statuses: [200, 302],
    });

    expect(fetchMock).toHaveBeenCalledWith(
      '/api/v1/sessions/session%2Fone/results?before_id=30001&limit=1000&q=admin&status=200&status=302',
      expect.objectContaining({ headers: expect.objectContaining({ Accept: 'application/json' }) }),
    );
    expect(page.total).toBe(1);
    expect(page.status_counts?.['404']).toBe(30_000);
    expect(page.results[0]).toMatchObject({ id: 1, status: 200 });
  });

  it('loads a bounded tree level with the same filters and stable snapshot', async () => {
    const fetchMock = vi.fn().mockResolvedValue({
      ok: true,
      status: 200,
      json: async () => ({
        nodes: [{
          key: 'child-key', name: 'admin', url: 'https://target.example/admin/', uri: '/admin/',
          kind: 'directory', result_count: 30_001, has_children: true,
        }],
        total: 1, offset: 0, limit: 1000, has_more: false, snapshot_id: 31_337,
      }),
    });
    vi.stubGlobal('fetch', fetchMock);

    const page = await api.exploreResultTree('session/one', {
      parent: 'parent/key', snapshotID: 31_337, statuses: [200], query: 'admin', offset: 0, limit: 1_000,
    });

    expect(fetchMock).toHaveBeenCalledWith(
      '/api/v1/sessions/session%2Fone/results/tree?q=admin&status=200&offset=0&limit=1000&parent=parent%2Fkey&snapshot_id=31337',
      expect.objectContaining({ headers: expect.objectContaining({ Accept: 'application/json' }) }),
    );
    expect(page).toMatchObject({ total: 1, snapshot_id: 31_337, nodes: [{ key: 'child-key', result_count: 30_001 }] });
  });
});

describe('live session filters', () => {
  it('patches sparse additive filters and normalizes the updated session', async () => {
    const fetchMock = vi.fn().mockResolvedValue({
      ok: true,
      status: 200,
      json: async () => ({
        id: 'session/one',
        name: 'Target',
        target: 'https://target.example/FUZZ',
        status: 'running',
        profile: 'dir',
        wordlist: 'final.txt',
        options: { filters: { mode: 'or', words: '656', status: '404' } },
        progress: {},
        result_count: 1,
        sequence: 9,
        created_at: '2026-10-04T00:00:00Z',
        updated_at: '2026-10-04T00:00:01Z',
      }),
    });
    vi.stubGlobal('fetch', fetchMock);

    const updated = await api.addSessionFilters('session/one', { words: '656' });

    expect(fetchMock).toHaveBeenCalledWith(
      '/api/v1/sessions/session%2Fone/filters',
      expect.objectContaining({
        method: 'PATCH',
        body: JSON.stringify({ words: '656' }),
      }),
    );
    expect(updated.options.filters.words).toBe('656');
    expect(updated.sequence).toBe(9);
  });
});

describe('bounded export preview', () => {
  it('cancels the response stream at the preview cap', async () => {
    const cancel = vi.fn().mockResolvedValue(undefined);
    const read = vi.fn().mockResolvedValue({
      done: false,
      value: new TextEncoder().encode('abcdef'),
    });
    const fetchMock = vi.fn().mockResolvedValue({
      ok: true,
      status: 200,
      body: { getReader: () => ({ read, cancel }) },
      headers: { get: (name: string) => (name === 'content-length' ? '6' : null) },
    });
    vi.stubGlobal('fetch', fetchMock);

    const preview = await api.exportPreview('session-1', 'json', 4);

    expect(preview).toEqual({
      content: 'abcd',
      captured_bytes: 4,
      total_bytes: 6,
      truncated: true,
    });
    expect(cancel).toHaveBeenCalledOnce();
    expect(fetchMock).toHaveBeenCalledWith(
      '/api/v1/sessions/session-1/export?format=json&preview=true&limit=200',
      expect.objectContaining({ headers: expect.objectContaining({ Accept: 'text/plain, application/json' }) }),
    );
  });
});

describe('protocol preflight action', () => {
  it('posts the rerun action and normalizes the accepted session', async () => {
    const fetchMock = vi.fn().mockResolvedValue({
      ok: true,
      status: 202,
      json: async () => ({
        id: 'session/one',
        name: 'Target',
        target: 'https://target.example/FUZZ',
        status: 'running',
        profile: 'dir',
        wordlist: 'final.txt',
        options: {},
        explicit_flags: [],
        progress: { req_count: 4, req_sec: 2 },
        result_count: 0,
        preflight_report: { state: 'running' },
        created_at: '2026-10-04T00:00:00Z',
        updated_at: '2026-10-04T00:00:01Z',
      }),
    });
    vi.stubGlobal('fetch', fetchMock);

    const session = await api.action('session/one', 'preflight');

    expect(fetchMock).toHaveBeenCalledWith(
      '/api/v1/sessions/session%2Fone/preflight',
      expect.objectContaining({ method: 'POST' }),
    );
    expect(session.preflight_report?.state).toBe('running');
    expect(session.progress.current_rate).toBe(2);
  });
});
