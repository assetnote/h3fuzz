import { screen } from '@testing-library/react';
import { describe, expect, it, vi } from 'vitest';
import { render } from '../test/render';
import type { OptionMetadata } from '../types/api';
import { cloneConfig, defaultConfig } from '../utils/config';
import { OptionField } from './OptionField';

describe('OptionField', () => {
  it('renders process flags whose omitted json_path cannot be persisted', () => {
    const option: OptionMetadata = {
      name: 'V',
      section: 'general',
      type: 'boolean',
      default: false,
      usage: 'Show version information.',
      aliases: [],
      repeatable: false,
      hidden: false,
      available: false,
      unavailable_reason: 'version display is a command-line process action',
    };

    render(
      <OptionField option={option} config={cloneConfig(defaultConfig)} onChange={vi.fn()} />,
    );

    expect(screen.getByText('-V')).toBeVisible();
    expect(screen.getByText('version display is a command-line process action')).toBeVisible();
  });
});
