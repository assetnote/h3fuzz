import { fireEvent, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { render } from '../test/render';
import type { Result, ResultExplorerPage, ResultTreePage, SavedResultView } from '../types/api';
import { ResultsTable } from './ResultsTable';

const apiMocks = vi.hoisted(() => ({
  exploreResults: vi.fn(),
  exploreResultTree: vi.fn(),
  resultViews: vi.fn(),
  saveResultView: vi.fn(),
  deleteResultView: vi.fn(),
}));
const copyText = vi.hoisted(() => vi.fn());

Object.defineProperties(HTMLElement.prototype, {
  hasPointerCapture: { configurable: true, value: vi.fn(() => false) },
  setPointerCapture: { configurable: true, value: vi.fn() },
  releasePointerCapture: { configurable: true, value: vi.fn() },
  scrollIntoView: { configurable: true, value: vi.fn() },
});

vi.mock('../api/client', () => ({ api: apiMocks }));
vi.mock('../utils/clipboard', () => ({ copyText }));
vi.mock('@tanstack/react-virtual', () => ({
  useVirtualizer: ({ count }: { count: number }) => ({
    getTotalSize: () => count * 48,
    getVirtualItems: () => Array.from({ length: count }, (_, index) => ({ index, start: index * 48 })),
  }),
}));

function result(id: number, status: number, path: string, overrides: Partial<Result> = {}): Result {
  return {
    id,
    session_id: 'session-1',
    input: { FUZZ: path },
    position: id,
    status,
    length: 1_200,
    words: 10,
    lines: 2,
    content_type: 'text/html',
    url: `https://target.example/${path}`,
    duration_ns: 1_000_000,
    host: 'target.example',
    created_at: '2026-10-04T00:00:00Z',
    ...overrides,
  };
}

function page(results: Result[], overrides: Partial<ResultExplorerPage> = {}): ResultExplorerPage {
  return {
    results,
    total: results.length,
    offset: 0,
    limit: 1_000,
    has_more: false,
    facets: {
      status: Object.fromEntries([...new Set(results.map((item) => item.status))].map((status) => [status, results.filter((item) => item.status === status).length])),
      content_type: { 'text/html': results.length },
      size: { min: 1_200, max: 1_200 },
      words: { min: 10, max: 10 },
      lines: { min: 2, max: 2 },
    },
    ...overrides,
  };
}

function treePage(nodes: ResultTreePage['nodes'], overrides: Partial<ResultTreePage> = {}): ResultTreePage {
  return { nodes, total: nodes.length, offset: 0, limit: 1_000, has_more: false, snapshot_id: 42, ...overrides };
}

describe('ResultsTable explorer', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    copyText.mockResolvedValue(undefined);
    apiMocks.resultViews.mockResolvedValue([]);
    apiMocks.saveResultView.mockImplementation(async (_sessionID, name, query): Promise<SavedResultView> => ({
      id: 'view-1', session_id: 'session-1', name, query,
      created_at: '2026-10-04T00:00:00Z', updated_at: '2026-10-04T00:00:00Z',
    }));
    apiMocks.deleteResultView.mockResolvedValue(undefined);
    apiMocks.exploreResultTree.mockResolvedValue(treePage([]));
    apiMocks.exploreResults.mockResolvedValue(page([result(30_001, 404, 'recent-not-found')], {
      total: 30_001,
      has_more: true,
      facets: {
        status: { '200': 1, '404': 30_000 },
        content_type: { 'application/json': 1, 'text/html': 30_000 },
        size: { min: 12, max: 8_192 }, words: { min: 1, max: 800 }, lines: { min: 1, max: 200 },
      },
    }));
  });

  it('sizes the facets trigger consistently with the toolbar controls', async () => {
    const user = userEvent.setup();
    render(<ResultsTable sessionID="session-1" results={[]} totalResults={0} />);

    const facets = screen.getByRole('button', { name: 'Facets' });
    expect(facets).toHaveClass('rt-r-size-2');
    expect(facets).toHaveAttribute('aria-expanded', 'false');
    expect(facets).toHaveAttribute('aria-controls', 'result-facet-panel');
    expect(screen.getByRole('button', { name: 'Refresh results' })).toHaveClass('rt-r-size-2');
    await user.click(facets);
    expect(facets).toHaveAttribute('aria-expanded', 'true');
    expect(document.getElementById('result-facet-panel')).toBeVisible();
    await waitFor(() => expect(apiMocks.exploreResults).toHaveBeenCalledWith('session-1', {
      sort: 'created', order: 'desc', offset: 0, limit: 1_000,
    }));
  });

  it('keeps an empty saved-view selector inert instead of opening a blank menu', async () => {
    const user = userEvent.setup();
    render(<ResultsTable sessionID="session-1" results={[]} totalResults={0} />);

    await waitFor(() => expect(apiMocks.resultViews).toHaveBeenCalledWith('session-1'));
    const selector = screen.getByRole('combobox', { name: 'Saved filter views' });
    await waitFor(() => {
      expect(selector).toBeDisabled();
      expect(selector).toHaveTextContent('No saved views');
    });
    expect(screen.getByRole('button', { name: 'Save view' })).toBeEnabled();

    await user.click(selector);
    expect(screen.queryByRole('listbox')).not.toBeInTheDocument();
  });

  it('loads authoritatively from an empty mount and refreshes after websocket discoveries', async () => {
    const authoritative = result(1, 200, 'persisted');
    apiMocks.exploreResults.mockResolvedValue(page([authoritative]));
    const { rerender } = render(<ResultsTable sessionID="session-1" results={[]} totalResults={0} />);

    expect(await screen.findByText(authoritative.url)).toBeInTheDocument();
    const initialCalls = apiMocks.exploreResults.mock.calls.length;
    rerender(<ResultsTable sessionID="session-1" results={[result(2, 201, 'live')]} totalResults={1} />);
    await waitFor(() => expect(apiMocks.exploreResults.mock.calls.length).toBeGreaterThan(initialCalls), { timeout: 1_500 });
  });

  it('uses server-side facets, sorting, and filtering without losing alternative values', async () => {
    const oldSuccess = result(1, 200, 'old-success', { content_type: 'application/json' });
    apiMocks.exploreResults
      .mockImplementation(async (_sessionID: string, options: { statuses?: number[] }) => options.statuses?.includes(200)
        ? page([oldSuccess], {
          facets: {
            status: { '200': 1 }, content_type: { 'application/json': 1 },
            size: { min: 90, max: 90 }, words: { min: 10, max: 10 }, lines: { min: 2, max: 2 },
          },
        })
        : page([result(30_001, 404, 'recent-not-found')], {
        total: 30_001,
        has_more: true,
        facets: {
          status: { '200': 1, '404': 30_000 },
          content_type: { 'application/json': 1, 'text/html': 30_000 },
          size: { min: 12, max: 8_192 }, words: { min: 1, max: 800 }, lines: { min: 1, max: 200 },
        },
      }));
    const user = userEvent.setup();
    render(<ResultsTable sessionID="session-1" results={[result(30_001, 404, 'recent-not-found')]} totalResults={30_001} windowTruncated />);

    await waitFor(() => expect(apiMocks.exploreResults).toHaveBeenCalledWith('session-1', {
      sort: 'created', order: 'desc', offset: 0, limit: 1_000,
    }));
    await user.click(screen.getByRole('button', { name: 'Filter by status' }));
    await user.click(await screen.findByRole('menuitemcheckbox', { name: /200 \(1\)/ }));
    await waitFor(() => expect(apiMocks.exploreResults).toHaveBeenLastCalledWith('session-1', {
      sort: 'created', order: 'desc', statuses: [200], offset: 0, limit: 1_000,
    }));
    expect(await screen.findByText('https://target.example/old-success')).toBeInTheDocument();

    await user.click(screen.getByRole('button', { name: 'Filter by status' }));
    expect(await screen.findByRole('menuitemcheckbox', { name: /404 \(30,000\)/ })).toBeInTheDocument();
  });

  it('virtualizes actionable rows and opens a complete detail drawer', async () => {
    const titled = result(1, 200, "docs?name=o'hara", {
      scraper: { title: ['  API   documentation  '] }, duplicate_count: 12, wildcard_group: true,
    });
    apiMocks.exploreResults.mockResolvedValue(page([titled]));
    const user = userEvent.setup();
    const { container } = render(<ResultsTable sessionID="session-1" results={[titled]} totalResults={1} />);

    expect(await screen.findByText(titled.url)).toBeInTheDocument();
    expect(container.querySelector('.resultsViewport > .resultHeader')).not.toBeNull();
    expect(screen.getByText('API documentation')).toBeInTheDocument();
    expect(screen.queryByText(/FUZZ=/)).not.toBeInTheDocument();
    expect(screen.getAllByRole('columnheader')).toHaveLength(6);
    const row = screen.getByText(titled.url).closest('[role="row"]');
    expect(within(row as HTMLElement).getAllByRole('cell')).toHaveLength(6);

    await user.click(screen.getByText(titled.url));
    expect(await screen.findByRole('heading', { name: 'Result details' })).toBeVisible();
    expect(screen.getByText('12 exact captured bodies')).toBeVisible();
    await user.click(screen.getByRole('button', { name: 'Copy cURL' }));
    await waitFor(() => expect(copyText).toHaveBeenCalledWith(
      "curl --request GET --url 'https://target.example/docs?name=o'\\''hara'",
    ));
    expect(screen.getByRole('link', { name: 'Open target' })).toHaveAttribute('target', '_blank');
  });

  it('saves, reapplies, and deletes named filter views', async () => {
    const saved: SavedResultView = {
      id: 'json-view', session_id: 'session-1', name: 'JSON success',
      query: { statuses: [200], contentTypes: ['application/json'], sort: 'size', order: 'desc' },
      created_at: '2026-10-04T00:00:00Z', updated_at: '2026-10-04T00:00:00Z',
    };
    apiMocks.resultViews.mockResolvedValue([saved]);
    const user = userEvent.setup();
    render(<ResultsTable sessionID="session-1" results={[result(1, 200, 'api')]} totalResults={1} />);

    await waitFor(() => expect(apiMocks.resultViews).toHaveBeenCalledWith('session-1'));
    const savedViewSelector = screen.getByRole('combobox', { name: 'Saved filter views' });
    await waitFor(() => expect(savedViewSelector).toBeEnabled());
    fireEvent.keyDown(savedViewSelector, { key: 'ArrowDown' });
    await user.click(await screen.findByRole('option', { name: 'JSON success' }));
    await waitFor(() => expect(apiMocks.exploreResults).toHaveBeenLastCalledWith('session-1', {
      statuses: [200], contentTypes: ['application/json'], sort: 'size', order: 'desc', offset: 0, limit: 1_000,
    }));

    await user.click(screen.getByRole('button', { name: 'Save view' }));
    await user.type(screen.getByLabelText('View name'), 'Updated view');
    await user.click(screen.getByRole('button', { name: 'Save view' }));
    await waitFor(() => expect(apiMocks.saveResultView).toHaveBeenCalledWith(
      'session-1', 'Updated view', expect.objectContaining({ statuses: [200], contentTypes: ['application/json'] }),
    ));
    await user.click(screen.getByRole('button', { name: 'Delete saved view' }));
    await waitFor(() => expect(apiMocks.deleteResultView).toHaveBeenCalledWith('session-1', 'view-1'));
  });

  it('renders a lazy server-authoritative tree instead of the current result page', async () => {
    const discoveries = [result(1, 200, 'admin/users'), result(2, 200, 'admin/settings'), result(3, 301, 'assets/app.js')];
    apiMocks.exploreResults.mockResolvedValue(page(discoveries));
    apiMocks.exploreResultTree
      .mockResolvedValueOnce(treePage([{
        key: 'origin-key', name: 'https://target.example', url: 'https://target.example/', uri: '/',
        kind: 'origin', result_count: 30_003, has_children: true,
      }]))
      .mockResolvedValueOnce(treePage([{
        key: 'admin-key', name: 'admin', url: 'https://target.example/admin/', uri: '/admin/',
        kind: 'directory', result_count: 30_000, has_children: true,
      }]))
      .mockResolvedValueOnce(treePage([
        { key: 'users-key', name: 'users', url: discoveries[0].url, uri: '/admin/users', kind: 'resource', result: discoveries[0], result_count: 1, has_children: false },
        { key: 'settings-key', name: 'settings', url: discoveries[1].url, uri: '/admin/settings', kind: 'resource', result: discoveries[1], result_count: 1, has_children: false },
      ]));
    const user = userEvent.setup();
    const { container } = render(<ResultsTable sessionID="session-1" results={discoveries} totalResults={30_003} windowTruncated />);

    await user.click(screen.getByRole('radio', { name: 'Tree' }));
    await waitFor(() => expect(container.querySelector('.resultTreeViewport > .resultTreeHeader')).not.toBeNull());
    expect(await screen.findByText('https://target.example')).toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: 'Expand https://target.example' }));
    expect(screen.getByText('admin')).toBeInTheDocument();
    expect(screen.queryByText('users')).not.toBeInTheDocument();
    expect(screen.getByText(/complete filtered result set/)).toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: 'Expand admin' }));
    expect(await screen.findByText('users')).toBeInTheDocument();
    expect(await screen.findByText('settings')).toBeInTheDocument();
    expect(screen.getByRole('link', { name: 'Open URL for https://target.example/admin/users' })).toHaveAttribute('target', '_blank');
  });
});
