import {
  CheckCircledIcon,
  CrossCircledIcon,
  ExclamationTriangleIcon,
  InfoCircledIcon,
  LightningBoltIcon,
  ReloadIcon,
} from '@radix-ui/react-icons';
import { useState } from 'react';
import type {
  ErrorDiagnostic,
  HTTP3Fingerprint,
  PreflightCheck,
  ProtocolPreflightReport,
} from '../types/api';
import { formatNumber, relativeTime } from '../utils/format';
import { Badge, Box, Button, Group, Paper, Stack, Tabs, Text, ThemeIcon, Title } from './ui';

const CATEGORY_LABELS: Record<string, string> = {
  dns: 'DNS',
  tls: 'TLS',
  quic_handshake: 'QUIC handshake',
  timeout: 'Timeout',
  connection_reset: 'Connection reset',
  rate_limited: 'Rate limited',
  persistence_backpressure: 'Persistence',
  other: 'Other',
};

function humanize(value: string): string {
  if (!value) return 'Not reported';
  return CATEGORY_LABELS[value] ?? value.replace(/[_-]+/g, ' ').replace(/^\w/, (letter) => letter.toUpperCase());
}

function formatMilliseconds(value: number | undefined): string {
  if (value === undefined || !Number.isFinite(value)) return '—';
  if (value < 1000) return `${formatNumber(value)} ms`;
  return `${(value / 1000).toFixed(value < 10_000 ? 2 : 1)} s`;
}

function yesNo(value: boolean | undefined): string {
  return value === undefined ? '—' : value ? 'Yes' : 'No';
}

function timestampLabel(value: string | undefined): string | undefined {
  if (!value) return undefined;
  const timestamp = new Date(value);
  return Number.isNaN(timestamp.getTime()) ? value : timestamp.toLocaleString();
}

function reportState(state: string | undefined) {
  switch (state) {
    case 'running':
      return { label: 'Running', color: 'yellow' as const, description: 'Capability probes are in flight.' };
    case 'complete':
    case 'completed':
      return { label: 'Complete', color: 'green' as const, description: 'The active protocol plan is based on this evidence.' };
    case 'failed':
      return { label: 'Failed', color: 'red' as const, description: 'The preflight could not produce a complete protocol plan.' };
    default:
      return { label: 'Not run', color: 'gray' as const, description: 'Run the bounded probe before interpreting transport performance.' };
  }
}

function Decision({ label, value, detail }: { label: string; value: string; detail?: string }) {
  return (
    <div className="preflightDecision">
      <Text className="telemetryEyebrow">{label}</Text>
      <Text className="preflightDecisionValue">{value || '—'}</Text>
      {detail ? <Text c="dimmed" fz="xs">{detail}</Text> : null}
    </div>
  );
}

function EvidenceList({
  title,
  entries,
}: {
  title: string;
  entries: Array<{ label: string; value: string | number | undefined }>;
}) {
  const visible = entries.filter((entry) => entry.value !== undefined && entry.value !== '');
  return (
    <div className="preflightEvidenceGroup">
      <Text className="telemetryEyebrow">{title}</Text>
      <dl>
        {visible.map((entry) => (
          <div key={entry.label}>
            <dt>{entry.label}</dt>
            <dd>{entry.value}</dd>
          </div>
        ))}
      </dl>
    </div>
  );
}

