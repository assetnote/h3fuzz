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
  compaction_available: true,
  compaction_blocked_reason: '',
  active_scan_count: 0,
};

describe('StorageWarning', () => {
  it('stays hidden below the server warning threshold', () => {
    render(<StorageWarning stats={{ ...oversized, warning: false }} onReviewSessions={vi.fn()} onCompact={vi.fn()} />);
    expect(screen.queryByRole('alert')).not.toBeInTheDocument();
  });

  it('offers the one-time upgrade below the size warning threshold', async () => {
    const user = userEvent.setup();
    const onCompact = vi.fn().mockResolvedValue(undefined);
    render(
      <StorageWarning
        stats={{
          ...oversized,
          database_bytes: 12_000_000,
          wal_bytes: 0,
          total_bytes: 12_000_000,
          reclaimable_bytes: 0,
          warning: false,
          maintenance_required: true,
          maintenance_reason: 'legacy_tree_metadata',
        }}
        onReviewSessions={vi.fn()}
        onCompact={onCompact}
      />,
    );

    expect(screen.getByRole('alert')).toHaveTextContent('SQLite storage maintenance required');
    expect(screen.getByRole('alert')).toHaveTextContent('upgrade stored directory-tree metadata and indexes');
    expect(screen.queryByRole('button', { name: 'Review saved scans' })).not.toBeInTheDocument();
    const compact = screen.getByRole('button', { name: 'Compact database' });
    expect(compact).toBeEnabled();

    await user.click(compact);
    expect(screen.getByRole('dialog')).toHaveTextContent('upgrades legacy storage metadata');
    await user.click(screen.getByRole('button', { name: 'Confirm compaction' }));
    expect(onCompact).toHaveBeenCalledOnce();
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

  it('shows the largest saved scans and selects them for cleanup without exposing target URLs', async () => {
    const user = userEvent.setup();
    const onReviewSessions = vi.fn();
    const stats: StorageStats = {
      ...oversized,
      sessions: [
        { session_id: 'large', name: 'Large API scan', status: 'completed', result_count: 125_000, estimated_bytes: 850_000_000 },
        { session_id: 'medium', name: 'Medium directory scan', status: 'stopped', result_count: 42_500, estimated_bytes: 310_000_000 },
        { session_id: 'small', name: 'Small scan', status: 'failed', result_count: 9_000, estimated_bytes: 65_000_000 },
        { session_id: 'tiny', name: 'Tiny scan', status: 'completed', result_count: 30, estimated_bytes: 2_000 },
      ],
    };

    render(<StorageWarning stats={stats} onReviewSessions={onReviewSessions} onCompact={vi.fn()} />);

    expect(screen.getByText('Largest saved scans')).toBeVisible();
    expect(screen.getByText('Large API scan')).toBeVisible();
    expect(screen.getByText('125,000 results')).toBeVisible();
    expect(screen.getByText('850 MB')).toBeVisible();
    expect(screen.getByText('Medium directory scan')).toBeVisible();
    expect(screen.getByText('Small scan')).toBeVisible();
    expect(screen.queryByText('Tiny scan')).not.toBeInTheDocument();
    expect(screen.queryByText(/https?:\/\//)).not.toBeInTheDocument();

    await user.click(screen.getByRole('button', { name: 'Select largest scans' }));
    expect(onReviewSessions).toHaveBeenCalledWith(['large', 'medium', 'small']);
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

  it('uses the authoritative server reason when compaction is unavailable', () => {
    render(
      <StorageWarning
        stats={{
          ...oversized,
          compaction_available: false,
          compaction_blocked_reason: 'active_scans',
          active_scan_count: 2,
        }}
        onReviewSessions={vi.fn()}
        onCompact={vi.fn()}
      />,
    );

    expect(screen.getByRole('button', { name: 'Compact database' })).toBeDisabled();
    expect(screen.getByRole('alert')).toHaveTextContent('2 active scans are still using the database');
  });
});
