import { screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { describe, expect, it, vi } from 'vitest';
import type { Session } from '../types/api';
import { render } from '../test/render';
import { cloneConfig, defaultConfig } from '../utils/config';
import { SessionFilters } from './SessionFilters';

function sessionFixture(status: Session['status'] = 'running'): Session {
  const options = cloneConfig(defaultConfig);
  options.filters.status = '404';
  return {
    id: 'session-1',
    name: 'Target',
    target: 'https://target.example/FUZZ',
    status,
    profile: 'dir',
    wordlist: 'final.txt',
    options,
    explicit_flags: ['fc'],
    progress: {
      req_count: 20,
      req_total: 100,
      req_sec: 10,
      queue_pos: 20,
      queue_total: 100,
      error_count: 0,
    },
    result_count: 2,
    created_at: '2026-10-04T00:00:00Z',
    updated_at: '2026-10-04T00:00:01Z',
  };
}

function renderFilters(session = sessionFixture(), onAdd = vi.fn().mockResolvedValue(undefined)) {
  render(<SessionFilters session={session} onAdd={onAdd} />);
  return onAdd;
}

describe('SessionFilters', () => {
  it('shows saved filters and adds a sparse word-count filter while running', async () => {
    const user = userEvent.setup();
    const onAdd = renderFilters();

    expect(screen.getByText('-fc 404')).toBeVisible();
    await user.click(screen.getByRole('button', { name: 'Add filters' }));
    expect(screen.getByLabelText('Regular expression (-fr)')).toBeDisabled();
    await user.type(screen.getByLabelText('Word counts (-fw)'), '656');
    await user.click(screen.getByRole('button', { name: 'Apply filters' }));

    expect(onAdd).toHaveBeenCalledWith({ words: '656' });
    expect(await screen.findByRole('button', { name: 'Add filters' })).toBeVisible();
  });

  it('requires at least one filter value', async () => {
    const user = userEvent.setup();
    const onAdd = renderFilters();

    await user.click(screen.getByRole('button', { name: 'Add filters' }));
    await user.click(screen.getByRole('button', { name: 'Apply filters' }));

    expect(screen.getByText('Enter at least one filter value.')).toBeVisible();
    expect(onAdd).not.toHaveBeenCalled();
  });

  it('allows a regular-expression filter before the scan starts', async () => {
    const user = userEvent.setup();
    const onAdd = renderFilters(sessionFixture('idle'));

    await user.click(screen.getByRole('button', { name: 'Add filters' }));
    const regexp = screen.getByLabelText('Regular expression (-fr)');
    expect(regexp).toBeEnabled();
    await user.type(regexp, 'Access denied');
    await user.click(screen.getByRole('button', { name: 'Apply filters' }));

    expect(onAdd).toHaveBeenCalledWith({ regexp: 'Access denied' });
  });

  it('explains why filters cannot be added to a started AND-mode scan', () => {
    const session = sessionFixture('running');
    session.options.filters.mode = 'and';
    const onAdd = renderFilters(session);

    expect(screen.getByRole('button', { name: 'Add filters' })).toBeDisabled();
    expect(screen.getByText(/AND-mode filters cannot be expanded after a scan starts/)).toBeVisible();
    expect(onAdd).not.toHaveBeenCalled();
  });
});
