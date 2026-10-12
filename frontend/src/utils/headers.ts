export const baselineUserAgent =
  'Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/111.0.0.0 Safari/537.36';

export const akamaiHeaders = [
  'Sec-Ch-Ua: "Chromium";v="136", "Brave";v="136", "Not.A/Brand";v="99"',
  'Sec-Ch-Ua-Mobile: ?0',
  'Sec-Ch-Ua-Platform: "macOS"',
  'Upgrade-Insecure-Requests: 1',
  'User-Agent: Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/136.0.0.0 Safari/537.36',
  'Accept: application/json',
  'Sec-Gpc: 1',
  'Accept-Language: en-GB,en;q=0.7',
  'Sec-Fetch-Site: none',
  'Sec-Fetch-Mode: navigate',
  'Sec-Fetch-User: ?1',
  'Sec-Fetch-Dest: document',
  'Accept-Encoding: gzip, deflate, br',
  'Priority: u=0, i',
] as const;

function headerName(header: string): string {
  return header.split(':', 1)[0].trim().toLowerCase();
}

const akamaiByName = new Map(akamaiHeaders.map((header) => [headerName(header), header]));

export function hasAkamaiHeaders(headers: string[]): boolean {
  const effective = new Map(headers.map((header) => [headerName(header), header.trim()]));
  return akamaiHeaders.every((header) => effective.get(headerName(header)) === header);
}

export function applyAkamaiHeaders(headers: string[]): string[] {
  const output: string[] = [];
  const seen = new Set<string>();
  headers.forEach((header) => {
    const name = headerName(header);
    if (!name || seen.has(name)) return;
    seen.add(name);
    output.push(akamaiByName.get(name) ?? header.trim());
  });
  akamaiHeaders.forEach((header) => {
    const name = headerName(header);
    if (!seen.has(name)) output.push(header);
  });
  return output;
}

export function removeAkamaiHeaders(headers: string[]): string[] {
  const output = headers.filter((header) => {
    const expected = akamaiByName.get(headerName(header));
    return !expected || header.trim() !== expected;
  });
  const hasUserAgent = output.some((header) => headerName(header) === 'user-agent');
  if (!hasUserAgent) output.push(`User-Agent: ${baselineUserAgent}`);
  return output;
}

