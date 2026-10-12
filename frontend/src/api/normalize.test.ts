import { describe, expect, it } from 'vitest';
import type { Session, Settings } from '../types/api';
import { normalizeSession, normalizeSettings, normalizeTerminalPayload } from './normalize';

const entry = {
  id: 17,
  session_id: 'session-1',
  at: '2026-10-03T00:00:00Z',
  level: 'info',
  message: 'scan started',
};

describe('normalizeTerminalPayload', () => {
  it('accepts the persisted WebSocket terminal batch shape', () => {
    expect(normalizeTerminalPayload({ entries: [entry], next_cursor: 17 })).toEqual([entry]);
  });

  it('accepts a bare batch, single entry, and entry wrapper', () => {
    expect(normalizeTerminalPayload([entry])).toEqual([entry]);
    expect(normalizeTerminalPayload(entry)).toEqual([entry]);
    expect(normalizeTerminalPayload({ entry })).toEqual([entry]);
  });

  it('ignores malformed terminal event data', () => {
    expect(normalizeTerminalPayload(undefined)).toEqual([]);
    expect(normalizeTerminalPayload({ entries: null })).toEqual([]);
  });
});

describe('normalizeSettings', () => {
  it('uses automatic worker sizing when an older server omits explicit flags', () => {
    const settings = normalizeSettings({
      defaults: {
        wordlist: 'final.txt',
        profile: 'aspm',
        options: {},
      },
    } as unknown as Settings);

    expect(settings.defaults.explicit_flags).not.toContain('t');
    expect(settings.defaults.options.general.rate).toBe(0);
    expect(settings.defaults.options.general.scrapers).toBe('');
    expect(settings.defaults.options.http).toMatchObject({
      http3: false,
      http1: false,
      no_http3: false,
      http3_connections: 0,
      http3_max_connections: 64,
      http3_max_workers: 65_536,
      http3_streams: 256,
    });
  });

  it('preserves an intentional explicit fixed thread setting', () => {
    const settings = normalizeSettings({
      defaults: {
        wordlist: 'final.txt',
        profile: 'aspm',
        options: {},
        explicit_flags: ['t'],
      },
    } as unknown as Settings);

    expect(settings.defaults.explicit_flags).toEqual(['t']);
  });
});

describe('normalizeSession telemetry', () => {
  it('preserves stable telemetry and falls back to legacy counters without dropping either contract', () => {
    const session = normalizeSession({
      id: 'session-1',
      name: 'Target',
      target: 'https://target.example/FUZZ',
      status: 'running',
      profile: 'dir',
      wordlist: 'final.txt',
      options: {},
      explicit_flags: [],
      progress: {
        req_count: 17,
        request_count: 2_001,
        completed_count: 0,
        req_total: 100,
        req_sec: 8,
        queue_pos: 2,
        queue_total: 4,
        error_count: 1,
        response_count: 15,
        average_30s: 7.5,
        overall_average: 6.25,
        peak_rate: 12,
        eta_seconds: 9,
      },
      error_diagnostics: [{ category: 'timeout', count: 1, samples: ['deadline'], suggestion: 'Increase timeout.' }],
      result_count: 0,
      created_at: '2026-10-04T00:00:00Z',
      updated_at: '2026-10-04T00:00:01Z',
    } as unknown as Session);

    expect(session.progress).toMatchObject({
      req_count: 17,
      req_sec: 8,
      request_count: 2_001,
      completed_count: 0,
      current_rate: 8,
      response_count: 15,
      average_30s: 7.5,
      overall_average: 6.25,
      peak_rate: 12,
      eta_seconds: 9,
    });
    expect(session.error_diagnostics).toHaveLength(1);
  });

  it('derives completed work for sessions returned by legacy servers', () => {
    const base = {
      id: 'legacy-session',
      name: 'Legacy target',
      target: 'https://target.example/FUZZ',
      status: 'completed',
      profile: 'dir',
      wordlist: 'final.txt',
      options: {},
      explicit_flags: [],
      result_count: 0,
      created_at: '2026-10-04T00:00:00Z',
      updated_at: '2026-10-04T00:00:01Z',
    };

    const withOutcomes = normalizeSession({
      ...base,
      progress: {
        req_count: 2_001,
        req_total: 3_000,
        req_sec: 0,
        queue_pos: 1,
        queue_total: 1,
        response_count: 70,
        error_count: 3,
      },
    } as unknown as Session);
    expect(withOutcomes.progress.request_count).toBe(2_001);
    expect(withOutcomes.progress.completed_count).toBe(73);

    const oldest = normalizeSession({
      ...base,
      progress: {
        req_count: 99,
        req_total: 99,
        req_sec: 0,
        queue_pos: 1,
        queue_total: 1,
        error_count: 0,
      },
    } as unknown as Session);
    expect(oldest.progress.completed_count).toBe(99);
  });
});