function CheckCard({
  title,
  result,
  check,
  evidence,
}: {
  title: string;
  result?: boolean;
  check?: PreflightCheck;
  evidence?: string;
}) {
  const attempted = check?.attempted ?? false;
  const positive = attempted && (result ?? check?.reliable ?? false);
  const label = !attempted ? 'Not attempted' : positive ? 'Confirmed' : 'Unavailable';
  const color = !attempted ? 'gray' : positive ? 'green' : 'red';

  return (
    <div className="preflightCheckCard" data-result={positive ? 'pass' : attempted ? 'fail' : 'idle'}>
      <Group justify="space-between" gap="xs" wrap="nowrap">
        <Text fw={650}>{title}</Text>
        <Badge color={color} size="sm" variant="light">{label}</Badge>
      </Group>
      <Text c="dimmed" fz="xs" className="preflightCheckReason">
        {check?.reason || evidence || (attempted ? 'No additional probe detail.' : 'This check was not required.')}
      </Text>
      <Group gap="md" className="preflightCheckMeta">
        <Text fz="xs"><span>Requests</span>{formatNumber(check?.requests ?? 0)}</Text>
        <Text fz="xs"><span>Elapsed</span>{formatMilliseconds(check?.elapsed_ms)}</Text>
        <Text fz="xs"><span>Evidence</span>{attempted ? (check?.reliable ? 'Reliable' : 'Best effort') : '—'}</Text>
      </Group>
    </div>
  );
}

function fingerprintEntries(fingerprint: HTTP3Fingerprint | undefined) {
  if (!fingerprint) return [];
  return [
    { label: 'Target', value: fingerprint.target },
    { label: 'Supported', value: yesNo(fingerprint.supported) },
    { label: 'High-rate ready', value: yesNo(fingerprint.high_rate_ready) },
    { label: 'ALPN', value: fingerprint.alpn },
    { label: 'QUIC version', value: fingerprint.quic_version },
    { label: 'HTTP version', value: fingerprint.http_version },
    { label: 'Status', value: fingerprint.status_code || undefined },
    { label: 'Remote address', value: fingerprint.remote_addr },
    { label: 'Connection reused', value: yesNo(fingerprint.connection_reused) },
    {
      label: 'Concurrent streams',
      value: `${formatNumber(fingerprint.concurrent_streams_completed)} / ${formatNumber(fingerprint.concurrent_streams_attempted)}`,
    },
    { label: 'QUIC datagrams', value: yesNo(fingerprint.quic_datagrams) },
    { label: 'HTTP/3 datagrams', value: yesNo(fingerprint.http3_datagrams) },
    { label: 'Elapsed', value: formatMilliseconds(fingerprint.elapsed_ms) },
    { label: 'Reason', value: fingerprint.reason },
  ];
}

