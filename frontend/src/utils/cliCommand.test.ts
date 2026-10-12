import { describe, expect, it } from 'vitest';
import type { OptionMetadata } from '../types/api';
import { cloneConfig, defaultConfig } from './config';
import { generateCLICommand, parseCLICommand, shellQuote, tokenizeCommand } from './cliCommand';

function flag(
  name: string,
  type: OptionMetadata['type'],
  jsonPath: string,
  overrides: Partial<OptionMetadata> = {},
): OptionMetadata {
  return {
    name,
    type,
    json_path: jsonPath,
    section: jsonPath.split('.')[0],
    default: type === 'boolean' ? false : type === 'integer' ? 0 : type.endsWith('_array') ? [] : '',
    usage: `${name} test flag`,
    aliases: [],
    repeatable: type === 'string_array' || type === 'object_array',
    hidden: false,
    available: true,
    ...overrides,
  };
}

const options: OptionMetadata[] = [
  flag('u', 'string', 'http.url', { aliases: ['url'] }),
  flag('w', 'string_array', 'input.wordlists', { aliases: ['wordlist'] }),
  flag('H', 'string_array', 'http.headers', { aliases: ['header'] }),
  flag('b', 'string_array', 'http.cookies', { aliases: ['cookie'] }),
  flag('t', 'integer', 'general.threads', { aliases: ['threads'], min: 1, max: 100_000 }),
	flag('http3-max-workers', 'integer', 'http.http3_max_workers', { min: 1, max: 65_536 }),
  flag('rate', 'integer', 'general.rate', { min: 0, max: 1_000_000 }),
  flag('D', 'boolean', 'input.dirsearch_compat', { aliases: ['dirsearch'], default: true }),
  flag('c', 'boolean', 'general.colors'),
  flag('ac', 'boolean', 'general.autocalibration'),
  flag('http2', 'boolean', 'http.http2'),
  flag('http1', 'boolean', 'http.http1'),
  flag('http3', 'boolean', 'http.http3'),
  flag('no-http3', 'boolean', 'http.no_http3'),
  flag('e', 'string', 'input.extensions'),
  flag('mode', 'string', 'input.input_mode', { choices: ['clusterbomb', 'pitchfork', 'sniper'] }),
  flag('mc', 'string', 'matcher.status'),
  flag('fc', 'string', 'filter.status'),
  flag('acs', 'string_array', 'general.autocalibration_strategies'),
  flag('enc', 'string_array', 'input.encoders'),
  flag('input-cmd', 'string_array', 'input.input_commands', {
    available: false,
    unavailable_reason: 'Commands are disabled in web sessions.',
  }),
  flag('preflight', 'object_array', 'http.preflights'),
  flag('preflight-var', 'object_array', 'http.preflights'),
  flag('preflight-var-auto', 'object_array', 'http.preflights'),
  flag('postflight', 'object_array', 'http.postflights'),
  flag('postflight-var', 'object_array', 'http.postflights'),
  flag('postflight-var-auto', 'object_array', 'http.postflights'),
];

describe('CLI command tokenization', () => {
  it('preserves quoted spaces, escaped quotes, and line continuations as argument data', () => {
    const parsed = tokenizeCommand(
      String.raw`h3fuzz -u 'https://example.test/a path/FUZZ' -H "X-Label: a\"b" \
-w wordlist.txt`,
    );

    expect(parsed).toEqual({
      tokens: [
        'h3fuzz',
        '-u',
        'https://example.test/a path/FUZZ',
        '-H',
        'X-Label: a"b',
        '-w',
        'wordlist.txt',
      ],
      diagnostics: [],
    });
  });

  it('preserves backslashes that are literal inside double quotes', () => {
    expect(tokenizeCommand(String.raw`h3fuzz -preflight-var "TOKEN:\d+"`).tokens).toEqual([
      'h3fuzz',
      '-preflight-var',
      String.raw`TOKEN:\d+`,
    ]);
  });

  it('reports malformed quoting and escapes without throwing', () => {
    expect(tokenizeCommand("h3fuzz -u 'https://example.test/FUZZ").diagnostics).toContainEqual({
      kind: 'error',
      message: "The command has an unterminated ' quote.",
    });
    expect(tokenizeCommand('h3fuzz -u https://example.test/FUZZ\\').diagnostics).toContainEqual({
      kind: 'error',
      message: 'The command ends with an incomplete escape.',
    });
  });

  it('quotes empty values and embedded apostrophes for a copyable shell command', () => {
    expect(shellQuote('')).toBe("''");
    expect(shellQuote('final.txt')).toBe('final.txt');
    expect(shellQuote("O'Reilly list.txt")).toBe(`'O'"'"'Reilly list.txt'`);
    expect(tokenizeCommand(`h3fuzz -w ${shellQuote("O'Reilly list.txt")}`).tokens).toEqual([
      'h3fuzz',
      '-w',
      "O'Reilly list.txt",
    ]);

    const untrusted = '$(touch /tmp/never); `whoami` && echo nope';
    expect(tokenizeCommand(`h3fuzz -H ${shellQuote(untrusted)}`)).toEqual({
      tokens: ['h3fuzz', '-H', untrusted],
      diagnostics: [],
    });
  });
});

