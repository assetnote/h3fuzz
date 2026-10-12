import {
  ActionIcon,
  Alert,
  Badge,
  Box,
  Button,
  Group,
  Loader,
  Menu,
  Modal,
  Paper,
  Progress,
  SimpleGrid,
  Stack,
  Tabs,
  Text,
  ThemeIcon,
  Title,
  Tooltip,
} from './ui';
import { notifications } from './ui/toast';
import {
  IconAlertTriangle,
  IconBolt,
  IconBraces,
  IconCheck,
  IconChevronDown,
  IconClock,
  IconCopy,
  IconDatabaseExport,
  IconDots,
  IconDownload,
  IconFileText,
  IconPlayerPause,
  IconPlayerPlay,
  IconPlayerStop,
  IconRefresh,
  IconRoute,
  IconTerminal2,
  IconTrash,
  IconWorld,
  IconZoomCheck,
} from './ui/icons';
import { useEffect, useRef, useState } from 'react';
import { Area, AreaChart, ResponsiveContainer, Tooltip as ChartTooltip, YAxis } from 'recharts';
import { api } from '../api/client';
import type { Result, Session, SessionFilterPatch, TerminalEntry } from '../types/api';
import { copyText } from '../utils/clipboard';
import { formatNumber, formatRate, progressPercent, relativeTime } from '../utils/format';
import { effectiveTransport } from '../utils/transport';
import type { RatePoint } from '../utils/rateHistory';
import { ResultsTable } from './ResultsTable';
import { SessionFilters } from './SessionFilters';
import { ErrorDiagnosticsPanel, ProtocolPreflightPanel } from './SessionTelemetry';
import { StatusIndicator } from './StatusIndicator';
import { TerminalExplorer } from './TerminalExplorer';

interface SessionDashboardProps {
  session: Session;
  results: Result[];
  terminal: TerminalEntry[];
  rateHistory: RatePoint[];
  loadingResults?: boolean;
  resultsTruncated?: boolean;
  actionPending?: boolean;
  actionsDisabled?: boolean;
  deletePending?: boolean;
  organizationPending?: boolean;
  preflightPending?: boolean;
  filtersPending?: boolean;
  onAction: (action: 'start' | 'pause' | 'resume' | 'stop') => void;
  onRunPreflight?: () => void;
  onAddFilters?: (filters: SessionFilterPatch) => Promise<void>;
  onCloneEdit?: () => void;
  onCompare?: () => void;
  onArchive?: () => void;
  onDelete: () => void;
  onRefresh: () => void;
}

type ExportFormat = 'json' | 'text' | 'terminal';

interface ExportPreviewState {
  format: ExportFormat;
  loading: boolean;
  content: string;
  capturedBytes: number;
  totalBytes?: number;
  truncated: boolean;
  error?: string;
}

const THROUGHPUT_COLLAPSED_STORAGE_KEY = 'h3fuzz.throughput.collapsed';

function storedThroughputPreference(): boolean {
  if (typeof window === 'undefined') return false;
  try {
    return window.localStorage.getItem(THROUGHPUT_COLLAPSED_STORAGE_KEY) === 'true';
  } catch {
    return false;
  }
}

function storeThroughputPreference(collapsed: boolean): void {
  if (typeof window === 'undefined') return;
  try {
    window.localStorage.setItem(THROUGHPUT_COLLAPSED_STORAGE_KEY, String(collapsed));
  } catch {
    // Storage can be unavailable in hardened or private browser contexts.
  }
}

function byteSize(value?: number): string {
  if (value === undefined) return 'unknown size';
  if (value < 1024) return `${value} B`;
  if (value < 1024 * 1024) return `${(value / 1024).toFixed(1)} KiB`;
  return `${(value / (1024 * 1024)).toFixed(1)} MiB`;
}

function optionalRate(value: number | undefined): string {
  return value === undefined ? '—' : formatRate(value);
}

