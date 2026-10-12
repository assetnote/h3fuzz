import { describe, expect, it } from 'vitest';
import type { Session } from '../types/api';
import { cloneConfig, defaultConfig } from './config';
import {
  advanceSessionSequence,
  mergeSessionDelta,
  mergeSessionUpdate,
  reconcileSessionList,
  upsertSession,
} from './sessionState';

function session(status: Session['status']): Session {
  return {
    id: 'session-1',
    name: 'Target',
    target: 'https://target.example/FUZZ',
    status,
    profile: 'dir',
    wordlist: 'final.txt',
    options: cloneConfig(defaultConfig),
    explicit_flags: [],
    progress: {
      req_count: 10,
      req_total: 100,
      req_sec: 20,
      queue_pos: 10,
      queue_total: 100,
      error_count: 0,
    },
    result_count: 2,
    created_at: '2026-10-03T00:00:00Z',
    updated_at: '2026-10-03T00:00:00Z',
  };
}

describe('mergeSessionUpdate', () => {
  it('moves immediately from the stop action response to the authoritative stopped event', () => {
    const running = session('running');
    const stopping = mergeSessionUpdate(running, { ...running, status: 'stopping' });
    const stopped = mergeSessionUpdate(stopping, { ...stopping, status: 'stopped' });

    expect(stopping.status).toBe('stopping');
    expect(stopped.status).toBe('stopped');
  });

  it('does not let a delayed stop response overwrite the final WebSocket state', () => {
    const running = { ...session('running'), sequence: 10 };
    const stopped = mergeSessionUpdate(running, { ...running, status: 'stopped', sequence: 12 });
    const staleStopping = mergeSessionUpdate(stopped, { ...running, status: 'stopping', sequence: 11 });

    expect(staleStopping).toBe(stopped);
    expect(staleStopping.status).toBe('stopped');
    expect(staleStopping.sequence).toBe(12);
  });

  it('applies an optimistic unsequenced stopping patch over a known sequence', () => {
    const running = { ...session('running'), sequence: 10 };
    const stopping = mergeSessionUpdate(running, { status: 'stopping' });

    expect(stopping.status).toBe('stopping');
    expect(stopping.sequence).toBe(10);
  });

  it('accepts equal and newer authoritative snapshots', () => {
    const running = { ...session('running'), sequence: 10 };
    const equal = mergeSessionUpdate(running, { ...running, status: 'stopping', sequence: 10 });
    const newer = mergeSessionUpdate(equal, { ...equal, status: 'stopped', sequence: 11 });

    expect(equal.status).toBe('stopping');
    expect(newer.status).toBe('stopped');
    expect(newer.sequence).toBe(11);
  });

  it('merges unsequenced progress but rejects an unsequenced legacy session over known state', () => {
    const stopped = { ...session('stopped'), sequence: 12 };
    const withProgress = mergeSessionUpdate(stopped, { req_count: 11, req_sec: 3 });
    const legacyRunning = mergeSessionUpdate(withProgress, { ...session('running'), sequence: undefined });
    const zeroSequenceRunning = mergeSessionUpdate(withProgress, { ...session('running'), sequence: 0 });

    expect(withProgress.progress.req_count).toBe(11);
    expect(withProgress.progress.req_sec).toBe(3);
    expect(legacyRunning).toBe(withProgress);
    expect(legacyRunning.status).toBe('stopped');
    expect(zeroSequenceRunning).toBe(withProgress);
  });

  it('advances progress with the WebSocket envelope sequence', () => {
    const running = { ...session('running'), sequence: 10 };
    const progressed = mergeSessionUpdate(running, { req_count: 20, req_sec: 50 }, 11);
    const stale = mergeSessionUpdate(progressed, { req_count: 1, req_sec: 0 }, 9);

    expect(progressed.sequence).toBe(11);
    expect(progressed.progress.req_count).toBe(20);
    expect(stale).toBe(progressed);
  });

  it('upserts a new session but rejects an older snapshot for an existing session', () => {
    const running = { ...session('running'), sequence: 3 };
    const inserted = upsertSession([], running, 3);
    const stale = upsertSession(inserted, { ...session('starting'), sequence: 2 });

    expect(inserted).toHaveLength(1);
    expect(stale).toBe(inserted);
    expect(stale[0].status).toBe('running');
  });

  it('reconciles a stale list without regressing or dropping newer cached sessions', () => {
    const running = { ...session('running'), sequence: 3 };
    const localOnly = { ...session('paused'), id: 'session-2', sequence: 4 };
    const incoming = [{ ...session('starting'), sequence: 2 }];

    const reconciled = reconcileSessionList([running, localOnly], incoming);

    expect(reconciled.map((entry) => [entry.id, entry.status, entry.sequence])).toEqual([
      ['session-1', 'running', 3],
      ['session-2', 'paused', 4],
    ]);
  });

  it('lets an authoritative stream snapshot remove a session absent from the snapshot', () => {
    const running = { ...session('running'), sequence: 3 };
    const deleted = { ...session('paused'), id: 'session-2', sequence: 4 };

    const reconciled = reconcileSessionList([running, deleted], [running], false);

    expect(reconciled.map((entry) => entry.id)).toEqual(['session-1']);
  });

  it('requires a strictly newer sequence before applying a result delta', () => {
    const previous = { ...session('running'), sequence: 5, result_count: 2 };
    const duplicate = mergeSessionDelta(previous, { result_count: 3 }, 5);
    const advanced = mergeSessionDelta(previous, { result_count: 3 }, 6);

    expect(duplicate.result_count).toBe(2);
    expect(advanced.result_count).toBe(3);
    expect(advanceSessionSequence(advanced, 7).sequence).toBe(7);
  });
});
