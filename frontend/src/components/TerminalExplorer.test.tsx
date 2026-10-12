import { screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { render } from '../test/render';
import type { TerminalEntry } from '../types/api';
import { TerminalExplorer } from './TerminalExplorer';

const searchTerminal = vi.hoisted(() => vi.fn());
const copyText = vi.hoisted(() => vi.fn());

vi.mock('../api/client', () => ({ api: { searchTerminal } }));
vi.mock('../utils/clipboard', () => ({ copyText }));
vi.mock('@tanstack/react-virtual', () => ({
  useVirtualizer: ({ count }: { count: number }) => ({
    getTotalSize: () => count * 24,
    getVirtualItems: () => Array.from({ length: count }, (_, index) => ({ index, start: index * 24 })),
    scrollToIndex: vi.fn(),
    measureElement: vi.fn(),
  }),
}));

function line(id: number, level: string, message: string): TerminalEntry {
  return { id, session_id: 'session-1', at: '2026-10-11T03:04:05.123Z', level, message };
}

describe('TerminalExplorer', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    copyText.mockResolvedValue(undefined);
    searchTerminal.mockImplementation(() => Promise.resolve({
      entries: [line(2, 'warning', 'slow response')],
      total: 2,
      offset: 1,
      limit: 1000,
      has_more: false,
      levels: { info: 1, warning: 1 },
      snapshot_id: 2,
      next_before_id: 2,
      has_older: false,
    }));
  });

  it('loads persisted history, filters it on the server, and exposes severity facets', async () => {
    const user = userEvent.setup();
    render(<TerminalExplorer sessionID="session-1" liveLines={[]} />);

    expect(await screen.findByText('slow response')).toBeInTheDocument();
    await user.type(screen.getByLabelText('Search terminal history'), 'timeout');
    await waitFor(() => expect(searchTerminal).toHaveBeenCalledWith('session-1', {
      query: 'timeout', levels: [], latest: true, limit: 1000,
    }));

    await user.click(screen.getByRole('button', { name: 'Filter terminal severity' }));
    expect(await screen.findByRole('menuitemcheckbox', { name: /Warning \(1\)/ })).toBeInTheDocument();
  });

  it('can wrap lines, pause autoscroll, and copy visible output', async () => {
    const user = userEvent.setup();
    render(<TerminalExplorer sessionID="session-1" liveLines={[line(3, 'error', 'handshake failed')]} />);

    expect(await screen.findByText('slow response')).toBeInTheDocument();
    await user.click(screen.getByRole('switch', { name: 'Wrap' }));
    expect(document.querySelector('.terminalViewportWrap')).not.toBeNull();
    await user.click(screen.getByRole('button', { name: 'Pause terminal autoscroll' }));
    expect(screen.getByRole('button', { name: 'Resume terminal autoscroll' })).toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: 'Copy terminal selection' }));
    await waitFor(() => expect(copyText).toHaveBeenCalledWith(expect.stringContaining('slow response')));
  });

  it('increments authoritative totals and severity facets for new live lines', async () => {
    searchTerminal.mockResolvedValue({
      entries: [line(5_000, 'info', 'persisted tail')],
      total: 5_000,
      offset: 4_999,
      limit: 1_000,
      has_more: true,
      levels: { info: 5_000 },
      snapshot_id: 5_000,
      next_before_id: 5_000,
      has_older: true,
    });
    const { rerender } = render(<TerminalExplorer sessionID="session-1" liveLines={[]} />);

    expect(await screen.findByText('persisted tail')).toBeInTheDocument();
    expect(screen.getByText('1 / 5,000 lines')).toBeVisible();

    rerender(
      <TerminalExplorer
        sessionID="session-1"
        liveLines={[line(5_001, 'error', 'new live failure')]}
      />,
    );

    expect(await screen.findByText('new live failure')).toBeInTheDocument();
    expect(screen.getByText('2 / 5,001 lines')).toBeVisible();

    await userEvent.click(screen.getByRole('button', { name: 'Filter terminal severity' }));
    expect(await screen.findByRole('menuitemcheckbox', { name: /Error \(1\)/ })).toBeInTheDocument();
  });

  it('loads older filtered history with a stable snapshot cursor instead of OFFSET', async () => {
    searchTerminal.mockImplementation((_id: string, options: { beforeID?: number }) => Promise.resolve(options.beforeID ? {
      entries: [line(1_001, 'info', 'older persisted line')],
      total: 2_001,
      offset: 1_000,
      limit: 1_000,
      has_more: true,
      levels: { info: 2_001 },
      snapshot_id: 2_001,
      next_before_id: 1_001,
      has_older: true,
    } : {
      entries: [line(2_001, 'info', 'latest persisted line')],
      total: 2_001,
      offset: 2_000,
      limit: 1_000,
      has_more: true,
      levels: { info: 2_001 },
      snapshot_id: 2_001,
      next_before_id: 2_001,
      has_older: true,
    }));
    const user = userEvent.setup();
    render(<TerminalExplorer sessionID="session-1" liveLines={[]} />);

    expect(await screen.findByText('latest persisted line')).toBeVisible();
    await user.click(screen.getByRole('button', { name: 'Load 1,000 older lines' }));

    await waitFor(() => expect(searchTerminal).toHaveBeenLastCalledWith('session-1', {
      query: '',
      levels: [],
      latest: true,
      beforeID: 2_001,
      snapshotID: 2_001,
      limit: 1_000,
    }));
    expect(await screen.findByText('older persisted line')).toBeVisible();
  });
});