export function ProtocolPreflightPanel({
  report,
  pending,
  onRun,
  runDisabledReason,
}: {
  report?: ProtocolPreflightReport;
  pending?: boolean;
  onRun?: () => void;
  runDisabledReason?: string;
}) {
  const state = reportState(report?.state);
  const running = report?.state === 'running';
  const head = report?.head_check ?? report?.head;
  const range = report?.range_check ?? report?.range;
  const ranAt = report?.completed_at ?? report?.ran_at ?? report?.started_at;
  const hasReport = Boolean(report && report.state !== 'not_run');

  return (
    <Paper withBorder p="md" className="telemetryPanel preflightPanel" aria-label="Protocol preflight report">
      <Group justify="space-between" align="flex-start" gap="md" className="telemetryPanelHeader">
        <Group gap="sm" wrap="nowrap">
          <ThemeIcon color="violet" variant="light" radius="md" size={34}>
            <LightningBoltIcon width={17} height={17} />
          </ThemeIcon>
          <Box>
            <Text className="telemetryEyebrow">Protocol intelligence</Text>
            <Title order={3}>Preflight report</Title>
            <Text c="dimmed" fz="xs">
              {state.description}{ranAt ? ` Last completed ${relativeTime(ranAt)}.` : ''}
              {runDisabledReason ? ` ${runDisabledReason}` : ''}
            </Text>
          </Box>
        </Group>
        <Group gap="xs" wrap="nowrap">
          <Badge color={state.color} variant="light">{state.label}</Badge>
          <Button
            size="xs"
            variant="default"
            leftSection={<ReloadIcon width={14} height={14} />}
            loading={pending || running}
            disabled={!onRun || pending || running}
            onClick={onRun}
          >
            Run preflight again
          </Button>
        </Group>
      </Group>

      {!hasReport ? (
        <div className="telemetryEmptyState">
          <InfoCircledIcon width={20} height={20} />
          <div>
            <Text fw={650}>No protocol evidence yet</Text>
            <Text c="dimmed" fz="sm">The scan can still run with configured defaults; preflight records why a protocol and request strategy were selected.</Text>
          </div>
        </div>
      ) : (
        <Stack gap="md" mt="md">
          <div className="preflightDecisionGrid">
            <Decision
              label="Selected protocol"
              value={report?.selected_protocol || (report?.http3_negotiated ? 'HTTP/3' : 'Fallback')}
              detail={report?.downgrade_reason}
            />
            <Decision label="Response strategy" value={(report?.selected_strategy || 'Not selected').toUpperCase()} detail={report?.strategy_reason} />
            <Decision label="Worker plan" value={formatNumber(report?.effective_workers ?? 0)} detail="Effective concurrent workers" />
            <Decision
              label="HTTP/3 capacity"
              value={`${formatNumber(report?.effective_connections ?? 0)} conn · ${formatNumber(report?.effective_streams ?? 0)} streams`}
              detail={`${formatNumber(report?.streams_per_connection ?? 0)} streams per connection`}
            />
          </div>

          {report?.error ? (
            <div className="preflightNotice" data-tone="red">
              <CrossCircledIcon width={16} height={16} />
              <Text fz="sm">{report.error}</Text>
            </div>
          ) : report?.downgrade_reason ? (
            <div className="preflightNotice" data-tone="yellow">
              <ExclamationTriangleIcon width={16} height={16} />
              <Text fz="sm">{report.downgrade_reason}</Text>
            </div>
          ) : null}

          <div className="preflightCheckGrid">
            <CheckCard
              title="HTTP/3 advertised"
              result={report?.http3_advertised}
              check={report?.advertisement_check}
              evidence={report?.http3_advertisement}
            />
            <CheckCard title="HTTP/3 negotiated" result={report?.http3_negotiated} check={report?.negotiation_check} />
            <CheckCard title="HEAD is reliable" result={head?.reliable} check={head} />
            <CheckCard title="Range is reliable" result={range?.reliable} check={range} />
          </div>

          <div className="preflightEvidenceGrid">
            <EvidenceList
              title="Run evidence"
              entries={[
                { label: 'Ran at', value: timestampLabel(report?.ran_at ?? report?.started_at) },
                { label: 'Completed at', value: timestampLabel(report?.completed_at) },
                { label: 'Advertisement', value: report?.http3_advertisement },
                { label: 'Advertised', value: yesNo(report?.http3_advertised) },
                { label: 'Negotiated', value: yesNo(report?.http3_negotiated) },
                { label: 'Protocol', value: report?.selected_protocol },
                { label: 'Strategy', value: report?.selected_strategy },
                { label: 'Strategy reason', value: report?.strategy_reason },
                { label: 'Downgrade reason', value: report?.downgrade_reason },
              ]}
            />
            <EvidenceList
              title="Probe timing"
              entries={[
                { label: 'Advertisement', value: formatMilliseconds(report?.timings_ms?.advertisement) },
                { label: 'HTTP/3', value: formatMilliseconds(report?.timings_ms?.http3) },
                { label: 'Baseline', value: formatMilliseconds(report?.timings_ms?.baseline) },
                { label: 'HEAD', value: formatMilliseconds(report?.timings_ms?.head) },
                { label: 'Range', value: formatMilliseconds(report?.timings_ms?.range) },
                { label: 'Total', value: formatMilliseconds(report?.timings_ms?.total) },
              ]}
            />
            <EvidenceList title="HTTP/3 fingerprint" entries={fingerprintEntries(report?.fingerprint)} />
          </div>
        </Stack>
      )}
    </Paper>
  );
}

