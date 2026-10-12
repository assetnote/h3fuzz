import {
  ActionIcon,
  Box,
  Button,
  Group,
  MultiSelect,
  Paper,
  Stack,
  Switch,
  Text,
  TextInput,
  Tooltip,
} from './ui';
import { useDebouncedValue } from './ui/hooks';
import { IconCopy, IconPlayerPause, IconPlayerPlay, IconSearch, IconX } from './ui/icons';
import { useVirtualizer } from '@tanstack/react-virtual';
import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { api } from '../api/client';
import type { TerminalEntry } from '../types/api';
import { copyText } from '../utils/clipboard';
import { notifications } from './ui/toast';

interface TerminalExplorerProps {
  sessionID: string;
  liveLines: TerminalEntry[];
  refreshKey?: number;
  onTotalChange?: (total: number) => void;
}

const PAGE_SIZE = 1000;
const LEVELS = ['debug', 'info', 'warning', 'error'];

interface TerminalServerSnapshot {
  total: number;
  levels: Record<string, number>;
  maxID: number;
  snapshotID: number;
  nextBeforeID: number;
  hasOlder: boolean;
  loadedCount: number;
  capturedAt: number;
  knownKeys: Set<string>;
}

const EMPTY_SERVER_SNAPSHOT: TerminalServerSnapshot = {
  total: 0,
  levels: {},
  maxID: 0,
  snapshotID: 0,
  nextBeforeID: 0,
  hasOlder: false,
  loadedCount: 0,
  capturedAt: 0,
  knownKeys: new Set(),
};

function terminalKey(entry: TerminalEntry): string {
  return entry.id === undefined ? `${entry.at}\0${entry.level}\0${entry.message}` : `id:${entry.id}`;
}

function mergeEntries(current: TerminalEntry[], incoming: TerminalEntry[]): TerminalEntry[] {
  const entries = new Map<string, TerminalEntry>();
  current.forEach((entry) => entries.set(terminalKey(entry), entry));
  incoming.forEach((entry) => entries.set(terminalKey(entry), entry));
  return [...entries.values()].sort((left, right) => {
    if (left.id !== undefined && right.id !== undefined) return left.id - right.id;
    return new Date(left.at).getTime() - new Date(right.at).getTime();
  });
}

function matches(entry: TerminalEntry, query: string, levels: string[]): boolean {
  return (!levels.length || levels.includes(entry.level)) &&
    (!query || entry.message.toLowerCase().includes(query.toLowerCase()));
}

