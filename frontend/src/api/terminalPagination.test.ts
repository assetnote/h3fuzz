import { describe, expect, it, vi } from 'vitest';
import type { TerminalEntry } from '../types/api';
import { collectTerminalPages } from './terminalPagination';

describe('collectTerminalPages', () => {
  it('retains the final completion entry without a page-count ceiling', async () => {
    const all: TerminalEntry[] = Array.from({ length: 12_002 }, (_, index) => ({
      id: index + 1,
      at: `2026-10-03T00:00:${String(index % 60).padStart(2, '0')}Z`,
      level: index === 12_001 ? 'info' : 'result',
      message: index === 12_001 ? 'scan completed' : `result ${index + 1}`,
    }));
    const fetchPage = vi.fn(async (afterID: number) => {
      const entries = all.filter((entry) => entry.id! > afterID).slice(0, 1000);
      return {
        entries,
        next_cursor: entries.at(-1)?.id ?? afterID,
        has_more: (entries.at(-1)?.id ?? afterID) < all.length,
      };
    });

    const entries = await collectTerminalPages(fetchPage);

    expect(fetchPage).toHaveBeenCalledTimes(13);
    expect(entries).toHaveLength(12_002);
    expect(entries.at(-1)?.message).toBe('scan completed');
    expect(entries.at(-1)?.id).toBe(12_002);
  });
});