function etaLabel(value: number | null | undefined): string {
  if (value === undefined || value === null || !Number.isFinite(value) || value < 0) return '—';
  if (value < 60) return `${Math.ceil(value)}s`;
  if (value < 3600) return `${Math.floor(value / 60)}m ${Math.ceil(value % 60)}s`;
  const hours = Math.floor(value / 3600);
  const minutes = Math.ceil((value % 3600) / 60);
  return `${hours}h ${minutes}m`;
}

export function SessionDashboard({
  session,
  results,
  terminal,
  rateHistory,
  loadingResults,
  resultsTruncated,
  actionPending,
  actionsDisabled,
  deletePending,
  organizationPending,
  preflightPending,
  filtersPending,
  onAction,
  onRunPreflight,
  onAddFilters,
  onCloneEdit,
  onCompare,
  onArchive,
  onDelete,
  onRefresh,
}: SessionDashboardProps) {
  const [confirmDelete, setConfirmDelete] = useState(false);
  const [exportPreview, setExportPreview] = useState<ExportPreviewState>();
  const [terminalTotal, setTerminalTotal] = useState(terminal.length);
  const [terminalRefreshKey, setTerminalRefreshKey] = useState(0);
  const [throughputCollapsed, setThroughputCollapsed] = useState(storedThroughputPreference);
  const exportRequest = useRef<AbortController | undefined>(undefined);
  const scheduledCount = session.progress.request_count ?? session.progress.req_count;
  const requestCount = session.progress.completed_count
    ?? (session.progress.response_count === undefined
      ? scheduledCount
      : Math.min(scheduledCount, Math.max(0, session.progress.response_count + session.progress.error_count)));
  const currentRate = session.progress.current_rate ?? session.progress.req_sec;
  const progress = progressPercent(requestCount, session.progress.req_total);
  const transport = effectiveTransport(session);
  const waitingForTarget =
    session.progress.stall_reason === 'no completed requests while result persistence has capacity';
  const preflightAllowed = !['starting', 'running', 'paused', 'stopping'].includes(session.status);

  useEffect(() => {
    exportRequest.current?.abort();
    exportRequest.current = undefined;
    setExportPreview(undefined);
    return () => exportRequest.current?.abort();
  }, [session.id]);

  useEffect(() => {
    setTerminalTotal(0);
    setTerminalRefreshKey(0);
  }, [session.id]);

  const loadExportPreview = async (format: ExportFormat) => {
    exportRequest.current?.abort();
    const controller = new AbortController();
    exportRequest.current = controller;
    setExportPreview({ format, loading: true, content: '', capturedBytes: 0, truncated: false });
    try {
      const preview = await api.exportPreview(session.id, format, undefined, controller.signal);
      if (exportRequest.current !== controller) return;
      setExportPreview({
        format,
        loading: false,
        content: preview.content,
        capturedBytes: preview.captured_bytes,
        totalBytes: preview.total_bytes,
        truncated: preview.truncated,
      });
    } catch (error) {
      if (controller.signal.aborted || exportRequest.current !== controller) return;
      setExportPreview({
        format,
        loading: false,
        content: '',
        capturedBytes: 0,
        truncated: false,
        error: error instanceof Error ? error.message : 'Could not load the export preview.',
      });
    }
  };

  const closeExportPreview = () => {
    exportRequest.current?.abort();
    exportRequest.current = undefined;
    setExportPreview(undefined);
  };

  return (
    <Box
      className="dashboardPage"
      role="tabpanel"
      aria-labelledby={`session-tab-${session.id}`}
    >
      <Box className="dashboardHeader">
        <Group justify="space-between" align="flex-start" wrap="wrap">
          <Box className="dashboardIdentity">
            <Group gap="xs" mb={7}>
              <StatusIndicator status={session.status} />
              <Tooltip
                label={session.runtime_transport_reason || 'Effective transport selected by the runtime probe.'}
                disabled={!session.runtime_transport_reason && session.status !== 'running'}
              >
                <Badge color={transport.color} variant="outline" size="sm">
                  {transport.label}
                </Badge>
              </Tooltip>
              {session.response_optimization_strategy ? (
                <Tooltip
                  label={session.response_optimization_reason || 'Effective response-size optimization.'}
                >
                  <Badge color="violet" variant="light" size="sm">
                    {session.response_optimization_strategy}
                  </Badge>
                </Tooltip>
              ) : null}
              <Text c="dimmed" fz="xs">
                Updated {relativeTime(session.updated_at)}
              </Text>
            </Group>
            {session.runtime_transport_reason && session.runtime_transport !== 'http3' ? (
              <Text c="yellow.4" fz="xs" mb={5} lineClamp={2}>
                {session.runtime_transport_reason}
              </Text>
            ) : null}
            <Title order={2} className="dashboardTitle">
              {session.name}
            </Title>
            <Group gap={6} mt={5} wrap="nowrap">
              <IconWorld size={14} color="var(--gray-11)" />
              <Text ff="monospace" c="dimmed" fz="sm" className="dashboardTarget">
                {session.target}
              </Text>
            </Group>
          </Box>
          <Group gap="xs" wrap="nowrap" className="dashboardActions">
            <Tooltip label="Refresh from database">
              <ActionIcon variant="default" size="md" onClick={() => { onRefresh(); setTerminalRefreshKey((value) => value + 1); }} aria-label="Refresh session">
                <IconRefresh size={17} />
              </ActionIcon>
            </Tooltip>
            <Menu shadow="xl" position="bottom-end" withinPortal>
              <Menu.Target>
                <Button size="md" variant="default" rightSection={<IconChevronDown size={14} />}>
                  Export
                </Button>
              </Menu.Target>
              <Menu.Dropdown>
                <Menu.Label>Download results</Menu.Label>
                <Menu.Item
                  leftSection={<IconBraces size={15} />}
                  onClick={() => void loadExportPreview('json')}
                >
                  JSON
                </Menu.Item>
                <Menu.Item
                  leftSection={<IconFileText size={15} />}
                  onClick={() => void loadExportPreview('text')}
                >
                  Paths only
                </Menu.Item>
                <Menu.Item
                  leftSection={<IconTerminal2 size={15} />}
                  onClick={() => void loadExportPreview('terminal')}
                >
                  Terminal output
                </Menu.Item>
              </Menu.Dropdown>
            </Menu>
            <Menu shadow="xl" position="bottom-end" withinPortal>
              <Menu.Target>
                <ActionIcon size="md" variant="default" aria-label="More session actions"><IconDots size={17} /></ActionIcon>
              </Menu.Target>
              <Menu.Dropdown>
                <Menu.Label>Session actions</Menu.Label>
                {onCloneEdit ? <Menu.Item leftSection={<IconCopy size={15} />} onClick={onCloneEdit}>Clone, edit, and rerun</Menu.Item> : null}
                {onCompare ? <Menu.Item leftSection={<IconRoute size={15} />} onClick={onCompare}>Compare with another run</Menu.Item> : null}
                {onArchive ? (
                  <Menu.Item disabled={organizationPending} leftSection={<IconDatabaseExport size={15} />} onClick={onArchive}>
                    {session.archived ? 'Restore session' : 'Archive session'}
                  </Menu.Item>
                ) : null}
                <Menu.Divider />
                <Menu.Item disabled={deletePending} leftSection={<IconTrash size={15} />} onClick={() => setConfirmDelete(true)}>Delete session</Menu.Item>
              </Menu.Dropdown>
            </Menu>
            <SessionActionButton session={session} pending={actionPending} disabled={actionsDisabled} onAction={onAction} />
            {['running', 'paused'].includes(session.status) ? (
              <Tooltip label="Stop session">
                <ActionIcon
                  size="md"
                  color="red"
                  variant="light"
                  disabled={actionsDisabled}
                  onClick={() => onAction('stop')}
                  aria-label="Stop session"
                >
                  <IconPlayerStop size={17} />
                </ActionIcon>
              </Tooltip>
            ) : null}
          </Group>
        </Group>

        <Box mt="lg">
          <Group justify="space-between" mb={6}>
            <Text fz="xs" c="dimmed">
              {formatNumber(requestCount)} of {formatNumber(session.progress.req_total)} requests
            </Text>
            <Text fz="xs" ff="monospace" c={session.status === 'running' ? 'green.4' : 'dimmed'}>
              {progress.toFixed(1)}%
            </Text>
          </Group>
          <Progress
            value={progress}
            size={4}
            color={session.status === 'failed' ? 'red' : session.status === 'paused' ? 'yellow' : 'violet'}
            animated={session.status === 'running'}
          />
        </Box>
      </Box>

      <Box className="dashboardScroll">
        {session.error ? (
          <Alert color="red" icon={<IconAlertTriangle size={17} />} mb="md" title="Session failed">
            {session.error}
          </Alert>
        ) : null}
        {session.progress.persistence_backpressure ? (
          <Alert color="yellow" variant="light" mb="md" title={`Persisting ${(session.progress.results_queued ?? 0).toLocaleString()} results`}>
            The persistence queue is full, so request workers are waiting for SQLite. A zero request rate here is storage backpressure, not a remote response stall.
            {session.progress.last_batch_duration_ms
              ? ` Last batch: ${formatNumber(session.progress.last_batch_duration_ms)} ms.`
              : ''}
          </Alert>
        ) : null}
        {waitingForTarget ? (
          <Alert color="yellow" variant="light" mb="md" title="Waiting for target responses">
            The server may be throttling or requests may be approaching timeout.
            {session.progress.stalled_for_ms
              ? ` No requests have completed for ${(session.progress.stalled_for_ms / 1000).toFixed(1)} seconds.`
              : ''}
          </Alert>
        ) : null}
        <Paper
          withBorder
          p="md"
          mb="md"
          className="throughputPanel"
          aria-label="Stable throughput metrics"
          data-collapsed={throughputCollapsed ? 'true' : 'false'}
        >
          <Group justify="space-between" align="flex-end" gap="md" mb={throughputCollapsed ? 0 : 'sm'}>
            <Box>
              <Text className="telemetryEyebrow">Stable telemetry</Text>
              <Title order={3}>Throughput</Title>
              <Text c="dimmed" fz="xs">
                {throughputCollapsed
                  ? 'Essential live scan health from the server clock.'
                  : 'Rolling, windowed, and lifetime rates from the server clock.'}
              </Text>
            </Box>
            <Group gap="xs" wrap="nowrap">
              <Badge color={session.status === 'running' ? 'green' : 'gray'} variant="light">
                {session.status === 'running' ? 'Live' : session.status}
              </Badge>
              <Tooltip label={throughputCollapsed ? 'Show all throughput metrics' : 'Show essential throughput metrics only'}>
                <ActionIcon
                  size="sm"
                  variant="default"
                  aria-label={throughputCollapsed ? 'Expand throughput metrics' : 'Minimize throughput metrics'}
                  aria-expanded={!throughputCollapsed}
                  aria-controls={`throughput-details-${session.id}`}
                  data-collapsed={throughputCollapsed ? 'true' : 'false'}
                  className="throughputCollapseButton"
                  onClick={() => setThroughputCollapsed((collapsed) => {
                    const next = !collapsed;
                    storeThroughputPreference(next);
                    return next;
                  })}
                >
                  <IconChevronDown size={15} />
                </ActionIcon>
              </Tooltip>
            </Group>
          </Group>
          <div id={`throughput-details-${session.id}`}>
            {throughputCollapsed ? (
              <div className="throughputCompactGrid" aria-label="Essential throughput summary">
                <CompactMetric label="Current rolling" value={optionalRate(currentRate)} />
                <CompactMetric label="30s average" value={optionalRate(session.progress.average_30s)} />
                <CompactMetric label="ETA" value={etaLabel(session.progress.eta_seconds)} />
                <CompactMetric label="Requests" value={formatNumber(requestCount)} />
                <CompactMetric label="Errors" value={formatNumber(session.progress.error_count)} tone={session.progress.error_count ? 'error' : 'default'} />
              </div>
            ) : (
              <>
              <SimpleGrid cols={{ base: 2, sm: 3, xl: 4 }} spacing="sm" className="throughputMetricGrid">
                <Metric label="Current rolling" value={optionalRate(currentRate)} icon={IconRoute} accent="green" />
                <Metric label="30s average" value={optionalRate(session.progress.average_30s)} icon={IconClock} accent="cyan" />
                <Metric label="Overall average" value={optionalRate(session.progress.overall_average)} icon={IconRoute} />
                <Metric label="Peak rate" value={optionalRate(session.progress.peak_rate)} icon={IconBolt} accent="yellow" />
                <Metric label="ETA" value={etaLabel(session.progress.eta_seconds)} icon={IconClock} accent="cyan" />
                <Metric label="Requests" value={formatNumber(requestCount)} icon={IconZoomCheck} />
                <Metric label="Responses" value={session.progress.response_count === undefined ? '—' : formatNumber(session.progress.response_count)} icon={IconCheck} accent="green" />
                <Metric
                  label="Errors"
                  value={formatNumber(session.progress.error_count)}
                  icon={IconAlertTriangle}
                  accent={session.progress.error_count ? 'red' : 'gray'}
                />
              </SimpleGrid>
              <div className="throughputHistory">
                <Group justify="space-between" gap={5}>
                  <Text className="telemetryEyebrow">Rolling rate history</Text>
                  <Text fz={10} c="green.4" ff="monospace">60s</Text>
                </Group>
                <Box h={58} mt={4}>
                  <ResponsiveContainer width="100%" height="100%">
                    <AreaChart data={rateHistory} margin={{ top: 2, right: 0, bottom: 0, left: 0 }}>
                      <defs>
                        <linearGradient id={`rate-${session.id}`} x1="0" y1="0" x2="0" y2="1">
                          <stop offset="0%" stopColor="#40c057" stopOpacity={0.42} />
                          <stop offset="100%" stopColor="#40c057" stopOpacity={0} />
                        </linearGradient>
                      </defs>
                      <YAxis hide domain={[0, 'dataMax']} />
                      <ChartTooltip
                        contentStyle={{ background: '#151922', border: '1px solid #303643', fontSize: 11 }}
                        formatter={(value) => [formatRate(Number(value)), 'Rate']}
                        labelFormatter={() => ''}
                      />
                      <Area
                        type="monotone"
                        dataKey="value"
                        stroke="#40c057"
                        strokeWidth={1.5}
                        fill={`url(#rate-${session.id})`}
                        isAnimationActive={false}
                      />
                    </AreaChart>
                  </ResponsiveContainer>
                </Box>
              </div>
              <Group gap="md" mt="sm" className="throughputLegacyStrip">
                <Text fz="xs"><span>Discoveries</span>{formatNumber(session.result_count)}</Text>
                <Text fz="xs"><span>Persistence queue</span>{formatNumber(session.progress.results_queued ?? 0)}</Text>
                <Text fz="xs"><span>Scheduler queue</span>{formatNumber(session.progress.queue_pos)} / {formatNumber(session.progress.queue_total)}</Text>
                </Group>
              </>
            )}
          </div>
        </Paper>

        <Tabs defaultValue="results" keepMounted={false} className="sessionTabs">
          <Tabs.List>
            <Tabs.Tab value="results" aria-label={`Results (${session.result_count})`} leftSection={<IconZoomCheck size={15} />}>
              Results <Badge size="xs" variant="light" color="gray">{session.result_count}</Badge>
            </Tabs.Tab>
            <Tabs.Tab value="terminal" aria-label={`Terminal (${Math.max(terminalTotal, terminal.length)})`} leftSection={<IconTerminal2 size={15} />}>
              Terminal <Badge size="xs" variant="light" color="gray">{Math.max(terminalTotal, terminal.length)}</Badge>
            </Tabs.Tab>
            <Tabs.Tab value="preflight" aria-label="Preflight" leftSection={<IconBolt size={15} />}>
              Preflight
            </Tabs.Tab>
            <Tabs.Tab value="configuration" aria-label="Configuration" leftSection={<IconBraces size={15} />}>
              Configuration
            </Tabs.Tab>
          </Tabs.List>
          <Tabs.Panel value="results" pt="md">
            {onAddFilters ? (
              <SessionFilters session={session} saving={filtersPending} onAdd={onAddFilters} />
            ) : null}
            <ResultsTable
              key={`${session.id}:${JSON.stringify(session.options.filters)}`}
              sessionID={session.id}
              results={results}
              loading={loadingResults}
              totalResults={session.result_count}
              windowTruncated={resultsTruncated}
            />
          </Tabs.Panel>
          <Tabs.Panel value="terminal" pt="md">
            <TerminalExplorer sessionID={session.id} liveLines={terminal} refreshKey={terminalRefreshKey} onTotalChange={setTerminalTotal} />
          </Tabs.Panel>
          <Tabs.Panel value="preflight" pt="md">
            <Stack gap="md" className="telemetrySurface">
              <ProtocolPreflightPanel
                report={session.preflight_report}
                pending={preflightPending}
                onRun={preflightAllowed ? onRunPreflight : undefined}
                runDisabledReason={preflightAllowed ? undefined : 'Run it again after the active scan stops.'}
              />
              <ErrorDiagnosticsPanel diagnostics={session.error_diagnostics ?? []} />
            </Stack>
          </Tabs.Panel>
          <Tabs.Panel value="configuration" pt="md">
            <ConfigurationView session={session} onDelete={() => setConfirmDelete(true)} />
          </Tabs.Panel>
        </Tabs>
      </Box>

      <Modal
        opened={Boolean(exportPreview)}
        onClose={closeExportPreview}
        title={`${exportPreview?.format === 'text' ? 'Paths' : exportPreview?.format === 'json' ? 'JSON' : 'Terminal'} export preview`}
        centered
        size="xl"
      >
        {exportPreview?.loading ? (
          <Stack align="center" justify="center" mih={280} gap="sm">
            <Loader size="sm" />
            <Text c="dimmed" fz="sm">Loading a bounded preview…</Text>
          </Stack>
        ) : exportPreview?.error ? (
          <Alert color="red" title="Could not load preview" icon={<IconAlertTriangle size={16} />}>
            <Text fz="sm">{exportPreview.error}</Text>
            <Button mt="sm" size="xs" variant="light" onClick={() => void loadExportPreview(exportPreview.format)}>
              Try again
            </Button>
          </Alert>
        ) : exportPreview ? (
          <Stack gap="sm">
            <Group justify="space-between" align="center">
              <Text c="dimmed" fz="xs">
                Previewed {byteSize(exportPreview.capturedBytes)}
                {exportPreview.totalBytes !== undefined ? ` of ${byteSize(exportPreview.totalBytes)}` : ''}
              </Text>
              <Group gap="xs">
                <Button
                  size="xs"
                  variant="default"
                  leftSection={<IconCopy size={14} />}
                  disabled={!exportPreview.content}
                  onClick={() => {
                    void copyText(exportPreview.content)
                      .then(() => notifications.show({ color: 'green', message: 'Preview copied' }))
                      .catch((error: unknown) =>
                        notifications.show({
                          color: 'red',
                          title: 'Could not copy preview',
                          message: error instanceof Error ? error.message : 'Clipboard access failed.',
                        }),
                      );
                  }}
                >
                  Copy
                </Button>
                <Button
                  component="a"
                  href={api.exportURL(session.id, exportPreview.format)}
                  download
                  size="xs"
                  leftSection={<IconDownload size={14} />}
                >
                  Download full export
                </Button>
              </Group>
            </Group>
            {exportPreview.truncated ? (
              <Alert color="yellow" variant="light">
                This preview is capped at 200 records and 256 KiB. Download the full export for all content.
              </Alert>
            ) : null}
            <Box component="pre" className="exportPreview" aria-label="Export preview">
              {exportPreview.content || 'The export is empty.'}
            </Box>
          </Stack>
        ) : null}
      </Modal>

      <Modal
        opened={confirmDelete}
        onClose={() => setConfirmDelete(false)}
        title="Delete this session?"
        centered
        size="sm"
      >
        <Text c="dimmed" fz="sm">
          The persisted configuration, discoveries, and terminal history for {session.name} will be removed. This cannot be undone.
        </Text>
        <Group justify="flex-end" mt="xl">
          <Button variant="default" onClick={() => setConfirmDelete(false)}>
            Cancel
          </Button>
          <Button
            color="red"
            leftSection={<IconTrash size={15} />}
            loading={deletePending}
            disabled={deletePending}
            onClick={onDelete}
          >
            Delete session
          </Button>
        </Group>
      </Modal>
    </Box>
  );
}

