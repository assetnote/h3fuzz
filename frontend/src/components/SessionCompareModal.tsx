import { Alert, Badge, Box, Button, Group, Loader, Modal, Select, SimpleGrid, Stack, Text, Title } from './ui';
import { useEffect, useMemo, useRef, useState } from 'react';
import { useVirtualizer } from '@tanstack/react-virtual';
import { api } from '../api/client';
import type { Result, Session, SessionComparison } from '../types/api';
import { IconArrowUpRight, IconRefresh } from './ui/icons';

const COMPARISON_PAGE_SIZE = 200;

interface SessionCompareModalProps {
  opened: boolean;
  session: Session;
  sessions: Session[];
  onClose: () => void;
}

function resultPath(result: Result): string {
  try {
    const url = new URL(result.url);
    return `${url.pathname}${url.search}`;
  } catch {
    return result.url;
  }
}

export function SessionCompareModal({ opened, session, sessions, onClose }: SessionCompareModalProps) {
  const candidates = useMemo(() => sessions.filter((candidate) => candidate.id !== session.id && ['completed', 'stopped'].includes(candidate.status)), [session.id, sessions]);
  const candidateSignature = candidates.map((candidate) => candidate.id).join('\0');
  const candidateIDs = useMemo(() => candidateSignature ? candidateSignature.split('\0') : [], [candidateSignature]);
  const [otherID, setOtherID] = useState<string | null>(null);
  const [comparison, setComparison] = useState<SessionComparison>();
  const [loading, setLoading] = useState(false);
  const [loadingSide, setLoadingSide] = useState<'added' | 'removed'>();
  const [error, setError] = useState<string>();
  const requestID = useRef(0);

  useEffect(() => {
    if (!opened) return;
    setOtherID(null);
    setComparison(undefined);
    setLoading(false);
    setLoadingSide(undefined);
    setError(undefined);
    requestID.current += 1;
  }, [opened, session.id]);

  useEffect(() => {
    if (!opened) return;
    if (otherID && candidateIDs.includes(otherID)) return;
    setOtherID(candidateIDs[0] ?? null);
    setComparison(undefined);
    setLoading(false);
    setLoadingSide(undefined);
  }, [candidateIDs, opened, otherID]);

  const compare = async () => {
    if (!otherID) return;
    const id = ++requestID.current;
    setLoading(true);
    setError(undefined);
    try {
      // Treat the selected run as the baseline so "added" always means paths
      // present in the currently viewed run and "removed" means regressions.
      const next = await api.compareSessions(otherID, session.id, { side: 'both', limit: COMPARISON_PAGE_SIZE });
      if (id === requestID.current) setComparison(next);
    } catch (reason) {
      if (id === requestID.current) setError(reason instanceof Error ? reason.message : 'Could not compare these sessions.');
    } finally {
      if (id === requestID.current) setLoading(false);
    }
  };

  const loadMore = async (side: 'added' | 'removed') => {
    if (!otherID || !comparison || loadingSide) return;
    const cursor = side === 'added' ? comparison.added_next_cursor : comparison.removed_next_cursor;
    if (!cursor) return;
    const id = ++requestID.current;
    setLoadingSide(side);
    setError(undefined);
    try {
      const page = await api.compareSessions(otherID, session.id, { side, cursor, limit: COMPARISON_PAGE_SIZE });
      if (id !== requestID.current) return;
      setComparison((current) => {
        if (!current) return page;
        if (side === 'added') {
          return {
            ...current,
            added: mergeResults(current.added, page.added),
            added_total: page.added_total,
            unchanged_count: page.unchanged_count,
            added_has_more: page.added_has_more,
            added_next_cursor: page.added_next_cursor,
          };
        }
        return {
          ...current,
          removed: mergeResults(current.removed, page.removed),
          removed_total: page.removed_total,
          unchanged_count: page.unchanged_count,
          removed_has_more: page.removed_has_more,
          removed_next_cursor: page.removed_next_cursor,
        };
      });
    } catch (reason) {
      if (id === requestID.current) setError(reason instanceof Error ? reason.message : 'Could not load more comparison results.');
    } finally {
      if (id === requestID.current) setLoadingSide(undefined);
    }
  };

  return (
    <Modal opened={opened} onClose={onClose} size="min(980px, 94vw)" title="Compare discovery runs" centered>
      <Stack gap="md">
        <Group align="flex-end" gap="sm" wrap="wrap">
          <Box style={{ flex: '1 1 300px' }}>
            <Text fz="xs" c="dimmed">Current run</Text>
            <Text fw={650}>{session.name}</Text>
          </Box>
          <Select
            label="Compare with"
            placeholder={candidates.length ? 'Choose another run' : 'No other completed runs'}
            searchable
            value={otherID}
            data={candidates.map((candidate) => ({ value: candidate.id, label: `${candidate.name} · ${candidate.result_count} results` }))}
            onChange={(value) => { requestID.current += 1; setOtherID(value); setComparison(undefined); setLoading(false); setLoadingSide(undefined); }}
            style={{ flex: '2 1 360px' }}
          />
          <Button leftSection={loading ? <Loader size="xs" /> : <IconRefresh size={15} />} disabled={!otherID || loading} onClick={() => void compare()}>Compare</Button>
        </Group>
        {error ? <Alert color="red">{error}</Alert> : null}
        {comparison ? <>
          <SimpleGrid cols={{ base: 1, md: 3 }}>
            <Summary label="Added" value={comparison.added_total ?? comparison.added.length} color="green" />
            <Summary label="Removed" value={comparison.removed_total ?? comparison.removed.length} color="red" />
            <Summary label="Unchanged" value={comparison.unchanged_count} color="gray" />
          </SimpleGrid>
          <SimpleGrid cols={{ base: 1, md: 2 }}>
            <ResultChanges
              title="Added paths"
              color="green"
              results={comparison.added}
              total={comparison.added_total ?? comparison.added.length}
              hasMore={comparison.added_has_more}
              loading={loadingSide === 'added'}
              onLoadMore={() => void loadMore('added')}
            />
            <ResultChanges
              title="Removed paths"
              color="red"
              results={comparison.removed}
              total={comparison.removed_total ?? comparison.removed.length}
              hasMore={comparison.removed_has_more}
              loading={loadingSide === 'removed'}
              onLoadMore={() => void loadMore('removed')}
            />
          </SimpleGrid>
        </> : null}
      </Stack>
    </Modal>
  );
}

