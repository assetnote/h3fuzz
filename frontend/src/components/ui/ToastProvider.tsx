import * as Toast from '@radix-ui/react-toast';
import { CheckCircledIcon, Cross2Icon, CrossCircledIcon, InfoCircledIcon } from '@radix-ui/react-icons';
import type { ReactNode } from 'react';
import { useEffect, useState } from 'react';
import { subscribeToToasts, type AppToast, type ToastInput } from './toast';

function toneFor(color: ToastInput['color']) {
  if (color === 'red') return 'error';
  if (color === 'green') return 'success';
  if (color === 'yellow') return 'warning';
  return 'info';
}

function ToneIcon({ tone }: { tone: ReturnType<typeof toneFor> }) {
  if (tone === 'success') return <CheckCircledIcon aria-hidden="true" />;
  if (tone === 'error') return <CrossCircledIcon aria-hidden="true" />;
  return <InfoCircledIcon aria-hidden="true" />;
}

export function ToastProvider({ children, limit = 4 }: { children: ReactNode; limit?: number }) {
  const [items, setItems] = useState<AppToast[]>([]);

  useEffect(() => {
    return subscribeToToasts((item) => {
      setItems((current) => [...current, item].slice(-limit));
    });
  }, [limit]);

  const dismiss = (id: string) => {
    setItems((current) => current.filter((item) => item.id !== id));
  };

  return (
    <Toast.Provider swipeDirection="right" duration={5_000}>
      {children}
      {items.map((item) => {
        const tone = toneFor(item.color);
        return (
          <Toast.Root
            key={item.id}
            className="appToast"
            data-tone={tone}
            duration={item.duration}
            defaultOpen
            onOpenChange={(open) => {
              if (!open) dismiss(item.id);
            }}
          >
            <span className="appToastIcon">
              <ToneIcon tone={tone} />
            </span>
            <div className="appToastContent">
              {item.title ? <Toast.Title className="appToastTitle">{item.title}</Toast.Title> : null}
              <Toast.Description className="appToastDescription">{item.message}</Toast.Description>
            </div>
            <Toast.Close className="appToastClose" aria-label="Dismiss notification">
              <Cross2Icon aria-hidden="true" />
            </Toast.Close>
          </Toast.Root>
        );
      })}
      <Toast.Viewport className="appToastViewport" />
    </Toast.Provider>
  );
}
