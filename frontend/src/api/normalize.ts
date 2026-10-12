import type {
  ConfigOptions,
  OptionMetadata,
  OptionsMetadata,
  Profile,
  Result,
  Session,
  Settings,
  TerminalEntry,
} from '../types/api';
import { cloneConfig, defaultConfig, DEFAULT_WEB_EXPLICIT_FLAGS } from '../utils/config';

function array<T>(value: T[] | null | undefined): T[] {
  return Array.isArray(value) ? value : [];
}

export function normalizeConfig(value?: Partial<ConfigOptions> | null): ConfigOptions {
  const fallback = cloneConfig(defaultConfig);
  const source = value ?? {};
  const general = { ...fallback.general, ...(source.general ?? {}) };
  const http = { ...fallback.http, ...(source.http ?? {}) };
  const input = { ...fallback.input, ...(source.input ?? {}) };
  const output = { ...fallback.output, ...(source.output ?? {}) };
  const normalized: ConfigOptions = {
    general: {
      ...general,
      autocalibration_strategies: array(general.autocalibration_strategies),
      autocalibration_strings: array(general.autocalibration_strings),
    },
    http: {
      ...http,
      cookies: array(http.cookies),
      headers: array(http.headers),
      preflights: array(http.preflights).map((flight) => ({
        request_file: flight?.request_file ?? '',
        vars: array(flight?.vars).map((variable) => ({
          name: variable?.name ?? '',
          regex: variable?.regex ?? '',
          source: variable?.source ?? '',
          key: variable?.key ?? '',
        })),
      })),
      postflights: array(http.postflights).map((flight) => ({
        request_file: flight?.request_file ?? '',
        vars: array(flight?.vars).map((variable) => ({
          name: variable?.name ?? '',
          regex: variable?.regex ?? '',
          source: variable?.source ?? '',
          key: variable?.key ?? '',
        })),
      })),
    },
    input: {
      ...input,
      encoders: array(input.encoders),
      input_commands: array(input.input_commands),
      wordlists: array(input.wordlists),
    },
    matchers: { ...fallback.matchers, ...(source.matchers ?? {}) },
    filters: { ...fallback.filters, ...(source.filters ?? {}) },
    output,
  };
  return normalized;
}

export function normalizeSession(value: Session): Session {
  const scheduledCount = value.progress?.request_count ?? value.progress?.req_count ?? 0;
  const responseCount = value.progress?.response_count;
  const errorCount = value.progress?.error_count ?? 0;
  // Older web servers did not expose a terminal-work counter. Their response
  // and error counters are the closest drain-aware representation and avoid
  // treating work merely queued to workers as completed.
  const completedCount = value.progress?.completed_count
    ?? (responseCount === undefined
      ? scheduledCount
      : Math.min(scheduledCount, Math.max(0, responseCount + errorCount)));
  return {
    ...value,
    name: value.name || value.target || 'Untitled target',
    options: normalizeConfig(value.options),
    explicit_flags: array(value.explicit_flags),
    progress: {
      started_at: value.progress?.started_at,
      req_count: value.progress?.req_count ?? 0,
      req_total: value.progress?.req_total ?? 0,
      req_sec: value.progress?.req_sec ?? 0,
      request_count: scheduledCount,
      completed_count: completedCount,
      response_count: responseCount,
      current_rate: value.progress?.current_rate ?? value.progress?.req_sec ?? 0,
      average_30s: value.progress?.average_30s,
      overall_average: value.progress?.overall_average,
      peak_rate: value.progress?.peak_rate,
      eta_seconds: value.progress?.eta_seconds,
      queue_pos: value.progress?.queue_pos ?? 0,
      queue_total: value.progress?.queue_total ?? 0,
      error_count: value.progress?.error_count ?? 0,
      results_queued: value.progress?.results_queued ?? 0,
      persistence_backpressure: value.progress?.persistence_backpressure ?? false,
      last_batch_duration_ms: value.progress?.last_batch_duration_ms ?? 0,
      stall_reason: value.progress?.stall_reason ?? '',
      stalled_for_ms: value.progress?.stalled_for_ms ?? 0,
    },
    error_diagnostics: array(value.error_diagnostics).map((diagnostic) => ({
      ...diagnostic,
      samples: array(diagnostic.samples),
    })),
    result_count: value.result_count ?? 0,
  };
}

export function normalizeSettings(value: Settings): Settings {
  return {
    ...value,
    defaults: {
      wordlist: value.defaults?.wordlist || 'final.txt',
      profile: value.defaults?.profile || 'aspm',
      options: normalizeConfig(value.defaults?.options),
      explicit_flags: Array.isArray(value.defaults?.explicit_flags)
        ? value.defaults.explicit_flags
        : [...DEFAULT_WEB_EXPLICIT_FLAGS],
    },
  };
}

export function normalizeProfile(value: Profile): Profile {
  return { ...value, extensions: array(value.extensions) };
}

export function normalizeResult(value: Result): Result {
  return { ...value, input: value.input && typeof value.input === 'object' ? value.input : value.input ?? '' };
}

export function normalizeTerminalEntry(value: TerminalEntry): TerminalEntry {
  return { ...value, level: value.level || 'info', message: value.message ?? '', at: value.at ?? '' };
}

export function normalizeTerminalPayload(value: unknown): TerminalEntry[] {
  let entries: unknown[];
  if (Array.isArray(value)) {
    entries = value;
  } else if (value && typeof value === 'object' && Array.isArray((value as { entries?: unknown }).entries)) {
    entries = (value as { entries: unknown[] }).entries;
  } else if (value && typeof value === 'object' && 'entry' in value) {
    entries = [(value as { entry: unknown }).entry];
  } else {
    entries = [value];
  }

  return entries
    .filter(
      (entry): entry is TerminalEntry =>
        Boolean(entry) &&
        typeof entry === 'object' &&
        ('message' in (entry as Record<string, unknown>) || 'level' in (entry as Record<string, unknown>)),
    )
    .map(normalizeTerminalEntry);
}

export function normalizeOptionsMetadata(value: OptionsMetadata): OptionsMetadata {
  return {
    defaults: normalizeConfig(value.defaults),
    flags: array(value.flags).map((flag: OptionMetadata) => ({
      ...flag,
      aliases: array(flag.aliases),
      choices: flag.choices ? array(flag.choices) : undefined,
    })),
  };
}
