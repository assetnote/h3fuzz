import { screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { api } from '../api/client';
import { render } from '../test/render';
import type { Session } from '../types/api';
import { cloneConfig, defaultConfig } from '../utils/config';
import { effectiveTransport } from '../utils/transport';
import { SessionDashboard } from './SessionDashboard';

vi.mock('@tanstack/react-virtual', () => ({
  useVirtualizer: ({ count }: { count: number }) => ({
    getTotalSize: () => count * 32,
    getVirtualItems: () => Array.from({ length: count }, (_, index) => ({ index, start: index * 32 })),
    scrollToIndex: vi.fn(),
    measureElement: vi.fn(),
  }),
}));

afterEach(() => {
  vi.restoreAllMocks();
  window.localStorage.clear();
});

function sessionFixture(): Session {
  return {
    id: 'session-1',
    name: 'Local target',
    target: 'http://127.0.0.1:9090/FUZZ',
    status: 'idle',
    profile: 'dir',
    wordlist: 'final.txt',
    options: cloneConfig(defaultConfig),
    explicit_flags: ['D', 'e', 'mc', 'fc', 'H'],
    progress: {
      req_count: 0,
      req_total: 0,
      req_sec: 0,
      queue_pos: 0,
      queue_total: 0,
      error_count: 0,
    },
    result_count: 0,
    created_at: '2026-10-03T00:00:00Z',
    updated_at: '2026-10-03T00:00:00Z',
  };
}

describe('SessionDashboard', () => {
  it('renders an idle persisted session and exposes the start action', () => {
    const session = sessionFixture();

    render(
      <SessionDashboard
        session={session}
        results={[]}
        terminal={[]}
        rateHistory={[]}
        onAction={vi.fn()}
        onDelete={vi.fn()}
        onRefresh={vi.fn()}
      />,
    );

    expect(screen.getByRole('heading', { name: 'Local target' })).toBeVisible();
    expect(screen.getByRole('button', { name: 'Start' })).toBeEnabled();
    expect(screen.getByText('Not probed')).toBeVisible();
  });

  it('shows the persisted effective transport and downgrade reason', () => {
    const session = sessionFixture();
    session.status = 'running';
    session.options.http.http3 = true;
    session.runtime_transport = 'http2';
    session.runtime_transport_reason = 'HTTP/3 probe timed out; selected HTTP/2.';
    session.response_optimization_strategy = 'HEAD';

    render(
      <SessionDashboard
        session={session}
        results={[]}
        terminal={[]}
        rateHistory={[]}
        onAction={vi.fn()}
        onDelete={vi.fn()}
        onRefresh={vi.fn()}
      />,
    );

    expect(screen.getByText('HTTP/2')).toBeVisible();
    expect(screen.queryByText('HTTP/3 forced')).not.toBeInTheDocument();
    expect(screen.getByText('HTTP/3 probe timed out; selected HTTP/2.')).toBeVisible();
    expect(screen.getByText('HEAD')).toBeVisible();
  });

  it('uses one Radix size for every dashboard action', () => {
    const session = sessionFixture();
    session.status = 'running';

    render(
      <SessionDashboard
        session={session}
        results={[]}
        terminal={[]}
        rateHistory={[]}
        onAction={vi.fn()}
        onDelete={vi.fn()}
        onRefresh={vi.fn()}
      />,
    );

    for (const name of ['Refresh session', 'Export', 'More session actions', 'Pause', 'Stop session']) {
      expect(screen.getByRole('button', { name })).toHaveClass('rt-r-size-2');
    }
  });

  it('keeps session management actions available from the overflow menu', async () => {
    const user = userEvent.setup();
    const onDelete = vi.fn();
    const onCloneEdit = vi.fn();
    const onCompare = vi.fn();
    const onArchive = vi.fn();

    render(
      <SessionDashboard
        session={sessionFixture()}
        results={[]}
        terminal={[]}
        rateHistory={[]}
        onAction={vi.fn()}
        onCloneEdit={onCloneEdit}
        onCompare={onCompare}
        onArchive={onArchive}
        onDelete={onDelete}
        onRefresh={vi.fn()}
      />,
    );

    await user.click(screen.getByRole('button', { name: 'More session actions' }));
    await user.click(await screen.findByText('Clone, edit, and rerun'));
    expect(onCloneEdit).toHaveBeenCalledOnce();

    await user.click(screen.getByRole('button', { name: 'More session actions' }));
    await user.click(await screen.findByText('Compare with another run'));
    expect(onCompare).toHaveBeenCalledOnce();

    await user.click(screen.getByRole('button', { name: 'More session actions' }));
    await user.click(await screen.findByText('Archive session'));
    expect(onArchive).toHaveBeenCalledOnce();

    await user.click(screen.getByRole('button', { name: 'More session actions' }));
    await user.click(await screen.findByText('Delete session'));
    expect(screen.getByRole('heading', { name: 'Delete this session?' })).toBeVisible();
    expect(onDelete).not.toHaveBeenCalled();
  });

  it('prevents duplicate deletion while the session delete is pending', async () => {
    const user = userEvent.setup();
    const onDelete = vi.fn();
    const props = {
      session: sessionFixture(),
      results: [],
      terminal: [],
      rateHistory: [],
      onAction: vi.fn(),
      onDelete,
      onRefresh: vi.fn(),
    };
    const { rerender } = render(
      <SessionDashboard
        {...props}
      />,
    );

    await user.click(screen.getByRole('button', { name: 'More session actions' }));
    await user.click(await screen.findByText('Delete session'));
    rerender(<SessionDashboard {...props} deletePending />);
    expect(screen.getByRole('button', { name: 'Delete session' })).toBeDisabled();
    expect(onDelete).not.toHaveBeenCalled();
  });

  it('reports probing while a session is starting', () => {
    const session = sessionFixture();
    session.status = 'starting';
    session.runtime_transport = 'http3';

    expect(effectiveTransport(session).label).toBe('Probing');
    render(
      <SessionDashboard
        session={session}
        results={[]}
        terminal={[]}
        rateHistory={[]}
        onAction={vi.fn()}
        onDelete={vi.fn()}
        onRefresh={vi.fn()}
      />,
    );
    expect(screen.getByRole('button', { name: 'Starting' })).toBeDisabled();
    expect(screen.queryByRole('button', { name: 'Stop session' })).not.toBeInTheDocument();
  });

  it('renders persisted and live terminal lines after switching tabs', async () => {
    const user = userEvent.setup();
    render(
      <SessionDashboard
        session={sessionFixture()}
        results={[]}
        terminal={[
          { id: 1, at: '2026-10-03T00:00:00Z', level: 'info', message: 'persisted start line' },
          { id: 2, at: '2026-10-03T00:00:01Z', level: 'result', message: 'live result line' },
        ]}
        rateHistory={[]}
        onAction={vi.fn()}
        onDelete={vi.fn()}
        onRefresh={vi.fn()}
      />,
    );

    await user.click(screen.getByRole('tab', { name: /Terminal/ }));
    expect(screen.getByText('persisted start line')).toBeVisible();
    expect(screen.getByText('live result line')).toBeVisible();
    expect(screen.getByText(/2 lines/)).toBeVisible();
  });

  it('previews an export, copies it, and keeps full download separate', async () => {
    const user = userEvent.setup();
    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, 'clipboard', { configurable: true, value: { writeText } });
    vi.spyOn(api, 'exportPreview').mockResolvedValue({
      content: '[{"url":"https://target.example/admin"}]',
      captured_bytes: 42,
      total_bytes: 42,
      truncated: false,
    });
    const session = sessionFixture();

    render(
      <SessionDashboard
        session={session}
        results={[]}
        terminal={[]}
        rateHistory={[]}
        onAction={vi.fn()}
        onDelete={vi.fn()}
        onRefresh={vi.fn()}
      />,
    );

    await user.click(screen.getByRole('button', { name: 'Export' }));
    await user.click(await screen.findByText('JSON'));
    expect(await screen.findByLabelText('Export preview')).toHaveTextContent('target.example/admin');
    await user.click(screen.getByRole('button', { name: 'Copy' }));
    expect(writeText).toHaveBeenCalledWith('[{"url":"https://target.example/admin"}]');
    expect(screen.getByRole('link', { name: 'Download full export' })).toHaveAttribute(
      'href',
      api.exportURL(session.id, 'json'),
    );
  });

  it('clears an export preview when switching sessions', async () => {
    const user = userEvent.setup();
    vi.spyOn(api, 'exportPreview').mockResolvedValue({
      content: 'old session output',
      captured_bytes: 18,
      total_bytes: 18,
      truncated: false,
    });
    const props = {
      results: [],
      terminal: [],
      rateHistory: [],
      onAction: vi.fn(),
      onDelete: vi.fn(),
      onRefresh: vi.fn(),
    };
    const first = sessionFixture();
    const { rerender } = render(<SessionDashboard session={first} {...props} />);
    await user.click(screen.getByRole('button', { name: 'Export' }));
    await user.click(await screen.findByText('JSON'));
    expect(await screen.findByLabelText('Export preview')).toHaveTextContent('old session output');

    const second = { ...sessionFixture(), id: 'session-2', name: 'Second target' };
    rerender(<SessionDashboard session={second} {...props} />);
    await waitFor(() => expect(screen.queryByLabelText('Export preview')).not.toBeInTheDocument());
  });

  it('disables actions while stopping', () => {
    const session = sessionFixture();
    session.status = 'stopping';
    render(
      <SessionDashboard
        session={session}
        results={[]}
        terminal={[]}
        rateHistory={[]}
        onAction={vi.fn()}
        onDelete={vi.fn()}
        onRefresh={vi.fn()}
      />,
    );

    expect(screen.getByRole('button', { name: 'Stopping' })).toBeDisabled();
    expect(screen.queryByRole('button', { name: 'Stop session' })).not.toBeInTheDocument();
  });

  it('distinguishes persistence backpressure from a remote stall', () => {
    const session = sessionFixture();
    session.status = 'running';
    session.progress.req_sec = 0;
    session.progress.results_queued = 32768;
    session.progress.persistence_backpressure = true;
    session.progress.last_batch_duration_ms = 240;
    render(
      <SessionDashboard
        session={session}
        results={[]}
        terminal={[]}
        rateHistory={[]}
        onAction={vi.fn()}
        onDelete={vi.fn()}
        onRefresh={vi.fn()}
      />,
    );

    expect(screen.getByRole('alert')).toHaveTextContent('Persisting 32,768 results');
    expect(screen.getByRole('alert')).toHaveTextContent(/storage backpressure, not a remote response stall/);
  });

  it('uses the authoritative remote stall diagnosis without alarming during startup', () => {
    const session = sessionFixture();
    session.status = 'starting';
    const props = {
      results: [],
      terminal: [],
      onAction: vi.fn(),
      onDelete: vi.fn(),
      onRefresh: vi.fn(),
    };
    const { rerender } = render(<SessionDashboard session={session} rateHistory={[]} {...props} />);
    expect(screen.queryByText('Waiting for target responses')).not.toBeInTheDocument();

    const running = { ...session, status: 'running' as const, progress: { ...session.progress, req_count: 30, req_sec: 0 } };
    running.progress.stall_reason = 'no completed requests while result persistence has capacity';
    running.progress.stalled_for_ms = 6200;
    rerender(<SessionDashboard session={running} rateHistory={[]} {...props} />);
    expect(screen.getByText('Waiting for target responses')).toBeVisible();
    expect(screen.getByText(/server may be throttling/)).toBeVisible();
    expect(screen.getByText(/No requests have completed for 6\.2 seconds/)).toBeVisible();
  });

  it('does not show scheduled work as completed progress while requests are draining', () => {
    const session = sessionFixture();
    session.status = 'running';
    session.progress = {
      ...session.progress,
      req_count: 2_001,
      request_count: 2_001,
      completed_count: 0,
      response_count: 0,
      req_total: 3_000,
    };

    render(
      <SessionDashboard
        session={session}
        results={[]}
        terminal={[]}
        rateHistory={[]}
        onAction={vi.fn()}
        onDelete={vi.fn()}
        onRefresh={vi.fn()}
      />,
    );

    expect(screen.getByText('0 of 3,000 requests')).toBeVisible();
    expect(screen.getByText('0.0%')).toBeVisible();
    expect(screen.queryByText('2,001 of 3,000 requests')).not.toBeInTheDocument();
  });

  it('lets the user minimize throughput to an essential live summary', async () => {
    const user = userEvent.setup();
    const session = sessionFixture();
    session.status = 'running';
    session.progress = {
      ...session.progress,
      request_count: 120,
      response_count: 118,
      current_rate: 25.5,
      average_30s: 24.2,
      overall_average: 20,
      peak_rate: 32.8,
      eta_seconds: 65,
      error_count: 5,
    };

    render(
      <SessionDashboard
        session={session}
        results={[]}
        terminal={[]}
        rateHistory={[{ at: 1, value: 25.5 }]}
        onAction={vi.fn()}
        onDelete={vi.fn()}
        onRefresh={vi.fn()}
      />,
    );

    const panel = screen.getByLabelText('Stable throughput metrics');
    const minimize = screen.getByRole('button', { name: 'Minimize throughput metrics' });
    expect(minimize).toHaveAttribute('aria-expanded', 'true');
    expect(within(panel).getByText('Rolling rate history')).toBeVisible();

    await user.click(minimize);

    expect(screen.getByRole('button', { name: 'Expand throughput metrics' })).toHaveAttribute('aria-expanded', 'false');
    expect(within(panel).queryByText('Rolling rate history')).not.toBeInTheDocument();
    expect(panel).toHaveTextContent('Current rolling25.5 req/s');
    expect(panel).toHaveTextContent('30s average24.2 req/s');
    expect(panel).toHaveTextContent('Requests120');
    expect(panel).toHaveTextContent('Errors5');
    expect(panel).not.toHaveTextContent('Overall average');
    expect(panel).not.toHaveTextContent('Peak rate');
  });

  it('remembers the minimized throughput preference across dashboard remounts', async () => {
    const user = userEvent.setup();
    const props = {
      session: sessionFixture(),
      results: [],
      terminal: [],
      rateHistory: [],
      onAction: vi.fn(),
      onDelete: vi.fn(),
      onRefresh: vi.fn(),
    };
    const first = render(<SessionDashboard {...props} />);

    await user.click(screen.getByRole('button', { name: 'Minimize throughput metrics' }));
    first.unmount();
    render(<SessionDashboard {...props} />);

    expect(screen.getByRole('button', { name: 'Expand throughput metrics' })).toHaveAttribute('aria-expanded', 'false');
    expect(screen.getByLabelText('Essential throughput summary')).toBeVisible();
  });

  it('renders stable throughput, complete preflight evidence, and selectable error diagnostics', async () => {
    const user = userEvent.setup();
    const session = sessionFixture();
    session.status = 'completed';
    session.progress = {
      ...session.progress,
      request_count: 120,
      response_count: 118,
      current_rate: 25.5,
      average_30s: 24.2,
      overall_average: 20,
      peak_rate: 32.8,
      eta_seconds: 65,
      error_count: 5,
    };
    const check = { attempted: true, reliable: true, requests: 1, elapsed_ms: 18 };
    session.preflight_report = {
      state: 'complete',
      ran_at: '2026-10-03T00:00:00Z',
      completed_at: '2026-10-03T00:00:01Z',
      http3_advertised: true,
      http3_negotiated: true,
      http3_advertisement: 'h3=":443"; ma=86400',
      advertisement_check: { ...check, reason: 'Alt-Svc advertised h3.' },
      negotiation_check: { ...check, elapsed_ms: 42 },
      head_check: { ...check, elapsed_ms: 9 },
      range_check: { ...check, elapsed_ms: 11 },
      fingerprint: {
        target: 'https://target.example',
        supported: true,
        high_rate_ready: true,
        alpn: 'h3',
        quic_version: 'v1',
        http_version: 'HTTP/3.0',
        status_code: 200,
        connection_reused: true,
        concurrent_streams_completed: 32,
        concurrent_streams_attempted: 32,
        quic_datagrams: false,
        http3_datagrams: false,
        remote_addr: '203.0.113.8:443',
        elapsed_ms: 42,
      },
      selected_protocol: 'HTTP/3',
      selected_strategy: 'range',
      strategy_reason: 'Range returned a representative response.',
      effective_workers: 256,
      effective_connections: 4,
      effective_streams: 64,
      streams_per_connection: 16,
      timings_ms: { advertisement: 18, http3: 42, baseline: 16, head: 9, range: 11, total: 96 },
    };
    session.error_diagnostics = [
      {
        category: 'tls',
        count: 2,
        samples: ['certificate verify failed'],
        suggestion: 'Install the target CA or correct the certificate chain.',
      },
      {
        category: 'timeout',
        count: 3,
        samples: ['context deadline exceeded'],
        suggestion: 'Reduce concurrency or increase the request timeout.',
      },
    ];
    const onRunPreflight = vi.fn();

    render(
      <SessionDashboard
        session={session}
        results={[]}
        terminal={[]}
        rateHistory={[]}
        onAction={vi.fn()}
        onRunPreflight={onRunPreflight}
        onDelete={vi.fn()}
        onRefresh={vi.fn()}
      />,
    );

    expect(screen.getByLabelText('Stable throughput metrics')).toHaveTextContent('Current rolling25.5 req/s');
    expect(screen.getByLabelText('Stable throughput metrics')).toHaveTextContent('30s average24.2 req/s');
    expect(screen.getByLabelText('Stable throughput metrics')).toHaveTextContent('Overall average20 req/s');
    expect(screen.getByLabelText('Stable throughput metrics')).toHaveTextContent('Peak rate32.8 req/s');
    expect(screen.getByLabelText('Stable throughput metrics')).toHaveTextContent('ETA1m 5s');
    expect(screen.getByLabelText('Stable throughput metrics')).toHaveTextContent('Requests120');
    expect(screen.getByLabelText('Stable throughput metrics')).toHaveTextContent('Responses118');

    expect(screen.queryByLabelText('Protocol preflight report')).not.toBeInTheDocument();
    await user.click(screen.getByRole('tab', { name: /Preflight/ }));
    const report = screen.getByLabelText('Protocol preflight report');
    expect(report).toHaveTextContent('HTTP/3');
    expect(report).toHaveTextContent('RANGE');
    expect(report).toHaveTextContent('Alt-Svc advertised h3.');
    expect(report).toHaveTextContent('203.0.113.8:443');
    await user.click(screen.getByRole('button', { name: 'Run preflight again' }));
    expect(onRunPreflight).toHaveBeenCalledOnce();

    expect(screen.getByText('certificate verify failed')).toBeVisible();
    await user.click(screen.getByRole('tab', { name: /Timeout/ }));
    expect(screen.getByText('context deadline exceeded')).toBeVisible();
    expect(screen.getByText('Reduce concurrency or increase the request timeout.')).toBeVisible();
  });

  it('does not offer a new preflight while the session is active', async () => {
    const user = userEvent.setup();
    const session = sessionFixture();
    session.status = 'running';
    const onRunPreflight = vi.fn();

    render(
      <SessionDashboard
        session={session}
        results={[]}
        terminal={[]}
        rateHistory={[]}
        onAction={vi.fn()}
        onRunPreflight={onRunPreflight}
        onDelete={vi.fn()}
        onRefresh={vi.fn()}
      />,
    );

    await user.click(screen.getByRole('tab', { name: /Preflight/ }));
    expect(screen.getByRole('button', { name: 'Run preflight again' })).toBeDisabled();
    expect(screen.getByText(/Run it again after the active scan stops/)).toBeVisible();
    expect(onRunPreflight).not.toHaveBeenCalled();
  });
});