function SessionActionButton({
  session,
  pending,
  disabled,
  onAction,
}: {
  session: Session;
  pending?: boolean;
  disabled?: boolean;
  onAction: (action: 'start' | 'pause' | 'resume') => void;
}) {
  if (session.status === 'stopping') {
    return (
      <Button
        size="md"
        variant="light"
        color="orange"
        leftSection={<IconPlayerStop size={16} />}
        disabled
      >
        Stopping
      </Button>
    );
  }
  if (session.status === 'starting') {
    return (
      <Button
        size="md"
        variant="light"
        color="violet"
        leftSection={<Loader size={14} />}
        disabled
      >
        Starting
      </Button>
    );
  }
  if (session.status === 'running') {
    return (
      <Button
        size="md"
        variant="light"
      color="yellow"
      leftSection={<IconPlayerPause size={16} />}
      loading={pending}
      disabled={disabled}
      onClick={() => onAction('pause')}
      >
        Pause
      </Button>
    );
  }
  if (session.status === 'idle' || session.status === 'paused') {
    return (
      <Button
      size="md"
      leftSection={<IconPlayerPlay size={16} />}
      loading={pending}
      disabled={disabled}
      onClick={() => onAction(session.status === 'paused' ? 'resume' : 'start')}
      >
        {session.status === 'paused' ? 'Resume' : 'Start'}
      </Button>
    );
  }
  return null;
}