describe('CLI command generation and import', () => {
  it('imports the standard h3fuzz discovery command shape', () => {
    const imported = parseCLICommand(
      [
        './h3fuzz',
        '-c',
        '-t 1000',
        "-w '/opt/wordlists/final list.txt'",
        '-D',
        '-e aspx,htm,html,js',
        '-mc all',
        '-fc 404',
        "-H 'User-Agent: Mozilla/5.0 Test Browser'",
        '-u https://example.test/FUZZ',
      ].join(' '),
      defaultConfig,
      options,
    );

    expect(imported.diagnostics).toEqual([]);
    expect(imported.target).toBe('https://example.test/FUZZ');
    expect(imported.wordlist).toBe('/opt/wordlists/final list.txt');
    expect(imported.config.general.colors).toBe(true);
    expect(imported.config.general.threads).toBe(1000);
		expect(imported.explicitFlags.has('t')).toBe(true);
    expect(imported.config.input.dirsearch_compat).toBe(true);
    expect(imported.config.input.extensions).toBe('aspx,htm,html,js');
    expect(imported.config.matchers.status).toBe('all');
    expect(imported.config.filters.status).toBe('404');
    expect(imported.config.http.headers).toEqual(['User-Agent: Mozilla/5.0 Test Browser']);
  });

	it('preserves explicit default worker values while omission remains automatic', () => {
		const automatic = parseCLICommand('h3fuzz', defaultConfig, options);
		expect(automatic.config.general.threads).toBe(1000);
		expect(automatic.config.http.http3_max_workers).toBe(65536);
		expect(automatic.explicitFlags.has('t')).toBe(false);
		expect(automatic.explicitFlags.has('http3-max-workers')).toBe(false);

		const explicit = parseCLICommand(
			'h3fuzz -t 40 -http3-max-workers 65536',
			defaultConfig,
			options,
		);
		expect(explicit.diagnostics).toEqual([]);
		expect(explicit.config.general.threads).toBe(40);
		expect(explicit.config.http.http3_max_workers).toBe(65536);
		expect(explicit.explicitFlags).toEqual(new Set(['t', 'http3-max-workers']));
	});

  it('round-trips scalar, repeated, and explicit false settings from any iterable', () => {
    const config = cloneConfig(defaultConfig);
    config.http.headers = ['User-Agent: Browser Test', 'X-Trace: one,two'];
    config.input.wordlists = ['lists/primary words.txt:FUZZ', 'lists/secondary.txt:ALT'];
    config.general.threads = 432;
    config.input.dirsearch_compat = false;
    config.filters.status = '404,429';
    function* explicitFlags() {
      yield* ['w', 'H', 't', 'D', 'fc'];
    }

    const generated = generateCLICommand(
      {
        target: "https://example.test/O'Reilly/FUZZ",
        wordlist: 'unused.txt',
        config,
        explicitFlags: explicitFlags(),
      },
      options,
    );

    expect(generated.diagnostics).toEqual([]);
    expect(tokenizeCommand(generated.command).tokens).toEqual([
      'h3fuzz',
      '-u',
      "https://example.test/O'Reilly/FUZZ",
      '-w',
      'lists/primary words.txt:FUZZ',
      '-w',
      'lists/secondary.txt:ALT',
      '-H',
      'User-Agent: Browser Test',
      '-H',
      'X-Trace: one,two',
      '-t',
      '432',
      '-D=false',
      '-fc',
      '404,429',
    ]);

    const imported = parseCLICommand(generated.command, defaultConfig, options);
    expect(imported.diagnostics).toEqual([]);
    expect(imported.target).toBe("https://example.test/O'Reilly/FUZZ");
    expect(imported.wordlist).toBe('lists/primary words.txt:FUZZ');
    expect(imported.config.input.wordlists).toEqual([
      'lists/primary words.txt:FUZZ',
      'lists/secondary.txt:ALT',
    ]);
    expect(imported.config.http.headers).toEqual(['User-Agent: Browser Test', 'X-Trace: one,two']);
    expect(imported.config.general.threads).toBe(432);
    expect(imported.config.input.dirsearch_compat).toBe(false);
    expect(imported.config.filters.status).toBe('404,429');
    expect(imported.explicitFlags).toEqual(new Set(['u', 'w', 'H', 't', 'D', 'fc']));
  });

  it('normalizes aliases while preserving repeatable values and commas in headers', () => {
    const base = cloneConfig(defaultConfig);
    const imported = parseCLICommand(
      [
        'ffuf',
        '--url=https://example.test/FUZZ',
        '--wordlist=one.txt,two.txt',
        '--header',
        "'X-Features: one,two'",
        '-H',
        "'Accept: application/json'",
        '--cookie',
        "'a=1; b=two'",
        '--threads=55',
        '--dirsearch=false',
        '-ac',
        '-acs',
        'advanced,greedy',
        '-acs',
        'custom',
        '-enc',
        'FUZZ:b64encode,FUZZ:urlencode',
      ].join(' '),
      base,
      options,
    );

    expect(imported.diagnostics).toEqual([]);
    expect(imported.target).toBe('https://example.test/FUZZ');
    expect(imported.wordlist).toBe('one.txt');
    expect(imported.config.input.wordlists).toEqual(['one.txt', 'two.txt']);
    expect(imported.config.http.headers).toEqual([
      'X-Features: one,two',
      'Accept: application/json',
    ]);
    expect(imported.config.http.cookies).toEqual(['a=1; b=two']);
    expect(imported.config.general.threads).toBe(55);
    expect(imported.config.input.dirsearch_compat).toBe(false);
    expect(imported.config.general.autocalibration).toBe(true);
    expect(imported.config.general.autocalibration_strategies).toEqual([
      'advanced',
      'greedy',
      'custom',
    ]);
    expect(imported.config.input.encoders).toEqual(['FUZZ:b64encode', 'FUZZ:urlencode']);
    expect(imported.explicitFlags).toEqual(
      new Set(['u', 'w', 'H', 'b', 't', 'D', 'ac', 'acs', 'enc']),
    );
    expect(base).toEqual(defaultConfig);
  });

  it('accepts the boolean spellings supported by Go flags', () => {
    const imported = parseCLICommand(
      'h3fuzz -ac=True -http2=TRUE -D=F',
      defaultConfig,
      options,
    );

    expect(imported.diagnostics).toEqual([]);
    expect(imported.config.general.autocalibration).toBe(true);
    expect(imported.config.http.http2).toBe(true);
    expect(imported.config.input.dirsearch_compat).toBe(false);
  });

  it.each([
    {
      flag: '-http3',
      base: { http3: false, no_http3: true, http2: true, http1: true },
      expected: { http3: true, no_http3: false, http2: false, http1: false },
    },
    {
      flag: '-no-http3',
      base: { http3: true, no_http3: false, http2: true, http1: true },
      expected: { http3: false, no_http3: true, http2: false, http1: false },
    },
    {
      flag: '-http2',
      base: { http3: true, no_http3: true, http2: false, http1: true },
      expected: { http3: false, no_http3: false, http2: true, http1: false },
    },
    {
      flag: '-http1',
      base: { http3: true, no_http3: true, http2: true, http1: false },
      expected: { http3: false, no_http3: false, http2: false, http1: true },
    },
  ])('lets an imported $flag override an inherited protocol mode', ({ flag: protocolFlag, base, expected }) => {
    const config = cloneConfig(defaultConfig);
    Object.assign(config.http, base);

    const imported = parseCLICommand(`h3fuzz ${protocolFlag}`, config, options);

    expect(imported.diagnostics).toEqual([]);
    expect({
      http3: imported.config.http.http3,
      no_http3: imported.config.http.no_http3,
      http2: imported.config.http.http2,
      http1: imported.config.http.http1,
    }).toEqual(expected);
  });

  it.each([
    {
      command: 'h3fuzz -http3 -no-http3',
      message: '-http3 and -no-http3 cannot be used together',
    },
    {
      command: 'h3fuzz -http3 -http2',
      message: '-http3 and -http2 cannot be used together',
    },
    {
      command: 'h3fuzz -http1 -http2',
      message: '-http1 and -http2 cannot be used together',
    },
    {
      command: 'h3fuzz -http1 -no-http3',
      message: '-http1 and -no-http3 cannot be used together',
    },
  ])('reports pasted protocol conflicts: $command', ({ command, message }) => {
    const imported = parseCLICommand(command, defaultConfig, options);

    expect(imported.diagnostics).toContainEqual({ kind: 'error', message });
  });

  it('accepts the redundant HTTP/2 preference and no-HTTP/3 combination supported by the CLI', () => {
    const config = cloneConfig(defaultConfig);
    config.http.http3 = true;
    config.http.http1 = true;

    const imported = parseCLICommand('h3fuzz -http2 -no-http3', config, options);

    expect(imported.diagnostics).toEqual([]);
    expect(imported.config.http).toMatchObject({
      http3: false,
      no_http3: true,
      http2: true,
      http1: false,
    });
    expect(imported.explicitFlags).toEqual(new Set(['http2', 'no-http3']));
  });

  it('uses final pasted boolean values when deciding protocol conflicts', () => {
    const imported = parseCLICommand(
      'h3fuzz -http3 -http3=false -no-http3 -no-http3=false -http1 -http1=false -http2',
      defaultConfig,
      options,
    );

    expect(imported.diagnostics).toEqual([]);
    expect(imported.config.http.http3).toBe(false);
    expect(imported.config.http.no_http3).toBe(false);
    expect(imported.config.http.http2).toBe(true);
    expect(imported.config.http.http1).toBe(false);
  });

  it('round-trips an explicit HTTP/1-only selection', () => {
    const config = cloneConfig(defaultConfig);
    config.http.http1 = true;
    const generated = generateCLICommand(
      {
        target: 'https://example.test/FUZZ',
        wordlist: 'final.txt',
        config,
        explicitFlags: ['http1'],
      },
      options,
    );

    expect(generated.diagnostics).toEqual([]);
    expect(tokenizeCommand(generated.command).tokens).toContain('-http1');
    const imported = parseCLICommand(generated.command, defaultConfig, options);
    expect(imported.diagnostics).toEqual([]);
    expect(imported.config.http).toMatchObject({
      http3: false,
      no_http3: false,
      http2: false,
      http1: true,
    });
  });

  it('rejects shell control operators while treating quoted operator text as data', () => {
    const imported = parseCLICommand(
      "h3fuzz -u https://example.test/FUZZ -w final.txt -H 'X-Rule: left && right' && touch /tmp/pwned",
      defaultConfig,
      options,
    );

    expect(imported.target).toBe('https://example.test/FUZZ');
    expect(imported.wordlist).toBe('final.txt');
    expect(imported.config.http.headers).toEqual(['X-Rule: left && right']);
    expect(imported.diagnostics).toContainEqual({
      kind: 'error',
      message: 'Shell operator && is not allowed. Commands are parsed, never executed.',
    });
    expect(imported.diagnostics.some(({ message }) => message.includes('touch'))).toBe(true);
  });

  it('warns and skips unknown or web-unavailable flags without applying their values', () => {
    const imported = parseCLICommand(
      "custom-fuzzer -u https://example.test/FUZZ -w final.txt -input-cmd 'touch /tmp/nope' -future value",
      defaultConfig,
      options,
    );

    expect(imported.config.input.input_commands).toEqual(defaultConfig.input.input_commands);
    expect(imported.explicitFlags.has('input-cmd')).toBe(false);
    expect(imported.diagnostics).toEqual(
      expect.arrayContaining([
        {
          kind: 'warning',
          message:
            'Command executable custom-fuzzer is not h3fuzz or ffuf; only recognized flags were imported.',
        },
        {
          kind: 'warning',
          message: '-input-cmd is unavailable in web sessions and was not imported.',
        },
        { kind: 'warning', message: 'Unknown flag -future was ignored.' },
      ]),
    );

    const config = cloneConfig(defaultConfig);
    config.input.input_commands = ['touch /tmp/nope'];
    const generated = generateCLICommand(
      {
        target: 'https://example.test/FUZZ',
        wordlist: 'final.txt',
        config,
        explicitFlags: new Set(['input-cmd', 'future']),
      },
      options,
    );
    expect(tokenizeCommand(generated.command).tokens).not.toContain('touch /tmp/nope');
    expect(generated.diagnostics).toEqual(
      expect.arrayContaining([
        {
          kind: 'warning',
          message: '-input-cmd is unavailable in web sessions and was omitted.',
        },
        {
          kind: 'warning',
          message: '-future is not present in the current flag registry and was omitted.',
        },
      ]),
    );
  });

  it.each([
    {
      label: 'non-integer',
      command: 'h3fuzz -t many',
      message: "-t expects a base-10 integer, received many.",
      unchanged: (config: typeof defaultConfig) => config.general.threads === defaultConfig.general.threads,
    },
    {
      label: 'exponent-form integer',
      command: 'h3fuzz -t 1e3',
      message: "-t expects a base-10 integer, received 1e3.",
      unchanged: (config: typeof defaultConfig) => config.general.threads === defaultConfig.general.threads,
    },
    {
      label: 'hexadecimal integer',
      command: 'h3fuzz -t 0x10',
      message: "-t expects a base-10 integer, received 0x10.",
      unchanged: (config: typeof defaultConfig) => config.general.threads === defaultConfig.general.threads,
    },
    {
      label: 'out-of-range integer',
      command: 'h3fuzz -t 0',
      message: '-t must be between 1 and 100000.',
      unchanged: (config: typeof defaultConfig) => config.general.threads === defaultConfig.general.threads,
    },
    {
      label: 'invalid choice',
      command: 'h3fuzz -mode diagonal',
      message: '-mode must be one of clusterbomb, pitchfork, sniper.',
      unchanged: (config: typeof defaultConfig) =>
        config.input.input_mode === defaultConfig.input.input_mode,
    },
    {
      label: 'invalid boolean assignment',
      command: 'h3fuzz -ac=TrUe',
      message: '-ac expects true or false when assigned a value.',
      unchanged: (config: typeof defaultConfig) =>
        config.general.autocalibration === defaultConfig.general.autocalibration,
    },
    {
      label: 'missing value',
      command: 'h3fuzz -rate',
      message: '-rate requires a value.',
      unchanged: (config: typeof defaultConfig) => config.general.rate === defaultConfig.general.rate,
    },
  ])('reports $label without changing that setting', ({ command, message, unchanged }) => {
    const imported = parseCLICommand(command, defaultConfig, options);

    expect(imported.diagnostics).toContainEqual({ kind: 'error', message });
    expect(unchanged(imported.config)).toBe(true);
  });

  it('round-trips ordered preflight and postflight request chains', () => {
    const config = cloneConfig(defaultConfig);
    config.http.preflights = [
      {
        request_file: 'requests/login flow.txt',
        vars: [
          { name: 'TOKEN', regex: String.raw`csrf: ([a-f0-9]+)` },
          { name: 'SESSION', regex: '', source: 'cookie', key: 'sid' },
          { name: 'AUTO', regex: '', source: 'auto', key: 'csrf_token' },
        ],
      },
      {
        request_file: 'requests/second.txt',
        vars: [{ name: 'NEXT', regex: '', source: 'header', key: 'X-Next' }],
      },
    ];
    config.http.postflights = [
      {
        request_file: 'requests/logout.txt',
        vars: [{ name: 'OUT', regex: String.raw`done:(\d+)` }],
      },
    ];

    const generated = generateCLICommand(
      {
        target: 'https://example.test/FUZZ',
        wordlist: 'final.txt',
        config,
        explicitFlags: new Set(),
      },
      options,
    );
    expect(generated.diagnostics).toEqual([]);
    expect(tokenizeCommand(generated.command).tokens).toEqual([
      'h3fuzz',
      '-u',
      'https://example.test/FUZZ',
      '-w',
      'final.txt',
      '-preflight',
      'requests/login flow.txt',
      '-preflight-var',
      String.raw`TOKEN:csrf: ([a-f0-9]+)`,
      '-preflight-var-auto',
      'SESSION:[cookie]sid',
      '-preflight-var-auto',
      'AUTO:csrf_token',
      '-preflight',
      'requests/second.txt',
      '-preflight-var-auto',
      'NEXT:[header]X-Next',
      '-postflight',
      'requests/logout.txt',
      '-postflight-var',
      String.raw`OUT:done:(\d+)`,
    ]);

    const imported = parseCLICommand(generated.command, defaultConfig, options);
    expect(imported.diagnostics).toEqual([]);
    expect(imported.config.http.preflights).toEqual(config.http.preflights);
    expect(imported.config.http.postflights).toEqual(config.http.postflights);
    expect(imported.explicitFlags).toEqual(
      new Set([
        'u',
        'w',
        'preflight',
        'preflight-var',
        'preflight-var-auto',
        'postflight',
        'postflight-var',
      ]),
    );
  });

  it('replaces only flight kinds present in the imported command', () => {
    const base = cloneConfig(defaultConfig);
    base.http.preflights = [
      { request_file: 'defaults/login.txt', vars: [{ name: 'OLD', regex: 'old=(.+)' }] },
    ];
    base.http.postflights = [
      { request_file: 'defaults/logout.txt', vars: [{ name: 'KEEP', regex: 'keep=(.+)' }] },
    ];

    const imported = parseCLICommand(
      'h3fuzz -preflight replacement.txt -preflight-var NEW:new=(.+)',
      base,
      options,
    );

    expect(imported.diagnostics).toEqual([]);
    expect(imported.config.http.preflights).toEqual([
      { request_file: 'replacement.txt', vars: [{ name: 'NEW', regex: 'new=(.+)' }] },
    ]);
    expect(imported.config.http.postflights).toEqual(base.http.postflights);
    expect(base.http.preflights[0].request_file).toBe('defaults/login.txt');
  });

  it('returns actionable diagnostics for empty or flag-free commands', () => {
    expect(parseCLICommand('   ', defaultConfig, options).diagnostics).toContainEqual({
      kind: 'error',
      message: 'Enter an h3fuzz or ffuf command to import.',
    });
    expect(parseCLICommand('h3fuzz', defaultConfig, options).diagnostics).toContainEqual({
      kind: 'error',
      message: 'No supported h3fuzz flags were found in the command.',
    });

    const generated = generateCLICommand(
      { target: ' ', wordlist: ' ', config: defaultConfig, explicitFlags: [] },
      options,
    );
    expect(generated.command).toBe('h3fuzz');
    expect(generated.diagnostics).toEqual([
      { kind: 'warning', message: 'A target URL is required before this command can run.' },
      { kind: 'warning', message: 'A wordlist is required before this command can run.' },
    ]);
  });

  it('omits malformed stored flight entries instead of generating a rejected command', () => {
    const config = cloneConfig(defaultConfig);
    config.http.preflights = [
      {
        request_file: '',
        vars: [{ name: 'IGNORED', regex: 'value=(.+)' }],
      },
      {
        request_file: 'login.txt',
        vars: [
          { name: '', regex: 'missing-name=(.+)' },
          { name: 'EMPTY_REGEX', regex: '' },
          { name: 'EMPTY_KEY', regex: '', source: 'auto', key: '' },
          { name: 'GOOD', regex: 'token=(.+)' },
        ],
      },
    ];
    config.http.postflights = [{ request_file: '   ', vars: [] }];

    const generated = generateCLICommand(
      {
        target: 'https://example.test/FUZZ',
        wordlist: 'final.txt',
        config,
        explicitFlags: [],
      },
      options,
    );

    expect(tokenizeCommand(generated.command).tokens).toEqual([
      'h3fuzz',
      '-u',
      'https://example.test/FUZZ',
      '-w',
      'final.txt',
      '-preflight',
      'login.txt',
      '-preflight-var',
      'GOOD:token=(.+)',
    ]);
    expect(generated.diagnostics).toEqual(
      expect.arrayContaining([
        { kind: 'warning', message: 'Preflight entry 1 has no request file and was omitted.' },
        { kind: 'warning', message: 'Preflight entry 2 variable 1 has no name and was omitted.' },
        {
          kind: 'warning',
          message: 'Preflight entry 2 variable EMPTY_REGEX has no regex and was omitted.',
        },
        {
          kind: 'warning',
          message: 'Preflight entry 2 variable EMPTY_KEY has no selector key and was omitted.',
        },
        { kind: 'warning', message: 'Postflight entry 1 has no request file and was omitted.' },
      ]),
    );
  });

  it.each([
    {
      command: 'h3fuzz -preflight-var TOKEN:value',
      message: '-preflight-var needs a preceding -preflight request file in the imported command.',
    },
    {
      command: 'h3fuzz -preflight login.txt -preflight-var TOKEN',
      message: '-preflight-var must use NAME:regex syntax.',
    },
    {
      command: 'h3fuzz -postflight logout.txt -postflight-var-auto TOKEN:[body]csrf',
      message: '-postflight-var-auto must use NAME:[source]key syntax.',
    },
    {
      command: 'h3fuzz -preflight=',
      message: '-preflight requires a non-empty request file.',
    },
  ])('rejects an invalid flight binding: $command', ({ command, message }) => {
    const imported = parseCLICommand(command, defaultConfig, options);

    expect(imported.diagnostics).toContainEqual({ kind: 'error', message });
  });
});
