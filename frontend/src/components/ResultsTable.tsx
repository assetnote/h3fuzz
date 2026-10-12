import { useVirtualizer } from '@tanstack/react-virtual';
import { useEffect, useRef, useState, type ReactNode } from 'react';
import { api } from '../api/client';
import type {
  NumericFacet,
  Result,
  ResultExplorerPage,
  ResultFacets,
  ResultFilterViewQuery,
  SavedResultView,
} from '../types/api';
import { copyText } from '../utils/clipboard';
import { resultCurl, resultLocation, resultTitle } from '../utils/resultView';
import {
  ActionIcon,
  Badge,
  Box,
  Button,
  Drawer,
  Group,
  Loader,
  Modal,
  MultiSelect,
  NumberInput,
  Paper,
  SegmentedControl,
  Select,
  SimpleGrid,
  Stack,
  Switch,
  Text,
  TextInput,
  Tooltip,
  notifications,
} from './ui';
import {
  IconAdjustments,
  IconBraces,
  IconCopy,
  IconFilter,
  IconRefresh,
  IconRoute,
  IconSearch,
  IconTrash,
} from './ui/icons';
import { ResultActions } from './ResultActions';
import { ResultDirectoryTree } from './ResultDirectoryTree';

interface ResultsTableProps {
  sessionID: string;
  results: Result[];
  loading?: boolean;
  totalResults?: number;
  windowTruncated?: boolean;
}

type ResultSort = 'created' | 'status' | 'url' | 'size' | 'words' | 'lines' | 'content_type' | 'duration';

const PAGE_SIZE = 1_000;
const EMPTY_NUMERIC_FACET: NumericFacet = { min: 0, max: 0 };
const EMPTY_FACETS: ResultFacets = {
  status: {},
  content_type: {},
  size: EMPTY_NUMERIC_FACET,
  words: EMPTY_NUMERIC_FACET,
  lines: EMPTY_NUMERIC_FACET,
};

function statusColor(status: number): string {
  if (status >= 500) return 'red';
  if (status >= 400) return 'yellow';
  if (status >= 300) return 'blue';
  if (status >= 200) return 'green';
  return 'gray';
}

function formatBytes(value: number): string {
  if (value < 1_024) return `${value} B`;
  if (value < 1_048_576) return `${(value / 1_024).toFixed(value < 10_240 ? 1 : 0)} KB`;
  return `${(value / 1_048_576).toFixed(1)} MB`;
}

function facetsFromResults(results: Result[]): ResultFacets {
  const status: Record<string, number> = {};
  const contentType: Record<string, number> = {};
  const values = (field: 'length' | 'words' | 'lines') => results.map((result) => result[field]);
  for (const result of results) {
    status[result.status] = (status[result.status] ?? 0) + 1;
    const content = result.content_type || 'unknown';
    contentType[content] = (contentType[content] ?? 0) + 1;
  }
  const numeric = (items: number[]): NumericFacet => items.length
    ? { min: Math.min(...items), max: Math.max(...items) }
    : EMPTY_NUMERIC_FACET;
  return {
    status,
    content_type: contentType,
    size: numeric(values('length')),
    words: numeric(values('words')),
    lines: numeric(values('lines')),
  };
}

function hasFilters(query: ResultFilterViewQuery): boolean {
  return Boolean(
    query.query || query.statuses?.length || query.contentTypes?.length
    || query.sizeMin !== undefined || query.sizeMax !== undefined
    || query.wordsMin !== undefined || query.wordsMax !== undefined
    || query.linesMin !== undefined || query.linesMax !== undefined
    || query.groupDuplicates,
  );
}

function matches(result: Result, query: ResultFilterViewQuery): boolean {
  if (query.statuses?.length && !query.statuses.includes(result.status)) return false;
  if (query.contentTypes?.length && !query.contentTypes.includes(result.content_type || 'unknown')) return false;
  if (query.sizeMin !== undefined && result.length < query.sizeMin) return false;
  if (query.sizeMax !== undefined && result.length > query.sizeMax) return false;
  if (query.wordsMin !== undefined && result.words < query.wordsMin) return false;
  if (query.wordsMax !== undefined && result.words > query.wordsMax) return false;
  if (query.linesMin !== undefined && result.lines < query.linesMin) return false;
  if (query.linesMax !== undefined && result.lines > query.linesMax) return false;
  if (query.query) {
    const needle = query.query.toLowerCase();
    const title = resultTitle(result) ?? '';
    if (!`${result.url}\n${result.content_type}\n${title}`.toLowerCase().includes(needle)) return false;
  }
  return true;
}

