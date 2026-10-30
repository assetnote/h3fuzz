import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { act, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import type {
  CreateSessionRequest,
  Result,
  Session,
  SessionFilterPatch,
  Settings,
  TerminalEntry,
  WebSocketEnvelope,
} from './types/api';
import { cloneConfig, defaultConfig } from './utils/config';
import { render } from './test/render';

const harness = vi.hoisted(() => ({
  onStreamEvent: undefined as ((event: WebSocketEnvelope) => void) | undefined,
  onSequenceGap: undefined as (() => void) | undefined,
  api: {
    sessions: vi.fn(),
    settings: vi.fn(),
    storage: vi.fn(),
    compactStorage: vi.fn(),
    profiles: vi.fn(),
    options: vi.fn(),
    presets: vi.fn(),
    createSession: vi.fn(),
    cloneSession: vi.fn(),
    latestResults: vi.fn(),
    terminal: vi.fn(),
    action: vi.fn(),
    addSessionFilters: vi.fn(),
    deleteSession: vi.fn(),
    saveSettings: vi.fn(),
  },
}));

vi.mock('./api/client', () => ({
  APIError: class APIError extends Error {},
  api: {
    ...harness.api,
    exportURL: vi.fn(() => '/export'),
  },
}));

vi.mock('./hooks/useSessionStream', () => ({
  useSessionStream: ({
    onEvent,
    onSequenceGap,
  }: {
    onEvent: (event: WebSocketEnvelope) => void;
    onSequenceGap: () => void;
  }) => {
    harness.onStreamEvent = onEvent;
    harness.onSequenceGap = onSequenceGap;
    return 'connected';
  },
}));

vi.mock('./components/SessionRail', () => ({
  SessionRail: ({ sessions, onNew, onSelect, requestedSelection }: { sessions: Session[]; onNew: () => void; onSelect: (id: string) => void; requestedSelection?: { ids: string[]; requestID: number } }) => (
    <div>
      <button onClick={onNew}>Open wizard</button>
      <span data-testid="requested-cleanup-selection">{requestedSelection?.ids.join(',') ?? ''}</span>
      {sessions.map((entry) => <button key={entry.id} onClick={() => onSelect(entry.id)}>Select {entry.id}</button>)}
    </div>
  ),
}));

vi.mock('./components/NewSessionWizard', () => ({
  NewSessionWizard: ({ opened, initialSession, initialTarget, onCreate, onClose }: { opened: boolean; initialSession?: Session; initialTarget?: string; onCreate: (request: CreateSessionRequest) => void; onClose: () => void }) =>
    opened ? (
      <div>
        {initialSession ? <span>Editing clone of {initialSession.id}</span> : null}
        {initialTarget ? <span data-testid="initial-clone-target">{initialTarget}</span> : null}
        <button onClick={onClose}>Close test wizard</button>
        <button
          onClick={() =>
            onCreate({
              target: initialTarget ?? 'https://target.example/FUZZ',
              profile: 'dir',
              wordlist: 'final.txt',
              auto_start: true,
            })
          }
        >
          Create test session
        </button>
      </div>
    ) : null,
}));

vi.mock('./components/SessionDashboard', () => ({
  SessionDashboard: ({
    session,
    results,
    terminal,
    actionPending,
    actionsDisabled,
    preflightPending,
    onAction,
    onRunPreflight,
    onAddFilters,
    onCloneEdit,
    onCloneResult,
  }: {
    session: Session;
    results: Result[];
    terminal: TerminalEntry[];
    actionPending: boolean;
    actionsDisabled?: boolean;
    preflightPending?: boolean;
    onAction: (action: 'start' | 'pause' | 'resume' | 'stop') => void;
    onRunPreflight?: () => void;
    onAddFilters: (filters: SessionFilterPatch) => Promise<void>;
    onCloneEdit?: () => void;
    onCloneResult?: (url: string) => void;
  }) => (
    <div>
      <div data-testid="session-status">{session.status}</div>
      <div data-testid="session-result-count">{session.result_count}</div>
      <div data-testid="visible-result-count">{results.length}</div>
      <div data-testid="visible-terminal-count">{terminal.length}</div>
      <div data-testid="action-pending">{String(actionPending)}</div>
      <div data-testid="actions-disabled">{String(actionsDisabled)}</div>
      <div data-testid="preflight-pending">{String(preflightPending)}</div>
      <button onClick={() => onAction('start')}>Start test session</button>
      <button onClick={() => onAction('stop')}>Stop test session</button>
      {onRunPreflight ? <button onClick={onRunPreflight}>Run test preflight</button> : null}
      <button onClick={() => void onAddFilters({ words: '656' })}>Add word filter</button>
      {onCloneEdit ? <button onClick={onCloneEdit}>Clone and edit test session</button> : null}
      {onCloneResult ? (
        <button onClick={() => onCloneResult('https://target.example/admin?from=result#details')}>
          Clone discovered path
        </button>
      ) : null}
    </div>
  ),
}));

import App from './App';

function session(status: Session['status'], sequence: number): Session {
  return {
    id: 'session-1',
    name: 'Target',
    target: 'https://target.example/FUZZ',
    status,
    profile: 'dir',
    wordlist: 'final.txt',
    options: cloneConfig(defaultConfig),
    explicit_flags: ['t'],
    progress: {
      req_count: 0,
      req_total: 10,
      req_sec: 0,
      queue_pos: 0,
      queue_total: 10,
      error_count: 0,
    },
    result_count: 0,
    sequence,
    created_at: '2026-10-04T00:00:00Z',
    updated_at: `2026-10-04T00:00:0${sequence}Z`,
  };
}

function result(id: number, words: number): Result {
  return {
    id,
    session_id: 'session-1',
    input: { FUZZ: `path-${id}` },
    position: id,
    status: 200,
    length: 100,
    words,
    lines: 10,
    content_type: 'text/html',
    url: `https://target.example/path-${id}`,
    duration_ns: 1_000_000,
    host: 'target.example',
    created_at: '2026-10-04T00:00:00Z',
  };
}

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (error: unknown) => void;
  const promise = new Promise<T>((done, fail) => {
    resolve = done;
    reject = fail;
  });
  return { promise, resolve, reject };
}

