import {
  ActionIcon,
  Badge,
  Box,
  Button,
  Checkbox,
  Divider,
  Group,
  Menu,
  Modal,
  Progress,
  ScrollArea,
  SegmentedControl,
  Stack,
  Text,
  TextInput,
  Tooltip,
  UnstyledButton,
} from './ui';
import { useLocalStorage } from './ui/hooks';
import {
  IconAdjustments,
  IconBrandSpeedtest,
  IconDatabaseExport,
  IconDots,
  IconPin,
  IconPlayerStop,
  IconPlus,
  IconRows,
  IconSearch,
  IconSettings,
  IconStack,
  IconTrash,
} from './ui/icons';
import { useCallback, useEffect, useMemo, useState, type PointerEvent as ReactPointerEvent } from 'react';
import type { Session, SessionOrganization } from '../types/api';
import { formatRate, progressPercent, targetLabel } from '../utils/format';
import { effectiveTransport } from '../utils/transport';
import { ConnectionIndicator, StatusIndicator } from './StatusIndicator';

type RailFilter = 'all' | 'active' | 'completed';

interface SessionRailProps {
  sessions: Session[];
  activeID?: string;
  page: 'sessions' | 'settings';
  connected: boolean;
  mobile?: boolean;
  organizationPendingIDs?: ReadonlySet<string>;
  bulkPending?: boolean;
  onSelect: (id: string) => void;
  onNew: () => void;
  onSettings: () => void;
  onOrganization?: (id: string, patch: Partial<Pick<SessionOrganization, 'pinned' | 'archived' | 'group_name'>>) => void;
  onBulk?: (ids: string[], action: 'stop' | 'delete' | 'archive') => void;
}

function sessionHost(session: Session): string {
  try { return new URL(session.target).host || 'Other'; } catch { return targetLabel(session.target) || 'Other'; }
}

function isActive(session: Session): boolean {
  return ['starting', 'running', 'paused', 'stopping'].includes(session.status);
}

function isCompleted(session: Session): boolean {
  return ['completed', 'stopped', 'failed'].includes(session.status);
}

function protocolLabel(session: Session): string {
  const transport = effectiveTransport(session).label;
  if (transport === 'HTTP/3') return 'H3';
  if (transport === 'HTTP/2') return 'H2';
  if (transport === 'HTTP/1.1') return 'H1.1';
  if (transport === 'Probing') return 'Probe';
  if (transport === 'Not probed') return 'Pending';
  return 'N/A';
}

