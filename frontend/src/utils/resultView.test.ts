import { describe, expect, it } from 'vitest';
import type { Result } from '../types/api';
import { buildResultTree, flattenResultTree, resultLocation, resultTitle } from './resultView';

function result(id: number, url: string): Result {
  return {
    id,
    session_id: 'session-1',
    input: { FUZZ: url },
    position: id,
    status: 200,
    length: 100,
    words: 10,
    lines: 2,
    content_type: 'text/html',
    url,
    duration_ns: 1_000_000,
    host: 'target.example',
    created_at: '2026-10-04T00:00:00Z',
  };
}

describe('result view data', () => {
  it('preserves query strings and fragments when deriving a URI', () => {
    expect(resultLocation(result(1, 'https://target.example/a%20b?q=one#two'))).toMatchObject({
      origin: 'https://target.example',
      pathname: '/a%20b',
      uri: '/a%20b?q=one#two',
      openURL: 'https://target.example/a%20b?q=one#two',
    });
  });

  it('does not make malformed or non-http result URLs openable', () => {
    expect(resultLocation(result(1, 'not a URL')).openURL).toBeUndefined();
    expect(resultLocation(result(2, 'javascript:alert(1)')).openURL).toBeUndefined();
  });

  it('uses only explicit title scraper values and normalizes whitespace', () => {
    expect(resultTitle({ scraper: { links: ['Wrong'], 'page-title': ['  The   title\n'] } })).toBe('The title');
    expect(resultTitle({ scraper: { links: ['Not a title'] } })).toBeUndefined();
  });

  it('builds stable host-aware nodes, aggregates duplicate URLs, and starts collapsed', () => {
    const tree = buildResultTree([
      result(1, 'https://target.example/admin/users'),
      result(2, 'https://target.example/admin/users'),
      { ...result(3, 'https://other.example/admin/users'), host: 'other.example' },
    ]);

    expect(tree).toHaveLength(2);
    expect(new Set(tree.map((node) => node.key)).size).toBe(2);
    expect(flattenResultTree(tree, new Set())).toHaveLength(2);

    const targetRoot = tree.find((node) => node.name === 'https://target.example');
    expect(targetRoot?.children[0].name).toBe('admin');
    expect(targetRoot?.children[0].resultCount).toBe(2);
    const expanded = flattenResultTree(tree, new Set([targetRoot!.key]));
    expect(expanded.some(({ node }) => node.name === 'admin')).toBe(true);
  });

  it('reuses a discovered path when that path is also a parent directory', () => {
    const [origin] = buildResultTree([
      result(1, 'https://target.example/admin'),
      result(2, 'https://target.example/admin/users'),
      result(3, 'https://target.example/admin/settings'),
    ]);

    expect(origin.children).toHaveLength(1);
    expect(origin.children[0]).toMatchObject({
      name: 'admin',
      kind: 'directory',
      resultCount: 3,
      result: { id: 1 },
    });
    expect(origin.children[0].children.map((child) => child.name)).toEqual(['settings', 'users']);
  });
});
