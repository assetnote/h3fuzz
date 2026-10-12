import type { ConfigOptions } from '../types/api';
import { cloneConfig } from './config';

export function normalizeOptionPath(path?: string): string[] {
  if (!path) return [];
  return path
    .replace(/^options\./, '')
    .split('.')
    .map((part) => (part === 'matcher' ? 'matchers' : part === 'filter' ? 'filters' : part));
}

export function getOptionValue(config: ConfigOptions, path?: string): unknown {
  if (!path) return undefined;
  let value: unknown = config;
  for (const part of normalizeOptionPath(path)) {
    if (value === null || typeof value !== 'object') return undefined;
    value = (value as Record<string, unknown>)[part];
  }
  return value;
}

export function setOptionValue(config: ConfigOptions, path: string | undefined, value: unknown): ConfigOptions {
  if (!path) return config;
  const next = cloneConfig(config);
  const parts = normalizeOptionPath(path);
  let cursor = next as unknown as Record<string, unknown>;
  parts.slice(0, -1).forEach((part) => {
    const child = cursor[part];
    if (!child || typeof child !== 'object') cursor[part] = {};
    cursor = cursor[part] as Record<string, unknown>;
  });
  cursor[parts.at(-1)!] = value;
  return next;
}
