import type { ConfigOptions } from '../types/api';

const flagByPath: Record<string, string> = {
  'input.extensions': 'e',
  'input.input_mode': 'mode',
  'input.dirsearch_compat': 'D',
  'general.autocalibration': 'ac',
  'general.delay': 'p',
  'general.rate': 'rate',
  'general.threads': 't',
  'http.headers': 'H',
  'http.data': 'd',
  'http.recursion': 'recursion',
  'http.follow_redirects': 'r',
  'http.http3': 'http3',
  'http.http1': 'http1',
  'http.no_http3': 'no-http3',
  'http.http2': 'http2',
  'http.no_response_optimization': 'no-response-optimization',
  'http.http3_connections': 'http3-connections',
  'http.http3_max_connections': 'http3-max-connections',
  'http.http3_max_workers': 'http3-max-workers',
  'http.http3_streams': 'http3-streams',
  'http.http3_probe_timeout': 'http3-probe-timeout',
};

const conditionSuffix: Record<string, string> = {
  status: 'c',
  size: 's',
  lines: 'l',
  regexp: 'r',
  time: 't',
  words: 'w',
  mode: 'mode',
};

export function canonicalPatchFlag(section: keyof ConfigOptions, key: string): string {
  if (section === 'matchers' || section === 'filters') {
    const suffix = conditionSuffix[key];
    if (suffix) return `${section === 'matchers' ? 'm' : 'f'}${suffix}`;
  }
  return flagByPath[`${section}.${key}`] ?? key;
}

export function addExplicitFlags(current: Iterable<string>, ...names: string[]): Set<string> {
  return new Set([...current, ...names.filter(Boolean)]);
}
