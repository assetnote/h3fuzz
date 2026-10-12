import { describe, expect, it } from 'vitest';
import {
  akamaiHeaders,
  applyAkamaiHeaders,
  baselineUserAgent,
  hasAkamaiHeaders,
  removeAkamaiHeaders,
} from './headers';

describe('Akamai request headers', () => {
  it('applies the exact header set and replaces duplicates case-insensitively', () => {
    const result = applyAkamaiHeaders([
      `User-Agent: ${baselineUserAgent}`,
      'accept: text/html',
      'Accept: duplicate',
      'X-Custom: retained',
    ]);

    expect(hasAkamaiHeaders(result)).toBe(true);
    expect(result.filter((header) => header.toLowerCase().startsWith('accept:'))).toHaveLength(1);
    expect(result).toContain('X-Custom: retained');
    expect(akamaiHeaders.every((header) => result.includes(header))).toBe(true);
  });

  it('removes only unchanged Akamai headers and restores the baseline user agent', () => {
    const result = removeAkamaiHeaders([...akamaiHeaders, 'X-Custom: retained']);
    expect(hasAkamaiHeaders(result)).toBe(false);
    expect(result).toContain(`User-Agent: ${baselineUserAgent}`);
    expect(result).toContain('X-Custom: retained');
  });
});
