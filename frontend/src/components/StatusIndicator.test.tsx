import { render, screen } from '@testing-library/react';
import { describe, expect, it } from 'vitest';
import { StatusIndicator } from './StatusIndicator';
import { AppThemeProvider } from './ui/AppThemeProvider';

describe('StatusIndicator', () => {
  it('exposes running state as visible text', () => {
    render(
      <AppThemeProvider>
        <StatusIndicator status="running" />
      </AppThemeProvider>,
    );
    expect(screen.getByText('Running')).toBeVisible();
  });

  it('labels the compact icon for assistive technology', () => {
    render(
      <AppThemeProvider>
        <StatusIndicator status="paused" compact />
      </AppThemeProvider>,
    );
    const indicator = screen.getByLabelText('Paused');
    expect(indicator).toBeInTheDocument();
    expect(indicator).not.toHaveAttribute('tabindex');
  });

  it('shows the stopping state immediately', () => {
    render(
      <AppThemeProvider>
        <StatusIndicator status="stopping" />
      </AppThemeProvider>,
    );
    expect(screen.getByText('Stopping')).toBeVisible();
  });
});
