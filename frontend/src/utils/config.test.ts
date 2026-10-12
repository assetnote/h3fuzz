import { describe, expect, it } from 'vitest';
import {
  applyProfile,
  bundledProfiles,
  cloneConfig,
  defaultConfig,
  DEFAULT_WEB_EXPLICIT_FLAGS,
  HTTP3_ADAPTIVE_INITIAL_CONNECTIONS,
  http3Capacity,
} from './config';

describe('language profiles', () => {
  it.each([
    ['aspm', 'aspx,htm,html,js'],
    ['asp', 'aspx,asmx,html,htm,js,ashx,asp,svc'],
    ['php', 'php,html,htm,js'],
    ['jsp', 'jsp,htm,html,do,action,js'],
    ['dir', 'html'],
  ])('applies the exact %s extension set', (id, expected) => {
    const profile = bundledProfiles.find((candidate) => candidate.id === id);
    expect(profile).toBeDefined();
    const config = applyProfile(defaultConfig, profile!);
    expect(config.input.extensions).toBe(expected);
    expect(config.input.dirsearch_compat).toBe(true);
  });
});

describe('configuration snapshots', () => {
  it('uses the retained adaptive HTTP/3 capacity without forcing the protocol', () => {
    expect(defaultConfig.general.threads).toBe(1000);
    expect(defaultConfig.general.rate).toBe(0);
    expect(defaultConfig.general.scrapers).toBe('');
    expect(defaultConfig.http).toMatchObject({
      http3: false,
      http2: false,
      no_http3: false,
      no_response_optimization: false,
      http3_connections: 0,
      http3_max_connections: 64,
      http3_max_workers: 65_536,
      http3_streams: 256,
    });
    expect(HTTP3_ADAPTIVE_INITIAL_CONNECTIONS).toBe(8);
    expect(DEFAULT_WEB_EXPLICIT_FLAGS).not.toContain('t');
    expect(DEFAULT_WEB_EXPLICIT_FLAGS).not.toContain('http3');
    expect(DEFAULT_WEB_EXPLICIT_FLAGS).not.toContain('http3-high-rate');
    expect(DEFAULT_WEB_EXPLICIT_FLAGS).not.toContain('http3-high-rate-acknowledge');

    expect(http3Capacity(defaultConfig, true)).toEqual({
      adaptiveConnections: true,
      automaticWorkers: true,
      initialConnections: 8,
      maximumConnections: 64,
      streamsPerConnection: 256,
      initialAdmission: 2_048,
      maximumAdmission: 16_384,
      initialWorkers: 2_048,
      maximumWorkers: 16_384,
    });
  });

  it('shows when explicit threads constrain otherwise available HTTP/3 streams', () => {
    expect(http3Capacity(defaultConfig, false)).toMatchObject({
      adaptiveConnections: true,
      automaticWorkers: false,
      initialAdmission: 2_048,
      maximumAdmission: 16_384,
      initialWorkers: 1000,
      maximumWorkers: 1000,
    });
  });

  it('does not mutate the source when applying a profile', () => {
    const source = cloneConfig(defaultConfig);
    const originalExtensions = source.input.extensions;
    const next = applyProfile(source, bundledProfiles[2]);
    next.http.headers.push('X-Test: changed');

    expect(source.input.extensions).toBe(originalExtensions);
    expect(source.http.headers).not.toContain('X-Test: changed');
  });
});
