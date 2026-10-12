import { useState } from 'react';
import type { StorageStats } from '../types/api';
import { Alert, Box, Button, Group, Modal, Stack, Text } from './ui';
import { IconAlertTriangle } from './ui/icons';

interface StorageWarningProps {
  stats?: StorageStats;
  compacting?: boolean;
  compactDisabled?: boolean;
  onReviewSessions: () => void;
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
  compactDisabled = false,
  onReviewSessions,
  onCompact,
}: StorageWarningProps) {
  const [confirmOpen, setConfirmOpen] = useState(false);
  const [submitting, setSubmitting] = useState(false);
  if (!stats?.warning) return null;

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
        <Alert color="yellow" icon={<IconAlertTriangle size={18} />} title={`SQLite storage is ${formatBytes(stats.total_bytes)}`}>
          <Stack gap="xs">
            <Text fz="sm">
              h3fuzz does not store raw response bodies. Disk space is used by saved result metadata, optional scraper output, terminal history, scan configuration, and search indexes.
            </Text>
            <Text fz="xs" c="dimmed">
              Delete scans you no longer need, then compact the database to return unused pages to disk. Nothing is removed automatically.
            </Text>
            <Text fz="xs" c="dimmed">
              SQLite currently reports {formatBytes(stats.reclaimable_bytes)} in free pages that compaction can return to disk.
            </Text>
            {compactDisabled ? <Text fz="xs" c="yellow">Stop active scans before compacting.</Text> : null}
            <Group gap="xs" wrap="wrap">
              <Button size="xs" variant="soft" color="yellow" onClick={onReviewSessions}>Review saved scans</Button>
              <Button size="xs" variant="outline" color="yellow" loading={pending} disabled={compactDisabled || pending} onClick={() => setConfirmOpen(true)}>Compact database</Button>
            </Group>
          </Stack>
        </Alert>
      </Box>
      <Modal opened={confirmOpen} onClose={() => { if (!pending) setConfirmOpen(false); }} title="Compact the SQLite database?" size="sm" centered>
        <Stack gap="md">
          <Text fz="sm">
            Compaction does not delete scans or results. It runs SQLite VACUUM to reclaim space left by scans you already deleted. It is available only when no scan is active.
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
