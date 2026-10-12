import {
  ActionIcon,
  Box,
  Burger,
  Button,
  Drawer,
  Group,
  Loader,
  Paper,
  Text,
  ThemeIcon,
  Title,
} from './components/ui';
import { useDisclosure, useLocalStorage } from './components/ui/hooks';
import { notifications } from './components/ui/toast';
import { useMutation, useMutationState, useQuery, useQueryClient } from '@tanstack/react-query';
import { IconBrandSpeedtest, IconPlus, IconRefresh } from './components/ui/icons';
import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { api, APIError } from './api/client';
import { normalizeResult, normalizeSession, normalizeTerminalPayload } from './api/normalize';
import { NewSessionWizard } from './components/NewSessionWizard';
import { SessionDashboard } from './components/SessionDashboard';
import { SessionCompareModal } from './components/SessionCompareModal';
import { SessionRail } from './components/SessionRail';
import { StorageWarning } from './components/StorageWarning';
import { useSessionStream } from './hooks/useSessionStream';
import { SettingsPage } from './pages/SettingsPage';
import type {
  CreateSessionRequest,
  Preset,
  Result,
  Session,
  SessionFilterPatch,
  Settings,
  TerminalEntry,
  WebSocketEnvelope,
} from './types/api';
import {
  advanceSessionSequence,
  mergeSessionDelta,
  mergeSessionUpdate,
  reconcileSessionList,
  upsertSession,
} from './utils/sessionState';
import { mergeResults, RESULT_CACHE_LIMIT } from './utils/resultCache';
import { appendRatePoint, type RatePoint } from './utils/rateHistory';

function errorMessage(error: unknown): string {
  if (error instanceof APIError || error instanceof Error) return error.message;
  return 'An unexpected error occurred.';
}

function mergeTerminal(current: TerminalEntry[], incoming: TerminalEntry[]): TerminalEntry[] {
  if (!incoming.length) return current;
  const keyed = new Map<string, TerminalEntry>();
  [...current, ...incoming].forEach((entry) => {
    const key = entry.id === undefined ? `${entry.at}\0${entry.level}\0${entry.message}` : `id:${entry.id}`;
    keyed.set(key, entry);
  });
  return [...keyed.values()]
    .sort((left, right) => {
      if (left.id !== undefined && right.id !== undefined) return left.id - right.id;
      return new Date(left.at).getTime() - new Date(right.at).getTime();
    });
}

const LIVE_TERMINAL_LIMIT = 2_000;
type SessionAction = 'start' | 'pause' | 'resume' | 'stop' | 'preflight';
interface SessionActionVariables { id: string; action: SessionAction }
interface SessionFilterVariables { id: string; filters: SessionFilterPatch }
interface SessionOrganizationVariables {
  id: string;
  patch: Parameters<typeof api.updateSessionOrganization>[1];
}

function filterSignature(session: Session): string {
  const filters = session.options.filters;
  return [filters.mode, filters.status, filters.size, filters.words, filters.lines, filters.time, filters.regexp].join('\0');
}

