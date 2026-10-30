import { useState } from 'react';
import type { StorageStats } from '../types/api';
import { Alert, Box, Button, Group, Modal, Stack, Text } from './ui';
import { IconAlertTriangle } from './ui/icons';

interface StorageWarningProps {
  stats?: StorageStats;
  compacting?: boolean;
  onReviewSessions: (sessionIDs?: string[]) => void;
  onCompact: () => Promise<unknown> | unknown;
}

function formatBytes(bytes: number): string {
  if (!Number.isFinite(bytes) || bytes <= 0) return '0 B';
  const units = ['B', 'KB', 'MB', 'GB', 'TB'];
  const exponent = Math.min(Math.floor(Math.log(bytes) / Math.log(1000)), units.length - 1);
  const value = bytes / (1000 ** exponent);
  return `${value >= 10 || exponent === 0 ? value.toFixed(0) : value.toFixed(1)} ${units[exponent]}`;
}

export function StorageWarning({
  stats,
  compacting = false,
  onReviewSessions,
  onCompact,
}: StorageWarningProps) {
  const [confirmOpen, setConfirmOpen] = useState(false);
  const [submitting, setSubmitting] = useState(false);
  if (!stats || (!stats.warning && !stats.maintenance_required)) return null;

  const maintenanceRequired = stats.maintenance_required === true;
  const maintenanceMessage = stats.maintenance_reason === 'legacy_tree_metadata'
    ? 'This database was created by an older h3fuzz build. Compact it once to upgrade stored directory-tree metadata and indexes. Scans and results are preserved.'
    : stats.maintenance_reason === 'legacy_result_summaries'
      ? 'This database was created by an older h3fuzz build. Compact it once to build indexed result summaries and remove legacy index overhead. Scans and results are preserved.'
      : 'This database needs a one-time storage upgrade before every results feature is available. Compaction preserves scans and results.';

  // Only an explicit server decision disables the action. Treating a missing
  // field as false would recreate the permanently-disabled state while a
  // browser and restarted binary briefly disagree on API shape.
  const compactDisabled = stats.compaction_available === false;
  const blockedMessage = stats.compaction_blocked_reason === 'active_scans'
    ? `${stats.active_scan_count} active ${stats.active_scan_count === 1 ? 'scan is' : 'scans are'} still using the database. Stop them or wait for them to finish before compacting.`
    : 'Database compaction is temporarily unavailable. Refresh the storage status and try again.';
  const largestSessions = [...(stats.sessions ?? [])]
    .filter((session) => session.estimated_bytes > 0)
    .sort((left, right) => right.estimated_bytes - left.estimated_bytes)
    .slice(0, 3);
  const pending = compacting || submitting;
  const compact = async () => {
    if (pending || compactDisabled) return;
    setSubmitting(true);
    try {
      await onCompact();
      setConfirmOpen(false);
    } catch {
      // The parent mutation owns the user-facing error notification. Keep the
      // confirmation open so the operator can retry without losing context.
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <>
      <Box className="storageWarningWrap">
        <Alert
          color="yellow"
          icon={<IconAlertTriangle size={18} />}
          title={maintenanceRequired ? 'SQLite storage maintenance required' : `SQLite storage is ${formatBytes(stats.total_bytes)}`}
        >
          <Stack gap="xs">
            {maintenanceRequired ? <Text fz="sm">{maintenanceMessage}</Text> : null}
            {stats.warning ? (
              <>
                <Text fz="sm">
                  h3fuzz does not store raw response bodies. Disk space is used by saved result metadata, optional scraper output, terminal history, scan configuration, and search indexes.
                </Text>
                <Text fz="xs" c="dimmed">
                  Delete scans you no longer need, then compact the database to return unused pages to disk. Nothing is removed automatically.
                </Text>
                <Text fz="xs" c="dimmed">
                  SQLite currently reports {formatBytes(stats.reclaimable_bytes)} in free pages that compaction can return to disk.
                </Text>
              </>
            ) : null}
            {stats.warning && largestSessions.length ? (
              <Box className="storageUsageBreakdown" aria-label="Largest saved scans">
                <Group justify="space-between" gap="xs" wrap="wrap">
                  <Text fz="xs" fw={650}>Largest saved scans</Text>
                  <Button
                    size="xs"
                    variant="ghost"
                    color="yellow"
                    onClick={() => onReviewSessions(largestSessions.map((session) => session.session_id))}
                  >
                    Select largest scans
                  </Button>
                </Group>
                <Stack gap={4} mt={4}>
                  {largestSessions.map((session) => (
                    <Box className="storageUsageRow" key={session.session_id}>
                      <Text className="storageUsageName" fz="xs" lineClamp={1}>{session.name || 'Unnamed scan'}</Text>
                      <Text fz="xs" c="dimmed">{session.result_count.toLocaleString('en-US')} results</Text>
                      <Text fz="xs" c="dimmed">{formatBytes(session.estimated_bytes)}</Text>
                    </Box>
                  ))}
                </Stack>
              </Box>
            ) : null}
            {compactDisabled ? <Text fz="xs" c="yellow">{blockedMessage}</Text> : null}
            <Group gap="xs" wrap="wrap">
              {stats.warning ? <Button size="xs" variant="soft" color="yellow" onClick={() => onReviewSessions()}>Review saved scans</Button> : null}
              <Button size="xs" variant="outline" color="yellow" loading={pending} disabled={compactDisabled || pending} onClick={() => setConfirmOpen(true)}>Compact database</Button>
            </Group>
          </Stack>
        </Alert>
      </Box>
      <Modal opened={confirmOpen} onClose={() => { if (!pending) setConfirmOpen(false); }} title="Compact the SQLite database?" size="sm" centered>
        <Stack gap="md">
          <Text fz="sm">
            {maintenanceRequired
              ? 'Compaction does not delete scans or results. It upgrades legacy storage metadata, rebuilds the required summaries and indexes, and runs SQLite VACUUM. It is available only when no scan is active.'
              : 'Compaction does not delete scans or results. It runs SQLite VACUUM to reclaim space left by scans you already deleted. It is available only when no scan is active.'}
          </Text>
          <Group justify="flex-end" gap="xs">
            <Button variant="default" disabled={pending} onClick={() => setConfirmOpen(false)}>Cancel</Button>
            <Button color="yellow" loading={pending} disabled={compactDisabled || pending} onClick={() => { void compact(); }}>Confirm compaction</Button>
          </Group>
        </Stack>
      </Modal>
    </>
  );
}
