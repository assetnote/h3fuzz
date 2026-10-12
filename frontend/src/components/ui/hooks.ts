import { useCallback, useEffect, useRef, useState, type Dispatch, type SetStateAction } from 'react';

export function useDisclosure(initial = false) {
  const [opened, setOpened] = useState(initial);
  const open = useCallback(() => setOpened(true), []);
  const close = useCallback(() => setOpened(false), []);
  const toggle = useCallback(() => setOpened((current) => !current), []);
  return [opened, { open, close, toggle }] as const;
}

export interface UseLocalStorageOptions<T> {
  key: string;
  defaultValue: T;
  getInitialValueInEffect?: boolean;
}

function fallbackValue<T>(value: T | (() => T)): T {
  return typeof value === 'function' ? (value as () => T)() : value;
}

function readStorage<T>(key: string, defaultValue: T | (() => T)): T {
  if (typeof window === 'undefined') return fallbackValue(defaultValue);
  const stored = window.localStorage.getItem(key);
  if (stored === null) return fallbackValue(defaultValue);
  try {
    return JSON.parse(stored) as T;
  } catch {
    return fallbackValue(defaultValue);
  }
}

export function useLocalStorage<T>({ key, defaultValue, getInitialValueInEffect = true }: UseLocalStorageOptions<T>) {
  const [value, setValueState] = useState<T>(() => getInitialValueInEffect ? fallbackValue(defaultValue) : readStorage(key, defaultValue));

  useEffect(() => {
    if (getInitialValueInEffect) setValueState(readStorage(key, defaultValue));
  }, [defaultValue, getInitialValueInEffect, key]);

  useEffect(() => {
    function refresh(event: StorageEvent | Event) {
      if (event instanceof StorageEvent && event.key !== key) return;
      if (event instanceof CustomEvent && event.detail?.key !== key) return;
      setValueState(readStorage(key, defaultValue));
    }
    window.addEventListener('storage', refresh);
    window.addEventListener('h3fuzz:storage', refresh);
    return () => {
      window.removeEventListener('storage', refresh);
      window.removeEventListener('h3fuzz:storage', refresh);
    };
  }, [defaultValue, key]);

  const setValue: Dispatch<SetStateAction<T>> = useCallback((next) => {
    setValueState((current) => {
      const resolved = typeof next === 'function' ? (next as (value: T) => T)(current) : next;
      if (resolved === undefined) window.localStorage.removeItem(key);
      else window.localStorage.setItem(key, JSON.stringify(resolved));
      window.dispatchEvent(new CustomEvent('h3fuzz:storage', { detail: { key } }));
      return resolved;
    });
  }, [key]);

  const remove = useCallback(() => {
    window.localStorage.removeItem(key);
    setValueState(fallbackValue(defaultValue));
    window.dispatchEvent(new CustomEvent('h3fuzz:storage', { detail: { key } }));
  }, [defaultValue, key]);

  return [value, setValue, remove] as const;
}

export function useMediaQuery(query: string, initialValue = false) {
  const [matches, setMatches] = useState(() => typeof window === 'undefined' || !window.matchMedia ? initialValue : window.matchMedia(query).matches);
  useEffect(() => {
    if (!window.matchMedia) return undefined;
    const media = window.matchMedia(query);
    const update = () => setMatches(media.matches);
    update();
    media.addEventListener('change', update);
    return () => media.removeEventListener('change', update);
  }, [query]);
  return matches;
}

export function useDebouncedValue<T>(value: T, delay: number) {
  const [debounced, setDebounced] = useState(value);
  const timer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  useEffect(() => {
    timer.current = setTimeout(() => setDebounced(value), delay);
    return () => clearTimeout(timer.current);
  }, [delay, value]);
  const cancel = useCallback(() => {
    clearTimeout(timer.current);
    setDebounced(value);
  }, [value]);
  return [debounced, cancel] as const;
}
