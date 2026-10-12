import { useCallback, useEffect, useRef, useState } from 'react';
import { websocketURL } from '../api/client';
import type { ConnectionState, WebSocketEnvelope } from '../types/api';

interface StreamOptions {
  onEvent: (event: WebSocketEnvelope) => void;
  onSequenceGap?: () => void;
}

export function useSessionStream({ onEvent, onSequenceGap }: StreamOptions): ConnectionState {
  const [state, setState] = useState<ConnectionState>('connecting');
  const eventHandler = useRef(onEvent);
  const gapHandler = useRef(onSequenceGap);
  const lastSequence = useRef(new Map<string, number>());
  const attempt = useRef(0);

  eventHandler.current = onEvent;
  gapHandler.current = onSequenceGap;

  const resetSequence = useCallback(() => {
    lastSequence.current.clear();
  }, []);

  useEffect(() => {
    let socket: WebSocket | undefined;
    let timer: number | undefined;
    let disposed = false;

    const connect = () => {
      if (disposed) return;
      setState(attempt.current === 0 ? 'connecting' : 'reconnecting');
      socket = new WebSocket(websocketURL());

      socket.onopen = () => {
        const wasReconnect = attempt.current > 0;
        attempt.current = 0;
        setState('connected');
        if (wasReconnect) {
          lastSequence.current.clear();
          gapHandler.current?.();
        }
      };

      socket.onmessage = (message) => {
        try {
          const event = JSON.parse(message.data as string) as WebSocketEnvelope;
          if (event.type === 'resync.required') {
            lastSequence.current.clear();
            gapHandler.current?.();
            return;
          }
          if (event.type === 'snapshot') {
            lastSequence.current.clear();
            eventHandler.current(event);
            return;
          }
          const streamKey = event.session_id ?? '$global';
          const previous = lastSequence.current.get(streamKey) ?? 0;
          if (event.sequence > 0 && previous >= event.sequence) return;
          if (
            previous > 0 &&
            event.sequence > previous + 1
          ) {
            gapHandler.current?.();
          }
          lastSequence.current.set(streamKey, Math.max(previous, event.sequence));
          eventHandler.current(event);
        } catch {
          // Ignore malformed messages and preserve the live connection.
        }
      };

      socket.onclose = () => {
        if (disposed) return;
        attempt.current += 1;
        setState('reconnecting');
        const delay = Math.min(10_000, 500 * 2 ** Math.min(attempt.current, 5));
        timer = window.setTimeout(connect, delay);
      };

      socket.onerror = () => socket?.close();
    };

    resetSequence();
    connect();

    const offline = () => setState('offline');
    const online = () => {
      socket?.close();
      attempt.current = 0;
    };
    window.addEventListener('offline', offline);
    window.addEventListener('online', online);

    return () => {
      disposed = true;
      if (timer) window.clearTimeout(timer);
      socket?.close();
      window.removeEventListener('offline', offline);
      window.removeEventListener('online', online);
    };
  }, [resetSequence]);

  return state;
}
