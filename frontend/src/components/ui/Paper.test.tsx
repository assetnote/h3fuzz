import { screen } from '@testing-library/react';
import { describe, expect, it } from 'vitest';
import { render } from '../../test/render';
import { Paper } from './layout';

describe('Paper', () => {
  it('does not inherit Card padding or a second surface border', () => {
    render(<Paper>Unadorned panel</Paper>);

    const panel = screen.getByText('Unadorned panel');
    expect(panel).toHaveClass('rt-Box');
    expect(panel).not.toHaveClass('rt-Card');
  });

  it('adds only the requested padding and border', () => {
    render(<Paper withBorder p="sm">Bordered panel</Paper>);

    const style = screen.getByText('Bordered panel').getAttribute('style') ?? '';
    expect(style).toContain('border: 1px solid var(--gray-a6)');
    expect(style).toContain('padding: var(--space-2, 8px)');
  });
});
