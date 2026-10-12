export function formatNumber(value: number): string {
  return new Intl.NumberFormat('en', { notation: value >= 10_000 ? 'compact' : 'standard' }).format(
    value,
  );
}

export function formatRate(value: number): string {
  if (!Number.isFinite(value)) return '0 req/s';
  return `${new Intl.NumberFormat('en', { maximumFractionDigits: 1, notation: 'compact' }).format(value)} req/s`;
}

export function formatBytes(value: number): string {
  if (value < 1024) return `${value} B`;
  if (value < 1024 ** 2) return `${(value / 1024).toFixed(1)} KB`;
  return `${(value / 1024 ** 2).toFixed(1)} MB`;
}

export function formatDuration(nanoseconds: number): string {
  const milliseconds = nanoseconds / 1_000_000;
  if (milliseconds < 1) return `${Math.round(nanoseconds / 1000)} μs`;
  if (milliseconds < 1000) return `${milliseconds.toFixed(milliseconds < 10 ? 1 : 0)} ms`;
  return `${(milliseconds / 1000).toFixed(2)} s`;
}

export function relativeTime(iso?: string): string {
  if (!iso) return 'Not started';
  const deltaSeconds = Math.round((new Date(iso).getTime() - Date.now()) / 1000);
  const formatter = new Intl.RelativeTimeFormat('en', { numeric: 'auto' });
  if (Math.abs(deltaSeconds) < 60) return formatter.format(deltaSeconds, 'second');
  const minutes = Math.round(deltaSeconds / 60);
  if (Math.abs(minutes) < 60) return formatter.format(minutes, 'minute');
  const hours = Math.round(minutes / 60);
  if (Math.abs(hours) < 24) return formatter.format(hours, 'hour');
  return formatter.format(Math.round(hours / 24), 'day');
}

export function progressPercent(current: number, total: number): number {
  if (total <= 0) return 0;
  return Math.min(100, Math.max(0, (current / total) * 100));
}

export function targetLabel(target: string): string {
  try {
    const url = new URL(target.replace('FUZZ', ''));
    return `${url.host}${url.pathname === '/' ? '' : url.pathname}`;
  } catch {
    return target || 'Untitled target';
  }
}

