import type { ConfigOptions } from '../types/api';

export type ProtocolMode = 'auto' | 'http3' | 'skip-http3' | 'http1';

export const protocolFlagNames = ['http3', 'http2', 'http1', 'no-http3'] as const;

export function protocolMode(http: ConfigOptions['http']): ProtocolMode {
  if (http.http3) return 'http3';
  if (http.http1) return 'http1';
  if (http.http2 || http.no_http3) return 'skip-http3';
  return 'auto';
}

export function protocolModePatch(mode: ProtocolMode): Pick<
  ConfigOptions['http'],
  'http3' | 'http2' | 'http1' | 'no_http3'
> {
  return {
    http3: mode === 'http3',
    http2: false,
    http1: mode === 'http1',
    no_http3: mode === 'skip-http3',
  };
}

export function protocolExplicitFlags(
  current: Iterable<string>,
  mode: ProtocolMode,
): Set<string> {
  const next = new Set(current);
  protocolFlagNames.forEach((flag) => next.delete(flag));
  if (mode === 'http3') next.add('http3');
  if (mode === 'skip-http3') next.add('no-http3');
  if (mode === 'http1') next.add('http1');
  return next;
}

export function protocolModeDescription(mode: ProtocolMode): string {
  if (mode === 'http3') return 'Require HTTP/3. The scan fails if QUIC cannot be negotiated.';
  if (mode === 'skip-http3') return 'Skip QUIC and negotiate HTTP/2, falling back to HTTP/1 when needed.';
  if (mode === 'http1') return 'Require HTTP/1 and do not attempt HTTP/3 or HTTP/2.';
  return 'Probe HTTP/3 first, then negotiate HTTP/2, then fall back to HTTP/1.';
}
