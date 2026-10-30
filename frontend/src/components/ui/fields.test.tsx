import { screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { describe, expect, it, vi } from 'vitest';
import { render } from '../../test/render';
import { Checkbox, MultiSelect, Select, TagsInput } from './fields';

describe('Radix field compatibility', () => {
  it('forwards compact sizing to a select trigger', () => {
    render(
      <Select
        aria-label="Input mode"
        data={[{ value: 'pitchfork', label: 'Single / pitchfork' }]}
        value="pitchfork"
        size="xs"
        onChange={vi.fn()}
      />,
    );

    expect(screen.getByRole('combobox', { name: 'Input mode' })).toHaveClass('rt-r-size-1');
  });

  it('pads the searchable multiselect menu with a valid spacing token', async () => {
    const user = userEvent.setup();
    render(
      <MultiSelect
        aria-label="Filter by status"
        data={['200', '404']}
        value={[]}
        onChange={vi.fn()}
        searchable
      />,
    );

    await user.click(screen.getByRole('button', { name: 'Filter by status' }));
    const search = await screen.findByRole('textbox', { name: 'Search options' });
    expect(search.closest('.rt-Box')).toHaveClass('rt-r-p-2');
  });

  it('contains long tag labels and ignores blank display values', () => {
    const longValue = `User-Agent: ${'browser/'.repeat(40)}`;
    const { container } = render(
      <TagsInput
        label="Headers"
        value={[longValue, '']}
        onChange={vi.fn()}
      />,
    );

    const label = screen.getByText(longValue);
    expect(label).toHaveClass('uiTagLabel');
    expect(label).toHaveAttribute('title', longValue);
    expect(container.querySelectorAll('.uiTag')).toHaveLength(1);
  });

  it('preserves an indeterminate checkbox and its explicit accessible name', async () => {
    const user = userEvent.setup();
    const onChange = vi.fn();
    render(
      <Checkbox
        aria-label="Select all matching sessions"
        label="Select all"
        checked="indeterminate"
        onChange={onChange}
      />,
    );

    const checkbox = screen.getByRole('checkbox', { name: 'Select all matching sessions' });
    expect(checkbox).toHaveAttribute('aria-checked', 'mixed');
    await user.click(checkbox);
    expect(onChange).toHaveBeenCalledOnce();
    expect(onChange.mock.calls[0][0].currentTarget.checked).toBe(true);
  });
});