function CompactMetric({ label, value, tone = 'default' }: { label: string; value: string; tone?: 'default' | 'error' }) {
  return (
    <div className="throughputCompactMetric" data-tone={tone}>
      <Text className="telemetryEyebrow">{label}</Text>
      <Text fw={650} ff="monospace" className="metricValue">{value}</Text>
    </div>
  );
}

function Metric({
  label,
  value,
  icon: Icon,
  accent = 'violet',
}: {
  label: string;
  value: string;
  icon: typeof IconClock;
  accent?: string;
}) {
  return (
    <Paper withBorder p="sm" className="metricCard">
      <Group gap="xs" wrap="nowrap">
        <ThemeIcon variant="light" color={accent} size={29} radius="md">
          <Icon size={15} />
        </ThemeIcon>
        <Box className="metricContent">
          <Text fz={10} c="dimmed" tt="uppercase" fw={700} lts="0.06em">
            {label}
          </Text>
          <Text fw={650} mt={1} ff="monospace" className="metricValue">
            {value}
          </Text>
        </Box>
      </Group>
    </Paper>
  );
}

function ConfigurationView({ session, onDelete }: { session: Session; onDelete: () => void }) {
  return (
    <Stack gap="md">
      <Paper withBorder p="md">
        <Group justify="space-between" mb="sm">
          <Box>
            <Text fw={600}>Effective options</Text>
            <Text fz="xs" c="dimmed">
              Stored with this session for reproducibility.
            </Text>
          </Box>
          <Badge color="gray" variant="light">
            {session.profile}
          </Badge>
        </Group>
        <Box component="pre" className="configCode">
          {JSON.stringify(session.options, null, 2)}
        </Box>
      </Paper>
      <Paper withBorder p="md" className="dangerZone">
        <Group justify="space-between">
          <Box>
            <Text fw={600}>Delete session</Text>
            <Text fz="xs" c="dimmed">
              Remove the session and all persisted results.
            </Text>
          </Box>
          <Button color="red" variant="light" leftSection={<IconTrash size={15} />} onClick={onDelete}>
            Delete
          </Button>
        </Group>
      </Paper>
    </Stack>
  );
}
