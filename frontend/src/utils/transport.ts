import type { Session } from '../types/api';

export function effectiveTransport(session: Session): { label: string; color: string } {
  if (session.status === 'starting') return { label: 'Probing', color: 'yellow' };
  if (session.runtime_transport === 'http3') return { label: 'HTTP/3', color: 'cyan' };
  if (session.runtime_transport === 'http2') return { label: 'HTTP/2', color: 'indigo' };
  if (session.runtime_transport === 'http1') return { label: 'HTTP/1.1', color: 'gray' };
  if (session.status === 'idle') return { label: 'Not probed', color: 'gray' };
  return { label: 'Transport unavailable', color: 'gray' };
}