function numericValue(value: string | number): number | undefined {
  if (value === '') return undefined;
  const parsed = Number(value);
  return Number.isFinite(parsed) ? parsed : undefined;
}

function filterLabel(facet: NumericFacet, unit = ''): string {
  if (!facet || (facet.min === 0 && facet.max === 0)) return 'No values yet';
  return `${facet.min.toLocaleString()}${unit} to ${facet.max.toLocaleString()}${unit}`;
}

function safeDate(value: string): string {
  const parsed = new Date(value);
  return Number.isNaN(parsed.getTime()) ? value : parsed.toLocaleString();
}

function DetailValue({ label, children }: { label: string; children: ReactNode }) {
  return (
    <Box className="resultDetailValue">
      <Text className="resultDetailLabel">{label}</Text>
      <Text ff="monospace" fz="xs">{children}</Text>
    </Box>
  );
}

function ResultDetailDrawer({ result, onClose }: { result?: Result; onClose: () => void }) {
  if (!result) return <Drawer opened={false} onClose={onClose} />;
  const location = resultLocation(result);
  const title = resultTitle(result);
  const copy = (value: string, message: string) => {
    void copyText(value).then(
      () => notifications.show({ color: 'green', message }),
      (error: unknown) => notifications.show({
        color: 'red',
        title: 'Clipboard unavailable',
        message: error instanceof Error ? error.message : 'Could not copy this value.',
      }),
    );
  };
  return (
    <Drawer
      opened
      onClose={onClose}
      title="Result details"
      position="right"
      size="lg"
      classNames={{ content: 'resultDetailDrawer', body: 'resultDetailBody' }}
    >
      <Stack gap="md">
        <Box>
          <Group justify="space-between" gap="sm" wrap="nowrap">
            <Badge color={statusColor(result.status)} variant="light" ff="monospace">{result.status}</Badge>
            {(result.duplicate_count ?? 0) > 1 ? (
              <Badge color="gray" variant="light">{result.duplicate_count} exact captured bodies</Badge>
            ) : null}
          </Group>
          <Text className="resultDetailURL" ff="monospace" fz="sm" mt="sm">{result.url}</Text>
          {title ? <Text c="dimmed" fz="sm" mt={4}>{title}</Text> : null}
        </Box>
        <SimpleGrid cols={{ base: 2, sm: 3 }} spacing="sm">
          <DetailValue label="Size">{formatBytes(result.length)}</DetailValue>
          <DetailValue label="Words">{result.words.toLocaleString()}</DetailValue>
          <DetailValue label="Lines">{result.lines.toLocaleString()}</DetailValue>
          <DetailValue label="Content type">{result.content_type || 'Unknown'}</DetailValue>
          <DetailValue label="Duration">{(result.duration_ns / 1_000_000).toFixed(1)} ms</DetailValue>
          <DetailValue label="Discovered">{safeDate(result.created_at)}</DetailValue>
        </SimpleGrid>
        {result.redirect_location ? <DetailValue label="Redirect location">{result.redirect_location}</DetailValue> : null}
        <Paper withBorder p="sm" className="resultDetailActions">
          <Text className="resultDetailLabel" mb="xs">Actions</Text>
          <Group gap="xs">
            <Button size="xs" variant="soft" leftSection={<IconRoute size={14} />} onClick={() => copy(location.uri, 'Path copied')}>Copy path</Button>
            <Button size="xs" variant="soft" leftSection={<IconCopy size={14} />} onClick={() => copy(result.url, 'URL copied')}>Copy URL</Button>
            <Button size="xs" variant="soft" leftSection={<IconBraces size={14} />} onClick={() => copy(resultCurl(result.url), 'cURL command copied')}>Copy cURL</Button>
            {location.openURL ? (
              <Button component="a" href={location.openURL} target="_blank" rel="noopener noreferrer" size="xs" variant="soft">Open target</Button>
            ) : null}
          </Group>
        </Paper>
        {result.scraper && Object.keys(result.scraper).length ? (
          <Paper withBorder p="sm">
            <Text className="resultDetailLabel" mb="xs">Scraped values</Text>
            <pre className="resultDetailJSON">{JSON.stringify(result.scraper, null, 2)}</pre>
          </Paper>
        ) : null}
      </Stack>
    </Drawer>
  );
}

