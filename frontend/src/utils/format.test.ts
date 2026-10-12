import { describe, expect, it } from 'vitest';
import { formatBytes, formatDuration, progressPercent, targetLabel } from './format';

describe('operator formatting', () => {
  it('clamps progress for incomplete and over-complete counters', () => {
    expect(progressPercent(50, 200)).toBe(25);
    expect(progressPercent(1, 0)).toBe(0);
    expect(progressPercent(220, 200)).toBe(100);
  });

  it('keeps target labels concise without losing the host', () => {
    expect(targetLabel('https://app.example.test/admin/FUZZ')).toBe('app.example.test/admin/');
  });

  it('formats response units for scan tables', () => {
    expect(formatBytes(512)).toBe('512 B');
    expect(formatBytes(2048)).toBe('2.0 KB');
    expect(formatDuration(15_500_000)).toBe('16 ms');
  });
});

