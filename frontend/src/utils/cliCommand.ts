import type { ConfigOptions, FlightConfig, FlightVariable, OptionMetadata } from '../types/api';
import { cloneConfig } from './config';

export interface CommandDiagnostic {
  kind: 'warning' | 'error';
  message: string;
}

export interface CommandFormState {
  target: string;
  wordlist: string;
  config: ConfigOptions;
  explicitFlags: Iterable<string>;
}

export interface GeneratedCommand {
  command: string;
  diagnostics: CommandDiagnostic[];
}

export interface ParsedCommand {
  target: string;
  wordlist: string;
  config: ConfigOptions;
  explicitFlags: Set<string>;
  diagnostics: CommandDiagnostic[];
}

const flightFlags = new Set([
  'preflight',
  'preflight-var',
  'preflight-var-auto',
  'postflight',
  'postflight-var',
  'postflight-var-auto',
]);
const commaSplitFlags = new Set(['acs', 'enc', 'w']);
const shellOperators = new Set([';', '|', '||', '&', '&&', '<', '>', '>>']);
const protocolFlags = new Set(['http3', 'no-http3', 'http2', 'http1']);

function normalizedPath(path?: string): string[] {
  if (!path) return [];
  return path
    .replace(/^options\./, '')
    .split('.')
    .map((part) => (part === 'matcher' ? 'matchers' : part === 'filter' ? 'filters' : part));
}

function configValue(config: ConfigOptions, path?: string): unknown {
  let value: unknown = config;
  for (const part of normalizedPath(path)) {
    if (value === null || typeof value !== 'object') return undefined;
    value = (value as Record<string, unknown>)[part];
  }
  return value;
}

function setConfigValue(config: ConfigOptions, path: string | undefined, value: unknown): boolean {
  const parts = normalizedPath(path);
  if (parts.length === 0) return false;
  let cursor = config as unknown as Record<string, unknown>;
  for (const part of parts.slice(0, -1)) {
    const child = cursor[part];
    if (child === null || typeof child !== 'object') return false;
    cursor = child as Record<string, unknown>;
  }
  cursor[parts.at(-1)!] = value;
  return true;
}

function optionIndexes(options: OptionMetadata[]) {
  const byToken = new Map<string, OptionMetadata>();
  const byName = new Map<string, OptionMetadata>();
  options.forEach((option) => {
    if (!byName.has(option.name)) byName.set(option.name, option);
    byToken.set(option.name, option);
    option.aliases.forEach((alias) => byToken.set(alias, option));
  });
  return { byToken, byName };
}

export function shellQuote(value: string): string {
  if (value.length > 0 && /^[A-Za-z0-9_@%+=:,./-]+$/.test(value)) return value;
  return `'${value.replaceAll("'", `'"'"'`)}'`;
}

export function tokenizeCommand(command: string): { tokens: string[]; diagnostics: CommandDiagnostic[] } {
  const tokens: string[] = [];
  const diagnostics: CommandDiagnostic[] = [];
  let token = '';
  let started = false;
  let quote: "'" | '"' | undefined;
  const flush = () => {
    if (!started) return;
    tokens.push(token);
    token = '';
    started = false;
  };

  for (let index = 0; index < command.length; index += 1) {
    const character = command[index];
    if (quote === "'") {
      if (character === "'") quote = undefined;
      else token += character;
      started = true;
      continue;
    }
    if (quote === '"') {
      if (character === '"') quote = undefined;
      else if (character === '\\' && index + 1 < command.length) {
        const escaped = command[index + 1];
        if (escaped === '\n') index += 1;
        else if ('$`"\\'.includes(escaped)) {
          index += 1;
          token += escaped;
        } else token += character;
      } else token += character;
      started = true;
      continue;
    }
    if (character === "'" || character === '"') {
      quote = character;
      started = true;
      continue;
    }
    if (character === '\\') {
      if (index + 1 >= command.length) {
        diagnostics.push({ kind: 'error', message: 'The command ends with an incomplete escape.' });
        continue;
      }
      index += 1;
      if (command[index] !== '\n') {
        token += command[index];
        started = true;
      }
      continue;
    }
    if (/\s/.test(character)) {
      flush();
      continue;
    }
    if (';|&<>'.includes(character)) {
      flush();
      let operator = character;
      const next = command[index + 1];
      if ((character === '|' || character === '&' || character === '>') && next === character) {
        operator += next;
        index += 1;
      }
      tokens.push(operator);
      continue;
    }
    token += character;
    started = true;
  }
  flush();
  if (quote) diagnostics.push({ kind: 'error', message: `The command has an unterminated ${quote} quote.` });
  return { tokens, diagnostics };
}

