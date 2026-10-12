import { render, screen } from '@testing-library/react';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { AppErrorBoundary } from './AppErrorBoundary';
import { AppThemeProvider } from './ui/AppThemeProvider';

function BrokenRecord(): never {
  throw new Error('malformed persisted session');
}

describe('AppErrorBoundary', () => {
  beforeEach(() => vi.spyOn(console, 'error').mockImplementation(() => undefined));

  it('shows a recoverable fallback instead of blanking the console', () => {
    render(
      <AppThemeProvider>
        <AppErrorBoundary>
          <BrokenRecord />
        </AppErrorBoundary>
      </AppThemeProvider>,
    );

    expect(screen.getByRole('alert')).toBeVisible();
    expect(screen.getByRole('button', { name: 'Reload console' })).toBeEnabled();
    expect(screen.getByText('malformed persisted session')).toBeVisible();
  });
});