export function SessionRail({
  sessions, activeID, page, connected, mobile = false, organizationPendingIDs, bulkPending,
  onSelect, onNew, onSettings, onOrganization, onBulk,
}: SessionRailProps) {
  const [width, setWidth] = useLocalStorage({ key: 'h3fuzz-session-rail-width', defaultValue: 284, getInitialValueInEffect: false });
  const [query, setQuery] = useState('');
  const [filter, setFilter] = useState<RailFilter>('all');
  const [showArchived, setShowArchived] = useState(false);
  const [groupByHost, setGroupByHost] = useState(true);
  const [checkedIDs, setCheckedIDs] = useState<Set<string>>(new Set());
  const [confirmBulkDelete, setConfirmBulkDelete] = useState(false);
  const storedWidth = typeof width === 'number' && Number.isFinite(width) ? width : 284;
  const railWidth = Math.min(440, Math.max(220, storedWidth));

  useEffect(() => {
    // Compact mode removed the context needed to identify a session. Remove
    // the legacy preference as part of the migration so it cannot return when
    // users restart an older build and then come back to this one.
    window.localStorage.removeItem('h3fuzz-session-rail-compact');
  }, []);

  useEffect(() => {
    const valid = new Set(sessions.map(({ id }) => id));
    setCheckedIDs((current) => {
      const retained = [...current].filter((id) => valid.has(id));
      return retained.length === current.size ? current : new Set(retained);
    });
  }, [sessions]);

  const startResize = useCallback((event: ReactPointerEvent<HTMLDivElement>) => {
    if (mobile) return;
    event.currentTarget.setPointerCapture(event.pointerId);
    const initialX = event.clientX;
    const initialWidth = railWidth;
    const target = event.currentTarget;
    const onMove = (moveEvent: PointerEvent) => setWidth(Math.min(440, Math.max(220, initialWidth + moveEvent.clientX - initialX)));
    const onUp = () => {
      target.removeEventListener('pointermove', onMove);
      target.removeEventListener('pointerup', onUp);
      target.removeEventListener('pointercancel', onUp);
    };
    target.addEventListener('pointermove', onMove);
    target.addEventListener('pointerup', onUp);
    target.addEventListener('pointercancel', onUp);
  }, [mobile, railWidth, setWidth]);

  const visible = useMemo(() => {
    const needle = query.trim().toLowerCase();
    return [...sessions]
      .filter((session) => showArchived || !session.archived)
      .filter((session) => filter === 'all' || (filter === 'active' ? isActive(session) : isCompleted(session)))
      .filter((session) => !needle || `${session.name} ${session.target} ${sessionHost(session)}`.toLowerCase().includes(needle))
      .sort((a, b) => Number(Boolean(b.pinned)) - Number(Boolean(a.pinned)) || Number(isActive(b)) - Number(isActive(a)) || new Date(b.updated_at).getTime() - new Date(a.updated_at).getTime());
  }, [filter, query, sessions, showArchived]);

  const groups = useMemo(() => {
    if (!groupByHost) return [['Sessions', visible] as const];
    const byHost = new Map<string, Session[]>();
    visible.forEach((session) => {
      const host = sessionHost(session);
      const group = byHost.get(host);
      if (group) group.push(session);
      else byHost.set(host, [session]);
    });
    return [...byHost.entries()];
  }, [groupByHost, visible]);

  const toggleChecked = (id: string, checked: boolean) => setCheckedIDs((current) => {
    const next = new Set(current);
    if (checked) next.add(id);
    else next.delete(id);
    return next;
  });
  const runBulk = (action: 'stop' | 'delete' | 'archive') => {
    if (!checkedIDs.size || !onBulk) return;
    if (action === 'delete') {
      setConfirmBulkDelete(true);
      return;
    }
    onBulk([...checkedIDs], action);
    if (action !== 'stop') setCheckedIDs(new Set());
  };

  const deleteSelected = () => {
    if (!checkedIDs.size || !onBulk) return;
    onBulk([...checkedIDs], 'delete');
    setCheckedIDs(new Set());
    setConfirmBulkDelete(false);
  };

  return (
    <Box component="aside" className={`sessionRail${mobile ? ' sessionRailMobile' : ''}`} style={{ width: mobile ? '100%' : railWidth }} aria-label="Scan sessions">
      <Box className="railHeader">
        <Group justify="space-between" wrap="nowrap">
          <Group gap={10} wrap="nowrap">
            <Box className="wordmarkIcon"><IconBrandSpeedtest size={19} /></Box>
            <Box className="railTitle"><Text fw={700} lh={1.1}>h3fuzz</Text><Text c="dimmed" fz={10} tt="uppercase" lts="0.12em">Console</Text></Box>
          </Group>
          <Tooltip label="New session"><ActionIcon variant="light" size="lg" onClick={onNew} aria-label="New session"><IconPlus size={18} /></ActionIcon></Tooltip>
        </Group>
        <>
          <Group justify="space-between" mt="sm"><Text fz="xs" c="dimmed">{sessions.length} {sessions.length === 1 ? 'session' : 'sessions'}</Text><ConnectionIndicator connected={connected} /></Group>
          <TextInput className="railSearch" aria-label="Search sessions" placeholder="Search sessions" value={query} onChange={(event) => setQuery(event.currentTarget.value)} leftSection={<IconSearch size={14} />} size="sm" mt="sm" />
          <SegmentedControl className="railFilters" aria-label="Session status filter" fullWidth value={filter} onChange={(value) => setFilter(value as RailFilter)} data={[{ value: 'all', label: 'All' }, { value: 'active', label: 'Active' }, { value: 'completed', label: 'Done' }]} />
          <Group className="railViewControls" justify="space-between" mt="md" gap="xs">
            <Checkbox label="Archived" checked={showArchived} onChange={(event) => setShowArchived(event.currentTarget.checked)} />
            <Button
              className="railGroupingToggle"
              variant="subtle"
              color="gray"
              size="compact-sm"
              leftSection={groupByHost ? <IconRows size={14} /> : <IconStack size={14} />}
              aria-label={groupByHost ? 'Show sessions without host groups' : 'Group sessions by host'}
              aria-pressed={groupByHost}
              onClick={() => setGroupByHost((value) => !value)}
            >
              {groupByHost ? 'Grouped' : 'Flat list'}
            </Button>
          </Group>
        </>
      </Box>

      {checkedIDs.size > 0 ? <Box className="railBulkBar"><Group justify="space-between" gap="xs" wrap="nowrap"><Text fz="xs" fw={650}>{checkedIDs.size} selected</Text><Group gap={3} wrap="nowrap">
        <Tooltip label="Stop selected"><ActionIcon size="sm" variant="ghost" disabled={bulkPending} onClick={() => runBulk('stop')} aria-label="Stop selected sessions"><IconPlayerStop size={14} /></ActionIcon></Tooltip>
        <Tooltip label="Archive selected"><ActionIcon size="sm" variant="ghost" disabled={bulkPending} onClick={() => runBulk('archive')} aria-label="Archive selected sessions"><IconDatabaseExport size={14} /></ActionIcon></Tooltip>
        <Tooltip label="Delete selected"><ActionIcon size="sm" variant="ghost" color="red" disabled={bulkPending} onClick={() => runBulk('delete')} aria-label="Delete selected sessions"><IconTrash size={14} /></ActionIcon></Tooltip>
      </Group></Group></Box> : null}

      <Divider />
      <ScrollArea className="railSessions" scrollbarSize={5}>
        <Stack className="railSessionList" gap={6} p="xs" aria-label="Discovery sessions">
          {visible.length === 0 ? <Box className="railEmpty"><IconAdjustments size={24} /><Text fz="sm" fw={600} mt={8}>No matching sessions</Text><Text fz="xs" c="dimmed" ta="center" mt={3}>{sessions.length ? 'Adjust the search or status filters.' : 'Create a target to begin discovering content.'}</Text></Box> : null}
          {groups.map(([host, hostSessions]) => <Box key={host} className="railSessionGroup">
            {groupByHost ? <Text className="railGroupLabel">{host}</Text> : null}
            <Stack gap={5}>{hostSessions.map((session) => <SessionRailItem key={session.id} session={session} selected={page === 'sessions' && session.id === activeID} checked={checkedIDs.has(session.id)} organizationPending={organizationPendingIDs?.has(session.id)} onSelect={() => onSelect(session.id)} onCheck={(checked) => toggleChecked(session.id, checked)} onOrganization={onOrganization ? (patch) => onOrganization(session.id, patch) : undefined} />)}</Stack>
          </Box>)}
        </Stack>
      </ScrollArea>

      <Box className="railFooter"><Button variant={page === 'settings' ? 'light' : 'subtle'} color={page === 'settings' ? 'violet' : 'gray'} leftSection={<IconSettings size={17} />} fullWidth justify="flex-start" onClick={onSettings} aria-label="Defaults & settings">Defaults & settings</Button></Box>
      {!mobile ? <Box className="railResizer" onPointerDown={startResize} role="separator" aria-label="Resize session panel" aria-orientation="vertical" aria-valuemin={220} aria-valuemax={440} aria-valuenow={railWidth} onKeyDown={(event) => {
        if (event.key === 'ArrowLeft') setWidth(Math.max(220, railWidth - 16)); else if (event.key === 'ArrowRight') setWidth(Math.min(440, railWidth + 16)); else if (event.key === 'Home') setWidth(220); else if (event.key === 'End') setWidth(440); else return; event.preventDefault();
      }} tabIndex={0} /> : null}
      <Modal opened={confirmBulkDelete} onClose={() => setConfirmBulkDelete(false)} title="Delete selected sessions?" size="sm" centered>
        <Stack gap="md">
          <Text fz="sm" c="dimmed">
            This permanently removes {checkedIDs.size} selected {checkedIDs.size === 1 ? 'session' : 'sessions'} and their persisted results.
          </Text>
          <Group justify="flex-end" gap="xs">
            <Button variant="default" onClick={() => setConfirmBulkDelete(false)}>Cancel</Button>
            <Button color="red" disabled={bulkPending} onClick={deleteSelected}>Delete sessions</Button>
          </Group>
        </Stack>
      </Modal>
    </Box>
  );
}

