import { describe, expect, it } from 'vitest';
import { appendRatePoint, RATE_HISTORY_WINDOW_MS } from './rateHistory';

describe('appendRatePoint', () => {
  it('keeps a true rolling sixty-second window rather than a sample count', () => {
    const now = 1_000_000;
    const points = [
      { at: now - RATE_HISTORY_WINDOW_MS - 1, value: 1 },
      { at: now - RATE_HISTORY_WINDOW_MS, value: 2 },
      { at: now - 1, value: 3 },
    ];

    expect(appendRatePoint(points, 4, now)).toEqual([
      { at: now - RATE_HISTORY_WINDOW_MS, value: 2 },
      { at: now - 1, value: 3 },
      { at: now, value: 4 },
    ]);
  });

  it('drops future points when the local clock moves backwards', () => {
    expect(appendRatePoint([{ at: 2_000, value: 1 }], 2, 1_000)).toEqual([
      { at: 1_000, value: 2 },
    ]);
  });
});