export function ResultsTable({
  sessionID,
  results,
  loading = false,
  totalResults = results.length,
}: ResultsTableProps) {
  const [view, setView] = useState<'list' | 'tree'>('list');
  const [filtersOpen, setFiltersOpen] = useState(false);
  const [query, setQuery] = useState<ResultFilterViewQuery>({ sort: 'created', order: 'desc' });
  const [search, setSearch] = useState('');
  const [debouncedSearch, setDebouncedSearch] = useState('');
  const [offset, setOffset] = useState(0);
  const [page, setPage] = useState<ResultExplorerPage>(() => ({
    results,
    total: totalResults,
    offset: 0,
    limit: PAGE_SIZE,
    has_more: results.length < totalResults,
    facets: facetsFromResults(results),
  }));
  const [facetUniverse, setFacetUniverse] = useState<ResultFacets>(() => facetsFromResults(results));
  const [pageLoading, setPageLoading] = useState(false);
  const [detail, setDetail] = useState<Result>();
  const [savedViews, setSavedViews] = useState<SavedResultView[]>([]);
  const [savedViewsLoading, setSavedViewsLoading] = useState(true);
  const [selectedView, setSelectedView] = useState('');
  const [saveOpened, setSaveOpened] = useState(false);
  const [viewName, setViewName] = useState('');
  const [savingView, setSavingView] = useState(false);
  const [reload, setReload] = useState(0);
  const viewportRef = useRef<HTMLDivElement>(null);
  const requestSequence = useRef(0);
  const facetRequestSequence = useRef(0);
  const refreshTimer = useRef<number | undefined>(undefined);
  const previousLiveIDs = useRef(new Set(results.map((result) => result.id)));

  useEffect(() => {
    const timer = window.setTimeout(() => setDebouncedSearch(search.trim()), 250);
    return () => window.clearTimeout(timer);
  }, [search]);

  useEffect(() => {
    setOffset(0);
    setQuery((current) => {
      const nextSearch = debouncedSearch || undefined;
      return current.query === nextSearch ? current : { ...current, query: nextSearch };
    });
  }, [debouncedSearch]);

  useEffect(() => {
    let current = true;
    setSavedViewsLoading(true);
    setSavedViews([]);
    setSelectedView('');
    void api.resultViews(sessionID).then((views) => {
      if (current) setSavedViews(views);
    }).catch((error: unknown) => {
      if (!current) return;
      notifications.show({
        color: 'red',
        title: 'Could not load saved views',
        message: error instanceof Error ? error.message : 'The saved filter views endpoint failed.',
      });
    }).finally(() => {
      if (current) setSavedViewsLoading(false);
    });
    return () => {
      current = false;
    };
  }, [sessionID]);

  useEffect(() => {
    const sequence = ++requestSequence.current;
    setPageLoading(true);
    void api.exploreResults(sessionID, { ...query, offset, limit: PAGE_SIZE }).then((nextPage) => {
      if (sequence !== requestSequence.current) return;
      const facets = nextPage.facets ?? EMPTY_FACETS;
      setPage({ ...nextPage, facets });
    }).catch((error: unknown) => {
      if (sequence !== requestSequence.current) return;
      notifications.show({
        color: 'red',
        title: 'Could not load results',
        message: error instanceof Error ? error.message : 'The results explorer request failed.',
      });
    }).finally(() => {
      if (sequence === requestSequence.current) setPageLoading(false);
    });
  }, [offset, query, reload, sessionID]);

  // Facet choices come from an independent unfiltered server aggregation.
  // They therefore cover the complete persisted scan, not the visible page,
  // and status options do not disappear when another status is selected.
  useEffect(() => {
    const sequence = ++facetRequestSequence.current;
    void api.exploreResults(sessionID, { sort: 'created', order: 'desc', offset: 0, limit: 1 }).then((nextPage) => {
      if (sequence !== facetRequestSequence.current) return;
      setFacetUniverse(nextPage.facets ?? EMPTY_FACETS);
    }).catch((error: unknown) => {
      if (sequence !== facetRequestSequence.current) return;
      notifications.show({
        color: 'red',
        title: 'Could not load result facets',
        message: error instanceof Error ? error.message : 'The result facet request failed.',
      });
    });
  }, [reload, sessionID]);

  // WebSocket discoveries should not disappear while the matching REST page is
  // in flight. The next server refresh remains authoritative.
  useEffect(() => {
    const previous = previousLiveIDs.current;
    const fresh = results.filter((result) => !previous.has(result.id));
    previousLiveIDs.current = new Set(results.map((result) => result.id));
    if (!fresh.length) return;
    const matching = fresh.filter((result) => matches(result, query));
    const optimisticOrder = (query.sort ?? 'created') === 'created'
      && (query.order ?? 'desc') === 'desc'
      && !query.groupDuplicates;
    if (matching.length && offset === 0 && optimisticOrder) {
      setPage((current) => {
        const byID = new Map(current.results.map((result) => [result.id, result]));
        let additions = 0;
        matching.forEach((result) => {
          if (!byID.has(result.id)) additions++;
          byID.set(result.id, result);
        });
        const merged = [...byID.values()].sort((left, right) => right.id - left.id).slice(0, current.limit);
        return { ...current, results: merged, total: current.total + additions };
      });
    }
    if (refreshTimer.current !== undefined) window.clearTimeout(refreshTimer.current);
    refreshTimer.current = window.setTimeout(() => setReload((value) => value + 1), 350);
    return () => {
      if (refreshTimer.current !== undefined) window.clearTimeout(refreshTimer.current);
    };
  }, [offset, query, results]);

  useEffect(() => () => {
    if (refreshTimer.current !== undefined) window.clearTimeout(refreshTimer.current);
  }, []);

  const virtualizer = useVirtualizer({
    count: page.results.length,
    getScrollElement: () => viewportRef.current,
    estimateSize: () => 48,
    getItemKey: (index) => page.results[index]?.id ?? index,
    overscan: 16,
  });

  const updateQuery = <K extends keyof ResultFilterViewQuery>(key: K, value: ResultFilterViewQuery[K]) => {
    setOffset(0);
    setSelectedView('');
    setQuery((current) => ({ ...current, [key]: value }));
  };

  const clearFilters = () => {
    setSearch('');
    setDebouncedSearch('');
    setSelectedView('');
    setOffset(0);
    setQuery({ sort: query.sort ?? 'created', order: query.order ?? 'desc' });
  };

  const applySavedView = (viewID: string | null) => {
    const saved = savedViews.find((item) => item.id === viewID);
    setSelectedView(viewID ?? '');
    if (!saved) return;
    const next = { sort: 'created', order: 'desc' as const, ...saved.query };
    setSearch(next.query ?? '');
    setDebouncedSearch(next.query ?? '');
    setOffset(0);
    setQuery(next);
  };

  const saveView = async () => {
    const name = viewName.trim();
    if (!name) return;
    setSavingView(true);
    try {
      const saved = await api.saveResultView(sessionID, name, query);
      setSavedViews((current) => [...current.filter((item) => item.id !== saved.id), saved]);
      setSelectedView(saved.id);
      setViewName('');
      setSaveOpened(false);
      notifications.show({ color: 'green', message: `Saved view “${saved.name}”` });
    } catch (error) {
      notifications.show({
        color: 'red',
        title: 'Could not save view',
        message: error instanceof Error ? error.message : 'The filter view could not be saved.',
      });
    } finally {
      setSavingView(false);
    }
  };

  const deleteView = async () => {
    if (!selectedView) return;
    try {
      await api.deleteResultView(sessionID, selectedView);
      setSavedViews((current) => current.filter((item) => item.id !== selectedView));
      setSelectedView('');
      notifications.show({ color: 'green', message: 'Saved view deleted' });
    } catch (error) {
      notifications.show({
        color: 'red',
        title: 'Could not delete view',
        message: error instanceof Error ? error.message : 'The filter view could not be deleted.',
      });
    }
  };

  const statuses = Object.entries(facetUniverse.status ?? {}).sort((a, b) => Number(a[0]) - Number(b[0]));
  const contentTypes = Object.entries(facetUniverse.content_type ?? {}).sort((a, b) => b[1] - a[1]);
  const activeFilters = hasFilters(query);
  const start = page.total ? page.offset + 1 : 0;
  const end = Math.min(page.offset + page.results.length, page.total);

  return (
    <Stack gap="sm" className="resultsPanel">
      <Paper withBorder p="sm" className="resultsExplorerToolbar">
        <Group justify="space-between" align="start" gap="sm">
          <Group className="resultsFilterGroup" gap="xs" align="end">
            <TextInput
              aria-label="Search all results"
              placeholder="Search URL, title, or content type"
              value={search}
              onChange={(event) => setSearch(event.currentTarget.value)}
              leftSection={<IconSearch size={15} />}
              className="resultsSearch"
            />
            <MultiSelect
              aria-label="Filter by status"
              placeholder="Status"
              searchable
              clearable
              data={statuses.map(([status, count]) => ({ value: status, label: `${status} (${count.toLocaleString()})` }))}
              value={(query.statuses ?? []).map(String)}
              onChange={(value) => updateQuery('statuses', value.map(Number))}
            />
            <MultiSelect
              aria-label="Filter by content type"
              placeholder="Content type"
              searchable
              clearable
              data={contentTypes.map(([contentType, count]) => ({
                value: contentType,
                label: `${contentType} (${count.toLocaleString()})`,
              }))}
              value={query.contentTypes ?? []}
              onChange={(value) => updateQuery('contentTypes', value)}
            />
            <Button
              className="resultsFacetToggle"
              size="md"
              variant={filtersOpen || activeFilters ? 'soft' : 'surface'}
              color={activeFilters ? 'violet' : 'gray'}
              leftSection={<IconFilter size={14} />}
              aria-expanded={filtersOpen}
              aria-controls="result-facet-panel"
              onClick={() => setFiltersOpen((open) => !open)}
            >
              Facets{activeFilters ? ' •' : ''}
            </Button>
          </Group>
          <Group gap="xs" className="resultsViewSwitch">
            <Tooltip label="Refresh results from the database">
              <ActionIcon aria-label="Refresh results" size="md" variant="soft" color="gray" onClick={() => setReload((value) => value + 1)}>
                <IconRefresh size={15} />
              </ActionIcon>
            </Tooltip>
            <SegmentedControl
              value={view}
              onChange={(next) => setView(next as 'list' | 'tree')}
              data={[{ value: 'list', label: 'List' }, { value: 'tree', label: 'Tree' }]}
            />
          </Group>
        </Group>

        {filtersOpen ? (
          <Box id="result-facet-panel" className="resultFacetPanel" mt="sm">
            <SimpleGrid cols={{ base: 1, sm: 2, lg: 3 }} spacing="sm">
              <Box className="resultFacetRange">
                <Text fw={600} fz="xs">Size</Text>
                <Text c="dimmed" fz={10}>{filterLabel(page.facets.size, ' B')}</Text>
                <Group gap="xs" wrap="nowrap">
                  <NumberInput value={query.sizeMin ?? ''} min={0} onChange={(value) => updateQuery('sizeMin', numericValue(value))} description="Minimum" />
                  <NumberInput value={query.sizeMax ?? ''} min={0} onChange={(value) => updateQuery('sizeMax', numericValue(value))} description="Maximum" />
                </Group>
              </Box>
              <Box className="resultFacetRange">
                <Text fw={600} fz="xs">Words</Text>
                <Text c="dimmed" fz={10}>{filterLabel(page.facets.words)}</Text>
                <Group gap="xs" wrap="nowrap">
                  <NumberInput value={query.wordsMin ?? ''} min={0} onChange={(value) => updateQuery('wordsMin', numericValue(value))} description="Minimum" />
                  <NumberInput value={query.wordsMax ?? ''} min={0} onChange={(value) => updateQuery('wordsMax', numericValue(value))} description="Maximum" />
                </Group>
              </Box>
              <Box className="resultFacetRange">
                <Text fw={600} fz="xs">Lines</Text>
                <Text c="dimmed" fz={10}>{filterLabel(page.facets.lines)}</Text>
                <Group gap="xs" wrap="nowrap">
                  <NumberInput value={query.linesMin ?? ''} min={0} onChange={(value) => updateQuery('linesMin', numericValue(value))} description="Minimum" />
                  <NumberInput value={query.linesMax ?? ''} min={0} onChange={(value) => updateQuery('linesMax', numericValue(value))} description="Maximum" />
                </Group>
              </Box>
            </SimpleGrid>
            <Group mt="sm" gap="sm" justify="space-between">
              <Switch
                checked={query.groupDuplicates ?? false}
                onChange={(event) => updateQuery('groupDuplicates', event.currentTarget.checked)}
                label="Group exact captured bodies"
                description="Collapse responses only when persisted captured-body fingerprints are identical. Bodyless responses stay separate."
              />
              <Button size="xs" variant="ghost" color="gray" onClick={clearFilters}>Clear filters</Button>
            </Group>
          </Box>
        ) : null}

        <Group mt="sm" justify="space-between" gap="sm" className="resultViewBar">
          <Group gap="xs" className="resultSavedViews">
            <Select
              aria-label="Saved filter views"
              placeholder={savedViewsLoading ? 'Loading views…' : savedViews.length ? 'Saved views' : 'No saved views'}
              clearable={Boolean(selectedView)}
              disabled={savedViewsLoading || savedViews.length === 0}
              data={savedViews.map((saved) => ({ value: saved.id, label: saved.name }))}
              value={selectedView || null}
              onChange={applySavedView}
            />
            <Button size="xs" variant="soft" onClick={() => setSaveOpened(true)}>Save view</Button>
            {selectedView ? (
              <Tooltip label="Delete saved view">
                <ActionIcon aria-label="Delete saved view" size="sm" variant="ghost" color="red" onClick={() => void deleteView()}>
                  <IconTrash size={14} />
                </ActionIcon>
              </Tooltip>
            ) : null}
          </Group>
          <Group gap="xs" className="resultSortControls">
            <IconAdjustments size={14} aria-hidden="true" />
            <Select
              aria-label="Sort results by"
              data={[
                { value: 'created', label: 'Discovered' },
                { value: 'status', label: 'Status' },
                { value: 'url', label: 'URL' },
                { value: 'size', label: 'Size' },
                { value: 'words', label: 'Words' },
                { value: 'lines', label: 'Lines' },
                { value: 'content_type', label: 'Content type' },
                { value: 'duration', label: 'Duration' },
              ]}
              value={query.sort ?? 'created'}
              onChange={(value) => updateQuery('sort', (value ?? 'created') as ResultSort)}
            />
            <SegmentedControl
              value={query.order ?? 'desc'}
              onChange={(value) => updateQuery('order', value as 'asc' | 'desc')}
              data={[{ value: 'asc', label: '↑' }, { value: 'desc', label: '↓' }]}
            />
          </Group>
        </Group>
      </Paper>

      {view === 'tree' ? (
        <ResultDirectoryTree
          sessionID={sessionID}
          query={query}
          revision={reload}
          loading={loading || pageLoading}
        />
      ) : (
        <Paper withBorder className="resultsTable">
          <Box ref={viewportRef} className="resultsViewport" aria-busy={loading || pageLoading}>
            <Box className="resultGrid resultHeader" role="row">
              <Text role="columnheader">URL</Text>
              <Text role="columnheader">Status</Text>
              <Text role="columnheader">Title</Text>
              <Text role="columnheader">Size</Text>
              <Text role="columnheader">Words</Text>
              <Text role="columnheader" ta="right">Actions</Text>
            </Box>
            {pageLoading && page.results.length ? <Box className="resultsLoadingBar"><Loader size="xs" /></Box> : null}
            {!page.results.length ? (
              <Box className="resultsEmpty resultsTableEmpty" role="status" aria-live="polite">
                <Text fw={600}>{loading || pageLoading ? 'Loading results…' : 'No matching results'}</Text>
                <Text fz="xs" c="dimmed" mt={3}>
                  {activeFilters ? 'Adjust the filters or clear them to widen this view.' : 'Discoveries will appear here as they arrive.'}
                </Text>
              </Box>
            ) : (
              <Box
                role="rowgroup"
                style={{ height: virtualizer.getTotalSize(), minWidth: 940, position: 'relative' }}
              >
                {virtualizer.getVirtualItems().map((virtualRow) => {
                  const result = page.results[virtualRow.index];
                  const location = resultLocation(result);
                  const title = resultTitle(result);
                  return (
                    <Box
                      key={result.id}
                      className="resultGrid resultRow"
                      role="row"
                      tabIndex={0}
                      aria-label={`Inspect ${result.url}`}
                      style={{ transform: `translateY(${virtualRow.start}px)` }}
                      onClick={() => setDetail(result)}
                      onKeyDown={(event) => {
                        if (event.key === 'Enter' || event.key === ' ') {
                          event.preventDefault();
                          setDetail(result);
                        }
                      }}
                    >
                      <Box role="cell" className="resultURLCell">
                        <Tooltip label={result.url} openDelay={600}>
                          <Text className="resultURL" ff="monospace" fz="xs">{result.url}</Text>
                        </Tooltip>
                        <Group gap={4} wrap="nowrap" className="resultSignatureBadges">
                          {(result.duplicate_count ?? 0) > 1 ? <Badge color="gray" variant="light" size="xs">×{result.duplicate_count}</Badge> : null}
                        </Group>
                      </Box>
                      <Box role="cell" className="resultStatusCell">
                        <Badge color={statusColor(result.status)} variant="light" size="sm" ff="monospace">{result.status}</Badge>
                      </Box>
                      <Text role="cell" className={`resultTitle${title ? '' : ' isUnavailable'}`} fz="xs">{title ?? 'No title'}</Text>
                      <Text role="cell" ff="monospace" fz="xs">{formatBytes(result.length)}</Text>
                      <Text role="cell" ff="monospace" fz="xs">{result.words.toLocaleString()}</Text>
                      <Box role="cell" className="resultActionCell">
                        <ResultActions uri={location.uri} url={result.url} openURL={location.openURL} label={result.url} />
                      </Box>
                    </Box>
                  );
                })}
              </Box>
            )}
          </Box>
        </Paper>
      )}

      {view === 'list' ? (
        <Group justify="space-between" gap="sm" className="resultPagination">
          <Text fz="xs" c="dimmed" ff="monospace">
            {start.toLocaleString()}–{end.toLocaleString()} of {page.total.toLocaleString()}
            {query.groupDuplicates ? ' groups' : ' results'}
          </Text>
          <Group gap="xs">
            <Button
              size="xs"
              variant="soft"
              color="gray"
              disabled={page.offset === 0 || pageLoading}
              onClick={() => setOffset(Math.max(0, page.offset - page.limit))}
            >
              Previous
            </Button>
            <Button
              size="xs"
              variant="soft"
              color="gray"
              disabled={!page.has_more || pageLoading}
              onClick={() => setOffset(page.offset + page.limit)}
            >
              Next
            </Button>
          </Group>
        </Group>
      ) : null}

      <ResultDetailDrawer result={detail} onClose={() => setDetail(undefined)} />

      <Modal opened={saveOpened} onClose={() => setSaveOpened(false)} title="Save filter view" size="sm" centered>
        <Stack gap="md">
          <Text fz="sm" c="dimmed">Save the current facets, sort order, and exact-response grouping for this scan.</Text>
          <TextInput
            label="View name"
            placeholder="Interesting API responses"
            value={viewName}
            onChange={(event) => setViewName(event.currentTarget.value)}
            autoFocus
          />
          <Group justify="end" gap="xs">
            <Button variant="ghost" color="gray" onClick={() => setSaveOpened(false)}>Cancel</Button>
            <Button loading={savingView} disabled={!viewName.trim()} onClick={() => void saveView()}>Save view</Button>
          </Group>
        </Stack>
      </Modal>
    </Stack>
  );
}