function SessionRailItem({ session, selected, checked, organizationPending, onSelect, onCheck, onOrganization }: {
  session: Session;
  selected: boolean;
  checked: boolean;
  organizationPending?: boolean;
  onSelect: () => void;
  onCheck: (checked: boolean) => void;
  onOrganization?: (patch: Partial<Pick<SessionOrganization, 'pinned' | 'archived'>>) => void;
}) {
  const [actionsSuppressed, setActionsSuppressed] = useState(false);
  const scheduledCount = session.progress.request_count ?? session.progress.req_count;
  const completedCount = session.progress.completed_count
    ?? (session.progress.response_count === undefined
      ? scheduledCount
      : Math.min(scheduledCount, Math.max(0, session.progress.response_count + session.progress.error_count)));
  const percent = progressPercent(completedCount, session.progress.req_total);
  const errorLabel = `${session.progress.error_count} errors`;
  const activityLabel = session.status === 'running' ? formatRate(session.progress.req_sec) : `${session.result_count} found`;
  return <div
    className={`sessionTabWrapper${selected ? ' sessionTabActive' : ''}${checked ? ' sessionTabChecked' : ''}${onOrganization ? ' sessionTabHasActions' : ''}${actionsSuppressed ? ' sessionTabActionsSuppressed' : ''}`}
    onPointerLeave={() => setActionsSuppressed(false)}
  >
    <Checkbox className="sessionSelect" label={`Select ${session.name}`} checked={checked} onChange={(event) => onCheck(event.currentTarget.checked)} />
    <UnstyledButton className="sessionTab" onClick={onSelect} id={`session-tab-${session.id}`} aria-current={selected ? 'page' : undefined} title={session.target}>
      <Group className="sessionTabLayout" gap={9} wrap="nowrap" align="flex-start"><StatusIndicator status={session.status} compact />
        <Box className="sessionTabContent">
          <Group className="sessionTabHeading" justify="space-between" gap={8} wrap="nowrap"><Text className="sessionTabTitle" fw={600} fz="sm">{session.name || targetLabel(session.target)}</Text><Text className="sessionTabPercent" c={session.status === 'running' ? 'green.4' : 'dimmed'} fz={10} ff="monospace">{Math.round(percent)}%</Text></Group>
          <Text className="sessionTarget" c="dimmed" fz="xs" mt={2}>{targetLabel(session.target)}</Text>
          <Progress className="sessionTabProgress" value={percent} size={2} mt={9} color={session.status === 'failed' ? 'red' : session.status === 'paused' ? 'yellow' : 'violet'} animated={session.status === 'running'} />
          <Box className="sessionTabFooter" mt={5}>
            <Group className="sessionTabMeta" gap={4} wrap="wrap">
              <Text className="sessionTabStatus" c="dimmed" fz={10} tt="capitalize">{session.status}</Text>
              <Badge size="xs" variant="soft" color={session.runtime_transport === 'http3' ? 'cyan' : 'gray'}>{protocolLabel(session)}</Badge>
              {session.progress.error_count > 0 ? <Badge className="sessionTabError" size="xs" variant="soft" color="red">{errorLabel}</Badge> : null}
            </Group>
            <Text className="sessionTabStat" c="dimmed" fz={10} ff="monospace">{activityLabel}</Text>
          </Box>
        </Box>
      </Group>
    </UnstyledButton>
    {onOrganization ? <Box className="sessionTabActions">
      <ActionIcon
        className="sessionTabPin"
        size="sm"
        variant={session.pinned ? 'soft' : 'ghost'}
        aria-label={`${session.pinned ? 'Unpin' : 'Pin'} ${session.name}`}
        disabled={organizationPending}
        onClick={(event) => {
          // Pointer focus otherwise follows this keyed row when pinned-first
          // sorting moves it, leaving the action cluster visibly detached from
          // the pointer. Keep keyboard focus intact for keyboard activation.
          if (event.detail > 0) {
            setActionsSuppressed(true);
            event.currentTarget.blur();
          }
          onOrganization({ pinned: !session.pinned });
        }}
      ><IconPin size={13} /></ActionIcon>
      <Menu withinPortal position="bottom-end"><Menu.Target><ActionIcon className="sessionTabMenu" size="sm" variant="ghost" aria-label={`Session actions for ${session.name}`} disabled={organizationPending}><IconDots size={14} /></ActionIcon></Menu.Target><Menu.Dropdown><Menu.Item leftSection={<IconDatabaseExport size={14} />} onClick={() => onOrganization({ archived: !session.archived })}>{session.archived ? 'Restore session' : 'Archive session'}</Menu.Item></Menu.Dropdown></Menu>
    </Box> : null}
  </div>;
}
