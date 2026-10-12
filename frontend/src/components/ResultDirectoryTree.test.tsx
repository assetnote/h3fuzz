import { screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { render } from '../test/render';
import type { Result, ResultTreeNode, ResultTreePage } from '../types/api';
import { ResultDirectoryTree } from './ResultDirectoryTree';

const apiMocks = vi.hoisted(() => ({ exploreResultTree: vi.fn() }));
vi.mock('../api/client', () => ({ api: apiMocks }));

vi.mock('@tanstack/react-virtual', () => ({
  useVirtualizer: ({ count }: { count: number }) => ({
    getTotalSize: () => count * 44,
    getVirtualItems: () => Array.from(
      { length: count },
      (_, index) => ({ index, start: index * 44 }),
    ),
  }),
}));

function result(id: number, path: string): Result {
  return {
    id,
    session_id: 'session-1',
    input: { FUZZ: path },
    position: id,
    status: 200,
    length: 100,
    words: 10,
    lines: 2,
    content_type: 'text/html',
    url: `https://target.example/${path}`,
    duration_ns: 1_000_000,
    host: 'target.example',
    created_at: '2026-10-04T00:00:00Z',
  };
}

function node(key: string, name: string, overrides: Partial<ResultTreeNode> = {}): ResultTreeNode {
  return {
    key,
    name,
    url: `https://target.example/${name}`,
    uri: `/${name}`,
    kind: 'resource',
    result_count: 1,
    has_children: false,
    ...overrides,
  };
}

function page(nodes: ResultTreeNode[], overrides: Partial<ResultTreePage> = {}): ResultTreePage {
  return { nodes, total: nodes.length, offset: 0, limit: 1_000, has_more: false, snapshot_id: 42, ...overrides };
}

describe('ResultDirectoryTree authoritative loading', () => {
  beforeEach(() => vi.clearAllMocks());

  it('represents lazy server hierarchy as an accessible disclosure list', async () => {
    apiMocks.exploreResultTree
      .mockResolvedValueOnce(page([node('origin', 'https://target.example', {
        url: 'https://target.example/', uri: '/', kind: 'origin', has_children: true, result_count: 3,
      })]))
      .mockResolvedValueOnce(page([
        node('admin', 'admin', { kind: 'directory', has_children: true, result_count: 2 }),
        node('assets', 'assets', { kind: 'directory', has_children: true }),
      ]))
      .mockResolvedValueOnce(page([
        node('users', 'users', { result: result(1, 'admin/users') }),
        node('settings', 'settings', { result: result(2, 'admin/settings') }),
      ]));
    const user = userEvent.setup();
    render(<ResultDirectoryTree sessionID="session-1" query={{ statuses: [200] }} />);

    const list = await screen.findByRole('list', { name: 'Discovered URL directory tree' });
    expect(within(list).getAllByRole('listitem')).toHaveLength(1);
    await user.click(screen.getByRole('button', { name: 'Expand https://target.example' }));
    expect(await screen.findByText('admin')).toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: 'Expand admin' }));
    expect(await screen.findByText('users')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Collapse admin' })).toHaveAttribute('aria-expanded', 'true');
    expect(apiMocks.exploreResultTree).toHaveBeenNthCalledWith(1, 'session-1', {
      statuses: [200], parent: undefined, snapshotID: undefined, offset: 0, limit: 1_000,
    });
    expect(apiMocks.exploreResultTree).toHaveBeenNthCalledWith(3, 'session-1', {
      statuses: [200], parent: 'admin', snapshotID: 42, offset: 0, limit: 1_000,
    });
  });

  it('loads every bounded page for a directory before rendering it complete', async () => {
    apiMocks.exploreResultTree
      .mockResolvedValueOnce(page([node('dir', 'dir', { kind: 'directory', has_children: true, result_count: 1_001 })]))
      .mockResolvedValueOnce(page([node('first', 'first')], { total: 2, has_more: true }))
      .mockResolvedValueOnce(page([node('second', 'second')], { total: 2, offset: 1 }));
    const user = userEvent.setup();
    render(<ResultDirectoryTree sessionID="session-1" query={{}} />);

    await user.click(await screen.findByRole('button', { name: 'Expand dir' }));
    expect(await screen.findByText('first')).toBeInTheDocument();
    expect(screen.getByText('second')).toBeInTheDocument();
    expect(apiMocks.exploreResultTree).toHaveBeenLastCalledWith('session-1', {
      parent: 'dir', snapshotID: 42, offset: 1, limit: 1_000,
    });
  });

  it('announces an empty authoritative tree', async () => {
    apiMocks.exploreResultTree.mockResolvedValue(page([]));
    const { container } = render(<ResultDirectoryTree sessionID="session-1" query={{}} />);

    const status = await screen.findByRole('status');
    await waitFor(() => expect(status).toHaveTextContent('No paths to display'));
    expect(container.querySelector('.resultTreeViewport')).toHaveAttribute('aria-busy', 'false');
    expect(screen.queryByRole('list', { name: 'Discovered URL directory tree' })).not.toBeInTheDocument();
  });
});