export function ErrorDiagnosticsPanel({ diagnostics }: { diagnostics: ErrorDiagnostic[] }) {
  const [requestedCategory, setRequestedCategory] = useState('');
  const selectedCategory = diagnostics.some((entry) => entry.category === requestedCategory)
    ? requestedCategory
    : diagnostics[0]?.category ?? '';
  const total = diagnostics.reduce((sum, diagnostic) => sum + diagnostic.count, 0);

  return (
    <Paper withBorder p="md" className="telemetryPanel diagnosticsPanel" aria-label="Error diagnostics">
      <Group justify="space-between" align="flex-start" gap="md" className="telemetryPanelHeader">
        <Group gap="sm" wrap="nowrap">
          <ThemeIcon color={total > 0 ? 'red' : 'green'} variant="light" radius="md" size={34}>
            {total > 0 ? <ExclamationTriangleIcon width={17} height={17} /> : <CheckCircledIcon width={17} height={17} />}
          </ThemeIcon>
          <Box>
            <Text className="telemetryEyebrow">Failure intelligence</Text>
            <Title order={3}>Error diagnostics</Title>
            <Text c="dimmed" fz="xs">Grouped causes, representative samples, and the next action to try.</Text>
          </Box>
        </Group>
        <Badge color={total > 0 ? 'red' : 'green'} variant="light">
          {formatNumber(total)} {total === 1 ? 'error' : 'errors'}
        </Badge>
      </Group>

      {diagnostics.length === 0 ? (
        <div className="telemetryEmptyState" data-tone="success">
          <CheckCircledIcon width={20} height={20} />
          <div>
            <Text fw={650}>No categorized errors</Text>
            <Text c="dimmed" fz="sm">Transport, response, and persistence errors will appear here with recovery guidance.</Text>
          </div>
        </div>
      ) : (
        <Tabs
          value={selectedCategory}
          onChange={(value) => setRequestedCategory(value ?? '')}
          keepMounted={false}
          className="diagnosticsTabs"
        >
          <Tabs.List>
            {diagnostics.map((diagnostic) => (
              <Tabs.Tab key={diagnostic.category} value={diagnostic.category}>
                {humanize(diagnostic.category)}
                <Badge size="xs" variant="light" color="red">{formatNumber(diagnostic.count)}</Badge>
              </Tabs.Tab>
            ))}
          </Tabs.List>
          {diagnostics.map((diagnostic) => (
            <Tabs.Panel key={diagnostic.category} value={diagnostic.category} className="diagnosticPanelBody">
              <div className="diagnosticSummary">
                <div>
                  <Text className="telemetryEyebrow">Selected category</Text>
                  <Text fw={700}>{humanize(diagnostic.category)}</Text>
                </div>
                <Text c="dimmed" fz="sm">{formatNumber(diagnostic.count)} observed</Text>
              </div>
              <div className="diagnosticContentGrid">
                <div>
                  <Text className="telemetryEyebrow">Representative samples</Text>
                  {diagnostic.samples.length > 0 ? (
                    <ol className="diagnosticSamples">
                      {diagnostic.samples.map((sample, index) => (
                        <li key={`${sample}:${index}`}><code>{sample}</code></li>
                      ))}
                    </ol>
                  ) : (
                    <Text c="dimmed" fz="sm" mt="xs">No sample message was retained.</Text>
                  )}
                </div>
                <div className="diagnosticSuggestion">
                  <InfoCircledIcon width={17} height={17} />
                  <div>
                    <Text className="telemetryEyebrow">Suggested next step</Text>
                    <Text fz="sm">{diagnostic.suggestion || 'Inspect the terminal log for the first occurrence and retry after correcting the underlying condition.'}</Text>
                  </div>
                </div>
              </div>
            </Tabs.Panel>
          ))}
        </Tabs>
      )}
    </Paper>
  );
}
