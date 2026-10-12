import { fireEvent, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { render } from '../test/render';
import type { Preset } from '../types/api';
import { bundledProfiles, cloneConfig, defaultConfig } from '../utils/config';
import { NewSessionWizard } from './NewSessionWizard';

const inspectWordlist = vi.hoisted(() => vi.fn());

vi.mock('../api/client', () => ({
  api: { inspectWordlist },
}));

function preset(): Preset {
  const options = cloneConfig(defaultConfig);
  options.input.extensions = 'json,xml';
  options.http.headers = ['Authorization: Bearer FUZZ'];
  return {
    id: 'authenticated-api',
    name: 'Authenticated API',
    description: 'API discovery defaults',
    wordlist: 'api-words.txt',
    profile: 'php',
    options,
    explicit_flags: ['D', 'e', 'H'],
    created_at: '2026-10-11T00:00:00Z',
    updated_at: '2026-10-11T00:00:00Z',
  };
}

describe('NewSessionWizard enhanced workflows', () => {
  beforeEach(() => {
    Element.prototype.scrollIntoView = vi.fn();
    inspectWordlist.mockReset();
    inspectWordlist.mockImplementation((path: string) => Promise.resolve({
      path,
      readable: true,
      entries: 10,
      extension_markers: 3,
    }));
  });

  it('validates the wordlist and estimates DirSearch expansion from extension markers', async () => {
    render(
      <NewSessionWizard
        opened
        profiles={bundledProfiles}
        onClose={vi.fn()}
        onCreate={vi.fn()}
      />,
    );

    await waitFor(() => expect(inspectWordlist).toHaveBeenCalledWith('final.txt'));
    expect(await screen.findByText('10 entries')).toBeVisible();
    // ASP Minimal has four extensions. In DirSearch mode only marker rows are
    // expanded: (10 - 3) ordinary rows + (3 * 4) expanded rows.
    expect(screen.getByText('About 19 requests after extension expansion')).toBeVisible();
  });

  it('prevents submission when the server reports an unreadable wordlist', async () => {
    inspectWordlist.mockResolvedValue({
      path: 'final.txt',
      readable: false,
      entries: 0,
      error: 'permission denied',
    });
    render(
      <NewSessionWizard
        opened
        profiles={bundledProfiles}
        onClose={vi.fn()}
        onCreate={vi.fn()}
      />,
    );
    fireEvent.change(screen.getByLabelText(/Target URL/), {
      target: { value: 'https://target.example/FUZZ' },
    });

    expect(await screen.findByText('Not readable')).toBeVisible();
    expect(screen.getByText('permission denied')).toBeVisible();
    expect(screen.getByRole('button', { name: 'Create & run' })).toBeDisabled();
  });

  it('applies, saves, and deletes named presets without losing their scan settings', async () => {
    const user = userEvent.setup();
    const saved = vi.fn();
    const deleted = vi.fn();
    const entry = preset();
    render(
      <NewSessionWizard
        opened
        profiles={bundledProfiles}
        presets={[entry]}
        onClose={vi.fn()}
        onCreate={vi.fn()}
        onSavePreset={saved}
        onDeletePreset={deleted}
      />,
    );

    fireEvent.keyDown(screen.getByRole('combobox', { name: /Named preset/ }), { key: 'ArrowDown' });
    await user.click(await screen.findByRole('option', { name: entry.name }));

    expect(screen.getByLabelText(/Wordlist/)).toHaveValue('api-words.txt');
    expect(screen.getByLabelText('Extensions (-e)')).toHaveValue('json,xml');
    expect(screen.getByLabelText('Headers (-H)')).toHaveValue('Authorization: Bearer FUZZ');

    await user.type(screen.getByLabelText('Save current settings as'), 'API copy');
    await user.click(screen.getByRole('button', { name: 'Save preset' }));
    expect(saved).toHaveBeenCalledWith(expect.objectContaining({
      name: 'API copy',
      wordlist: 'api-words.txt',
      profile: 'php',
      explicit_flags: expect.arrayContaining(['D', 'e', 'H']),
    }));
    expect(saved.mock.calls[0][0].options.input.extensions).toBe('json,xml');

    await user.click(screen.getByRole('button', { name: 'Delete' }));
    expect(deleted).toHaveBeenCalledWith(entry.id);
  });
});