function emitFlag(tokens: string[], name: string, value?: string) {
  tokens.push(`-${name}`);
  if (value !== undefined) tokens.push(value);
}

function serializeFlights(
  tokens: string[],
  kind: 'preflight' | 'postflight',
  flights: FlightConfig[],
  diagnostics: CommandDiagnostic[],
) {
  const label = kind === 'preflight' ? 'Preflight' : 'Postflight';
  flights.forEach((flight, flightIndex) => {
    if (!flight.request_file.trim()) {
      diagnostics.push({
        kind: 'warning',
        message: `${label} entry ${flightIndex + 1} has no request file and was omitted.`,
      });
      return;
    }
    emitFlag(tokens, kind, flight.request_file);
    flight.vars.forEach((variable, variableIndex) => {
      const variableLabel = variable.name.trim() || String(variableIndex + 1);
      if (!variable.name.trim()) {
        diagnostics.push({
          kind: 'warning',
          message: `${label} entry ${flightIndex + 1} variable ${variableIndex + 1} has no name and was omitted.`,
        });
        return;
      }
      if (variable.source || variable.key) {
        if (!variable.key?.trim()) {
          diagnostics.push({
            kind: 'warning',
            message: `${label} entry ${flightIndex + 1} variable ${variableLabel} has no selector key and was omitted.`,
          });
          return;
        }
        const source = variable.source && variable.source !== 'auto' ? `[${variable.source}]` : '';
        emitFlag(tokens, `${kind}-var-auto`, `${variable.name}:${source}${variable.key}`);
      } else {
        if (!variable.regex.trim()) {
          diagnostics.push({
            kind: 'warning',
            message: `${label} entry ${flightIndex + 1} variable ${variableLabel} has no regex and was omitted.`,
          });
          return;
        }
        emitFlag(tokens, `${kind}-var`, `${variable.name}:${variable.regex}`);
      }
    });
  });
}

export function generateCLICommand(
  state: CommandFormState,
  options: OptionMetadata[],
  executable = 'h3fuzz',
): GeneratedCommand {
  const diagnostics: CommandDiagnostic[] = [];
  const tokens = [executable];
  const { byToken } = optionIndexes(options);
  const emitted = new Set<string>();
  const explicitFlags = new Set(state.explicitFlags);

  if (state.target.trim()) emitFlag(tokens, 'u', state.target.trim());
  else diagnostics.push({ kind: 'warning', message: 'A target URL is required before this command can run.' });

  const configuredWordlists = state.config.input.wordlists.filter(Boolean);
  const wordlists = configuredWordlists.length > 0 && explicitFlags.has('w')
    ? configuredWordlists
    : state.wordlist.trim()
      ? [state.wordlist.trim()]
      : [];
  wordlists.forEach((wordlist) => emitFlag(tokens, 'w', wordlist));
  if (wordlists.length === 0) diagnostics.push({ kind: 'warning', message: 'A wordlist is required before this command can run.' });

  for (const requestedName of explicitFlags) {
    const option = byToken.get(requestedName);
    const name = option?.name ?? requestedName;
    if (emitted.has(name) || name === 'u' || name === 'w' || flightFlags.has(name)) continue;
    emitted.add(name);
    if (!option) {
      diagnostics.push({ kind: 'warning', message: `-${requestedName} is not present in the current flag registry and was omitted.` });
      continue;
    }
    if (option.available === false) {
      diagnostics.push({ kind: 'warning', message: `-${name} is unavailable in web sessions and was omitted.` });
      continue;
    }
    const value = configValue(state.config, option.json_path);
    if (option.type === 'boolean') {
      if (value) emitFlag(tokens, name);
      else if (option.default) tokens.push(`-${name}=false`);
      continue;
    }
    if (value === undefined || value === null) {
      diagnostics.push({ kind: 'warning', message: `-${name} has no serializable value and was omitted.` });
      continue;
    }
    if (option.type === 'string_array') {
      const values = Array.isArray(value) ? value.map(String).filter((entry) => entry.length > 0) : [];
      values.forEach((entry) => emitFlag(tokens, name, entry));
      continue;
    }
    if (option.type === 'object_array') continue;
    emitFlag(tokens, name, String(value));
  }

  serializeFlights(tokens, 'preflight', state.config.http.preflights, diagnostics);
  serializeFlights(tokens, 'postflight', state.config.http.postflights, diagnostics);

  return { command: tokens.map(shellQuote).join(' '), diagnostics };
}

function splitValue(value: string, name: string): string[] {
  return commaSplitFlags.has(name) ? value.split(',').filter((entry) => entry.length > 0) : [value];
}

