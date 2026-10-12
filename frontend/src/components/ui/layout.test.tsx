import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { screen } from '@testing-library/react';
import { afterAll, beforeAll, describe, expect, it, vi } from 'vitest';
import { render } from '../../test/render';
import { RESPONSIVE_BREAKPOINTS } from './compat';
import { Box, Button, Grid, Group, Progress, ScrollArea, SimpleGrid, Stack, Text, UnstyledButton } from './layout';
import { Modal } from './overlays';

let railStyleElement: HTMLStyleElement;

beforeAll(() => {
  const appStyles = readFileSync(resolve(process.cwd(), 'src/styles.css'), 'utf8');
  const railSizingRule = appStyles.match(/\.railSessions \.rt-ScrollAreaViewport > \* \{[^}]+\}/)?.[0] ?? '';
  railStyleElement = document.createElement('style');
  railStyleElement.textContent = railSizingRule;
  document.head.append(railStyleElement);
});

afterAll(() => railStyleElement.remove());

describe('Radix layout compatibility', () => {
  it('preserves CSS flex alignment values used by existing screens', () => {
    const { container } = render(
      <>
        <Group align="flex-start" justify="space-between" wrap="nowrap">
          <span>group</span>
        </Group>
        <Stack align="flex-end" justify="space-between">
          <span>stack</span>
        </Stack>
      </>,
    );

    const [group, stack] = Array.from(container.querySelectorAll('.rt-Flex'));
    expect(group).toHaveStyle({
      alignItems: 'flex-start',
      justifyContent: 'space-between',
      flexWrap: 'nowrap',
    });
    expect(stack).toHaveStyle({
      alignItems: 'flex-end',
      justifyContent: 'space-between',
    });
  });

  it('keeps every responsive spacing and dimension value on Box', () => {
    render(
      <Box
        p={{ base: 'xs', xs: 'sm', sm: 'md', md: 'lg', lg: 'xl', xl: 32 }}
        w={{ base: '100%', xs: 320, sm: 480, md: 640, lg: 800, xl: 960 }}
      >
        responsive box
      </Box>,
    );

    const box = screen.getByText('responsive box');
    expect(box).toHaveClass('uiResponsive');
    expect(box.style.padding).toBe('var(--ui-p-active)');
    expect(box.style.width).toBe('var(--ui-w-active)');
    expect(box.style.getPropertyValue('--ui-p-base')).toBe('var(--space-1, 4px)');
    expect(box.style.getPropertyValue('--ui-p-xs')).toBe('var(--space-2, 8px)');
    expect(box.style.getPropertyValue('--ui-p-sm')).toBe('var(--space-3, 12px)');
    expect(box.style.getPropertyValue('--ui-p-md')).toBe('var(--space-4, 16px)');
    expect(box.style.getPropertyValue('--ui-p-lg')).toBe('var(--space-5, 24px)');
    expect(box.style.getPropertyValue('--ui-p-xl')).toBe('32px');
    expect(box.style.getPropertyValue('--ui-w-lg')).toBe('800px');
    expect(box.style.getPropertyValue('--ui-w-xl')).toBe('960px');
  });

  it('keeps responsive gaps, grid columns, and grid spans at all breakpoints', () => {
    const { container } = render(
      <>
        <Group gap={{ base: 'xs', md: 'md', xl: 'xl' }}><span>group gap</span></Group>
        <Stack gap={{ xs: 6, lg: 18 }}><span>stack gap</span></Stack>
        <SimpleGrid
          cols={{ base: 1, xs: 2, sm: 3, md: 4, lg: 5, xl: 6 }}
          spacing={{ base: 'xs', xs: 'sm', sm: 'md', md: 'lg', lg: 'xl', xl: 32 }}
        >
          <span>simple grid</span>
        </SimpleGrid>
        <Grid>
          <Grid.Col span={{ base: 12, xs: 11, sm: 10, md: 9, lg: 8, xl: 7 }}>
            grid column
          </Grid.Col>
        </Grid>
      </>,
    );

    const group = screen.getByText('group gap').parentElement as HTMLElement;
    const stack = screen.getByText('stack gap').parentElement as HTMLElement;
    const simpleGrid = screen.getByText('simple grid').parentElement as HTMLElement;
    const gridColumn = screen.getByText('grid column');

    expect(group.style.gap).toBe('var(--ui-gap-active)');
    expect(group.style.getPropertyValue('--ui-gap-xl')).toBe('var(--space-5, 24px)');
    expect(stack.style.getPropertyValue('--ui-gap-xs')).toBe('6px');
    expect(stack.style.getPropertyValue('--ui-gap-lg')).toBe('18px');
    expect(simpleGrid.style.getPropertyValue('--ui-cols-xs')).toBe('2');
    expect(simpleGrid.style.getPropertyValue('--ui-cols-lg')).toBe('5');
    expect(simpleGrid.style.getPropertyValue('--ui-cols-xl')).toBe('6');
    expect(simpleGrid.style.getPropertyValue('--ui-grid-gap-xl')).toBe('32px');
    expect(gridColumn.style.getPropertyValue('--ui-span-xs')).toBe('11');
    expect(gridColumn.style.getPropertyValue('--ui-span-lg')).toBe('8');
    expect(gridColumn.style.getPropertyValue('--ui-span-xl')).toBe('7');
    expect(container.querySelector('.uiGrid')).toBeInTheDocument();
  });

  it('defines media breakpoints for every responsive key', () => {
    expect(RESPONSIVE_BREAKPOINTS).toEqual({
      xs: '36em',
      sm: '48em',
      md: '62em',
      lg: '75em',
      xl: '88em',
    });
  });

  it('preserves scalar styles and does not erase explicit component width', () => {
    render(
      <>
        <Box p="md" w={320}>scalar box</Box>
        <Button fullWidth>full-width button</Button>
      </>,
    );

    expect(screen.getByText('scalar box')).toHaveStyle({
      padding: 'var(--space-3, 12px)',
      width: '320px',
    });
    expect(screen.getByRole('button', { name: 'full-width button' })).toHaveStyle({ width: '100%' });
  });

  it('renders text as a block by default', () => {
    render(<Text>Console label</Text>);

    expect(screen.getByText('Console label').tagName).toBe('DIV');
  });

  it('keeps unstyled button resets overridable by component classes', () => {
    render(<UnstyledButton className="sessionTab">Session target</UnstyledButton>);

    const button = screen.getByRole('button', { name: 'Session target' });
    expect(button).toHaveClass('uiUnstyledButton', 'sessionTab');
    expect(button.style.padding).toBe('');
    expect(button.style.border).toBe('');
    expect(button.style.background).toBe('');
  });

  it('keeps the Radix viewport as the vertical scroll owner while sizing rail content', () => {
    const { container } = render(
      <ScrollArea className="railSessions">
        <div>
          mobilesolutionsuzuprd.t-mobile.example
          <Progress value={100} />
        </div>
      </ScrollArea>,
    );

    const viewport = container.querySelector<HTMLElement>('.railSessions .rt-ScrollAreaViewport');
    const viewportContent = viewport?.firstElementChild as HTMLElement | null;
    expect(viewport).not.toBeNull();
    expect(viewport?.style.overflowY).toBe('scroll');
    expect(viewportContent).not.toBeNull();
    expect(getComputedStyle(viewportContent as Element).width).toBe('100%');
    // Radix measures this wrapper's intrinsic height to determine whether its
    // viewport can scroll. Clipping the wrapper collapses that scroll extent.
    expect(getComputedStyle(viewportContent as Element).overflow).not.toBe('hidden');
    expect(getComputedStyle(viewportContent as Element).flexShrink).toBe('0');
    expect(screen.getByRole('progressbar')).toBeInTheDocument();
  });

  it('keeps an accessibility-only modal title out of the visual layout', () => {
    render(
      <Modal opened onClose={vi.fn()} withCloseButton={false} aria-label="Create a new h3fuzz session">
        <div>Modal content</div>
      </Modal>,
    );

    expect(screen.getByText('Create a new h3fuzz session')).toHaveStyle({ position: 'absolute' });
  });
});
