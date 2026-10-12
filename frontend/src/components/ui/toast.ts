export interface ToastInput {
  title?: string;
  message: string;
  color?: 'red' | 'green' | 'blue' | 'yellow' | string;
  duration?: number;
}

export interface AppToast extends ToastInput {
  id: string;
}

type ToastListener = (item: AppToast) => void;

const listeners = new Set<ToastListener>();
let nextToastID = 0;

function publish(input: ToastInput) {
  const item = { ...input, id: `toast-${++nextToastID}` };
  listeners.forEach((listener) => listener(item));
  return item.id;
}

// Imperative facade keeps async mutation callbacks concise without tying them
// to a component library. `notifications` is a source-compatible migration
// alias for existing mutation callbacks.
export const toast = { show: publish };
export const notifications = toast;

export function subscribeToToasts(listener: ToastListener) {
  listeners.add(listener);
  return () => {
    listeners.delete(listener);
  };
}
