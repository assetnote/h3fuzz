import { useVirtualizer } from '@tanstack/react-virtual';
import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { api } from '../api/client';
import type { ResultFilterViewQuery, ResultTreeNode } from '../types/api';
import { resultLocation } from '../utils/resultView';
import { ActionIcon, Alert, Badge, Box, Group, Loader, Paper, Text, Tooltip } from './ui';
import { IconChevronDown } from './ui/icons';
import { ResultActions } from './ResultActions';

interface ResultDirectoryTreeProps {
  sessionID: string;
  query: ResultFilterViewQuery;
  revision?: number;
  loading?: boolean;
}

interface LoadedTreeNode extends ResultTreeNode {
  children?: LoadedTreeNode[];
}

interface VisibleTreeNode {
  node: LoadedTreeNode;
  depth: number;
}

const TREE_PAGE_SIZE = 1_000;

function statusColor(status: number): string {
  if (status >= 500) return 'red';
  if (status >= 400) return 'yellow';
  if (status >= 300) return 'blue';
  if (status >= 200) return 'green';
  return 'gray';
}

function replaceNode(nodes: LoadedTreeNode[], key: string, update: (node: LoadedTreeNode) => LoadedTreeNode): LoadedTreeNode[] {
  let changed = false;
  const next = nodes.map((node) => {
    if (node.key === key) {
      changed = true;
      return update(node);
    }
    if (!node.children) return node;
    const children = replaceNode(node.children, key, update);
    if (children === node.children) return node;
    changed = true;
    return { ...node, children };
  });
  return changed ? next : nodes;
}

function flattenTree(nodes: LoadedTreeNode[], expanded: ReadonlySet<string>, depth = 0): VisibleTreeNode[] {
  const visible: VisibleTreeNode[] = [];
  for (const node of nodes) {
    visible.push({ node, depth });
    if (expanded.has(node.key) && node.children) {
      visible.push(...flattenTree(node.children, expanded, depth + 1));
    }
  }
  return visible;
}

function treeFilterQuery(query: ResultFilterViewQuery): ResultFilterViewQuery {
  // Sorting and exact-response grouping change list presentation only. The
  // hierarchy always represents every path selected by the actual filters.
  const filters = { ...query };
  delete filters.sort;
  delete filters.order;
  delete filters.groupDuplicates;
  return filters;
}