export function TerminalExplorer({ sessionID, liveLines, refreshKey = 0, onTotalChange }: TerminalExplorerProps) {
  const [query, setQuery] = useState('');
  const [debouncedQuery] = useDebouncedValue(query.trim(), 250);
  const [levels, setLevels] = useState<string[]>([]);
  const [entries, setEntries] = useState<TerminalEntry[]>([]);
  const [serverSnapshot, setServerSnapshot] = useState<TerminalServerSnapshot>(EMPTY_SERVER_SNAPSHOT);
  const [loading, setLoading] = useState(false);
  const [loadError, setLoadError] = useState('');
  const [wrap, setWrap] = useState(false);
  const [autoscrollPaused, setAutoscrollPaused] = useState(false);
  const parentRef = useRef<HTMLDivElement>(null);
  const requestID = useRef(0);
  const skipNextAutoscroll = useRef(false);
  const liveLinesRef = useRef(liveLines);
  liveLinesRef.current = liveLines;
  const latestLiveID = liveLines.at(-1)?.id ?? 0;

  const loadWindow = useCallback(async () => {
    const id = ++requestID.current;
    const capturedAt = Date.now();
    setLoading(true);
    setLoadError('');
    try {
      const page = await api.searchTerminal(sessionID, {
        query: debouncedQuery, levels, latest: true, limit: PAGE_SIZE,
      });
      if (id !== requestID.current) return;
      const matchingLive = liveLinesRef.current.filter((entry) => matches(entry, debouncedQuery, levels));
      const merged = mergeEntries(page.entries, matchingLive);
      setEntries(merged);
      const snapshotID = page.snapshot_id ?? page.entries.reduce((highest, entry) => Math.max(highest, entry.id ?? 0), 0);
      setServerSnapshot({
        total: page.total,
        levels: page.levels ?? {},
        maxID: snapshotID,
        snapshotID,
        nextBeforeID: page.next_before_id ?? page.entries[0]?.id ?? 0,
        hasOlder: page.has_older ?? page.has_more,
        loadedCount: page.entries.length,
        capturedAt,
        knownKeys: new Set(page.entries.map(terminalKey)),
      });
    } catch (error) {
      if (id !== requestID.current) return;
      setLoadError(error instanceof Error ? error.message : 'Could not load terminal history.');
    } finally {
      if (id === requestID.current) setLoading(false);
    }
  }, [debouncedQuery, levels, sessionID]);

  useEffect(() => {
    setEntries(liveLinesRef.current.filter((entry) => matches(entry, debouncedQuery, levels)));
    setServerSnapshot(EMPTY_SERVER_SNAPSHOT);
    void loadWindow();
  }, [debouncedQuery, levels, loadWindow, refreshKey]);

  useEffect(() => {
    const matchingLive = liveLines.filter((entry) => matches(entry, debouncedQuery, levels));
    setEntries((current) => mergeEntries(current, matchingLive));
  }, [debouncedQuery, latestLiveID, levels, liveLines, onTotalChange]);

  const liveAfterSnapshot = useMemo(() => {
    return liveLines.filter((entry) => {
      if (!matches(entry, debouncedQuery, levels)) return false;
      if (entry.id !== undefined) return entry.id > serverSnapshot.maxID;
      const timestamp = new Date(entry.at).getTime();
      return !serverSnapshot.knownKeys.has(terminalKey(entry)) &&
        (!Number.isFinite(timestamp) || timestamp >= serverSnapshot.capturedAt);
    });
  }, [debouncedQuery, levels, liveLines, serverSnapshot]);

  const total = Math.max(entries.length, serverSnapshot.total + liveAfterSnapshot.length);
  const offset = Math.max(0, serverSnapshot.total - serverSnapshot.loadedCount);
  const levelCounts = useMemo(() => {
    const counts = { ...serverSnapshot.levels };
    liveAfterSnapshot.forEach((entry) => {
      const level = entry.level || 'info';
      counts[level] = (counts[level] ?? 0) + 1;
    });
    return counts;
  }, [liveAfterSnapshot, serverSnapshot.levels]);

  const virtualizer = useVirtualizer({
    count: entries.length,
    getScrollElement: () => parentRef.current,
    estimateSize: () => wrap ? 38 : 24,
    overscan: 30,
    measureElement: wrap ? (element) => element.getBoundingClientRect().height : undefined,
  });

  useEffect(() => {
    if (skipNextAutoscroll.current) {
      skipNextAutoscroll.current = false;
      return;
    }
    if (!autoscrollPaused && entries.length) {
      virtualizer.scrollToIndex(entries.length - 1, { align: 'end' });
    }
  }, [autoscrollPaused, entries.length, virtualizer]);

  const loadOlder = async () => {
    if (loading || !serverSnapshot.hasOlder || serverSnapshot.nextBeforeID <= 0) return;
    const id = ++requestID.current;
    const viewport = parentRef.current;
    const previousScrollHeight = viewport?.scrollHeight ?? 0;
    setLoading(true);
    setLoadError('');
    try {
      const page = await api.searchTerminal(sessionID, {
        query: debouncedQuery,
        levels,
        latest: true,
        beforeID: serverSnapshot.nextBeforeID,
        snapshotID: serverSnapshot.snapshotID,
        limit: PAGE_SIZE,
      });
      if (id !== requestID.current) return;
      skipNextAutoscroll.current = true;
      setEntries((current) => mergeEntries(page.entries, current));
      setServerSnapshot((current) => ({
        total: page.total,
        levels: page.levels ?? current.levels,
        maxID: current.maxID,
        snapshotID: current.snapshotID,
        nextBeforeID: page.next_before_id ?? page.entries[0]?.id ?? current.nextBeforeID,
        hasOlder: page.has_older ?? page.has_more,
        loadedCount: current.loadedCount + page.entries.filter((entry) => !current.knownKeys.has(terminalKey(entry))).length,
        capturedAt: current.capturedAt,
        knownKeys: new Set([...current.knownKeys, ...page.entries.map(terminalKey)]),
      }));
      window.requestAnimationFrame(() => {
        if (viewport) viewport.scrollTop += viewport.scrollHeight - previousScrollHeight;
      });
    } catch (error) {
      if (id !== requestID.current) return;
      setLoadError(error instanceof Error ? error.message : 'Could not load older terminal history.');
    } finally {
      if (id === requestID.current) setLoading(false);
    }
  };

  useEffect(() => {
    onTotalChange?.(total);
  }, [onTotalChange, total]);

  const copySelection = async () => {
    const selected = window.getSelection()?.toString();
    const value = selected?.trim() || entries.map((entry) => entry.message).join('\n');
    if (!value) return;
    try {
      await copyText(value);
      notifications.show({ color: 'green', message: selected?.trim() ? 'Selection copied.' : 'Visible terminal lines copied.' });
    } catch (error) {
      notifications.show({ color: 'red', title: 'Could not copy terminal output', message: error instanceof Error ? error.message : 'Clipboard access is unavailable.' });
    }
  };

  const levelOptions = useMemo(() => LEVELS.map((level) => ({
    value: level,
    label: `${level[0].toUpperCase()}${level.slice(1)}${levelCounts[level] === undefined ? '' : ` (${levelCounts[level].toLocaleString()})`}`,
  })), [levelCounts]);

  return (
    <Stack gap="sm" className="terminalExplorer">
      <Paper withBorder className="terminalFrame">
        <Group className="terminalToolbar terminalExplorerToolbar" justify="space-between" gap="sm">
          <Group gap="xs" className="terminalSearchGroup">
            <Group gap={6} className="terminalWindowDots" aria-hidden="true">
              <Box className="terminalDot terminalDotRed" />
              <Box className="terminalDot terminalDotYellow" />
              <Box className="terminalDot terminalDotGreen" />
            </Group>
            <TextInput
              aria-label="Search terminal history"
              placeholder="Search all terminal history"
              value={query}
              onChange={(event) => setQuery(event.currentTarget.value)}
              leftSection={<IconSearch size={14} />}
              rightSection={query ? <ActionIcon size="xs" variant="transparent" aria-label="Clear terminal search" onClick={() => setQuery('')}><IconX size={12} /></ActionIcon> : null}
            />
            <MultiSelect
              aria-label="Filter terminal severity"
              placeholder="Severity"
              data={levelOptions}
              value={levels}
              onChange={setLevels}
              clearable
              w={190}
            />
          </Group>
          <Group gap="xs" wrap="nowrap">
            <Switch checked={wrap} onChange={(event) => setWrap(event.currentTarget.checked)} label="Wrap" />
            <Tooltip label={autoscrollPaused ? 'Resume autoscroll' : 'Pause autoscroll'}>
              <ActionIcon variant={autoscrollPaused ? 'light' : 'ghost'} color={autoscrollPaused ? 'yellow' : 'gray'} onClick={() => setAutoscrollPaused((paused) => !paused)} aria-label={autoscrollPaused ? 'Resume terminal autoscroll' : 'Pause terminal autoscroll'}>
                {autoscrollPaused ? <IconPlayerPlay size={14} /> : <IconPlayerPause size={14} />}
              </ActionIcon>
            </Tooltip>
            <Tooltip label="Copy selection or visible lines"><ActionIcon variant="ghost" color="gray" onClick={() => void copySelection()} aria-label="Copy terminal selection"><IconCopy size={14} /></ActionIcon></Tooltip>
            <Text fz={10} c="dimmed" ff="monospace" className="terminalCount">
              {entries.length.toLocaleString()} / {total.toLocaleString()} lines
            </Text>
          </Group>
        </Group>

        {serverSnapshot.hasOlder ? (
          <Button fullWidth variant="ghost" size="xs" loading={loading} onClick={() => void loadOlder()} className="terminalLoadOlder">
            Load {Math.min(PAGE_SIZE, offset).toLocaleString()} older lines
          </Button>
        ) : null}

        <Box ref={parentRef} className={`terminalViewport${wrap ? ' terminalViewportWrap' : ''}`} role="log" aria-live="off" aria-busy={loading}>
          {entries.length ? (
            <Box style={{ height: virtualizer.getTotalSize(), position: 'relative' }}>
              {virtualizer.getVirtualItems().map((row) => {
                const entry = entries[row.index];
                return (
                  <Box
                    key={terminalKey(entry)}
                    ref={virtualizer.measureElement}
                    data-index={row.index}
                    className={`terminalLine terminalLine-${entry.level}${wrap ? ' terminalLineWrap' : ''}`}
                    style={{ transform: `translateY(${row.start}px)` }}
                  >
                    <Text component="span" className="terminalLineNumber">{offset + row.index + 1}</Text>
                    <time className="terminalTimestamp" dateTime={entry.at}>{entry.at ? new Date(entry.at).toLocaleTimeString([], { hour12: false, fractionalSecondDigits: 3 }) : '--:--:--'}</time>
                    <Text component="span" className="terminalLevel">{entry.level || 'info'}</Text>
                    <Text component="span" className="terminalMessage">{entry.message}</Text>
                  </Box>
                );
              })}
            </Box>
          ) : (
            <Box className="terminalEmpty"><Text fz="sm">{loading ? 'Loading terminal history…' : 'No terminal lines match this view.'}</Text></Box>
          )}
        </Box>
      </Paper>
      {loadError ? <Text c="red.4" fz="xs">{loadError}</Text> : null}
    </Stack>
  );
}
