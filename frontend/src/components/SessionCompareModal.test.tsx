import { screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { render } from '../test/render';
import type { Result, Session } from '../types/api';
import { cloneConfig, defaultConfig } from '../utils/config';
import { SessionCompareModal } from './SessionCompareModal';

const compareSessions = vi.hoisted(() => vi.fn());

vi.mock('../api/client', () => ({
  api: { compareSessions },
}));

vi.mock('@tanstack/react-virtual', () => ({
  useVirtualizer: ({ count }: { count: number }) => ({
    getTotalSize: () => count * 38,
    getVirtualItems: () => Array.from({ length: count }, (_, index) => ({ index, start: index * 38 })),
  }),
}));

function session(id: string, name: string, status: Session['status'] = 'completed'): Session {
  return {
    id,
    name,
    target: `https://${id}.example/FUZZ`,
    status,
    profile: 'dir',
    wordlist: 'final.txt',
    options: cloneConfig(defaultConfig),
    explicit_flags: [],
    progress: { req_count: 10, req_total: 10, req_sec: 0, queue_pos: 0, queue_total: 0, error_count: 0 },
    result_count: 1,
    created_at: '2026-10-11T00:00:00Z',
    updated_at: '2026-10-11T00:01:00Z',
  };
}

function result(id: number, url: string): Result {
  return {
    id,
    session_id: 'current',
    input: { FUZZ: 'admin' },
    position: id,
    status: 200,
    length: 100,
    words: 10,
    lines: 2,
    content_type: 'text/html',
    url,
    duration_ns: 1,
    host: 'current.example',
    created_at: '2026-10-11T00:00:00Z',
  };
}

describe('SessionCompareModal', () => {
  beforeEach(() => compareSessions.mockReset());

  it('compares the selected baseline to the current run and renders directional changes', async () => {
    const user = userEvent.setup();
    const current = session('current', 'Current run');
    const baseline = session('baseline', 'Baseline run');
    compareSessions.mockResolvedValueOnce({
      left_session_id: baseline.id,
      right_session_id: current.id,
      added: [result(1, 'https://current.example/new/path?source=fuzz')],
      removed: [result(2, 'https://current.example/removed')],
      added_total: 501,
      removed_total: 1,
      unchanged_count: 8,
      added_next_cursor: 'v1.next',
      added_has_more: true,
      removed_has_more: false,
      limit: 200,
    }).mockResolvedValueOnce({
      left_session_id: baseline.id,
      right_session_id: current.id,
      added: [result(3, 'https://current.example/newer')],
      removed: [],
      added_total: 501,
      removed_total: 1,
      unchanged_count: 8,
      added_has_more: false,
      removed_has_more: false,
      limit: 200,
    });

    const view = render(
      <SessionCompareModal
        opened
        session={current}
        sessions={[current, baseline, session('idle', 'Draft', 'idle')]}
        onClose={vi.fn()}
      />,
    );

    expect(screen.getByText('Baseline run · 1 results')).toBeVisible();
    expect(screen.queryByText(/Draft/)).not.toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: 'Compare' }));

    await waitFor(() => expect(compareSessions).toHaveBeenCalledWith('baseline', 'current', { side: 'both', limit: 200 }));
    expect(await screen.findByText('/new/path?source=fuzz')).toBeVisible();
    expect(screen.getByText('/removed')).toBeVisible();
    expect(screen.getByText('8')).toBeVisible();
    expect(screen.getByText('501')).toBeVisible();
    expect(screen.getByRole('link', { name: 'Open https://current.example/new/path?source=fuzz' }))
      .toHaveAttribute('target', '_blank');

    await user.click(screen.getByRole('button', { name: 'Load more changes' }));
    await waitFor(() => expect(compareSessions).toHaveBeenLastCalledWith('baseline', 'current', {
      side: 'added', cursor: 'v1.next', limit: 200,
    }));
    expect(await screen.findByText('/newer')).toBeVisible();

    const updated = { ...current, progress: { ...current.progress, req_count: 11 } };
    view.rerender(<SessionCompareModal opened session={updated} sessions={[updated, { ...baseline }]} onClose={vi.fn()} />);
    expect(screen.getByText('/new/path?source=fuzz')).toBeVisible();
  });

  it('disables comparison when no other runnable session exists', () => {
    const current = session('current', 'Current run');
    render(
      <SessionCompareModal
        opened
        session={current}
        sessions={[current, session('idle', 'Draft', 'idle')]}
        onClose={vi.fn()}
      />,
    );

    expect(screen.getByRole('button', { name: 'Compare' })).toBeDisabled();
    expect(compareSessions).not.toHaveBeenCalled();
  });
});