export function ResultDirectoryTree({ sessionID, query, revision = 0, loading = false }: ResultDirectoryTreeProps) {
  const [roots, setRoots] = useState<LoadedTreeNode[]>([]);
  const [expanded, setExpanded] = useState<Set<string>>(() => new Set());
  const [loadingKeys, setLoadingKeys] = useState<Set<string>>(() => new Set());
  const [rootLoading, setRootLoading] = useState(true);
  const [error, setError] = useState('');
  const generation = useRef(0);
  const snapshotID = useRef<number | undefined>(undefined);
  const viewportRef = useRef<HTMLDivElement>(null);
  const filters = useMemo(() => treeFilterQuery(query), [query]);

  const loadLevel = useCallback(async (parent: string | undefined, expectedGeneration: number): Promise<LoadedTreeNode[] | undefined> => {
    const nodes: ResultTreeNode[] = [];
    let offset = 0;
    let levelSnapshot = snapshotID.current;
    for (;;) {
      const page = await api.exploreResultTree(sessionID, {
        ...filters,
        parent,
        snapshotID: levelSnapshot,
        offset,
        limit: TREE_PAGE_SIZE,
      });
      if (generation.current !== expectedGeneration) return undefined;
      if (levelSnapshot === undefined) levelSnapshot = page.snapshot_id;
      if (page.snapshot_id !== levelSnapshot) throw new Error('Tree snapshot changed while loading a directory');
      if (snapshotID.current === undefined) snapshotID.current = levelSnapshot;
      nodes.push(...page.nodes);
      if (!page.has_more) return nodes;
      if (page.nodes.length === 0) throw new Error('Tree pagination made no progress');
      offset = page.offset + page.nodes.length;
    }
  }, [filters, sessionID]);

  useEffect(() => {
    const currentGeneration = ++generation.current;
    setRootLoading(true);
    snapshotID.current = undefined;
    setError('');
    setExpanded(new Set());
    setLoadingKeys(new Set());
    void loadLevel(undefined, currentGeneration).then((nodes) => {
      if (nodes && generation.current === currentGeneration) setRoots(nodes);
    }).catch((reason: unknown) => {
      if (generation.current !== currentGeneration) return;
      setError(reason instanceof Error ? reason.message : 'The directory tree request failed.');
      setRoots([]);
    }).finally(() => {
      if (generation.current === currentGeneration) setRootLoading(false);
    });
  }, [loadLevel, revision]);

  const visible = useMemo(() => flattenTree(roots, expanded), [expanded, roots]);
  const virtualizer = useVirtualizer({
    count: visible.length,
    getScrollElement: () => viewportRef.current,
    estimateSize: () => 44,
    getItemKey: (index) => visible[index]?.node.key ?? index,
    overscan: 14,
  });

  const toggle = (node: LoadedTreeNode) => {
    if (expanded.has(node.key)) {
      setExpanded((current) => {
        const next = new Set(current);
        next.delete(node.key);
        return next;
      });
      return;
    }
    if (node.children) {
      setExpanded((current) => new Set(current).add(node.key));
      return;
    }
    const currentGeneration = generation.current;
    setLoadingKeys((current) => new Set(current).add(node.key));
    void loadLevel(node.key, currentGeneration).then((children) => {
      if (!children || generation.current !== currentGeneration) return;
      setRoots((current) => replaceNode(current, node.key, (entry) => ({ ...entry, children })));
      setExpanded((current) => new Set(current).add(node.key));
    }).catch((reason: unknown) => {
      if (generation.current !== currentGeneration) return;
      setError(reason instanceof Error ? reason.message : 'Could not load this directory.');
    }).finally(() => {
      if (generation.current !== currentGeneration) return;
      setLoadingKeys((current) => {
        const next = new Set(current);
        next.delete(node.key);
        return next;
      });
    });
  };

  return (
    <Paper withBorder className="resultsTable resultTreeFrame">
      <Box ref={viewportRef} className="resultsViewport resultTreeViewport" aria-busy={loading || rootLoading}>
        <Group className="resultTreeHeader" justify="space-between" gap="sm" wrap="nowrap">
          <Box style={{ minWidth: 0 }}>
            <Text fz="xs" fw={700}>Directory tree</Text>
            <Text fz={10} c="dimmed">
              Server-authoritative paths for the complete filtered result set. Directories load on expansion.
            </Text>
          </Box>
          <Text fz="xs" c="dimmed" ff="monospace" style={{ flex: '0 0 auto' }}>
            {roots.length.toLocaleString()} roots
          </Text>
        </Group>
        {error ? <Alert color="red" title="Could not load directory tree">{error}</Alert> : null}
        {!error && visible.length === 0 ? (
          <Box className="resultsEmpty resultTreeEmpty" role="status" aria-live="polite">
            <Text fw={600}>{loading || rootLoading ? 'Loading results…' : 'No paths to display'}</Text>
            <Text fz="xs" c="dimmed" mt={3}>Discoveries will appear here as they arrive.</Text>
          </Box>
        ) : null}
        {visible.length > 0 ? (
          <Box role="list" aria-label="Discovered URL directory tree" style={{ height: virtualizer.getTotalSize(), position: 'relative' }}>
            {virtualizer.getVirtualItems().map((virtualRow) => {
              const { node, depth } = visible[virtualRow.index];
              const expandable = node.has_children;
              const isExpanded = expanded.has(node.key);
              const nodeLoading = loadingKeys.has(node.key);
              const location = resultLocation({ url: node.url, host: node.result?.host ?? '' });
              return (
                <Box
                  key={node.key}
                  className="resultTreeRow"
                  role="listitem"
                  aria-posinset={virtualRow.index + 1}
                  aria-setsize={visible.length}
                  style={{ transform: `translateY(${virtualRow.start}px)` }}
                >
                  <Box className="resultTreeIndent" style={{ width: depth * 18 }} aria-hidden="true" />
                  {expandable ? (
                    <Tooltip label={isExpanded ? `Collapse ${node.name}` : `Expand ${node.name}`}>
                      <ActionIcon
                        component="button"
                        type="button"
                        size="sm"
                        variant="ghost"
                        color="gray"
                        className={`resultTreeToggle${isExpanded ? ' isExpanded' : ''}`}
                        aria-label={`${isExpanded ? 'Collapse' : 'Expand'} ${node.name}`}
                        {...{ 'aria-expanded': isExpanded }}
                        disabled={nodeLoading}
                        onClick={() => toggle(node)}
                      >
                        {nodeLoading ? <Loader size="xs" /> : <IconChevronDown size={14} />}
                      </ActionIcon>
                    </Tooltip>
                  ) : <Box className="resultTreeLeafMarker" aria-hidden="true" />}
                  <Tooltip label={node.url} openDelay={600}>
                    <Text className="resultTreeName" ff="monospace" fz="xs">{node.name}</Text>
                  </Tooltip>
                  {node.result ? (
                    <Badge color={statusColor(node.result.status)} variant="light" size="sm" ff="monospace">
                      {node.result.status}
                    </Badge>
                  ) : null}
                  {node.result_count > 1 ? (
                    <Text className="resultTreeCount" fz={10} c="dimmed">{node.result_count.toLocaleString()}</Text>
                  ) : null}
                  <ResultActions uri={node.uri} url={node.url} openURL={location.openURL} label={node.url} />
                </Box>
              );
            })}
          </Box>
        ) : null}
      </Box>
    </Paper>
  );
}
