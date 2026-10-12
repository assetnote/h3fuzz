export interface RatePoint {
  at: number;
  value: number;
}

export const RATE_HISTORY_WINDOW_MS = 60_000;
const RATE_HISTORY_SAFETY_LIMIT = 6_000;

export function appendRatePoint(
  current: RatePoint[],
  value: number,
  at = Date.now(),
): RatePoint[] {
  const windowStart = at - RATE_HISTORY_WINDOW_MS;
  const retained = current.filter((point) => point.at >= windowStart && point.at <= at);
  return [...retained, { at, value }].slice(-RATE_HISTORY_SAFETY_LIMIT);
}