function Summary({ label, value, color }: { label: string; value: number; color: string }) {
  return <Box className="compareSummary"><Text fz="xs" c="dimmed">{label}</Text><Group gap="xs"><Title order={3}>{value.toLocaleString()}</Title><Badge color={color} variant="soft">paths</Badge></Group></Box>;
}

function mergeResults(current: Result[], incoming: Result[]): Result[] {
  const byURL = new Map(current.map((result) => [result.url, result]));
  incoming.forEach((result) => byURL.set(result.url, result));
  return [...byURL.values()];
}

function ResultChanges({ title, color, results, total, hasMore, loading, onLoadMore }: {
  title: string;
  color: string;
  results: Result[];
  total: number;
  hasMore: boolean;
  loading: boolean;
  onLoadMore: () => void;
}) {
  const viewportRef = useRef<HTMLDivElement>(null);
  const virtualizer = useVirtualizer({
    count: results.length,
    getScrollElement: () => viewportRef.current,
    estimateSize: () => 38,
    overscan: 20,
  });
  return <Box className="compareChanges"><Group justify="space-between"><Text fw={650}>{title}</Text><Badge color={color} variant="soft">{results.length.toLocaleString()} / {total.toLocaleString()}</Badge></Group>{results.length ? <Box ref={viewportRef} className="compareChangesScroll" role="list" aria-label={title}><Box style={{ height: virtualizer.getTotalSize(), position: 'relative' }}>{virtualizer.getVirtualItems().map((row) => { const result = results[row.index]; return <Group key={`${result.id}:${result.url}`} className="comparePath" justify="space-between" wrap="nowrap" style={{ position: 'absolute', insetInline: 0, transform: `translateY(${row.start}px)` }}><Text ff="monospace" fz="xs" className="breakAnywhere">{resultPath(result)}</Text><Button component="a" href={result.url} target="_blank" rel="noreferrer" variant="ghost" size="compact-xs" aria-label={`Open ${result.url}`}><IconArrowUpRight size={13} /></Button></Group>; })}</Box></Box> : <Text c="dimmed" fz="sm" p="md">No changes in this direction.</Text>}{hasMore ? <Button fullWidth variant="ghost" size="xs" loading={loading} onClick={onLoadMore} className="compareChangesLoadMore">Load more changes</Button> : null}</Box>;
}