function parseBoolean(value: string | undefined): boolean | undefined {
  if (value === undefined || value === '') return true;
  if (['1', 't', 'T', 'TRUE', 'true', 'True'].includes(value)) return true;
  if (['0', 'f', 'F', 'FALSE', 'false', 'False'].includes(value)) return false;
  return undefined;
}

function parseVariable(value: string, automatic: boolean): FlightVariable | undefined {
  const separator = value.indexOf(':');
  if (separator <= 0 || separator === value.length - 1) return undefined;
  const name = value.slice(0, separator);
  const selector = value.slice(separator + 1);
  if (!automatic) return { name, regex: selector };
  const sourceMatch = selector.match(/^\[(auto|form|meta|cookie|header)\](.+)$/);
  if (selector.startsWith('[') && !sourceMatch) return undefined;
  return {
    name,
    regex: '',
    source: (sourceMatch?.[1] as FlightVariable['source']) ?? 'auto',
    key: sourceMatch?.[2] ?? selector,
  };
}

function parseFlight(
  config: ConfigOptions,
  name: string,
  value: string,
  diagnostics: CommandDiagnostic[],
  seenFlightKinds: Set<'preflight' | 'postflight'>,
): boolean {
  const kind = name.startsWith('preflight') ? 'preflight' : 'postflight';
  const flights = kind === 'preflight' ? config.http.preflights : config.http.postflights;
  if (name === kind) {
    if (!value.trim()) {
      diagnostics.push({ kind: 'error', message: `-${name} requires a non-empty request file.` });
      return false;
    }
    if (!seenFlightKinds.has(kind)) {
      flights.splice(0, flights.length);
      seenFlightKinds.add(kind);
    }
    flights.push({ request_file: value, vars: [] });
    return true;
  }
  if (!seenFlightKinds.has(kind)) {
    diagnostics.push({ kind: 'error', message: `-${name} needs a preceding -${kind} request file in the imported command.` });
    return false;
  }
  const latest = flights.at(-1);
  if (!latest) {
    diagnostics.push({ kind: 'error', message: `-${name} needs a preceding -${kind} request file.` });
    return false;
  }
  const variable = parseVariable(value, name.endsWith('-auto'));
  if (!variable) {
    diagnostics.push({ kind: 'error', message: `-${name} must use NAME:${name.endsWith('-auto') ? '[source]key' : 'regex'} syntax.` });
    return false;
  }
  latest.vars.push(variable);
  return true;
}

