import { render, screen } from '@testing-library/react';
import { describe, expect, it } from 'vitest';
import { AppThemeProvider } from './AppThemeProvider';

describe('AppThemeProvider', () => {
  it('applies the Radix dark theme at the application boundary', () => {
    const { container } = render(
      <AppThemeProvider>
        <span>themed content</span>
      </AppThemeProvider>,
    );

    expect(screen.getByText('themed content')).toBeVisible();
    const root = container.querySelector('.radix-themes');
    expect(root).toHaveAttribute('data-is-root-theme', 'true');
    expect(root).toHaveClass('radix-themes', 'dark', 'h3Theme');
    expect(root).toHaveAttribute('data-accent-color', 'violet');
  });
});
