import { screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { describe, expect, it, vi } from 'vitest';
import type { StorageStats } from '../types/api';
import { render } from '../test/render';
import { StorageWarning } from './StorageWarning';

const oversized: StorageStats = {
  database_bytes: 1_400_000_000,
  wal_bytes: 100_000_000,
  total_bytes: 1_500_000_000,
  reclaimable_bytes: 300_000_000,
  warning_threshold_bytes: 1_073_741_824,
  warning: true,
  response_bodies_stored: false,
};

describe('StorageWarning', () => {
  it('stays hidden below the server warning threshold', () => {
    render(<StorageWarning stats={{ ...oversized, warning: false }} onReviewSessions={vi.fn()} onCompact={vi.fn()} />);
    expect(screen.queryByRole('alert')).not.toBeInTheDocument();
  });

  it('explains what uses space and sends users to explicit session cleanup', async () => {
    const user = userEvent.setup();
    const onReviewSessions = vi.fn();
    render(<StorageWarning stats={oversized} onReviewSessions={onReviewSessions} onCompact={vi.fn()} />);

    expect(screen.getByRole('alert')).toHaveTextContent('1.5 GB');
    expect(screen.getByRole('alert')).toHaveTextContent('does not store raw response bodies');
    expect(screen.getByRole('alert')).toHaveTextContent('result metadata, optional scraper output, terminal history, scan configuration, and search indexes');
    expect(screen.getByRole('alert')).toHaveTextContent('300 MB in free pages');
    await user.click(screen.getByRole('button', { name: 'Review saved scans' }));
    expect(onReviewSessions).toHaveBeenCalledOnce();
  });

  it('requires a second explicit confirmation before compacting', async () => {
    const user = userEvent.setup();
    const onCompact = vi.fn();
    render(<StorageWarning stats={oversized} onReviewSessions={vi.fn()} onCompact={onCompact} />);

    await user.click(screen.getByRole('button', { name: 'Compact database' }));
    expect(screen.getByRole('heading', { name: 'Compact the SQLite database?' })).toBeVisible();
    expect(screen.getByText(/does not delete scans/i)).toBeVisible();
    expect(onCompact).not.toHaveBeenCalled();

    await user.click(screen.getByRole('button', { name: 'Confirm compaction' }));
    expect(onCompact).toHaveBeenCalledOnce();
    await waitFor(() => expect(screen.queryByRole('heading', { name: 'Compact the SQLite database?' })).not.toBeInTheDocument());
  });

  it('keeps visible pending state and suppresses duplicate compaction requests', async () => {
    const user = userEvent.setup();
    let resolveCompaction: (() => void) | undefined;
    const onCompact = vi.fn(() => new Promise<void>((resolve) => { resolveCompaction = resolve; }));
    render(<StorageWarning stats={oversized} onReviewSessions={vi.fn()} onCompact={onCompact} />);

    await user.click(screen.getByRole('button', { name: 'Compact database' }));
    const confirm = screen.getByRole('button', { name: 'Confirm compaction' });
    await user.click(confirm);

    expect(onCompact).toHaveBeenCalledOnce();
    expect(confirm).toBeDisabled();
    expect(screen.getByRole('button', { name: 'Cancel' })).toBeDisabled();
    await user.click(confirm);
    expect(onCompact).toHaveBeenCalledOnce();

    resolveCompaction?.();
    await waitFor(() => expect(screen.queryByRole('heading', { name: 'Compact the SQLite database?' })).not.toBeInTheDocument());
  });

  it('keeps confirmation context available when compaction fails', async () => {
    const user = userEvent.setup();
    const onCompact = vi.fn().mockRejectedValue(new Error('checkpoint busy'));
    render(<StorageWarning stats={oversized} onReviewSessions={vi.fn()} onCompact={onCompact} />);

    await user.click(screen.getByRole('button', { name: 'Compact database' }));
    await user.click(screen.getByRole('button', { name: 'Confirm compaction' }));

    await waitFor(() => expect(screen.getByRole('button', { name: 'Confirm compaction' })).toBeEnabled());
    expect(screen.getByRole('heading', { name: 'Compact the SQLite database?' })).toBeVisible();
    expect(onCompact).toHaveBeenCalledOnce();
  });

  it('does not offer compaction while a scan is active', () => {
    render(
      <StorageWarning
        stats={oversized}
        compactDisabled
        onReviewSessions={vi.fn()}
        onCompact={vi.fn()}
      />,
    );

    expect(screen.getByRole('button', { name: 'Compact database' })).toBeDisabled();
    expect(screen.getByRole('alert')).toHaveTextContent('Stop active scans before compacting');
  });
});
