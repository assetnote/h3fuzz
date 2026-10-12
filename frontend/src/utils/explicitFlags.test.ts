import { describe, expect, it } from 'vitest';
import { addExplicitFlags, canonicalPatchFlag } from './explicitFlags';

describe('explicit flag tracking', () => {
  it('preserves advanced thread and matcher semantics in create requests', () => {
    let flags = addExplicitFlags(['D', 'e', 'mc', 'fc', 'H'], 't');
    flags = addExplicitFlags(flags, 'ms');
    expect([...flags]).toEqual(['D', 'e', 'mc', 'fc', 'H', 't', 'ms']);
  });

  it('maps curated matcher and filter controls to canonical ffuf names', () => {
    expect(canonicalPatchFlag('matchers', 'size')).toBe('ms');
    expect(canonicalPatchFlag('filters', 'status')).toBe('fc');
    expect(canonicalPatchFlag('general', 'threads')).toBe('t');
    expect(canonicalPatchFlag('general', 'delay')).toBe('p');
    expect(canonicalPatchFlag('http', 'http3_streams')).toBe('http3-streams');
    expect(canonicalPatchFlag('http', 'http3_max_workers')).toBe('http3-max-workers');
  });
});
