import { describe, expect, it } from 'vitest';
import { estimateWordlistRequests, parseWordlistSpec, sniperTemplatePresent, wordlistActivity } from './wordlists';

describe('wordlist specifications', () => {
  it('inspects the path portion while preserving ffuf keyword defaults and duplicate providers', () => {
    const specs = [
      parseWordlistSpec('/lists/first.txt:PATH'),
      parseWordlistSpec('/lists/second.txt:PATH'),
      parseWordlistSpec('/lists/default.txt'),
    ];

    expect(specs).toEqual([
      { raw: '/lists/first.txt:PATH', path: '/lists/first.txt', keyword: 'PATH', named: true },
      { raw: '/lists/second.txt:PATH', path: '/lists/second.txt', keyword: 'PATH', named: true },
      { raw: '/lists/default.txt', path: '/lists/default.txt', keyword: 'FUZZ', named: false },
    ]);

    const activity = wordlistActivity(specs, {
      method: 'GET',
      target: 'https://example.test/PATH',
      data: '',
      headers: [],
    });
    expect(activity.active).toEqual(specs.slice(0, 2));
    expect(activity.inactive).toEqual(specs.slice(2));
  });

  it('recognizes only paired sniper templates in each request field', () => {
    expect(sniperTemplatePresent({
      method: 'GET', target: 'https://example.test/§admin§', data: '', headers: [],
    })).toBe(true);
    expect(sniperTemplatePresent({
      method: 'GET', target: 'https://example.test/§admin', data: '', headers: ['X-Safe: §one§'],
    })).toBe(false);
  });
});

describe('wordlist request estimates', () => {
  it('uses ffuf pitchfork cardinality after expanding each provider', () => {
    const estimate = estimateWordlistRequests(
      [
        {
          spec: parseWordlistSpec('paths.txt:PATH'),
          inspection: { path: 'paths.txt', readable: true, entries: 5, extension_markers: 2 },
        },
        {
          spec: parseWordlistSpec('names.txt'),
          inspection: { path: 'names.txt', readable: true, entries: 3, extension_markers: 1 },
        },
      ],
      { mode: 'pitchfork', extensions: 'php,html', dirsearchCompat: false, ignoreComments: false },
    );

    // Extensions apply only to FUZZ outside DirSearch mode. Provider totals are
    // 5 and 3 * (base + two extensions) = 9, and pitchfork uses the maximum.
    expect(estimate).toEqual({ count: 9n, display: '9' });
  });

  it('uses an overflow-safe product for clusterbomb providers', () => {
    const estimate = estimateWordlistRequests(
      [
        {
          spec: parseWordlistSpec('first.txt:SAME'),
          inspection: { path: 'first.txt', readable: true, entries: 3_000_000_000 },
        },
        {
          spec: parseWordlistSpec('second.txt:SAME'),
          inspection: { path: 'second.txt', readable: true, entries: 3_000_000_000 },
        },
      ],
      { mode: 'clusterbomb', extensions: '', dirsearchCompat: false, ignoreComments: false },
    );

    // ffuf keeps providers with duplicate keywords in its cardinality even
    // though the later value wins in the request-value map.
    expect(estimate).toEqual({ count: 9_000_000_000_000_000_000n, display: '9,000,000,000,000,000,000' });
  });

  it('applies DirSearch marker expansion to each provider before single or multi-list cardinality', () => {
    const provider = {
      spec: parseWordlistSpec('final.txt'),
      inspection: { path: 'final.txt', readable: true, entries: 10, extension_markers: 3 },
    };
    const options = {
      mode: 'pitchfork', extensions: 'aspx,htm,html,js', dirsearchCompat: true, ignoreComments: false,
    };

    expect(estimateWordlistRequests([provider], options)).toEqual({ count: 19n, display: '19' });
    expect(estimateWordlistRequests([
      provider,
      {
        spec: parseWordlistSpec('short.txt:ALT'),
        inspection: { path: 'short.txt', readable: true, entries: 20, extension_markers: 20 },
      },
    ], options)).toEqual({ count: 80n, display: '80' });
  });

  it('accounts for ffuf comment filtering before extension expansion', () => {
    const provider = {
      spec: parseWordlistSpec('final.txt'),
      inspection: {
        path: 'final.txt', readable: true, entries: 10, extension_markers: 3,
        comment_entries: 2, comment_extension_markers: 1,
      },
    };

    expect(estimateWordlistRequests([provider], {
      mode: 'pitchfork', extensions: 'php', dirsearchCompat: false, ignoreComments: true,
    })).toEqual({ count: 16n, display: '16' });
    expect(estimateWordlistRequests([provider], {
      mode: 'pitchfork', extensions: 'php,html', dirsearchCompat: true, ignoreComments: true,
    })).toEqual({ count: 12n, display: '12' });
  });
});
