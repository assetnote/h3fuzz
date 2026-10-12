import type { Progress, Session } from '../types/api';

function effectiveSequence(data: Partial<Session>, eventSequence?: number): number {
  return Math.max(data.sequence ?? 0, eventSequence ?? 0);
}

export function mergeSessionEvent(
  previous: Session,
  data: Partial<Session>,
  eventSequence?: number,
): Session {
  const previousSequence = previous.sequence ?? 0;
  const incomingSequence = effectiveSequence(data, eventSequence);
  const isFullSession = 'id' in data;
  if (
    (incomingSequence > 0 && incomingSequence < previousSequence) ||
    (isFullSession && previousSequence > 0 && incomingSequence === 0)
  ) {
    return previous;
  }
  return {
    ...previous,
    ...data,
    sequence: incomingSequence > 0 ? Math.max(previousSequence, incomingSequence) : previous.sequence,
    progress: data.progress ? { ...previous.progress, ...data.progress } : previous.progress,
  };
}

export function mergeSessionUpdate(previous: Session, data: unknown, eventSequence?: number): Session {
  const payload = (data ?? {}) as Partial<Session> & Partial<Progress> & { progress?: Partial<Progress> };
  const looksLikeProgress = !('status' in payload) && !('target' in payload) && !('id' in payload);
  if (looksLikeProgress) {
    const previousSequence = previous.sequence ?? 0;
    const incomingSequence = eventSequence ?? 0;
    if (incomingSequence > 0 && previousSequence > incomingSequence) return previous;
    return {
      ...previous,
      sequence: incomingSequence > 0 ? Math.max(previousSequence, incomingSequence) : previous.sequence,
      progress: { ...previous.progress, ...payload },
    };
  }
  return mergeSessionEvent(previous, payload, eventSequence);
}

export function upsertSession(current: Session[], incoming: Session, eventSequence?: number): Session[] {
  const index = current.findIndex((session) => session.id === incoming.id);
  if (index < 0) {
    const sequence = effectiveSequence(incoming, eventSequence);
    return [{ ...incoming, sequence: sequence || incoming.sequence }, ...current];
  }
  const next = mergeSessionEvent(current[index], incoming, eventSequence);
  if (next === current[index]) return current;
  return [...current.slice(0, index), next, ...current.slice(index + 1)];
}

export function reconcileSessionList(
  current: Session[],
  incoming: Session[],
  preserveMissing = true,
): Session[] {
  const currentByID = new Map(current.map((session) => [session.id, session]));
  const incomingIDs = new Set(incoming.map((session) => session.id));
  const reconciled = incoming.map((session) => {
    const previous = currentByID.get(session.id);
    return previous ? mergeSessionEvent(previous, session) : session;
  });
  if (!preserveMissing) return reconciled;
  return [...reconciled, ...current.filter((session) => !incomingIDs.has(session.id))];
}

export function mergeSessionDelta(
  previous: Session,
  data: Partial<Session>,
  eventSequence: number,
): Session {
  if (eventSequence <= (previous.sequence ?? 0)) return previous;
  return mergeSessionEvent(previous, data, eventSequence);
}

export function advanceSessionSequence(previous: Session, eventSequence: number): Session {
  if (eventSequence <= (previous.sequence ?? 0)) return previous;
  return { ...previous, sequence: eventSequence };
}
