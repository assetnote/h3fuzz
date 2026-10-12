import { render, screen } from '@testing-library/react';
import { act } from 'react';
import { describe, expect, it } from 'vitest';
import { notifications } from './toast';
import { ToastProvider } from './ToastProvider';

describe('ToastProvider', () => {
  it('renders imperative notifications through the Radix viewport', () => {
    render(
      <ToastProvider>
        <span>application</span>
      </ToastProvider>,
    );

    act(() => {
      notifications.show({ color: 'green', title: 'Filters applied', message: 'Results were refreshed.' });
    });

    expect(screen.getByText('Filters applied')).toBeVisible();
    expect(screen.getByText('Results were refreshed.')).toBeVisible();
    expect(screen.getByRole('button', { name: 'Dismiss notification' })).toBeEnabled();
  });
});
