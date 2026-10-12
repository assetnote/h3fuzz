import { describe, expect, it } from 'vitest';
import type { Result } from '../types/api';
import { mergeResults } from './resultCache';

function result(id: number, url = `https://example.test/${id}`): Result {
  return {
    id,
    session_id: 'session',
    url,
    position: id,
    status: 200,
    length: 10,
    words: 1,
    lines: 1,
    content_type: 'text/html',
    duration_ns: 1_000_000,
    input: {},
    host: 'example.test',
    created_at: '2026-01-01T00:00:00Z',
  };
}

describe('mergeResults', () => {
  it('appends a strictly newer batch in order', () => {
    const current = [result(1), result(2)];
    expect(mergeResults(current, [result(3), result(4)], 10).map(({ id }) => id)).toEqual([1, 2, 3, 4]);
  });

  it('retains only the newest entries when an append exceeds the cache limit', () => {
    expect(mergeResults([result(1), result(2), result(3)], [result(4), result(5)], 3).map(({ id }) => id)).toEqual([3, 4, 5]);
  });

  it('handles an incoming batch larger than the cache without copying stale rows', () => {
    expect(mergeResults([result(1)], [result(2), result(3), result(4)], 2).map(({ id }) => id)).toEqual([3, 4]);
  });

  it('falls back to deduplication and sorting for replayed or out-of-order rows', () => {
    const replacement = result(2, 'https://example.test/replaced');
    const merged = mergeResults([result(1), result(2)], [result(4), replacement, result(3)], 10);
    expect(merged.map(({ id }) => id)).toEqual([1, 2, 3, 4]);
    expect(merged[1].url).toBe(replacement.url);
  });

  it('returns the existing reference for an empty batch and honors an empty cache', () => {
    const current = [result(1)];
    expect(mergeResults(current, [])).toBe(current);
    expect(mergeResults(current, [result(2)], 0)).toEqual([]);
  });
});