export default function App() {
  const queryClient = useQueryClient();
  const [selectedID, setSelectedID] = useLocalStorage<string | undefined>({
    key: 'h3fuzz-selected-session',
    defaultValue: undefined,
  });
  const [page, setPage] = useState<'sessions' | 'settings'>('sessions');
  const [wizardOpen, wizard] = useDisclosure(false);
  const [compareOpen, compare] = useDisclosure(false);
  const [cloneSource, setCloneSource] = useState<Session>();
  const [mobileOpen, mobile] = useDisclosure(false);
  const [resultsBySession, setResultsBySession] = useState<Record<string, Result[]>>({});
  const [terminalBySession, setTerminalBySession] = useState<Record<string, TerminalEntry[]>>({});
  const [rateBySession, setRateBySession] = useState<Record<string, RatePoint[]>>({});
  const [loadingResults, setLoadingResults] = useState<Record<string, boolean>>({});
  const [truncatedResults, setTruncatedResults] = useState<Record<string, boolean>>({});
  const loadedResultSessions = useRef(new Set<string>());
  const pendingFilterUpdates = useRef(new Set<string>());
  const streamRevision = useRef(0);
  const pendingSessionActions = useMutationState({
    filters: { mutationKey: ['session-action'], status: 'pending' },
    select: (mutation) => mutation.state.variables as SessionActionVariables | undefined,
  });
  const pendingSessionDeletes = useMutationState({
    filters: { mutationKey: ['delete-session'], status: 'pending' },
    select: (mutation) => mutation.state.variables as string | undefined,
  });
  const pendingSessionFilters = useMutationState({
    filters: { mutationKey: ['session-filters'], status: 'pending' },
    select: (mutation) => mutation.state.variables as SessionFilterVariables | undefined,
  });
  const pendingSessionOrganizations = useMutationState({
    filters: { mutationKey: ['session-organization'], status: 'pending' },
    select: (mutation) => mutation.state.variables as SessionOrganizationVariables | undefined,
  });
  const pendingActionBySession = useMemo(() => {
    const pending = new Map<string, SessionAction>();
    pendingSessionActions.forEach((variables) => {
      if (variables) pending.set(variables.id, variables.action);
    });
    return pending;
  }, [pendingSessionActions]);
  const pendingDeleteIDs = useMemo(
    () => new Set(pendingSessionDeletes.filter((id): id is string => Boolean(id))),
    [pendingSessionDeletes],
  );
  const pendingFilterIDs = useMemo(
    () => new Set(pendingSessionFilters.flatMap((variables) => variables ? [variables.id] : [])),
    [pendingSessionFilters],
  );
  const pendingOrganizationIDs = useMemo(
    () => new Set(pendingSessionOrganizations.flatMap((variables) => variables ? [variables.id] : [])),
    [pendingSessionOrganizations],
  );

  const sessionsQuery = useQuery({
    queryKey: ['sessions'],
    queryFn: async () => {
      const revisionAtStart = streamRevision.current;
      const incoming = await api.sessions();
      const streamChangedDuringFetch = streamRevision.current !== revisionAtStart;
      return reconcileSessionList(
        queryClient.getQueryData<Session[]>(['sessions']) ?? [],
        incoming,
        streamChangedDuringFetch,
      );
    },
    refetchInterval: false,
    retry: 2,
  });
  const settingsQuery = useQuery({ queryKey: ['settings'], queryFn: api.settings, retry: 2 });
  const profilesQuery = useQuery({ queryKey: ['profiles'], queryFn: api.profiles, staleTime: Infinity });
  const optionsQuery = useQuery({ queryKey: ['options'], queryFn: api.options, staleTime: Infinity });
  const presetsQuery = useQuery({ queryKey: ['presets'], queryFn: api.presets, retry: 2 });
  const storageQuery = useQuery({
    queryKey: ['storage'],
    queryFn: api.storage,
    refetchInterval: 60_000,
    retry: 2,
  });

  const sessions = useMemo(() => sessionsQuery.data ?? [], [sessionsQuery.data]);
  const selected = sessions.find((session) => session.id === selectedID);

  useEffect(() => {
    if (sessions.length && (!selectedID || !sessions.some((session) => session.id === selectedID))) {
      setSelectedID(sessions[0].id);
    }
  }, [selectedID, sessions, setSelectedID]);

  const fetchResultDelta = useCallback(async (sessionID: string, replace = false) => {
    loadedResultSessions.current.add(sessionID);
    setLoadingResults((current) => ({ ...current, [sessionID]: true }));
    try {
      if (replace) {
        const response = await api.latestResults(sessionID, RESULT_CACHE_LIMIT);
        setResultsBySession((all) => ({
          ...all,
          [sessionID]: response.results,
        }));
        setTruncatedResults((all) => ({ ...all, [sessionID]: Boolean(response.window_truncated) }));
        return;
      }
      const current = resultsBySession[sessionID] ?? [];
      let cursor = replace ? 0 : (current.at(-1)?.id ?? 0);
      let accumulated = current;
      let pages = 0;
      while (pages < 25) {
        const response = await api.results(sessionID, cursor, 1000);
        accumulated = mergeResults(accumulated, response.results);
        cursor = response.next_cursor;
        pages += 1;
        if (!response.has_more || response.results.length === 0 || accumulated.length >= RESULT_CACHE_LIMIT) break;
      }
      setResultsBySession((all) => ({
        ...all,
        [sessionID]: mergeResults(all[sessionID] ?? [], accumulated),
      }));
    } catch (error) {
      loadedResultSessions.current.delete(sessionID);
      notifications.show({ color: 'red', title: 'Could not load results', message: errorMessage(error) });
    } finally {
      setLoadingResults((current) => ({ ...current, [sessionID]: false }));
    }
  }, [resultsBySession]);

  const replaceSessionResults = useCallback(async (sessionID: string) => {
    loadedResultSessions.current.add(sessionID);
    setResultsBySession((current) => ({ ...current, [sessionID]: [] }));
    setTruncatedResults((current) => ({ ...current, [sessionID]: false }));
    await fetchResultDelta(sessionID, true);
  }, [fetchResultDelta]);

  useEffect(() => {
    // A selected ID is restored from localStorage before the authoritative
    // session list has loaded. Only hydrate data after that ID resolves to a
    // real session, otherwise a stale selection from another database emits
    // avoidable result and terminal 404 notifications on startup.
    if (!selected) return;
    if (!loadedResultSessions.current.has(selected.id)) void fetchResultDelta(selected.id, true);
  }, [fetchResultDelta, selected]);

  const updateSession = useCallback(
    (sessionID: string, data: unknown, eventSequence?: number) => {
      let accepted = false;
      queryClient.setQueryData<Session[]>(['sessions'], (current = []) => {
        const index = current.findIndex((session) => session.id === sessionID);
        if (index < 0) return current;
        const next = mergeSessionUpdate(current[index], data, eventSequence);
        accepted = next !== current[index];
        return [...current.slice(0, index), next, ...current.slice(index + 1)];
      });
      if (!accepted) return;
      const payload = data as {
        current_rate?: number;
        req_sec?: number;
        progress?: { current_rate?: number; req_sec?: number };
      };
      const rate = payload.progress?.current_rate ?? payload.current_rate ?? payload.progress?.req_sec ?? payload.req_sec;
      if (typeof rate === 'number') {
        const observedAt = Date.now();
        setRateBySession((current) => ({
          ...current,
          [sessionID]: appendRatePoint(current[sessionID] ?? [], rate, observedAt),
        }));
      }
    },
    [queryClient],
  );

  const handleStream = useCallback(
    (event: WebSocketEnvelope) => {
      streamRevision.current += 1;
      if (event.type === 'snapshot') {
        const payload = event.data as { sessions?: Session[] } | Session[];
        const snapshot = (Array.isArray(payload) ? payload : (payload.sessions ?? [])).map(normalizeSession);
        queryClient.setQueryData<Session[]>(['sessions'], (current = []) =>
          reconcileSessionList(current, snapshot, false),
        );
        return;
      }
      if (event.type === 'session.created') {
        const session = normalizeSession(event.data as Session);
        queryClient.setQueryData<Session[]>(['sessions'], (current = []) =>
          upsertSession(current, session, event.sequence),
        );
        return;
      }
      if (event.type === 'session.deleted') {
        const id = event.session_id ?? (event.data as { id?: string }).id;
        void queryClient.cancelQueries({ queryKey: ['sessions'] });
        queryClient.setQueryData<Session[]>(['sessions'], (current = []) =>
          current.filter((session) => session.id !== id),
        );
        if (id) {
          loadedResultSessions.current.delete(id);
          setResultsBySession((current) => {
            const next = { ...current };
            delete next[id];
            return next;
          });
          setTerminalBySession((current) => {
            const next = { ...current };
            delete next[id];
            return next;
          });
          setRateBySession((current) => {
            const next = { ...current };
            delete next[id];
            return next;
          });
        }
        void queryClient.invalidateQueries({ queryKey: ['storage'] });
        return;
      }
      if (!event.session_id) return;
      if (event.type === 'session.updated') {
        const session = normalizeSession(event.data as Session);
        const previous = queryClient.getQueryData<Session[]>(['sessions'])
          ?.find((entry) => entry.id === event.session_id);
        const filtersChanged = previous && filterSignature(previous) !== filterSignature(session);
        updateSession(event.session_id, session, event.sequence);
        if (filtersChanged && !pendingFilterUpdates.current.has(event.session_id)) {
          void replaceSessionResults(event.session_id);
        }
      } else if (event.type === 'progress') {
        updateSession(event.session_id, event.data, event.sequence);
      } else if (event.type === 'result') {
        const payload = event.data as Result | { results?: Result[] };
        const incoming = ('results' in payload ? (payload.results ?? []) : [payload as Result]).map(normalizeResult);
        queryClient.setQueryData<Session[]>(['sessions'], (current = []) =>
          current.map((session) =>
            session.id === event.session_id
              ? mergeSessionDelta(
                  session,
                  { result_count: session.result_count + incoming.length },
                  event.sequence,
                )
              : session,
          ),
        );
        setResultsBySession((current) => ({
          ...current,
          [event.session_id!]: mergeResults(current[event.session_id!] ?? [], incoming),
        }));
      } else if (event.type === 'terminal') {
        const entries = normalizeTerminalPayload(event.data);
        queryClient.setQueryData<Session[]>(['sessions'], (current = []) =>
          current.map((session) =>
            session.id === event.session_id
              ? advanceSessionSequence(session, event.sequence)
              : session,
          ),
        );
        setTerminalBySession((current) => ({
          ...current,
          [event.session_id!]: mergeTerminal(current[event.session_id!] ?? [], entries).slice(-LIVE_TERMINAL_LIMIT),
        }));
      }
    },
    [queryClient, replaceSessionResults, updateSession],
  );

  const resync = useCallback(() => {
    void queryClient.invalidateQueries({ queryKey: ['sessions'] });
    loadedResultSessions.current.forEach((id) => {
      void fetchResultDelta(id);
    });
  }, [fetchResultDelta, queryClient]);

  const connection = useSessionStream({ onEvent: handleStream, onSequenceGap: resync });

  const createMutation = useMutation({
    mutationFn: ({ request, sourceID }: { request: CreateSessionRequest; sourceID?: string }) =>
      sourceID ? api.cloneSession(sourceID, request) : api.createSession(request),
    onSuccess: (session) => {
      queryClient.setQueryData<Session[]>(['sessions'], (current = []) =>
        upsertSession(current, session),
      );
      setSelectedID(session.id);
      setPage('sessions');
      wizard.close();
      setCloneSource(undefined);
      notifications.show({ color: 'green', title: session.cloned_from_id ? 'Cloned session created' : 'Session created', message: session.target });
    },
    onError: (error) => notifications.show({ color: 'red', title: 'Could not create session', message: errorMessage(error) }),
  });

  const actionMutation = useMutation({
    mutationKey: ['session-action'],
    mutationFn: ({ id, action }: SessionActionVariables) =>
      api.action(id, action),
    onMutate: ({ id, action }) => {
      if (action !== 'stop') return undefined;
      const previous = queryClient.getQueryData<Session[]>(['sessions'])?.find((session) => session.id === id);
      updateSession(id, { status: 'stopping' });
      return previous ? { previous, optimisticSequence: previous.sequence ?? 0 } : undefined;
    },
    onSuccess: (session, variables) => {
      updateSession(session.id, session);
      if (variables.action === 'preflight') {
        notifications.show({ color: 'green', title: 'Protocol preflight started', message: 'Capability evidence will update live.' });
      }
    },
    onError: (error, variables, context) => {
      if (context?.previous) {
        queryClient.setQueryData<Session[]>(['sessions'], (current = []) =>
          current.map((session) =>
            session.id === variables.id &&
            session.status === 'stopping' &&
            (session.sequence ?? 0) === context.optimisticSequence
              ? context.previous
              : session,
          ),
        );
      }
      void queryClient.invalidateQueries({ queryKey: ['sessions'] });
      notifications.show({
        color: 'red',
        title: variables.action === 'preflight' ? 'Could not run protocol preflight' : 'Session action failed',
        message: errorMessage(error),
      });
    },
  });

  const filtersMutation = useMutation({
    mutationKey: ['session-filters'],
    mutationFn: ({ id, filters }: SessionFilterVariables) =>
      api.addSessionFilters(id, filters),
    onMutate: ({ id }) => {
      pendingFilterUpdates.current.add(id);
    },
    onSuccess: async (session) => {
      pendingFilterUpdates.current.delete(session.id);
      updateSession(session.id, session);
      await replaceSessionResults(session.id);
      notifications.show({
        color: 'green',
        title: 'Filters applied',
        message: 'Existing and future findings now use the saved filters.',
      });
    },
    onError: (error, variables) => {
      pendingFilterUpdates.current.delete(variables.id);
      void queryClient.invalidateQueries({ queryKey: ['sessions'] });
      void replaceSessionResults(variables.id);
      notifications.show({
        color: 'red',
        title: 'Could not apply filters',
        message: errorMessage(error),
      });
    },
  });

  const deleteMutation = useMutation({
    mutationKey: ['delete-session'],
    mutationFn: api.deleteSession,
    onMutate: async () => {
      await queryClient.cancelQueries({ queryKey: ['sessions'] });
    },
    onSuccess: (_, id) => {
      queryClient.setQueryData<Session[]>(['sessions'], (current = []) =>
        current.filter((session) => session.id !== id),
      );
      loadedResultSessions.current.delete(id);
      setResultsBySession((current) => {
        const next = { ...current };
        delete next[id];
        return next;
      });
      setTerminalBySession((current) => {
        const next = { ...current };
        delete next[id];
        return next;
      });
      setRateBySession((current) => {
        const next = { ...current };
        delete next[id];
        return next;
      });
      setTruncatedResults((current) => {
        const next = { ...current };
        delete next[id];
        return next;
      });
      notifications.show({ color: 'green', message: 'Session deleted' });
      void queryClient.invalidateQueries({ queryKey: ['storage'] });
    },
    onError: (error) => notifications.show({ color: 'red', title: 'Could not delete session', message: errorMessage(error) }),
  });

  const settingsMutation = useMutation({
    mutationFn: api.saveSettings,
    onSuccess: (settings) => {
      queryClient.setQueryData(['settings'], settings);
      notifications.show({ color: 'green', title: 'Defaults saved', message: 'New sessions will inherit these values.' });
    },
    onError: (error) => notifications.show({ color: 'red', title: 'Could not save defaults', message: errorMessage(error) }),
  });

  const organizationMutation = useMutation({
    mutationKey: ['session-organization'],
    mutationFn: ({ id, patch }: SessionOrganizationVariables) => api.updateSessionOrganization(id, patch),
    onSuccess: (organization, variables) => {
      queryClient.setQueryData<Session[]>(['sessions'], (current = []) => current.map((session) => session.id === variables.id ? { ...session, ...organization } : session));
    },
    onError: (error) => notifications.show({ color: 'red', title: 'Could not update session', message: errorMessage(error) }),
  });

  const bulkMutation = useMutation({
    mutationFn: ({ ids, action }: { ids: string[]; action: 'stop' | 'delete' | 'archive' }) => api.bulkSessions(ids, action),
    onSuccess: (result, variables) => {
      void queryClient.invalidateQueries({ queryKey: ['sessions'] });
      if (variables.action === 'delete') void queryClient.invalidateQueries({ queryKey: ['storage'] });
      notifications.show({ color: result.failed && Object.keys(result.failed).length ? 'yellow' : 'green', title: `${variables.action} completed`, message: `${result.succeeded?.length ?? 0} session(s) updated.` });
    },
    onError: (error) => notifications.show({ color: 'red', title: 'Bulk action failed', message: errorMessage(error) }),
  });

  const compactStorageMutation = useMutation({
    mutationFn: api.compactStorage,
    onSuccess: (stats) => {
      queryClient.setQueryData(['storage'], stats);
      notifications.show({
        color: 'green',
        title: 'Database compacted',
        message: 'Unused SQLite pages were returned to disk. No scan data was deleted.',
      });
    },
    onError: (error) => notifications.show({
      color: 'red',
      title: 'Could not compact database',
      message: errorMessage(error),
    }),
  });

  const presetMutation = useMutation({
    mutationFn: async (operation: { type: 'create'; preset: Omit<Preset, 'id' | 'created_at' | 'updated_at'> } | { type: 'update'; preset: Preset } | { type: 'delete'; id: string }) => {
      if (operation.type === 'create') return api.createPreset(operation.preset);
      if (operation.type === 'update') return api.updatePreset(operation.preset);
      await api.deletePreset(operation.id);
      return undefined;
    },
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ['presets'] });
      notifications.show({ color: 'green', message: 'Preset library updated.' });
    },
    onError: (error) => notifications.show({ color: 'red', title: 'Could not update presets', message: errorMessage(error) }),
  });

  const rail = (
    <SessionRail
      sessions={sessions}
      activeID={selectedID}
      page={page}
      connected={connection === 'connected'}
      organizationPendingIDs={pendingOrganizationIDs}
      bulkPending={bulkMutation.isPending}
      onOrganization={(id, patch) => organizationMutation.mutate({ id, patch })}
      onBulk={(ids, action) => bulkMutation.mutate({ ids, action })}
      onSelect={(id) => {
        setSelectedID(id);
        setPage('sessions');
        mobile.close();
      }}
      onNew={() => {
        setCloneSource(undefined);
        wizard.open();
        mobile.close();
      }}
      onSettings={() => {
        setPage('settings');
        mobile.close();
      }}
    />
  );

  const content = useMemo(() => {
    if (sessionsQuery.isLoading) return <LoadingScreen />;
    if (sessionsQuery.isError) {
      return (
        <EmptyScreen
          title="The console is unavailable"
          description={errorMessage(sessionsQuery.error)}
          action="Try again"
          icon="error"
          onAction={() => void sessionsQuery.refetch()}
        />
      );
    }
    if (page === 'settings') {
      return (
        <SettingsPage
          settings={settingsQuery.data}
          profiles={profilesQuery.data}
          options={optionsQuery.data?.flags}
          presets={presetsQuery.data}
          saving={settingsMutation.isPending}
          presetSaving={presetMutation.isPending}
          onSave={(settings: Settings) => settingsMutation.mutate(settings)}
          onCreatePreset={(preset) => presetMutation.mutate({ type: 'create', preset })}
          onUpdatePreset={(preset) => presetMutation.mutate({ type: 'update', preset })}
          onDeletePreset={(id) => presetMutation.mutate({ type: 'delete', id })}
        />
      );
    }
    if (!selected) {
      return (
        <EmptyScreen
          title="Start a discovery session"
          description="Configure a target, select a language profile, and let h3fuzz choose the fastest compatible transport."
          action="New session"
          icon="new"
          onAction={wizard.open}
        />
      );
    }
    const selectedPendingAction = pendingActionBySession.get(selected.id);
    const selectedActionPending = selectedPendingAction !== undefined;
    const selectedPreflightPending = selectedPendingAction === 'preflight';
    const selectedLifecyclePending = selectedActionPending && !selectedPreflightPending;
    const selectedDeletePending = pendingDeleteIDs.has(selected.id);
    const preflightAllowed = !['starting', 'running', 'paused', 'stopping'].includes(selected.status);
    return (
      <SessionDashboard
        session={selected}
        results={resultsBySession[selected.id] ?? []}
        terminal={terminalBySession[selected.id] ?? []}
        rateHistory={rateBySession[selected.id] ?? []}
        loadingResults={loadingResults[selected.id]}
        resultsTruncated={truncatedResults[selected.id]}
        actionPending={selectedLifecyclePending}
        actionsDisabled={selectedActionPending}
        deletePending={selectedDeletePending}
        preflightPending={selectedPreflightPending}
        filtersPending={pendingFilterIDs.has(selected.id)}
        organizationPending={pendingOrganizationIDs.has(selected.id)}
        onAction={(action) => actionMutation.mutate({ id: selected.id, action })}
        onRunPreflight={!selectedActionPending && preflightAllowed ? () => actionMutation.mutate({ id: selected.id, action: 'preflight' }) : undefined}
        onAddFilters={async (filters) => {
          await filtersMutation.mutateAsync({ id: selected.id, filters });
        }}
        onCloneEdit={() => {
          setCloneSource(selected);
          wizard.open();
        }}
        onCompare={['completed', 'stopped'].includes(selected.status) && sessions.some(
          (candidate) => candidate.id !== selected.id && ['completed', 'stopped'].includes(candidate.status),
        ) ? compare.open : undefined}
        onArchive={() => organizationMutation.mutate({ id: selected.id, patch: { archived: !selected.archived } })}
        onDelete={() => deleteMutation.mutate(selected.id)}
        onRefresh={() => {
          void queryClient.invalidateQueries({ queryKey: ['sessions'] });
          void fetchResultDelta(selected.id, true);
        }}
      />
    );
  }, [
    actionMutation,
    deleteMutation,
    fetchResultDelta,
    filtersMutation,
    loadingResults,
    optionsQuery.data?.flags,
    organizationMutation,
    page,
    pendingActionBySession,
    pendingDeleteIDs,
    pendingFilterIDs,
    pendingOrganizationIDs,
    profilesQuery.data,
    presetsQuery.data,
    presetMutation,
    queryClient,
    rateBySession,
    resultsBySession,
    selected,
    sessionsQuery,
    settingsMutation,
    settingsQuery.data,
    terminalBySession,
    truncatedResults,
    wizard,
    compare,
    sessions,
  ]);

  return (
    <Box className="appShell">
      <Box className="desktopRail">{rail}</Box>
      <Box component="main" className="mainPane">
        <Box className="mobileHeader">
          <Group justify="space-between">
            <Group gap="xs">
              <Burger opened={mobileOpen} onClick={mobile.toggle} size="sm" aria-label="Open sessions" />
              <IconBrandSpeedtest size={19} />
              <Text fw={700}>h3fuzz</Text>
            </Group>
            <ActionIcon variant="light" onClick={wizard.open} aria-label="New session">
              <IconPlus size={17} />
            </ActionIcon>
          </Group>
        </Box>
        <StorageWarning
          stats={storageQuery.data}
          compacting={compactStorageMutation.isPending}
          compactDisabled={sessions.some((session) => ['starting', 'running', 'paused', 'stopping'].includes(session.status))}
          onReviewSessions={() => {
            setPage('sessions');
            notifications.show({
              title: 'Choose scans to remove',
              message: 'Use the session menu or bulk selection in the left panel, then compact the database.',
            });
          }}
          onCompact={() => compactStorageMutation.mutateAsync()}
        />
        {content}
      </Box>
      <Drawer
        opened={mobileOpen}
        onClose={mobile.close}
        position="left"
        size="min(88vw, 360px)"
        padding={0}
        withCloseButton={false}
        classNames={{ body: 'mobileDrawerBody' }}
      >
        <SessionRail
          sessions={sessions}
          activeID={selectedID}
          page={page}
          connected={connection === 'connected'}
          mobile
          organizationPendingIDs={pendingOrganizationIDs}
          bulkPending={bulkMutation.isPending}
          onOrganization={(id, patch) => organizationMutation.mutate({ id, patch })}
          onBulk={(ids, action) => bulkMutation.mutate({ ids, action })}
          onSelect={(id) => {
            setSelectedID(id);
            setPage('sessions');
            mobile.close();
          }}
          onNew={() => {
            setCloneSource(undefined);
            wizard.open();
            mobile.close();
          }}
          onSettings={() => {
            setPage('settings');
            mobile.close();
          }}
        />
      </Drawer>
      {wizardOpen ? (
        <NewSessionWizard
          opened={wizardOpen}
          settings={settingsQuery.data}
          profiles={profilesQuery.data}
          options={optionsQuery.data?.flags}
          initialSession={cloneSource}
          presets={presetsQuery.data}
          presetSaving={presetMutation.isPending}
          submitting={createMutation.isPending}
          onClose={() => {
            setCloneSource(undefined);
            wizard.close();
          }}
          onCreate={(request: CreateSessionRequest) => createMutation.mutate({ request, sourceID: cloneSource?.id })}
          onSavePreset={(preset) => presetMutation.mutate({ type: 'create', preset })}
          onDeletePreset={(id) => presetMutation.mutate({ type: 'delete', id })}
        />
      ) : null}
      {selected ? <SessionCompareModal opened={compareOpen} session={selected} sessions={sessions} onClose={compare.close} /> : null}
    </Box>
  );
}

function LoadingScreen() {
  return (
    <Box className="centerScreen">
      <Loader size="sm" />
      <Text c="dimmed" fz="sm" mt="md">
        Loading persisted sessions…
      </Text>
    </Box>
  );
}

function EmptyScreen({
  title,
  description,
  action,
  icon,
  onAction,
}: {
  title: string;
  description: string;
  action: string;
  icon: 'new' | 'error';
  onAction: () => void;
}) {
  return (
    <Box className="centerScreen">
      <Paper className="welcomeCard" withBorder p="xl">
        <ThemeIcon size={44} radius="lg" color={icon === 'error' ? 'red' : 'violet'} variant="light">
          {icon === 'error' ? <IconRefresh size={22} /> : <IconBrandSpeedtest size={23} />}
        </ThemeIcon>
        <Title order={2} mt="lg">
          {title}
        </Title>
        <Text c="dimmed" mt="xs" maw={480}>
          {description}
        </Text>
        <Button mt="xl" leftSection={icon === 'new' ? <IconPlus size={16} /> : <IconRefresh size={16} />} onClick={onAction}>
          {action}
        </Button>
      </Paper>
    </Box>
  );
}
