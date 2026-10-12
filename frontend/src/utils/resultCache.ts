import type { Result } from '../types/api';

export const RESULT_CACHE_LIMIT = 25_000;

function isStrictlyNewerAscendingBatch(current: Result[], incoming: Result[]): boolean {
  let previousID = current.at(-1)?.id ?? Number.NEGATIVE_INFINITY;
  for (const result of incoming) {
    if (result.id <= previousID) return false;
    previousID = result.id;
  }
  return true;
}

/**
 * Merge persisted and streamed results while retaining a bounded, ascending cache.
 * WebSocket batches normally append newer IDs, so keep that hot path linear and
 * avoid rebuilding and sorting a 25k-entry map for every message.
 */
export function mergeResults(
  current: Result[],
  incoming: Result[],
  limit = RESULT_CACHE_LIMIT,
): Result[] {
  if (incoming.length === 0) return current;
  if (limit <= 0) return [];

  if (isStrictlyNewerAscendingBatch(current, incoming)) {
    const overflow = current.length + incoming.length - limit;
    if (overflow <= 0) return [...current, ...incoming];
    if (overflow >= current.length) return incoming.slice(-limit);
    return [...current.slice(overflow), ...incoming];
  }

  const merged = new Map(current.map((result) => [result.id, result]));
  incoming.forEach((result) => merged.set(result.id, result));
  return [...merged.values()].sort((a, b) => a.id - b.id).slice(-limit);
}
