import type { Result } from '../types/api';

export interface ResultLocation {
  fullURL: string;
  origin: string;
  pathname: string;
  uri: string;
  openURL?: string;
}

const TITLE_KEYS = new Set(['title', 'page-title', 'page_title', 'html-title', 'html_title']);
const TREE_SORTER = new Intl.Collator(undefined, { numeric: true, sensitivity: 'base' });

function shellQuote(value: string): string {
  return `'${value.replaceAll("'", `'\\''`)}'`;
}

export function resultCurl(url: string): string {
  return `curl --request GET --url ${shellQuote(url)}`;
}

export function resultLocation(result: Pick<Result, 'url' | 'host'>): ResultLocation {
  const fullURL = result.url.trim();
  try {
    const parsed = new URL(fullURL);
    const openURL = parsed.protocol === 'http:' || parsed.protocol === 'https:' ? parsed.href : undefined;
    return {
      fullURL,
      origin: parsed.origin,
      pathname: parsed.pathname || '/',
      uri: `${parsed.pathname || '/'}${parsed.search}${parsed.hash}`,
      openURL,
    };
  } catch {
    const uri = fullURL || '/';
    const origin = result.host ? `https://${result.host}` : '';
    return { fullURL, origin, pathname: uri.split(/[?#]/, 1)[0] || '/', uri };
  }
}

export function resultTitle(result: Pick<Result, 'scraper'>): string | undefined {
  if (!result.scraper) return undefined;
  for (const [name, values] of Object.entries(result.scraper)) {
    if (!TITLE_KEYS.has(name.trim().toLowerCase())) continue;
    if (!Array.isArray(values)) continue;
    const title = values.find((value) => value.trim())?.replace(/\s+/g, ' ').trim();
    if (title) return title;
  }
  return undefined;
}

export type ResultTreeNodeKind = 'origin' | 'directory' | 'resource';

export interface ResultTreeNode {
  key: string;
  name: string;
  url: string;
  uri: string;
  kind: ResultTreeNodeKind;
  result?: Result;
  resultCount: number;
  children: ResultTreeNode[];
}

interface MutableResultTreeNode extends Omit<ResultTreeNode, 'children'> {
  children: Map<string, MutableResultTreeNode>;
}

function displaySegment(value: string): string {
  try {
    return decodeURIComponent(value);
  } catch {
    return value;
  }
}

function appendURL(origin: string, path: string): string {
  if (!origin) return path;
  return `${origin}${path.startsWith('/') ? '' : '/'}${path}`;
}

function immutableTree(node: MutableResultTreeNode): ResultTreeNode {
  const children = [...node.children.values()]
    .sort((left, right) => {
      if (left.kind !== right.kind) return left.kind === 'directory' ? -1 : 1;
      return TREE_SORTER.compare(left.name, right.name);
    })
    .map(immutableTree);
  return { ...node, children };
}

export function buildResultTree(results: Result[]): ResultTreeNode[] {
  const origins = new Map<string, MutableResultTreeNode>();

  for (const result of results) {
    const location = resultLocation(result);
    const originIdentity = location.origin || `invalid:${result.host || 'unknown'}`;
    let origin = origins.get(originIdentity);
    if (!origin) {
      origin = {
        key: `origin\u0000${originIdentity}`,
        name: location.origin || result.host || 'Unknown host',
        url: location.origin ? `${location.origin}/` : location.fullURL,
        uri: '/',
        kind: 'origin',
        resultCount: 0,
        children: new Map(),
      };
      origins.set(originIdentity, origin);
    }
    origin.resultCount += 1;

    const rawSegments = location.pathname.split('/').filter(Boolean);
    const segments = rawSegments.length ? rawSegments : ['/'];
    const trailingSlash = location.pathname.endsWith('/');
    let parent = origin;
    let prefix = '';

    segments.forEach((rawSegment, index) => {
      const last = index === segments.length - 1;
      const rootResource = rawSegment === '/';
      if (!rootResource) prefix += `/${rawSegment}`;
      const suffix = last ? location.uri.slice(location.pathname.length) : '';
      const directory = !last || trailingSlash;
      const path = rootResource ? `/${suffix}` : `${prefix}${directory ? '/' : ''}${suffix}`;
      const nodeIdentity = `segment\u0000${rawSegment}${last && suffix ? `\u0000${suffix}` : ''}`;
      let node = parent.children.get(nodeIdentity);
      if (!node) {
        node = {
          key: `${parent.key}\u0000${nodeIdentity}`,
          name: rootResource ? `/${suffix}` : `${displaySegment(rawSegment)}${last ? suffix : ''}`,
          url: last ? location.fullURL : appendURL(location.origin, `${prefix}/`),
          uri: path,
          kind: directory ? 'directory' : 'resource',
          resultCount: 0,
          children: new Map(),
        };
        parent.children.set(nodeIdentity, node);
      } else if (directory) {
        // A discovered URL can also be the parent of deeper assets. Reuse one
        // path node so /admin and /admin/users do not render as duplicate
        // sibling entries named "admin".
        node.kind = 'directory';
      }
      node.resultCount += 1;
      if (last && (!node.result || result.id >= node.result.id)) {
        node.result = result;
        node.url = location.fullURL;
        node.uri = location.uri;
      }
      parent = node;
    });
  }

  return [...origins.values()]
    .sort((left, right) => TREE_SORTER.compare(left.name, right.name))
    .map(immutableTree);
}

export interface VisibleResultTreeNode {
  node: ResultTreeNode;
  depth: number;
}

export function flattenResultTree(
  origins: ResultTreeNode[],
  expanded: ReadonlySet<string>,
): VisibleResultTreeNode[] {
  const visible: VisibleResultTreeNode[] = [];
  const roots = origins.length === 1 ? origins[0].children : origins;

  const visit = (node: ResultTreeNode, depth: number) => {
    visible.push({ node, depth });
    if (expanded.has(node.key)) node.children.forEach((child) => visit(child, depth + 1));
  };
  roots.forEach((root) => visit(root, 0));
  return visible;
}
