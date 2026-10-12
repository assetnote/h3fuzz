import { fireEvent, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { api } from '../api/client';
import type { ConfigOptions, OptionMetadata, Session, Settings } from '../types/api';
import { bundledProfiles, cloneConfig, defaultConfig } from '../utils/config';
import { akamaiHeaders } from '../utils/headers';
import { render } from '../test/render';
import { NewSessionWizard } from './NewSessionWizard';

function renderWizard() {
  const onCreate = vi.fn();
  render(
    <NewSessionWizard
      opened
      profiles={bundledProfiles}
      options={[]}
      onClose={vi.fn()}
      onCreate={onCreate}
    />,
  );
  return { onCreate, user: userEvent.setup() };
}

const headerOption: OptionMetadata = {
  name: 'H',
  json_path: 'http.headers',
  section: 'http',
  type: 'string_array',
  default: [],
  usage: 'Header "Name: Value", separated by colon. Multiple -H flags are accepted.',
  aliases: [],
  repeatable: true,
  hidden: false,
  available: true,
};

function commandOption(
  name: string,
  jsonPath: string,
  type: OptionMetadata['type'] = 'string',
  defaultValue: unknown = '',
): OptionMetadata {
  return {
    name,
    json_path: jsonPath,
    section: jsonPath.split('.')[0],
    type,
    default: defaultValue,
    usage: `${name} option`,
    aliases: [],
    repeatable: type === 'string_array',
    hidden: false,
    available: true,
  };
}

const commandOptions: OptionMetadata[] = [
  commandOption('u', 'http.url'),
  commandOption('w', 'input.wordlists', 'string_array', []),
  commandOption('D', 'input.dirsearch_compat', 'boolean', false),
  commandOption('e', 'input.extensions'),
  commandOption('mc', 'matchers.status'),
  commandOption('fc', 'filters.status'),
  headerOption,
  { ...commandOption('t', 'general.threads', 'integer', 40), min: 1 },
  { ...commandOption('rate', 'general.rate', 'integer', 0), min: 0 },
  commandOption('p', 'general.delay'),
  {
    ...commandOption('mode', 'input.input_mode', 'string', 'clusterbomb'),
    choices: ['clusterbomb', 'pitchfork', 'sniper'],
  },
  commandOption('d', 'http.data'),
  commandOption('http3', 'http.http3', 'boolean', false),
  commandOption('http2', 'http.http2', 'boolean', false),
  commandOption('http1', 'http.http1', 'boolean', false),
  commandOption('no-http3', 'http.no_http3', 'boolean', false),
];

describe('NewSessionWizard', () => {
  beforeEach(() => {
    vi.spyOn(api, 'inspectWordlist').mockImplementation((path: string) => Promise.resolve({
      path,
      readable: true,
      entries: 233_583,
      extension_markers: 42_000,
    }));
  });

  it('submits one-form sessions from server-shaped nullable settings', async () => {
    const user = userEvent.setup();
    const options = cloneConfig(defaultConfig);
    options.http.headers = null as unknown as string[];
    options.http.cookies = null as unknown as string[];
    options.http.preflights = null as unknown as ConfigOptions['http']['preflights'];
    options.general.autocalibration_strings = null as unknown as string[];
    options.input.input_commands = null as unknown as string[];
    options.input.wordlists = null as unknown as string[];
    const settings: Settings = {
      defaults: {
        wordlist: 'final.txt',
        profile: 'aspm',
        options,
        explicit_flags: ['D', 'e', 'mc', 'fc', 'H'],
      },
    };
    const processFlag: OptionMetadata = {
      name: 'V',
      section: 'general',
      type: 'boolean',
      default: false,
      usage: 'Show version information.',
      aliases: [],
      repeatable: false,
      hidden: false,
      available: false,
    };
    const onCreate = vi.fn();

    render(
      <NewSessionWizard
        opened
        settings={settings}
        profiles={bundledProfiles}
        options={[processFlag]}
        onClose={vi.fn()}
        onCreate={onCreate}
      />,
    );

    expect(screen.queryByText('Target', { selector: '.wizardStepperLabel' })).not.toBeInTheDocument();
    expect(screen.getByLabelText('Words (-fw)')).toBeVisible();
    expect(screen.getByLabelText('Regexp (-mr)')).toBeVisible();
    await user.type(
      screen.getByPlaceholderText('https://target.example/FUZZ'),
      'https://target.example/FUZZ',
    );
    await user.click(screen.getByRole('switch', { name: /Akamai target/i }));
    await waitFor(() => expect(screen.getByRole('button', { name: 'Create & run' })).toBeEnabled());
    await user.click(screen.getByRole('button', { name: 'Create & run' }));

    expect(onCreate).toHaveBeenCalledOnce();
    const request = onCreate.mock.calls[0][0];
    expect(request.wordlist).toBe('final.txt');
    expect(request.options.input.input_mode).toBe('pitchfork');
    expect(request.options.input.input_commands).toEqual([]);
    expect(request.options.http.preflights).toEqual([]);
    expect(akamaiHeaders.every((header) => request.options.http.headers.includes(header))).toBe(true);
    expect(new Set(request.options.http.headers).size).toBe(request.options.http.headers.length);
  });

  it('blocks a bare target until a FUZZ keyword is present', () => {
    const { onCreate } = renderWizard();
    fireEvent.change(screen.getByPlaceholderText('https://target.example/FUZZ'), {
      target: { value: 'https://target.example' },
    });

    expect(screen.getByText('FUZZ keyword required')).toBeVisible();
    expect(screen.getByRole('button', { name: 'Create & run' })).toBeDisabled();
    expect(onCreate).not.toHaveBeenCalled();
  });

  it('allows a FUZZ keyword in the URL', async () => {
    const { onCreate, user } = renderWizard();
    await user.type(
      screen.getByPlaceholderText('https://target.example/FUZZ'),
      'https://target.example/FUZZ',
    );
    await waitFor(() => expect(screen.getByRole('button', { name: 'Create & run' })).toBeEnabled());
    await user.click(screen.getByRole('button', { name: 'Create & run' }));

    expect(onCreate).toHaveBeenCalledOnce();
    expect(onCreate.mock.calls[0][0].target).toBe('https://target.example/FUZZ');
		expect(onCreate.mock.calls[0][0].options.general.threads).toBe(1000);
		expect(onCreate.mock.calls[0][0].options).toMatchObject({
			general: { rate: 0, scrapers: '' },
			http: {
				http3: false,
				http2: false,
				http1: false,
				no_http3: false,
				no_response_optimization: false,
				http3_connections: 0,
				http3_max_connections: 64,
				http3_max_workers: 65_536,
				http3_streams: 256,
			},
		});
		expect(onCreate.mock.calls[0][0].explicit_flags).not.toEqual(expect.arrayContaining([
			't',
			'http3',
			'http3-high-rate',
			'http3-high-rate-acknowledge',
		]));
	  });

  it('explains the effective validated adaptive capacity and downgrade behavior', () => {
    renderWizard();

    expect(screen.getByText('Retained adaptive capacity defaults are configured without forcing HTTP/3.')).toBeVisible();
    expect(screen.getByText('8 initial / 64 max')).toBeVisible();
    expect(screen.getByText('256 per connection')).toBeVisible();
    expect(screen.getByText('2,048 initial / 16,384 max')).toBeVisible();
    expect(screen.getByText('Unlimited')).toBeVisible();
    expect(screen.getByText(/Probe HTTP\/3 first, then negotiate HTTP\/2, then fall back to HTTP\/1/i)).toBeVisible();
    expect(screen.getByText(/short scans may finish before adaptation converges/i)).toBeVisible();
  });

  it('forces HTTP/1 explicitly and emits the corresponding CLI flag', async () => {
    const user = userEvent.setup();
    const onCreate = vi.fn();
    render(
      <NewSessionWizard
        opened
        profiles={bundledProfiles}
        options={commandOptions}
        onClose={vi.fn()}
        onCreate={onCreate}
      />,
    );

    await user.type(
      screen.getByPlaceholderText('https://target.example/FUZZ'),
      'https://target.example/FUZZ',
    );
    await user.click(screen.getByRole('radio', { name: 'Force HTTP/1' }));

    expect(screen.getByLabelText('Generated CLI command')).toHaveTextContent('-http1');
    await user.click(screen.getByRole('button', { name: 'Create & run' }));

    const request = onCreate.mock.calls[0][0];
    expect(request.options.http).toMatchObject({
      http3: false,
      no_http3: false,
      http2: false,
      http1: true,
    });
    expect(request.explicit_flags).toContain('http1');
  });

	it('can opt into a fixed HTTP/3 worker count', async () => {
    const { onCreate, user } = renderWizard();
    await user.type(
      screen.getByPlaceholderText('https://target.example/FUZZ'),
      'https://target.example/FUZZ',
    );
    await user.click(screen.getByRole('switch', { name: /Automatic HTTP\/3 worker sizing/ }));
    await waitFor(() => expect(screen.getByRole('button', { name: 'Create & run' })).toBeEnabled());
    await user.click(screen.getByRole('button', { name: 'Create & run' }));

		expect(onCreate.mock.calls[0][0].options.general.threads).toBe(1000);
		expect(onCreate.mock.calls[0][0].explicit_flags).toContain('t');
  });

  it('allows a FUZZ keyword in a request header without changing the target', async () => {
    const { onCreate, user } = renderWizard();
    await user.type(
      screen.getByPlaceholderText('https://target.example/FUZZ'),
      'https://target.example',
    );
    const headers = screen.getByLabelText('Headers (-H)');
    await user.clear(headers);
    await user.type(headers, 'X-Discovery: FUZZ');
    await user.click(screen.getByRole('button', { name: 'Create & run' }));

    expect(onCreate).toHaveBeenCalledOnce();
    expect(onCreate.mock.calls[0][0].target).toBe('https://target.example');
    expect(onCreate.mock.calls[0][0].options.http.headers).toContain('X-Discovery: FUZZ');
  });

  it('preserves blank header lines while editing and removes them only on submit', async () => {
    const { onCreate, user } = renderWizard();
    await user.type(
      screen.getByPlaceholderText('https://target.example/FUZZ'),
      'https://target.example',
    );
    const headers = screen.getByLabelText('Headers (-H)');
    await user.clear(headers);
    await user.type(headers, 'X-First: one{Enter}{Enter}X-Discovery: FUZZ');

    expect(headers).toHaveValue('X-First: one\n\nX-Discovery: FUZZ');

    await user.click(screen.getByRole('button', { name: 'Create & run' }));

    expect(onCreate).toHaveBeenCalledOnce();
    expect(onCreate.mock.calls[0][0].options.http.headers).toEqual([
      'X-First: one',
      'X-Discovery: FUZZ',
    ]);
  });

  it('keeps the primary and advanced header editors synchronized without splitting commas', async () => {
    const user = userEvent.setup();
    const onCreate = vi.fn();
    render(
      <NewSessionWizard
        opened
        profiles={bundledProfiles}
        options={[headerOption]}
        onClose={vi.fn()}
        onCreate={onCreate}
      />,
    );

    await user.type(
      screen.getByPlaceholderText('https://target.example/FUZZ'),
      'https://target.example',
    );
    await user.click(screen.getByRole('button', { name: /Advanced options/i }));

    const primary = screen.getByLabelText('Headers (-H)');
    const advanced = screen.getByLabelText(/^-H/);
    expect(advanced).toHaveValue((primary as HTMLTextAreaElement).value);

    fireEvent.change(primary, { target: { value: 'X-Top: FUZZ' } });
    expect(advanced).toHaveValue('X-Top: FUZZ');

    const akamai = screen.getByRole('switch', { name: /Akamai target/i });
    await user.click(akamai);
    expect(akamai).toBeChecked();

    fireEvent.change(advanced, {
      target: { value: 'Accept: text/html, application/xhtml+xml\nX-Advanced: FUZZ' },
    });
    expect(primary).toHaveValue(
      'Accept: text/html, application/xhtml+xml\nX-Advanced: FUZZ',
    );
    expect(akamai).not.toBeChecked();

    await user.click(screen.getByRole('button', { name: 'Create & run' }));
    expect(onCreate.mock.calls[0][0].options.http.headers).toEqual([
      'Accept: text/html, application/xhtml+xml',
      'X-Advanced: FUZZ',
    ]);
  });

  it('allows a FUZZ keyword in the request body without changing the target', async () => {
    const { onCreate, user } = renderWizard();
    await user.type(
      screen.getByPlaceholderText('https://target.example/FUZZ'),
      'https://target.example',
    );
    await user.type(screen.getByLabelText('Request body (-d)'), 'username=admin&password=FUZZ');
    await user.click(screen.getByRole('button', { name: 'Create & run' }));

    expect(onCreate).toHaveBeenCalledOnce();
    expect(onCreate.mock.calls[0][0].target).toBe('https://target.example');
    expect(onCreate.mock.calls[0][0].options.http.data).toBe('username=admin&password=FUZZ');
    expect(onCreate.mock.calls[0][0].explicit_flags).toContain('d');
  });

  it('builds a copyable CLI command that updates with the form', async () => {
    const user = userEvent.setup();
    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, 'clipboard', { configurable: true, value: { writeText } });
    render(
      <NewSessionWizard
        opened
        profiles={bundledProfiles}
        options={commandOptions}
        onClose={vi.fn()}
        onCreate={vi.fn()}
      />,
    );

    await user.type(
      screen.getByPlaceholderText('https://target.example/FUZZ'),
      'https://target.example/FUZZ',
    );
    fireEvent.change(screen.getByLabelText('Rate limit (-rate)'), { target: { value: '175' } });

    const preview = screen.getByLabelText('Generated CLI command');
    expect(preview).toHaveTextContent('h3fuzz -u https://target.example/FUZZ -w final.txt');
    expect(preview).toHaveTextContent('-rate 175');
		expect(preview).not.toHaveTextContent(/(?:^|\s)-t(?:\s|$)/);
		expect(preview).not.toHaveTextContent(/(?:^|\s)-http3(?:\s|$)/);
		expect(preview).not.toHaveTextContent(/(?:^|\s)-http2(?:\s|$)/);
		expect(preview).not.toHaveTextContent(/(?:^|\s)-http1(?:\s|$)/);
		expect(preview).not.toHaveTextContent(/(?:^|\s)-no-http3(?:\s|$)/);
		expect(preview).not.toHaveTextContent('-http3-high-rate');
    expect(preview).not.toHaveTextContent('FFUFHASH');

    await user.click(screen.getByRole('button', { name: 'Copy command' }));
    expect(writeText).toHaveBeenCalledWith(preview.textContent);
  });

  it('imports a CLI command into the corresponding form settings', async () => {
    const user = userEvent.setup();
    const onCreate = vi.fn();
    render(
      <NewSessionWizard
        opened
        profiles={bundledProfiles}
        options={commandOptions}
        onClose={vi.fn()}
        onCreate={onCreate}
      />,
    );

    fireEvent.change(screen.getByLabelText('Import a CLI command'), {
      target: {
        value: './h3fuzz -u https://imported.example/FUZZ -w custom.txt -w secondary.txt -e php,html,htm,js -t 77 -rate 200 -fc 403 -H "X-Source: imported" -no-http3',
      },
    });
    await user.click(screen.getByRole('button', { name: 'Apply command to form' }));

    expect(screen.getByText('Command applied')).toBeVisible();
    expect(screen.getByLabelText(/Target URL/)).toHaveValue('https://imported.example/FUZZ');
    const wordlist = screen.getByLabelText(/Wordlist/);
    expect(wordlist).toHaveValue('custom.txt');
    expect(screen.getByLabelText('Extensions (-e)')).toHaveValue('php,html,htm,js');
    expect(screen.getByLabelText('Threads (-t)')).toHaveValue(77);
    expect(screen.getByLabelText('Rate limit (-rate)')).toHaveValue(200);
    expect(screen.getByLabelText('Status (-fc)')).toHaveValue('403');
    expect(screen.getByLabelText('Headers (-H)')).toHaveValue('X-Source: imported');
    expect(screen.getByRole('radio', { name: 'Skip HTTP/3' })).toBeChecked();

    fireEvent.change(wordlist, { target: { value: 'updated.txt' } });
    expect(screen.getByLabelText('Generated CLI command')).toHaveTextContent(
      '-w updated.txt -w secondary.txt',
    );

    await waitFor(() => expect(screen.getByRole('button', { name: 'Create & run' })).toBeEnabled());
    await user.click(screen.getByRole('button', { name: 'Create & run' }));
    const request = onCreate.mock.calls[0][0];
    expect(request.profile).toBe('php');
    expect(request.wordlist).toBe('updated.txt');
    expect(request.options.input.wordlists).toEqual(['updated.txt', 'secondary.txt']);
    expect(request.options.http.no_http3).toBe(true);
    expect(request.explicit_flags).toEqual(expect.arrayContaining(['u', 'w', 'e', 't', 'rate', 'fc', 'H', 'no-http3']));
  });

  it('shows an imported HTTP/2 preference as Skip HTTP/3 without rewriting its CLI intent', async () => {
    const user = userEvent.setup();
    render(
      <NewSessionWizard
        opened
        profiles={bundledProfiles}
        options={commandOptions}
        onClose={vi.fn()}
        onCreate={vi.fn()}
      />,
    );

    fireEvent.change(screen.getByLabelText('Import a CLI command'), {
      target: { value: 'h3fuzz -u https://imported.example/FUZZ -w final.txt -http2' },
    });
    await user.click(screen.getByRole('button', { name: 'Apply command to form' }));

    expect(screen.getByRole('radio', { name: 'Skip HTTP/3' })).toBeChecked();
    const preview = screen.getByLabelText('Generated CLI command');
    expect(preview).toHaveTextContent('-http2');
    expect(preview).not.toHaveTextContent('-no-http3');

    await user.click(screen.getByRole('button', { name: /Advanced options/i }));
    for (const flag of ['http3', 'http2', 'http1', 'no-http3']) {
      expect(screen.queryByRole('switch', { name: `-${flag}` })).not.toBeInTheDocument();
    }
    expect(preview).toHaveTextContent('-http2');

    await user.click(screen.getByRole('radio', { name: 'Automatic' }));
    expect(preview).not.toHaveTextContent('-http2');
    expect(preview).not.toHaveTextContent('-no-http3');
  });

  it('validates every imported named wordlist by path and accepts their request keywords', async () => {
    vi.mocked(api.inspectWordlist).mockImplementation((path: string) => Promise.resolve({
      path,
      readable: true,
      entries: path === 'first.txt' ? 5 : 10,
      extension_markers: 0,
    }));
    const onCreate = vi.fn();
    render(
      <NewSessionWizard
        opened
        profiles={bundledProfiles}
        options={commandOptions}
        onClose={vi.fn()}
        onCreate={onCreate}
      />,
    );

    fireEvent.change(screen.getByLabelText('Import a CLI command'), {
      target: {
        value: 'h3fuzz -u https://imported.example/PATH/ALT -w first.txt:PATH -w second.txt:ALT -mode pitchfork -D=false -e php',
      },
    });
    fireEvent.click(screen.getByRole('button', { name: 'Apply command to form' }));

    await waitFor(() => {
      expect(api.inspectWordlist).toHaveBeenCalledWith('first.txt');
      expect(api.inspectWordlist).toHaveBeenCalledWith('second.txt');
    });
    expect(await screen.findByText('10 estimated requests')).toBeVisible();
    expect(screen.getByRole('button', { name: 'Create & run' })).toBeEnabled();

    fireEvent.click(screen.getByRole('button', { name: 'Create & run' }));
    expect(onCreate.mock.calls[0][0].options.input.wordlists).toEqual([
      'first.txt:PATH',
      'second.txt:ALT',
    ]);
  });

  it('blocks submission when any repeated wordlist is unreadable', async () => {
    vi.mocked(api.inspectWordlist).mockImplementation((path: string) => Promise.resolve({
      path,
      readable: path !== 'missing.txt',
      entries: path === 'missing.txt' ? 0 : 5,
      error: path === 'missing.txt' ? 'file not found' : undefined,
    }));
    render(
      <NewSessionWizard
        opened
        profiles={bundledProfiles}
        options={commandOptions}
        onClose={vi.fn()}
        onCreate={vi.fn()}
      />,
    );
    fireEvent.change(screen.getByLabelText('Import a CLI command'), {
      target: {
        value: 'h3fuzz -u https://imported.example/ONE/TWO -w ready.txt:ONE -w missing.txt:TWO -mode clusterbomb',
      },
    });
    fireEvent.click(screen.getByRole('button', { name: 'Apply command to form' }));

    expect(await screen.findByText('missing.txt:TWO: file not found')).toBeVisible();
    expect(screen.getByRole('button', { name: 'Create & run' })).toBeDisabled();
  });

  it('accepts a single unnamed sniper wordlist with paired section templates', async () => {
    const onCreate = vi.fn();
    render(
      <NewSessionWizard
        opened
        profiles={bundledProfiles}
        options={commandOptions}
        onClose={vi.fn()}
        onCreate={onCreate}
      />,
    );
    fireEvent.change(screen.getByLabelText('Import a CLI command'), {
      target: { value: 'h3fuzz -u https://target.example/§admin§ -w words.txt -mode sniper' },
    });
    fireEvent.click(screen.getByRole('button', { name: 'Apply command to form' }));

    await waitFor(() => expect(screen.getByRole('button', { name: 'Create & run' })).toBeEnabled());
    fireEvent.click(screen.getByRole('button', { name: 'Create & run' }));
    expect(onCreate).toHaveBeenCalledWith(expect.objectContaining({
      target: 'https://target.example/§admin§',
      wordlist: 'words.txt',
    }));
  });

  it('rejects shell operators without applying any part of the command', async () => {
    const user = userEvent.setup();
    render(
      <NewSessionWizard
        opened
        profiles={bundledProfiles}
        options={commandOptions}
        onClose={vi.fn()}
        onCreate={vi.fn()}
      />,
    );
    const target = screen.getByLabelText(/Target URL/);
    await user.type(target, 'https://original.example/FUZZ');
    fireEvent.change(screen.getByLabelText('Import a CLI command'), {
      target: { value: 'h3fuzz -u https://untrusted.example/FUZZ ; touch /tmp/marker' },
    });

    await user.click(screen.getByRole('button', { name: 'Apply command to form' }));

    expect(screen.getByText(/Shell operator ; is not allowed/)).toBeVisible();
    expect(target).toHaveValue('https://original.example/FUZZ');
    expect(screen.queryByText('Command applied')).not.toBeInTheDocument();
  });

  it('opens a completed session as an editable clone without starting it first', async () => {
    const user = userEvent.setup();
    const onCreate = vi.fn();
    const source: Session = {
      id: 'source', name: 'Production', target: 'https://source.example/FUZZ', status: 'completed',
      profile: 'php', wordlist: 'source.txt', options: cloneConfig(defaultConfig), explicit_flags: ['e', 't'],
      progress: { req_count: 10, req_total: 10, req_sec: 0, queue_pos: 0, queue_total: 0, error_count: 0 },
      result_count: 2, created_at: '2026-10-10T00:00:00Z', updated_at: '2026-10-10T00:01:00Z',
    };
    source.options.input.extensions = 'php,json';
    render(<NewSessionWizard opened initialSession={source} profiles={bundledProfiles} options={commandOptions} onClose={vi.fn()} onCreate={onCreate} />);

    expect(screen.getByRole('heading', { name: 'Clone and edit Production' })).toBeVisible();
    expect(screen.getByLabelText(/Target URL/)).toHaveValue(source.target);
    expect(screen.getByLabelText(/Wordlist/)).toHaveValue('source.txt');
    fireEvent.change(screen.getByLabelText('Extensions (-e)'), { target: { value: 'php,json,xml' } });
    await waitFor(() => expect(screen.getByRole('button', { name: 'Create & run' })).toBeEnabled());
    await user.click(screen.getByRole('button', { name: 'Create & run' }));

    expect(onCreate).toHaveBeenCalledWith(expect.objectContaining({ target: source.target, wordlist: 'source.txt' }));
    expect(onCreate.mock.calls[0][0].options.input.extensions).toBe('php,json,xml');
  });

  it('resets only the selected section and preserves unrelated explicit flags', async () => {
    const user = userEvent.setup();
    const onCreate = vi.fn();
    const settings: Settings = {
      defaults: {
        wordlist: 'final.txt',
        profile: 'dir',
        options: cloneConfig(defaultConfig),
        explicit_flags: ['H'],
      },
    };
    render(<NewSessionWizard opened settings={settings} profiles={bundledProfiles} options={commandOptions} onClose={vi.fn()} onCreate={onCreate} />);

    fireEvent.change(screen.getByLabelText(/Target URL/), { target: { value: 'https://target.example/FUZZ' } });
    fireEvent.change(screen.getByLabelText('Rate limit (-rate)'), { target: { value: '123' } });
    expect(screen.getByLabelText('Rate limit (-rate)')).toHaveValue(123);

    await user.click(screen.getAllByRole('button', { name: 'Reset section' })[1]);
    expect(screen.getByLabelText('Rate limit (-rate)')).toHaveValue(0);
    await waitFor(() => expect(screen.getByRole('button', { name: 'Create & run' })).toBeEnabled());
    await user.click(screen.getByRole('button', { name: 'Create & run' }));

    expect(onCreate.mock.calls[0][0].explicit_flags).toContain('H');
    expect(onCreate.mock.calls[0][0].explicit_flags).not.toContain('rate');
  });
});
