export interface WordlistSpec {
  raw: string;
  path: string;
  keyword: string;
  named: boolean;
}

export interface RequestInputs {
  method: string;
  target: string;
  data: string;
  headers: string[];
  cookies?: string[];
}

/** Parse ffuf's path[:KEYWORD] spelling without discarding duplicate providers. */
export function parseWordlistSpec(rawValue: string): WordlistSpec {
  const raw = rawValue.trim();
  // On Windows the drive separator isn't a keyword separator. ConfigFromOptions
  // uses the last colon after checking the file, which this spelling mirrors.
  const windowsDrive = /^[a-zA-Z]:[\\/]/.test(raw);
  const separator = windowsDrive ? raw.lastIndexOf(':') : raw.indexOf(':');
  const named = separator >= (windowsDrive ? 2 : 0);
  return {
    raw,
    path: named ? raw.slice(0, separator) : raw,
    keyword: named ? raw.slice(separator + 1) : 'FUZZ',
    named,
  };
}

export function wordlistActivity(specs: WordlistSpec[], request: RequestInputs) {
  const fields = [request.method, request.target, request.data, ...request.headers];
  if (request.cookies?.length) fields.push(`Cookie: ${request.cookies.join('; ')}`);
  const active: WordlistSpec[] = [];
  const inactive: WordlistSpec[] = [];
  for (const spec of specs) {
    (fields.some((field) => field.includes(spec.keyword)) ? active : inactive).push(spec);
  }
  return { active, inactive };
}

/** Match ffuf's sniper template validation: every populated field needs pairs. */
export function sniperTemplatePresent(request: RequestInputs, template = '§'): boolean {
  const fields = [request.method, request.target, request.data, ...request.headers];
  if (request.cookies?.length) fields.push(`Cookie: ${request.cookies.join('; ')}`);
  let present = false;
  for (const field of fields) {
    const count = field.split(template).length - 1;
    if (count % 2 !== 0) return false;
    if (count > 0) present = true;
  }
  return present;
}

export interface InspectedWordlist {
  spec: WordlistSpec;
  inspection: WordlistInspection;
}

export interface WordlistEstimateOptions {
  mode: 'clusterbomb' | 'pitchfork' | 'sniper' | string;
  extensions: string;
  dirsearchCompat: boolean;
  ignoreComments: boolean;
}

function extensionCount(value: string): bigint {
  // ConfigFromOptions treats every comma-separated item as an extension when
  // the flag is non-empty, including duplicates and empty interior items.
  return value === '' ? 0n : BigInt(value.split(',').length);
}

function expandedCount(provider: InspectedWordlist, options: WordlistEstimateOptions): bigint {
  const entries = BigInt(Math.max(0, Math.trunc(provider.inspection.entries)));
  const extensions = extensionCount(options.extensions);
  const comments = options.ignoreComments
    ? BigInt(Math.min(Number(entries), Math.max(0, Math.trunc(provider.inspection.comment_entries ?? 0))))
    : 0n;
  if (options.dirsearchCompat && extensions > 0n) {
    const markers = BigInt(Math.min(
      Number(entries),
      Math.max(0, Math.trunc(provider.inspection.extension_markers ?? 0)),
    ));
    // ffuf expands marker rows before its comment stripping branch. A marker
    // on a full-line comment therefore still expands in DirSearch mode.
    const commentMarkers = options.ignoreComments
      ? BigInt(Math.min(
        Number(markers),
        Math.max(0, Math.trunc(provider.inspection.comment_extension_markers ?? 0)),
      ))
      : 0n;
    const ignoredNonMarkerComments = comments - commentMarkers;
    return entries - markers - ignoredNonMarkerComments + markers * extensions;
  }
  const effectiveEntries = entries - comments;
  if (provider.spec.keyword === 'FUZZ' && extensions > 0n) {
    return effectiveEntries * (1n + extensions);
  }
  return effectiveEntries;
}

export function estimateWordlistRequests(
  providers: InspectedWordlist[],
  options: WordlistEstimateOptions,
): { count: bigint; display: string } | undefined {
  if (providers.length === 0 || providers.some(({ inspection }) => !inspection.readable)) return undefined;
  const counts = providers.map((provider) => expandedCount(provider, options));
  const count = options.mode === 'clusterbomb' || options.mode === 'sniper'
    ? counts.reduce((product, value) => product * value, 1n)
    : counts.reduce((maximum, value) => value > maximum ? value : maximum, 0n);
  return { count, display: count.toLocaleString('en-US') };
}
import type { WordlistInspection } from '../types/api';