export function parseCLICommand(
  command: string,
  baseConfig: ConfigOptions,
  options: OptionMetadata[],
): ParsedCommand {
  const lexed = tokenizeCommand(command);
  const diagnostics = [...lexed.diagnostics];
  const config = cloneConfig(baseConfig);
  const explicitFlags = new Set<string>();
  const seenArrays = new Set<string>();
  const seenFlightKinds = new Set<'preflight' | 'postflight'>();
  const importedProtocolValues = new Map<string, boolean>();
  const { byToken } = optionIndexes(options);
  let target = '';
  let wordlist = '';
  let cursor = 0;

  if (lexed.tokens.length === 0) {
    diagnostics.push({ kind: 'error', message: 'Enter an h3fuzz or ffuf command to import.' });
  }

  if (lexed.tokens[0] && !lexed.tokens[0].startsWith('-') && !shellOperators.has(lexed.tokens[0])) {
    const executable = lexed.tokens[0].split(/[\\/]/).at(-1)?.toLowerCase();
    if (executable !== 'h3fuzz' && executable !== 'ffuf') {
      diagnostics.push({ kind: 'warning', message: `Command executable ${lexed.tokens[0]} is not h3fuzz or ffuf; only recognized flags were imported.` });
    }
    cursor = 1;
  }

  while (cursor < lexed.tokens.length) {
    const token = lexed.tokens[cursor];
    cursor += 1;
    if (shellOperators.has(token)) {
      diagnostics.push({ kind: 'error', message: `Shell operator ${token} is not allowed. Commands are parsed, never executed.` });
      continue;
    }
    if (!token.startsWith('-') || token === '-') {
      diagnostics.push({ kind: 'warning', message: `Positional token ${shellQuote(token)} was ignored.` });
      continue;
    }
    const withoutPrefix = token.replace(/^-{1,2}/, '');
    const equalsAt = withoutPrefix.indexOf('=');
    const suppliedName = equalsAt >= 0 ? withoutPrefix.slice(0, equalsAt) : withoutPrefix;
    const inlineValue = equalsAt >= 0 ? withoutPrefix.slice(equalsAt + 1) : undefined;
    const option = byToken.get(suppliedName);
    if (!option) {
      diagnostics.push({ kind: 'warning', message: `Unknown flag -${suppliedName} was ignored.` });
      continue;
    }
    const name = option.name;
    const needsValue = option.type !== 'boolean';
    let rawValue = inlineValue;
    if (needsValue && rawValue === undefined) {
      const next = lexed.tokens[cursor];
      if (next === undefined || shellOperators.has(next)) {
        diagnostics.push({ kind: 'error', message: `-${suppliedName} requires a value.` });
        continue;
      }
      rawValue = next;
      cursor += 1;
    }
    if (option.available === false) {
      diagnostics.push({ kind: 'warning', message: `-${suppliedName} is unavailable in web sessions and was not imported.` });
      continue;
    }
    if (name === 'u') {
      target = rawValue ?? '';
      config.http.url = target;
      explicitFlags.add(name);
      continue;
    }
    if (flightFlags.has(name)) {
      if (parseFlight(config, name, rawValue ?? '', diagnostics, seenFlightKinds)) explicitFlags.add(name);
      continue;
    }
    if (option.type === 'boolean') {
      const value = parseBoolean(inlineValue);
      if (value === undefined) {
        diagnostics.push({ kind: 'error', message: `-${suppliedName} expects true or false when assigned a value.` });
        continue;
      }
      if (!setConfigValue(config, option.json_path, value)) {
        diagnostics.push({ kind: 'warning', message: `-${suppliedName} has no web-session setting and was ignored.` });
        continue;
      }
      if (protocolFlags.has(name)) importedProtocolValues.set(name, value);
      explicitFlags.add(name);
      continue;
    }
    if (option.type === 'integer') {
      const value = Number(rawValue);
      if (!/^[+-]?\d+$/.test(rawValue ?? '') || !Number.isSafeInteger(value)) {
        diagnostics.push({ kind: 'error', message: `-${suppliedName} expects a base-10 integer, received ${shellQuote(rawValue ?? '')}.` });
        continue;
      }
      if ((option.min !== undefined && value < option.min) || (option.max !== undefined && value > option.max)) {
        diagnostics.push({ kind: 'error', message: `-${suppliedName} must be between ${option.min ?? '-∞'} and ${option.max ?? '∞'}.` });
        continue;
      }
      setConfigValue(config, option.json_path, value);
      explicitFlags.add(name);
      continue;
    }
    if (option.choices?.length && !option.choices.includes(rawValue ?? '')) {
      diagnostics.push({ kind: 'error', message: `-${suppliedName} must be one of ${option.choices.join(', ')}.` });
      continue;
    }
    if (option.type === 'string_array') {
      if (!seenArrays.has(name)) {
        setConfigValue(config, option.json_path, []);
        seenArrays.add(name);
      }
      const current = configValue(config, option.json_path);
      const next = [...(Array.isArray(current) ? current.map(String) : []), ...splitValue(rawValue ?? '', name)];
      setConfigValue(config, option.json_path, next);
      if (name === 'w' && !wordlist) wordlist = next[0] ?? '';
      explicitFlags.add(name);
      continue;
    }
    if (!setConfigValue(config, option.json_path, rawValue ?? '')) {
      diagnostics.push({ kind: 'warning', message: `-${suppliedName} has no web-session setting and was ignored.` });
      continue;
    }
    explicitFlags.add(name);
  }

  const selectedProtocols = [...importedProtocolValues.entries()]
    .filter(([, enabled]) => enabled)
    .map(([name]) => name);
  for (let left = 0; left < selectedProtocols.length; left += 1) {
    for (let right = left + 1; right < selectedProtocols.length; right += 1) {
      const pair = new Set([selectedProtocols[left], selectedProtocols[right]]);
      // The Go CLI accepts this redundant pair: both flags skip HTTP/3 and
      // retain the normal HTTP/2-to-HTTP/1.1 negotiation path.
      if (pair.has('http2') && pair.has('no-http3')) continue;
      diagnostics.push({
        kind: 'error',
        message: `-${selectedProtocols[left]} and -${selectedProtocols[right]} cannot be used together`,
      });
    }
  }
  if (selectedProtocols.length > 0) {
    config.http.http3 = selectedProtocols.includes('http3');
    config.http.no_http3 = selectedProtocols.includes('no-http3');
    config.http.http2 = selectedProtocols.includes('http2');
    config.http.http1 = selectedProtocols.includes('http1');
  }

  if (lexed.tokens.length > 0 && explicitFlags.size === 0) {
    diagnostics.push({ kind: 'error', message: 'No supported h3fuzz flags were found in the command.' });
  }
  if (!wordlist) wordlist = config.input.wordlists[0] ?? '';
  return { target, wordlist, config, explicitFlags, diagnostics };
}
