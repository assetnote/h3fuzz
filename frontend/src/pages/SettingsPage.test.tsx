import { screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { describe, expect, it, vi } from 'vitest';
import { render } from '../test/render';
import type { OptionMetadata, Preset, Settings } from '../types/api';
import { bundledProfiles, cloneConfig, defaultConfig } from '../utils/config';
import { SettingsPage } from './SettingsPage';

describe('SettingsPage', () => {
  const protocolOptions: OptionMetadata[] = [
    ['http3', 'http.http3'],
    ['http2', 'http.http2'],
    ['http1', 'http.http1'],
    ['no-http3', 'http.no_http3'],
  ].map(([name, jsonPath]) => ({
    name,
    json_path: jsonPath,
    section: 'http',
    type: 'boolean',
    default: false,
    usage: `${name} protocol option`,
    aliases: [],
    repeatable: false,
    hidden: false,
    available: true,
  }));

  it('renders persisted Go settings with nullable slice fields', () => {
    const options = cloneConfig(defaultConfig);
    options.general.autocalibration_strings = null as unknown as string[];
    options.input.input_commands = null as unknown as string[];
    options.http.cookies = null as unknown as string[];
    const settings: Settings = {
      defaults: {
        wordlist: 'final.txt',
        profile: 'aspm',
        options,
        explicit_flags: ['D', 'e', 'mc', 'fc', 'H'],
      },
      updated_at: '2026-10-03T00:00:00Z',
    };

    render(<SettingsPage settings={settings} profiles={bundledProfiles} onSave={vi.fn()} />);

    expect(screen.getByRole('heading', { name: 'Defaults & settings' })).toBeVisible();
    expect(screen.getByDisplayValue('final.txt')).toBeVisible();
  });

  it('shows only the selected settings section', async () => {
    const user = userEvent.setup();
    const { container } = render(<SettingsPage profiles={bundledProfiles} onSave={vi.fn()} />);

    expect(screen.getByRole('heading', { name: 'New session defaults' })).toBeVisible();
    expect(screen.queryByRole('heading', { name: 'Protocol acceleration' })).not.toBeInTheDocument();
    expect(screen.queryByRole('heading', { name: 'All inherited options' })).not.toBeInTheDocument();

    const scroll = container.querySelector('.settingsScroll') as HTMLElement;
    scroll.scrollTop = 240;
    await user.click(screen.getByRole('tab', { name: /Protocols/ }));
    expect(screen.queryByRole('heading', { name: 'New session defaults' })).not.toBeInTheDocument();
    expect(screen.getByRole('heading', { name: 'Protocol acceleration' })).toBeVisible();
    expect(scroll.scrollTop).toBe(0);

    await user.click(screen.getByRole('tab', { name: /All options/ }));
    expect(screen.queryByRole('heading', { name: 'Protocol acceleration' })).not.toBeInTheDocument();
    expect(screen.getByRole('heading', { name: 'All inherited options' })).toBeVisible();
  });

  it('falls back to the validated automatic HTTP/3 defaults without an explicit thread count', async () => {
    const user = userEvent.setup();
    const onSave = vi.fn();
    render(<SettingsPage profiles={bundledProfiles} onSave={onSave} />);

    expect(screen.getByRole('switch', { name: /Automatic HTTP\/3 worker sizing/ })).toBeChecked();
    expect(screen.getByLabelText('Threads (-t)')).toHaveValue(1000);
    await user.click(screen.getByRole('tab', { name: /Protocols/ }));
    expect(screen.getByRole('radio', { name: 'Automatic' })).toBeChecked();
    expect(screen.getByText('Retained adaptive capacity defaults are configured without forcing HTTP/3.')).toBeVisible();
    expect(screen.getByText('8 initial / 64 max')).toBeVisible();
    expect(screen.getByText('2,048 initial / 16,384 max')).toBeVisible();

    await user.click(screen.getByRole('button', { name: 'Save defaults' }));
    expect(onSave).toHaveBeenCalledOnce();
    const saved = onSave.mock.calls[0][0] as Settings;
    expect(saved.defaults.explicit_flags).not.toContain('t');
    expect(saved.defaults.options.general.threads).toBe(1000);
    expect(saved.defaults.options.general.rate).toBe(0);
    expect(saved.defaults.options.general.scrapers).toBe('');
    expect(saved.defaults.options.http).toMatchObject({
      http3: false,
      http2: false,
      http1: false,
      no_http3: false,
      http3_connections: 0,
      http3_max_connections: 64,
      http3_max_workers: 65_536,
      http3_streams: 256,
    });
  });

  it('persists an HTTP/1-only default without retaining another protocol mode', async () => {
    const user = userEvent.setup();
    const onSave = vi.fn();
    render(<SettingsPage profiles={bundledProfiles} onSave={onSave} />);

    await user.click(screen.getByRole('tab', { name: /Protocol/ }));
    await user.click(screen.getByRole('radio', { name: 'Force HTTP/1' }));
    await user.click(screen.getByRole('button', { name: 'Save defaults' }));

    const saved = onSave.mock.calls[0][0] as Settings;
    expect(saved.defaults.options.http).toMatchObject({
      http3: false,
      no_http3: false,
      http2: false,
      http1: true,
    });
    expect(saved.defaults.explicit_flags).toContain('http1');
  });

  it('keeps mutually exclusive protocol flags out of inherited option fields', async () => {
    const user = userEvent.setup();
    render(
      <SettingsPage
        profiles={bundledProfiles}
        options={protocolOptions}
        onSave={vi.fn()}
      />,
    );

    await user.click(screen.getByRole('tab', { name: /All options/ }));
    for (const flag of ['http3', 'http2', 'http1', 'no-http3']) {
      expect(screen.queryByRole('switch', { name: `-${flag}` })).not.toBeInTheDocument();
    }
  });

  it('creates, applies, replaces, and deletes named presets', async () => {
    const user = userEvent.setup();
    const onSave = vi.fn();
    const onCreatePreset = vi.fn();
    const onUpdatePreset = vi.fn();
    const onDeletePreset = vi.fn();
    const presetOptions = cloneConfig(defaultConfig);
    presetOptions.input.extensions = 'json,xml';
    const preset: Preset = {
      id: 'api',
      name: 'API preset',
      description: 'Reusable API scan',
      wordlist: 'api.txt',
      profile: 'php',
      options: presetOptions,
      explicit_flags: ['D', 'e'],
      created_at: '2026-10-11T00:00:00Z',
      updated_at: '2026-10-11T00:00:00Z',
    };
    render(
      <SettingsPage
        profiles={bundledProfiles}
        presets={[preset]}
        onSave={onSave}
        onCreatePreset={onCreatePreset}
        onUpdatePreset={onUpdatePreset}
        onDeletePreset={onDeletePreset}
      />,
    );

    await user.click(screen.getByRole('tab', { name: /Presets/ }));
    await user.type(screen.getByLabelText('Preset name'), 'Team default');
    await user.type(screen.getByLabelText('Description'), 'Shared scan settings');
    await user.click(screen.getByRole('button', { name: 'Save current defaults' }));
    expect(onCreatePreset).toHaveBeenCalledWith(expect.objectContaining({
      name: 'Team default',
      description: 'Shared scan settings',
      wordlist: 'final.txt',
    }));

    await user.click(screen.getByRole('button', { name: 'Use as defaults' }));
    await user.click(screen.getByRole('button', { name: 'Save defaults' }));
    expect(onSave).toHaveBeenCalledWith(expect.objectContaining({
      defaults: expect.objectContaining({ wordlist: 'api.txt', profile: 'php' }),
    }));

    await user.click(screen.getByRole('button', { name: 'Replace contents' }));
    expect(onUpdatePreset).toHaveBeenCalledWith(expect.objectContaining({
      id: 'api',
      wordlist: 'api.txt',
      profile: 'php',
    }));
    await user.click(screen.getByRole('button', { name: 'Delete' }));
    expect(onDeletePreset).toHaveBeenCalledWith('api');
  });
});
