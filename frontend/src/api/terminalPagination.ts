import type { TerminalEntry } from '../types/api';

interface TerminalPage {
  entries: TerminalEntry[];
  next_cursor: number;
  has_more: boolean;
}

export async function collectTerminalPages(
  fetchPage: (afterID: number) => Promise<TerminalPage>,
  afterID = 0,
): Promise<TerminalEntry[]> {
  const entries: TerminalEntry[] = [];
  let cursor = afterID;
  while (true) {
    const page = await fetchPage(cursor);
    entries.push(...page.entries);
    if (!page.has_more || page.entries.length === 0) break;
    if (page.next_cursor <= cursor) {
      throw new Error('Terminal pagination cursor did not advance.');
    }
    cursor = page.next_cursor;
  }
  return entries;
}