describe('App session cache ordering', () => {
  beforeEach(() => {
    localStorage.clear();
    vi.clearAllMocks();
    harness.onStreamEvent = undefined;
    harness.onSequenceGap = undefined;
    const settings: Settings = {
      defaults: {
        wordlist: 'final.txt',
        profile: 'dir',
        options: cloneConfig(defaultConfig),
        explicit_flags: ['t'],
      },
    };
    harness.api.sessions.mockResolvedValue([]);
    harness.api.settings.mockResolvedValue(settings);
    harness.api.profiles.mockResolvedValue([]);
    harness.api.options.mockResolvedValue({ defaults: cloneConfig(defaultConfig), flags: [] });
    harness.api.presets.mockResolvedValue([]);
    harness.api.storage.mockResolvedValue({
      database_bytes: 0,
      wal_bytes: 0,
      total_bytes: 0,
      reclaimable_bytes: 0,
      warning_threshold_bytes: 1_073_741_824,
      warning: false,
      response_bodies_stored: false,
      compaction_available: true,
      compaction_blocked_reason: '',
      active_scan_count: 0,
    });
    harness.api.compactStorage.mockResolvedValue({
      database_bytes: 0,
      wal_bytes: 0,
      total_bytes: 0,
      reclaimable_bytes: 0,
      warning_threshold_bytes: 1_073_741_824,
      warning: false,
      response_bodies_stored: false,
      compaction_available: true,
      compaction_blocked_reason: '',
      active_scan_count: 0,
    });
    harness.api.latestResults.mockResolvedValue({ results: [], next_cursor: 0, has_more: false });
    harness.api.terminal.mockResolvedValue({ entries: [], next_cursor: 0, has_more: false });
  });

  it('does not hydrate results or terminal for a persisted session that no longer exists', async () => {
    localStorage.setItem('h3fuzz-selected-session', JSON.stringify('deleted-session'));
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });

    render(
      <QueryClientProvider client={client}>
        <App />
      </QueryClientProvider>,
    );

    expect(await screen.findByText('Start a discovery session')).toBeInTheDocument();
    expect(harness.api.latestResults).not.toHaveBeenCalled();
    expect(harness.api.terminal).not.toHaveBeenCalled();
  });

  it('leaves persisted terminal paging to the virtualized explorer instead of hydrating it into App memory', async () => {
    const running = session('running', 1);
    harness.api.sessions.mockResolvedValue([running]);
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(
      <QueryClientProvider client={client}>
        <App />
      </QueryClientProvider>,
    );

    await waitFor(() => expect(screen.getByTestId('visible-terminal-count')).toHaveTextContent('0'));
    expect(harness.api.terminal).not.toHaveBeenCalled();
  });

  it('uses authoritative storage eligibility and refreshes it after lifecycle changes', async () => {
    const running = session('running', 1);
    harness.api.sessions.mockResolvedValue([running]);
    harness.api.storage.mockResolvedValue({
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
    });
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(
      <QueryClientProvider client={client}>
        <App />
      </QueryClientProvider>,
    );

    // The cached session can lag the manager. It must not override the
    // server's compaction decision and permanently disable this action.
    expect(await screen.findByRole('button', { name: 'Compact database' })).toBeEnabled();
    expect(harness.api.storage).toHaveBeenCalledOnce();

    act(() => {
      harness.onStreamEvent?.({
        type: 'session.updated',
        session_id: running.id,
        sequence: 2,
        data: session('completed', 2),
        at: '2026-10-04T00:00:02Z',
      });
    });
    await waitFor(() => expect(harness.api.storage).toHaveBeenCalledTimes(2));
  });

  it('passes the storage cleanup shortlist into the session rail selection', async () => {
    const user = userEvent.setup();
    const completed = session('completed', 1);
    harness.api.sessions.mockResolvedValue([completed]);
    harness.api.storage.mockResolvedValue({
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
      sessions: [{ session_id: completed.id, name: completed.name, status: completed.status, result_count: 50_000, estimated_bytes: 500_000_000 }],
    });
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(
      <QueryClientProvider client={client}>
        <App />
      </QueryClientProvider>,
    );

    await user.click(await screen.findByRole('button', { name: 'Select largest scans' }));
    await waitFor(() => {
      expect(screen.getAllByTestId('requested-cleanup-selection').every((node) => node.textContent === completed.id)).toBe(true);
    });
  });

  it('does not let a stale create response overwrite a newer running WebSocket event', async () => {
    const create = deferred<Session>();
    harness.api.createSession.mockReturnValue(create.promise);
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const user = userEvent.setup();
    render(
      <QueryClientProvider client={client}>
        <App />
      </QueryClientProvider>,
    );

    await user.click(await screen.findByRole('button', { name: 'Open wizard' }));
    await user.click(screen.getByRole('button', { name: 'Create test session' }));
    await waitFor(() => expect(harness.api.createSession).toHaveBeenCalledOnce());

    act(() => {
      harness.onStreamEvent?.({
        type: 'session.created',
        session_id: 'session-1',
        sequence: 1,
        data: session('idle', 1),
        at: '2026-10-04T00:00:01Z',
      });
      harness.onStreamEvent?.({
        type: 'session.updated',
        session_id: 'session-1',
        sequence: 3,
        data: session('running', 3),
        at: '2026-10-04T00:00:03Z',
      });
    });
    expect(await screen.findByTestId('session-status')).toHaveTextContent('running');

    act(() => create.resolve(session('starting', 2)));

    await waitFor(() => expect(screen.queryByRole('button', { name: 'Create test session' })).not.toBeInTheDocument());
    expect(screen.getByTestId('session-status')).toHaveTextContent('running');
  });

  it('opens a completed session in the editable clone form and submits overrides to the clone API', async () => {
    const completed = session('completed', 1);
    const cloned = { ...completed, id: 'session-2', status: 'starting' as const, cloned_from_id: completed.id };
    harness.api.sessions.mockResolvedValue([completed]);
    harness.api.cloneSession.mockResolvedValue(cloned);
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const user = userEvent.setup();

    render(
      <QueryClientProvider client={client}>
        <App />
      </QueryClientProvider>,
    );

    await user.click(await screen.findByRole('button', { name: 'Clone and edit test session' }));
    expect(screen.getByText('Editing clone of session-1')).toBeVisible();
    await user.click(screen.getByRole('button', { name: 'Create test session' }));
    await waitFor(() => expect(harness.api.cloneSession).toHaveBeenCalledWith('session-1', expect.objectContaining({
      target: 'https://target.example/FUZZ',
      auto_start: true,
    })));
  });

  it('opens a result path as a recursive cloned scan target', async () => {
    const completed = session('completed', 1);
    const cloned = { ...completed, id: 'session-2', status: 'starting' as const, cloned_from_id: completed.id };
    harness.api.sessions.mockResolvedValue([completed]);
    harness.api.cloneSession.mockResolvedValue(cloned);
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const user = userEvent.setup();

    render(
      <QueryClientProvider client={client}>
        <App />
      </QueryClientProvider>,
    );

    await user.click(await screen.findByRole('button', { name: 'Clone discovered path' }));
    expect(screen.getByText('Editing clone of session-1')).toBeVisible();
    expect(screen.getByTestId('initial-clone-target')).toHaveTextContent('https://target.example/admin/FUZZ');
    await user.click(screen.getByRole('button', { name: 'Create test session' }));
    await waitFor(() => expect(harness.api.cloneSession).toHaveBeenCalledWith('session-1', expect.objectContaining({
      target: 'https://target.example/admin/FUZZ',
      auto_start: true,
    })));
  });

  it('does not reuse a result clone target after the clone is created', async () => {
    const completed = session('completed', 1);
    const cloned = { ...completed, id: 'session-2', status: 'starting' as const, cloned_from_id: completed.id };
    harness.api.sessions.mockResolvedValue([completed]);
    harness.api.cloneSession.mockResolvedValue(cloned);
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const user = userEvent.setup();

    render(
      <QueryClientProvider client={client}>
        <App />
      </QueryClientProvider>,
    );

    await user.click(await screen.findByRole('button', { name: 'Clone discovered path' }));
    expect(screen.getByTestId('initial-clone-target')).toHaveTextContent('https://target.example/admin/FUZZ');
    await user.click(screen.getByRole('button', { name: 'Create test session' }));
    await waitFor(() => expect(screen.queryByRole('button', { name: 'Create test session' })).not.toBeInTheDocument());

    await user.click(screen.getByRole('button', { name: 'Open wizard' }));
    expect(screen.queryByTestId('initial-clone-target')).not.toBeInTheDocument();
    expect(screen.queryByText('Editing clone of session-1')).not.toBeInTheDocument();
  });

  it('does not reuse a result clone target after the clone dialog is closed', async () => {
    const completed = session('completed', 1);
    harness.api.sessions.mockResolvedValue([completed]);
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const user = userEvent.setup();

    render(
      <QueryClientProvider client={client}>
        <App />
      </QueryClientProvider>,
    );

    await user.click(await screen.findByRole('button', { name: 'Clone discovered path' }));
    expect(screen.getByTestId('initial-clone-target')).toHaveTextContent('https://target.example/admin/FUZZ');
    await user.click(screen.getByRole('button', { name: 'Close test wizard' }));
    await user.click(screen.getByRole('button', { name: 'Open wizard' }));

    expect(screen.queryByTestId('initial-clone-target')).not.toBeInTheDocument();
    expect(screen.queryByText('Editing clone of session-1')).not.toBeInTheDocument();
  });

  it('does not let a stale action response overwrite a newer running WebSocket event', async () => {
    const actionResponse = deferred<Session>();
    harness.api.sessions.mockResolvedValue([session('idle', 1)]);
    harness.api.action.mockReturnValue(actionResponse.promise);
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const user = userEvent.setup();
    render(
      <QueryClientProvider client={client}>
        <App />
      </QueryClientProvider>,
    );

    await user.click(await screen.findByRole('button', { name: 'Start test session' }));
    await waitFor(() => expect(harness.api.action).toHaveBeenCalledWith('session-1', 'start'));
    act(() => {
      harness.onStreamEvent?.({
        type: 'session.updated',
        session_id: 'session-1',
        sequence: 3,
        data: session('running', 3),
        at: '2026-10-04T00:00:03Z',
      });
    });
    expect(screen.getByTestId('session-status')).toHaveTextContent('running');

    await act(async () => actionResponse.resolve(session('starting', 2)));
    await waitFor(() => expect(screen.getByTestId('action-pending')).toHaveTextContent('false'));
    expect(screen.getByTestId('session-status')).toHaveTextContent('running');
  });

  it('scopes pending actions to their session and does not offer preflight for an active run', async () => {
    const firstAction = deferred<Session>();
    const first = session('idle', 1);
    const second = { ...session('running', 1), id: 'session-2', name: 'Second target' };
    harness.api.sessions.mockResolvedValue([first, second]);
    harness.api.action.mockReturnValue(firstAction.promise);
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const user = userEvent.setup();
    render(
      <QueryClientProvider client={client}>
        <App />
      </QueryClientProvider>,
    );

    expect(await screen.findByTestId('session-status')).toHaveTextContent('idle');
    expect(screen.getByRole('button', { name: 'Run test preflight' })).toBeVisible();
    await user.click(screen.getByRole('button', { name: 'Start test session' }));
    expect(screen.getByTestId('action-pending')).toHaveTextContent('true');
    expect(screen.getByTestId('actions-disabled')).toHaveTextContent('true');

    await user.click(screen.getByRole('button', { name: 'Select session-2' }));
    expect(screen.getByTestId('session-status')).toHaveTextContent('running');
    expect(screen.getByTestId('action-pending')).toHaveTextContent('false');
    expect(screen.getByTestId('actions-disabled')).toHaveTextContent('false');
    expect(screen.queryByRole('button', { name: 'Run test preflight' })).not.toBeInTheDocument();

    await user.click(screen.getByRole('button', { name: 'Start test session' }));
    await user.click(screen.getByRole('button', { name: 'Select session-1' }));
    expect(screen.getByTestId('action-pending')).toHaveTextContent('true');
    expect(screen.getByTestId('actions-disabled')).toHaveTextContent('true');

    await act(async () => firstAction.resolve({ ...first, status: 'running', sequence: 2 }));
  });

  it('reconciles a stale REST resync response against a newer WebSocket state', async () => {
    const refetchResponse = deferred<Session[]>();
    harness.api.sessions
      .mockResolvedValueOnce([session('idle', 1)])
      .mockReturnValueOnce(refetchResponse.promise);
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(
      <QueryClientProvider client={client}>
        <App />
      </QueryClientProvider>,
    );

    expect(await screen.findByTestId('session-status')).toHaveTextContent('idle');
    act(() => {
      harness.onStreamEvent?.({
        type: 'session.updated',
        session_id: 'session-1',
        sequence: 3,
        data: session('running', 3),
        at: '2026-10-04T00:00:03Z',
      });
      harness.onSequenceGap?.();
    });
    await waitFor(() => expect(harness.api.sessions).toHaveBeenCalledTimes(2));

    await act(async () => refetchResponse.resolve([session('starting', 2)]));
    await waitFor(() => expect(client.getQueryState(['sessions'])?.fetchStatus).toBe('idle'));
    expect(screen.getByTestId('session-status')).toHaveTextContent('running');
  });

  it('treats a quiet REST resync as authoritative for session membership', async () => {
    const refetchResponse = deferred<Session[]>();
    harness.api.sessions
      .mockResolvedValueOnce([session('completed', 3)])
      .mockReturnValueOnce(refetchResponse.promise);
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(
      <QueryClientProvider client={client}>
        <App />
      </QueryClientProvider>,
    );

    expect(await screen.findByTestId('session-status')).toHaveTextContent('completed');
    act(() => harness.onSequenceGap?.());
    await waitFor(() => expect(harness.api.sessions).toHaveBeenCalledTimes(2));

    await act(async () => refetchResponse.resolve([]));
    await waitFor(() => expect(client.getQueryState(['sessions'])?.fetchStatus).toBe('idle'));
    expect(screen.queryByTestId('session-status')).not.toBeInTheDocument();
  });

  it('does not let an in-flight stale REST list drop a WebSocket-created session', async () => {
    const refetchResponse = deferred<Session[]>();
    harness.api.sessions
      .mockResolvedValueOnce([])
      .mockReturnValueOnce(refetchResponse.promise);
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(
      <QueryClientProvider client={client}>
        <App />
      </QueryClientProvider>,
    );

    await waitFor(() => expect(harness.api.sessions).toHaveBeenCalledOnce());
    act(() => harness.onSequenceGap?.());
    await waitFor(() => expect(harness.api.sessions).toHaveBeenCalledTimes(2));
    act(() => {
      harness.onStreamEvent?.({
        type: 'session.created',
        session_id: 'session-1',
        sequence: 1,
        data: session('running', 1),
        at: '2026-10-04T00:00:01Z',
      });
    });
    expect(await screen.findByTestId('session-status')).toHaveTextContent('running');

    await act(async () => refetchResponse.resolve([]));
    await waitFor(() => expect(client.getQueryState(['sessions'])?.fetchStatus).toBe('idle'));
    expect(screen.getByTestId('session-status')).toHaveTextContent('running');
  });

  it('does not roll back a final WebSocket state when the stop response fails afterwards', async () => {
    const actionResponse = deferred<Session>();
    harness.api.sessions.mockResolvedValue([session('running', 10)]);
    harness.api.action.mockReturnValue(actionResponse.promise);
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const user = userEvent.setup();
    render(
      <QueryClientProvider client={client}>
        <App />
      </QueryClientProvider>,
    );

    await user.click(await screen.findByRole('button', { name: 'Stop test session' }));
    expect(screen.getByTestId('session-status')).toHaveTextContent('stopping');
    act(() => {
      harness.onStreamEvent?.({
        type: 'session.updated',
        session_id: 'session-1',
        sequence: 12,
        data: session('stopped', 12),
        at: '2026-10-04T00:00:12Z',
      });
    });

    await act(async () => actionResponse.reject(new Error('response lost')));
    await waitFor(() => expect(screen.getByTestId('action-pending')).toHaveTextContent('false'));
    expect(screen.getByTestId('session-status')).toHaveTextContent('stopped');
  });

  it('persists a live filter and replaces cached results with the filtered result set', async () => {
    const running = { ...session('running', 10), result_count: 2 };
    const kept = result(1, 12);
    const filtered = result(2, 656);
    const updatedOptions = cloneConfig(running.options);
    updatedOptions.filters.words = '656';
    const updated = { ...running, options: updatedOptions, result_count: 1, sequence: 11 };
    harness.api.sessions.mockResolvedValue([running]);
    harness.api.latestResults
      .mockResolvedValueOnce({ results: [kept, filtered], next_cursor: 2, has_more: false })
      .mockResolvedValueOnce({ results: [kept], next_cursor: 1, has_more: false });
    harness.api.addSessionFilters.mockResolvedValue(updated);
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const user = userEvent.setup();
    render(
      <QueryClientProvider client={client}>
        <App />
      </QueryClientProvider>,
    );

    await waitFor(() => expect(screen.getByTestId('visible-result-count')).toHaveTextContent('2'));
    await user.click(screen.getByRole('button', { name: 'Add word filter' }));

    await waitFor(() => expect(harness.api.addSessionFilters).toHaveBeenCalledWith('session-1', { words: '656' }));
    await waitFor(() => expect(screen.getByTestId('visible-result-count')).toHaveTextContent('1'));
    expect(screen.getByTestId('session-result-count')).toHaveTextContent('1');
    expect(harness.api.latestResults).toHaveBeenCalledTimes(2);
  });

  it('refetches results when another client changes the saved filters', async () => {
    const running = { ...session('running', 10), result_count: 2 };
    const kept = result(1, 12);
    const filtered = result(2, 656);
    const updatedOptions = cloneConfig(running.options);
    updatedOptions.filters.words = '656';
    const updated = { ...running, options: updatedOptions, result_count: 1, sequence: 11 };
    harness.api.sessions.mockResolvedValue([running]);
    harness.api.latestResults
      .mockResolvedValueOnce({ results: [kept, filtered], next_cursor: 2, has_more: false })
      .mockResolvedValueOnce({ results: [kept], next_cursor: 1, has_more: false });
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(
      <QueryClientProvider client={client}>
        <App />
      </QueryClientProvider>,
    );

    await waitFor(() => expect(screen.getByTestId('visible-result-count')).toHaveTextContent('2'));
    act(() => {
      harness.onStreamEvent?.({
        type: 'session.updated',
        session_id: 'session-1',
        sequence: 11,
        data: updated,
        at: '2026-10-04T00:00:11Z',
      });
    });

    await waitFor(() => expect(screen.getByTestId('visible-result-count')).toHaveTextContent('1'));
    expect(screen.getByTestId('session-result-count')).toHaveTextContent('1');
    expect(harness.api.latestResults).toHaveBeenCalledTimes(2);
  });
});
