import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { cleanup, fireEvent, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { useState } from 'react';
import { afterAll, afterEach, beforeAll, describe, expect, it, vi } from 'vitest';
import { render } from '../test/render';
import type { Session } from '../types/api';
import { SessionRail } from './SessionRail';

const longTarget =
  'https://mobilesolutionsuzuprd.t-mobile.example.invalid/a/very/long/discovery/path/FUZZ';

const session: Session = {
  id: 'long-target',
  name: 'mobilesolutionsuzuprd.t-mobile.example.invalid',
  target: longTarget,
  status: 'running',
  profile: 'dir',
  wordlist: 'final.txt',
  options: {} as Session['options'],
  explicit_flags: [],
  progress: {
    req_count: 12_345,
    req_total: 23_358,
    req_sec: 18_900,
    queue_pos: 1,
    queue_total: 1,
    error_count: 0,
  },
  result_count: 42,
  created_at: '2026-10-10T00:00:00Z',
  updated_at: '2026-10-10T00:01:00Z',
};

let railStyleElement: HTMLStyleElement;

beforeAll(() => {
  const appStyles = readFileSync(resolve(process.cwd(), 'src/styles.css'), 'utf8');
  const selectors = [
    '.sessionRail',
    '.railSessions',
    '.railSessions .rt-ScrollAreaViewport',
    '.railSessions .rt-ScrollAreaViewport > *',
    '.railSessionList',
    '.railViewControls',
    '.sessionTabActions',
    '.sessionTabWrapper:hover .sessionTabActions,\n.sessionTabWrapper:focus-within .sessionTabActions,\n.sessionTabChecked .sessionTabActions',
    '.sessionTabWrapper.sessionTabActionsSuppressed .sessionTabActions',
    '.sessionTab',
    '.sessionTabLayout',
    '.sessionTabContent',
    '.sessionTabHeading',
    '.sessionTabFooter',
    '.sessionTabMeta',
    '.sessionTabProgress',
    '.sessionTabTitle,\n.sessionTarget',
  ];
  const ruleFor = (selector: string) => {
    const start = appStyles.indexOf(`${selector} {`);
    if (start < 0) return '';
    const end = appStyles.indexOf('}', start);
    return appStyles.slice(start, end + 1);
  };
  const rules = selectors
    .map(ruleFor)
    .join('\n');
  railStyleElement = document.createElement('style');
  railStyleElement.textContent = rules;
  document.head.appendChild(railStyleElement);
});

afterEach(() => {
  cleanup();
  window.localStorage.clear();
});

afterAll(() => railStyleElement.remove());

function renderRail(width: number) {
  window.localStorage.setItem('h3fuzz-session-rail-width', JSON.stringify(width));
  return render(
    <SessionRail
      sessions={[session]}
      activeID={session.id}
      page="sessions"
      connected
      onSelect={vi.fn()}
      onNew={vi.fn()}
      onSettings={vi.fn()}
    />,
  );
}

describe('SessionRail responsive sizing', () => {
  it.each([220, 284, 440])('clamps long session content inside a %ipx rail', (width) => {
    const { container } = renderRail(width);
    const rail = container.querySelector<HTMLElement>('.sessionRail');
    const viewport = container.querySelector<HTMLElement>('.railSessions .rt-ScrollAreaViewport');
    const viewportContent = viewport?.firstElementChild as HTMLElement | null;
    const list = container.querySelector<HTMLElement>('.railSessionList');
    const tab = container.querySelector<HTMLElement>('.sessionTab');
    const layout = container.querySelector<HTMLElement>('.sessionTabLayout');
    const content = container.querySelector<HTMLElement>('.sessionTabContent');
    const progress = container.querySelector<HTMLElement>('.sessionTabProgress');
    const title = container.querySelector<HTMLElement>('.sessionTabTitle');

    expect(rail).toHaveStyle({ width: `${width}px` });
    const viewportStyle = getComputedStyle(viewport!);
    expect(viewportStyle.minWidth).toBe('0');
    expect(viewportStyle.maxWidth).toBe('100%');
    expect(viewportStyle.overflowX).toBe('hidden');
    expect(viewportStyle.overflowY).toBe('scroll');

    const scrollAreaStyle = getComputedStyle(container.querySelector<HTMLElement>('.railSessions')!);
    expect(scrollAreaStyle.height).toBe('0px');
    expect(scrollAreaStyle.minHeight).toBe('0');

    const viewportContentStyle = getComputedStyle(viewportContent!);
    expect(viewportContentStyle.width).toBe('100%');
    expect(viewportContentStyle.maxWidth).toBe('100%');
    // The generated Radix content wrapper must retain its intrinsic height so
    // the viewport, rather than the wrapper, owns vertical scrolling.
    expect(viewportContentStyle.overflow).not.toBe('hidden');
    expect(viewportContentStyle.flexShrink).toBe('0');

    expect(getComputedStyle(list!).paddingInlineEnd).toContain('--scrollarea-scrollbar-size');

    for (const element of [list, tab, layout, content, progress]) {
      const style = getComputedStyle(element!);
      expect(style.width).toBe('100%');
      expect(style.minWidth).toBe('0');
      expect(style.maxWidth).toBe('100%');
    }
    expect(getComputedStyle(content!).overflow).toBe('hidden');

    const titleStyle = getComputedStyle(title!);
    expect(titleStyle.overflow).toBe('hidden');
    expect(titleStyle.textOverflow).toBe('ellipsis');
    expect(titleStyle.whiteSpace).toBe('nowrap');
  });

  it('keeps keyboard resizing inside the supported rail bounds', () => {
    const { container } = renderRail(284);
    const rail = container.querySelector<HTMLElement>('.sessionRail');
    const resizer = screen.getByRole('separator', { name: 'Resize session panel' });

    fireEvent.keyDown(resizer, { key: 'ArrowLeft' });
    expect(rail).toHaveStyle({ width: '268px' });

    fireEvent.keyDown(resizer, { key: 'Home' });
    fireEvent.keyDown(resizer, { key: 'ArrowLeft' });
    expect(rail).toHaveStyle({ width: '220px' });
    expect(resizer).toHaveAttribute('aria-valuenow', '220');

    fireEvent.keyDown(resizer, { key: 'End' });
    fireEvent.keyDown(resizer, { key: 'ArrowRight' });
    expect(rail).toHaveStyle({ width: '440px' });
    expect(resizer).toHaveAttribute('aria-valuenow', '440');
  });

  it('shows completed rather than merely scheduled progress while requests drain', () => {
    const draining = {
      ...session,
      progress: {
        ...session.progress,
        req_count: 2_001,
        request_count: 2_001,
        completed_count: 0,
        response_count: 0,
        req_total: 3_000,
      },
    };
    render(
      <SessionRail
        sessions={[draining]}
        activeID={draining.id}
        page="sessions"
        connected
        onSelect={vi.fn()}
        onNew={vi.fn()}
        onSettings={vi.fn()}
      />,
    );

    expect(screen.getByText('0%')).toBeVisible();
    expect(screen.queryByText('67%')).not.toBeInTheDocument();
  });

  it('falls back safely when a persisted width is not numeric', () => {
    window.localStorage.setItem('h3fuzz-session-rail-width', JSON.stringify('wide'));
    const { container } = render(
      <SessionRail
        sessions={[session]}
        activeID={session.id}
        page="sessions"
        connected
        onSelect={vi.fn()}
        onNew={vi.fn()}
        onSettings={vi.fn()}
      />,
    );

    expect(container.querySelector('.sessionRail')).toHaveStyle({ width: '284px' });
  });

  it('searches, filters, and exposes bulk and organization actions', async () => {
    const user = userEvent.setup();
    const completed = { ...session, id: 'completed', name: 'Archive target', target: 'https://archive.example/FUZZ', status: 'completed' as const };
    const onOrganization = vi.fn();
    const onBulk = vi.fn();
    render(<SessionRail sessions={[session, completed]} activeID={session.id} page="sessions" connected onSelect={vi.fn()} onNew={vi.fn()} onSettings={vi.fn()} onOrganization={onOrganization} onBulk={onBulk} />);

    await user.type(screen.getByLabelText('Search sessions'), 'archive');
    expect(screen.getByText('Archive target')).toBeVisible();
    expect(screen.queryByText(session.name)).not.toBeInTheDocument();
    await user.clear(screen.getByLabelText('Search sessions'));
    await user.click(screen.getAllByText('Done')[0]);
    expect(screen.getByText('Archive target')).toBeVisible();
    expect(screen.queryByText(session.name)).not.toBeInTheDocument();

    screen.getByText('Archive target').closest<HTMLButtonElement>('.sessionTab')?.focus();
    await user.click(screen.getByLabelText('Pin Archive target'));
    expect(onOrganization).toHaveBeenCalledWith('completed', { pinned: true });

    await user.click(screen.getByRole('checkbox', { name: 'Select Archive target' }));
    await user.click(screen.getByLabelText('Archive selected sessions'));
    expect(onBulk).toHaveBeenCalledWith(['completed'], 'archive');
  });

  it('hides archived sessions until requested and removes the obsolete compact rail state', async () => {
    const user = userEvent.setup();
    const archived = { ...session, id: 'archived', name: 'Archived target', archived: true, status: 'completed' as const };
    window.localStorage.setItem('h3fuzz-session-rail-compact', 'true');
    render(<SessionRail sessions={[session, archived]} activeID={session.id} page="sessions" connected onSelect={vi.fn()} onNew={vi.fn()} onSettings={vi.fn()} />);

    expect(screen.queryByText('Archived target')).not.toBeInTheDocument();
    expect(screen.getByLabelText('Search sessions')).toBeVisible();
    expect(screen.queryByLabelText('Use compact rail')).not.toBeInTheDocument();
    expect(screen.queryByLabelText('Expand session rail')).not.toBeInTheDocument();
    expect(window.localStorage.getItem('h3fuzz-session-rail-compact')).toBeNull();
    await user.click(screen.getByRole('checkbox', { name: 'Archived' }));
    expect(screen.getByText('Archived target')).toBeVisible();
  });

  it('uses one clearly labelled control for grouped and flat organization', async () => {
    const user = userEvent.setup();
    renderRail(284);

    const grouping = screen.getByRole('button', { name: 'Show sessions without host groups' });
    expect(grouping).toHaveAttribute('aria-pressed', 'true');
    await user.click(grouping);
    expect(screen.getByRole('button', { name: 'Group sessions by host' })).toHaveAttribute('aria-pressed', 'false');
  });

  it('keeps a full spacing step between the status filter and organization controls', () => {
    const { container } = renderRail(284);

    expect(getComputedStyle(container.querySelector<HTMLElement>('.railViewControls')!).marginTop).toBe('var(--space-3, 12px)');
  });

  it('keeps pinning sorted first without leaving a detached focused action behind', () => {
    const newer = {
      ...session,
      id: 'newer',
      name: 'Newer target',
      target: 'https://example.test/newer/FUZZ',
      status: 'completed' as const,
      updated_at: '2026-10-10T00:02:00Z',
    };
    const older = {
      ...session,
      id: 'older',
      name: 'Older target',
      target: 'https://example.test/older/FUZZ',
      status: 'completed' as const,
      updated_at: '2026-10-10T00:01:00Z',
    };

    function OrganizationHarness() {
      const [sessions, setSessions] = useState<Session[]>([older, newer]);
      return <SessionRail
        sessions={sessions}
        activeID={older.id}
        page="sessions"
        connected
        onSelect={vi.fn()}
        onNew={vi.fn()}
        onSettings={vi.fn()}
        onOrganization={(id, patch) => setSessions((current) => current.map((item) => item.id === id ? { ...item, ...patch } : item))}
      />;
    }

    const { container } = render(<OrganizationHarness />);
    const titles = () => [...container.querySelectorAll('.sessionTabTitle')].map((node) => node.textContent);
    const actions = () => container.querySelector<HTMLElement>('.sessionTabActive .sessionTabActions')!;

    expect(titles()).toEqual(['Newer target', 'Older target']);
    expect(getComputedStyle(actions()).opacity).toBe('0');

    const pinAction = screen.getByLabelText('Pin Older target');
    const pinBlur = vi.spyOn(pinAction, 'blur');
    fireEvent.click(pinAction, { detail: 1 });
    expect(titles()).toEqual(['Older target', 'Newer target']);
    expect(pinBlur).toHaveBeenCalledOnce();
    expect(actions().parentElement).toHaveClass('sessionTabActionsSuppressed');
    expect(getComputedStyle(actions()).opacity).toBe('0');

    fireEvent.pointerLeave(actions().parentElement!);
    const unpinAction = screen.getByLabelText('Unpin Older target');
    const unpinBlur = vi.spyOn(unpinAction, 'blur');
    fireEvent.click(unpinAction, { detail: 1 });
    expect(titles()).toEqual(['Newer target', 'Older target']);
    expect(unpinBlur).toHaveBeenCalledOnce();
  });

  it('keeps stopped-session metadata readable at the minimum rail width', () => {
    const stopped = {
      ...session,
      status: 'stopped' as const,
      runtime_transport: '' as const,
      result_count: 26_740,
      progress: { ...session.progress, error_count: 2_001 },
    };
    window.localStorage.setItem('h3fuzz-session-rail-width', JSON.stringify(220));
    const { container } = render(
      <SessionRail sessions={[stopped]} activeID={stopped.id} page="sessions" connected onSelect={vi.fn()} onNew={vi.fn()} onSettings={vi.fn()} />,
    );

    expect(screen.getByText('N/A')).toBeVisible();
    expect(screen.queryByText('Transport unavailable')).not.toBeInTheDocument();
    expect(screen.getByText('2001 errors')).toBeVisible();
    expect(screen.getByText('26740 found')).toBeVisible();
    expect(getComputedStyle(container.querySelector<HTMLElement>('.sessionTab')!).paddingRight).toBe('10px');
    const footerStyle = getComputedStyle(container.querySelector<HTMLElement>('.sessionTabFooter')!);
    expect(footerStyle.display).toBe('grid');
    expect(footerStyle.gridTemplateColumns).toBe('minmax(0, 1fr)');
    expect(footerStyle.overflow).toBe('hidden');
    expect(container.querySelector('.sessionTabMeta')).toContainElement(screen.getByText('2001 errors'));
    expect(container.querySelector('.sessionTabStat')).toHaveTextContent('26740 found');
  });

  it('confirms destructive bulk deletion and exposes bulk stop separately', async () => {
    const user = userEvent.setup();
    const onBulk = vi.fn();
    render(<SessionRail sessions={[session]} activeID={session.id} page="sessions" connected onSelect={vi.fn()} onNew={vi.fn()} onSettings={vi.fn()} onBulk={onBulk} />);

    await user.click(screen.getByRole('checkbox', { name: `Select ${session.name}` }));
    await user.click(screen.getByLabelText('Stop selected sessions'));
    expect(onBulk).toHaveBeenCalledWith([session.id], 'stop');

    await user.click(screen.getByLabelText('Delete selected sessions'));
    expect(screen.getByRole('heading', { name: 'Delete selected sessions?' })).toBeVisible();
    expect(onBulk).not.toHaveBeenCalledWith([session.id], 'delete');
    await user.click(screen.getByRole('button', { name: 'Delete sessions' }));
    expect(onBulk).toHaveBeenCalledWith([session.id], 'delete');
  });
});
