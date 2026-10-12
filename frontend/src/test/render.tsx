import { render as testingLibraryRender, type RenderOptions } from '@testing-library/react';
import type { ReactElement, ReactNode } from 'react';
import { AppThemeProvider } from '../components/ui/AppThemeProvider';
import { ToastProvider } from '../components/ui/ToastProvider';

export function TestProviders({ children }: { children: ReactNode }) {
  return (
    <AppThemeProvider>
      <ToastProvider>{children}</ToastProvider>
    </AppThemeProvider>
  );
}

// Test helpers deliberately export a render function beside their provider.
// eslint-disable-next-line react-refresh/only-export-components
export function render(ui: ReactElement, options?: Omit<RenderOptions, 'wrapper'>) {
  return testingLibraryRender(ui, { ...options, wrapper: TestProviders });
}
