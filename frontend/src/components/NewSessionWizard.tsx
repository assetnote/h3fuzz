import {
  Accordion,
  Alert,
  Badge,
  Box,
  Button,
  Checkbox,
  Divider,
  Grid,
  Group,
  Modal,
  NumberInput,
  Paper,
  ScrollArea,
  SegmentedControl,
  Select,
  SimpleGrid,
  Stack,
  Switch,
  Text,
  TextInput,
  Textarea,
  ThemeIcon,
  Title,
  notifications,
} from './ui';
import { useMediaQuery } from './ui/hooks';
import {
  IconAdjustmentsHorizontal,
  IconBolt,
  IconCheck,
  IconCopy,
  IconFileText,
  IconInfoCircle,
  IconRocket,
  IconRefresh,
  IconTrash,
  IconShieldCheck,
  IconTerminal2,
  IconWorld,
} from './ui/icons';
import { useEffect, useMemo, useRef, useState } from 'react';
import { api } from '../api/client';
import { normalizeConfig } from '../api/normalize';
import type {
  ConfigOptions,
  CreateSessionRequest,
  OptionMetadata,
  Preset,
  Profile,
  ProfileID,
  Session,
  Settings,
  WordlistInspection,
} from '../types/api';
import {
  applyProfile,
  bundledProfiles,
  cloneConfig,
  defaultConfig,
  DEFAULT_WEB_EXPLICIT_FLAGS,
  DEFAULT_WEB_THREAD_COUNT,
} from '../utils/config';
import { generateCLICommand, parseCLICommand } from '../utils/cliCommand';
import { copyText } from '../utils/clipboard';
import { addExplicitFlags, canonicalPatchFlag } from '../utils/explicitFlags';
import {
  protocolExplicitFlags,
  protocolFlagNames,
  protocolMode,
  protocolModeDescription,
  protocolModePatch,
  type ProtocolMode,
} from '../utils/protocolMode';
import { applyAkamaiHeaders, hasAkamaiHeaders, removeAkamaiHeaders } from '../utils/headers';
import { getOptionValue, normalizeOptionPath, setOptionValue } from '../utils/optionValue';
import {
  estimateWordlistRequests,
  parseWordlistSpec,
  sniperTemplatePresent,
  wordlistActivity,
  type InspectedWordlist,
} from '../utils/wordlists';
import { OptionField } from './OptionField';
import { Http3CapacitySummary } from './Http3CapacitySummary';

interface NewSessionWizardProps {
  opened: boolean;
  settings?: Settings;
  profiles?: Profile[];
  options?: OptionMetadata[];
  initialSession?: Session;
  presets?: Preset[];
  presetSaving?: boolean;
  submitting?: boolean;
  onClose: () => void;
  onCreate: (request: CreateSessionRequest) => void;
  onSavePreset?: (preset: Omit<Preset, 'id' | 'created_at' | 'updated_at'>) => void;
  onDeletePreset?: (id: string) => void;
}

function initialForm(settings?: Settings, initialSession?: Session) {
  const config = normalizeConfig(initialSession?.options ?? settings?.defaults.options ?? defaultConfig);
  const profileID = initialSession?.profile ?? settings?.defaults.profile ?? 'aspm';
  const profile = bundledProfiles.find((entry) => entry.id === profileID) ?? bundledProfiles[0];
  const profiled = initialSession ? config : applyProfile(config, profile);
  if (!settings) profiled.input.input_mode = 'pitchfork';
  return {
    target: initialSession?.target ?? '',
    name: initialSession ? `${initialSession.name} copy` : '',
    wordlist: initialSession?.wordlist || settings?.defaults.wordlist || 'final.txt',
    profile: profileID,
    config: profiled,
    explicitFlags: new Set(
      Array.isArray(initialSession?.explicit_flags ?? settings?.defaults.explicit_flags)
        ? (initialSession?.explicit_flags ?? settings?.defaults.explicit_flags)!
        : DEFAULT_WEB_EXPLICIT_FLAGS,
    ),
    autoStart: true,
    akamaiTarget: hasAkamaiHeaders(profiled.http.headers),
  };
}

function isAbsoluteWebURL(value: string): boolean {
  try {
    const parsed = new URL(value);
    return parsed.protocol === 'http:' || parsed.protocol === 'https:';
  } catch {
    return false;
  }
}

function inferProfile(
  extensions: string,
  profiles: Profile[],
  fallback: ProfileID,
): ProfileID {
  const normalized = extensions
    .split(',')
    .map((extension) => extension.trim().toLowerCase())
    .filter(Boolean)
    .sort();
  const match = profiles.find((profile) => {
    const candidate = profile.extensions.map((extension) => extension.toLowerCase()).sort();
    return candidate.length === normalized.length && candidate.every((extension, index) => extension === normalized[index]);
  });
  return match?.id ?? fallback;
}

export function NewSessionWizard({
  opened,
  settings,
  profiles,
  options = [],
  initialSession,
  presets = [],
  presetSaving,
  submitting,
  onClose,
  onCreate,
  onSavePreset,
  onDeletePreset,
}: NewSessionWizardProps) {
  const fullScreen = useMediaQuery('(max-width: 52em)');
  const availableProfiles = profiles?.length ? profiles : bundledProfiles;
  const [form, setForm] = useState(() => initialForm(settings, initialSession));
  const [baseline, setBaseline] = useState(() => initialForm(settings, initialSession));
  const [advancedSearch, setAdvancedSearch] = useState('');
  const [changedOnly, setChangedOnly] = useState(false);
  const [showCompatibility, setShowCompatibility] = useState(false);
  const [presetName, setPresetName] = useState('');
  const [selectedPreset, setSelectedPreset] = useState<string | null>(null);
  const [wordlistChecks, setWordlistChecks] = useState<Array<{
    spec: ReturnType<typeof parseWordlistSpec>;
    inspection?: WordlistInspection;
    error?: string;
  }>>([]);
  const [wordlistInspecting, setWordlistInspecting] = useState(false);
  const [wordlistStale, setWordlistStale] = useState(true);
  const [allowUnverifiedWordlist, setAllowUnverifiedWordlist] = useState(false);
  const [cliInput, setCLIInput] = useState('');
  const [cliDiagnostics, setCLIDiagnostics] = useState<ReturnType<typeof parseCLICommand>['diagnostics']>([]);
  const [cliApplied, setCLIApplied] = useState(false);
  const wasOpened = useRef(false);

  useEffect(() => {
    if (opened && !wasOpened.current) {
      const next = initialForm(settings, initialSession);
      setForm(next);
      setBaseline({ ...next, config: cloneConfig(next.config), explicitFlags: new Set(next.explicitFlags) });
      setAdvancedSearch('');
      setChangedOnly(false);
      setShowCompatibility(false);
      setCLIInput('');
      setCLIDiagnostics([]);
      setCLIApplied(false);
      setPresetName('');
      setSelectedPreset(null);
      setWordlistChecks([]);
      setWordlistStale(true);
      setAllowUnverifiedWordlist(false);
    }
    wasOpened.current = opened;
  }, [initialSession, opened, settings]);

  const configuredWordlistKey = JSON.stringify((() => {
    const configured = form.config.input.wordlists.map((value) => value.trim()).filter(Boolean);
    if (form.explicitFlags.has('w') && configured.length > 0) return configured;
    return form.wordlist.trim() ? [form.wordlist.trim()] : [];
  })());
  const configuredWordlistValues = useMemo(
    () => JSON.parse(configuredWordlistKey) as string[],
    [configuredWordlistKey],
  );
  const wordlistSpecs = useMemo(
    () => configuredWordlistValues.map(parseWordlistSpec),
    [configuredWordlistValues],
  );
  const targetValid = isAbsoluteWebURL(form.target);
  const requestInputs = useMemo(() => ({
    method: form.config.http.method || 'GET',
    target: form.target,
    data: form.config.http.data,
    headers: form.config.http.headers,
    cookies: form.config.http.cookies,
  }), [form.config.http.cookies, form.config.http.data, form.config.http.headers, form.config.http.method, form.target]);
  const wordlistKeywordActivity = useMemo(
    () => wordlistActivity(wordlistSpecs, requestInputs),
    [requestInputs, wordlistSpecs],
  );
  const sniperMode = form.config.input.input_mode === 'sniper';
  const validSniperInput = sniperMode && wordlistSpecs.length === 1 && !wordlistSpecs[0].named && sniperTemplatePresent(requestInputs);
  const hasFuzzKeyword = sniperMode ? validSniperInput : wordlistKeywordActivity.active.length > 0;
  const validationUnavailable = wordlistChecks.some((check) => Boolean(check.error));
  const allWordlistsReadable = wordlistChecks.length === wordlistSpecs.length && wordlistChecks.every((check) => check.inspection?.readable === true);
  const wordlistReady = allWordlistsReadable || (
    wordlistChecks.length === wordlistSpecs.length &&
    allowUnverifiedWordlist &&
    wordlistChecks.every((check) => check.inspection?.readable === true || Boolean(check.error))
  );
  const canSubmit = targetValid && hasFuzzKeyword && wordlistSpecs.length > 0 && !wordlistStale && !wordlistInspecting && wordlistReady;
  const inspectionGeneration = useRef(0);
  useEffect(() => {
    if (!opened || wordlistSpecs.length === 0) {
      inspectionGeneration.current += 1;
      setWordlistChecks([]);
      setWordlistInspecting(false);
      setWordlistStale(true);
      return;
    }
    const generation = ++inspectionGeneration.current;
    setWordlistStale(true);
    setWordlistChecks([]);
    const timer = window.setTimeout(() => {
      setWordlistInspecting(true);
      void Promise.all(wordlistSpecs.map(async (spec) => {
        try {
          return { spec, inspection: await api.inspectWordlist(spec.path) };
        } catch (error) {
          return {
            spec,
            error: error instanceof Error ? error.message : 'Could not inspect this wordlist.',
          };
        }
      })).then((checks) => {
        if (inspectionGeneration.current !== generation) return;
        setWordlistChecks(checks);
        setWordlistStale(false);
      }).finally(() => {
        if (inspectionGeneration.current === generation) setWordlistInspecting(false);
      });
    }, 500);
    return () => {
      window.clearTimeout(timer);
      if (inspectionGeneration.current === generation) inspectionGeneration.current += 1;
    };
  }, [opened, wordlistSpecs]);

  const estimatedRequests = useMemo(() => {
    if (wordlistStale || wordlistChecks.length !== wordlistSpecs.length || wordlistChecks.some((check) => !check.inspection)) return undefined;
    const activeChecks = wordlistChecks.filter((_, index) =>
      wordlistKeywordActivity.active.includes(wordlistSpecs[index])) as InspectedWordlist[];
    // Before a request keyword is entered, show the configured workload as a
    // planning estimate. As soon as at least one provider is active, mirror
    // ffuf and exclude providers whose keywords are absent from the request.
    const providers = activeChecks.length > 0 ? activeChecks : wordlistChecks as InspectedWordlist[];
    return estimateWordlistRequests(providers, {
      mode: form.config.input.input_mode,
      extensions: form.config.input.extensions,
      dirsearchCompat: form.config.input.dirsearch_compat,
      ignoreComments: form.config.input.ignore_wordlist_comments,
    });
  }, [form.config.input.dirsearch_compat, form.config.input.extensions, form.config.input.ignore_wordlist_comments, form.config.input.input_mode, wordlistChecks, wordlistKeywordActivity.active, wordlistSpecs, wordlistStale]);
  const generatedCommand = useMemo(
    () => generateCLICommand(form, options),
    [form, options],
  );

  const setConfig = (config: ConfigOptions, flagName?: string) =>
    setForm((current) => ({
      ...current,
      config,
      wordlist: flagName === 'w' ? (config.input.wordlists[0] ?? '') : current.wordlist,
      akamaiTarget: flagName === 'H' ? hasAkamaiHeaders(config.http.headers) : current.akamaiTarget,
      explicitFlags: flagName ? addExplicitFlags(current.explicitFlags, flagName) : current.explicitFlags,
    }));

  const patchConfig = <S extends keyof ConfigOptions>(section: S, patch: Partial<ConfigOptions[S]>) => {
    const names = Object.keys(patch).map((key) => canonicalPatchFlag(section, key));
    setForm((current) => ({
      ...current,
      config: { ...current.config, [section]: { ...current.config[section], ...patch } },
      explicitFlags: addExplicitFlags(current.explicitFlags, ...names),
    }));
  };

  const setProtocolMode = (mode: ProtocolMode) => {
    setForm((current) => ({
      ...current,
      config: {
        ...current.config,
        http: { ...current.config.http, ...protocolModePatch(mode) },
      },
      explicitFlags: protocolExplicitFlags(current.explicitFlags, mode),
    }));
  };

  const selectProfile = (id: ProfileID) => {
    const profile = availableProfiles.find((entry) => entry.id === id);
    setForm((current) => ({
      ...current,
      profile: id,
      config: profile ? applyProfile(current.config, profile) : current.config,
      explicitFlags: addExplicitFlags(current.explicitFlags, 'D', 'e', 'mc', 'fc', 'H'),
    }));
  };

  const setAkamaiTarget = (enabled: boolean) => {
    setForm((current) => ({
      ...current,
      akamaiTarget: enabled,
      config: {
        ...current.config,
        http: {
          ...current.config.http,
          headers: enabled
            ? applyAkamaiHeaders(current.config.http.headers)
            : removeAkamaiHeaders(current.config.http.headers),
        },
      },
      explicitFlags: addExplicitFlags(current.explicitFlags, 'H'),
    }));
  };

  const setPrimaryWordlist = (wordlist: string) => {
    setWordlistStale(true);
    setAllowUnverifiedWordlist(false);
    setForm((current) => ({
      ...current,
      wordlist,
      config: {
        ...current.config,
        input: {
          ...current.config.input,
          wordlists: [
            wordlist,
            ...(current.explicitFlags.has('w') ? current.config.input.wordlists.slice(1) : []),
          ],
        },
      },
    }));
  };

  const setAutomaticWorkerSizing = (enabled: boolean) => {
    setForm((current) => {
      const explicitFlags = new Set(current.explicitFlags);
      if (enabled) explicitFlags.delete('t');
      else explicitFlags.add('t');
      return {
        ...current,
        config: {
          ...current.config,
          general: {
            ...current.config.general,
            threads: current.config.general.threads > 0
              ? current.config.general.threads
              : DEFAULT_WEB_THREAD_COUNT,
          },
        },
        explicitFlags,
      };
    });
  };

  const filteredOptions = useMemo(() => {
    const query = advancedSearch.trim().toLowerCase();
    const seenPaths = new Set<string>();
    return [...options]
      .sort((left, right) => Number(right.type === 'object_array') - Number(left.type === 'object_array'))
      .filter((option) => {
        // Protocol selection has one canonical editor above. Rendering these
        // booleans again would let users construct contradictory combinations
        // such as -http1 plus -http3 while the segmented control masks one of
        // them. Imported legacy -http2 state is still retained in form.config
        // and in explicitFlags until the canonical selector is changed.
        if ((protocolFlagNames as readonly string[]).includes(option.name)) return false;
        if (option.hidden && !showCompatibility) return false;
        const pathKey = option.json_path || `$flag:${option.name}`;
        if (seenPaths.has(pathKey)) return false;
        seenPaths.add(pathKey);
        const valueChanged = JSON.stringify(getOptionValue(form.config, option.json_path) ?? option.default) !== JSON.stringify(getOptionValue(baseline.config, option.json_path) ?? option.default);
        const explicitnessChanged = form.explicitFlags.has(option.name) !== baseline.explicitFlags.has(option.name);
        if (changedOnly && !valueChanged && !explicitnessChanged) return false;
        return !query || `${option.name} ${option.usage} ${option.section}`.toLowerCase().includes(query);
      });
  }, [advancedSearch, baseline.config, baseline.explicitFlags, changedOnly, form.config, form.explicitFlags, options, showCompatibility]);

  const optionGroups = useMemo(() => {
    const groups = new Map<string, OptionMetadata[]>();
    filteredOptions.forEach((option) =>
      groups.set(option.section, [...(groups.get(option.section) ?? []), option]),
    );
    return groups;
  }, [filteredOptions]);

  const restoreFlagMembership = (current: Set<string>, names: Iterable<string>) => {
    const next = new Set(current);
    for (const name of names) {
      if (baseline.explicitFlags.has(name)) next.add(name);
      else next.delete(name);
    }
    return next;
  };

  const sectionFlags = (section: keyof ConfigOptions) => {
    const names = options
      .filter((option) => normalizeOptionPath(option.json_path)[0] === section)
      .map((option) => option.name);
    return section === 'http' ? [...new Set([...names, ...protocolFlagNames])] : names;
  };

  const resetSection = (section: keyof ConfigOptions) => {
    setForm((current) => ({
      ...current,
      config: { ...current.config, [section]: cloneConfig(baseline.config)[section] },
      explicitFlags: restoreFlagMembership(current.explicitFlags, sectionFlags(section)),
      akamaiTarget: section === 'http' ? baseline.akamaiTarget : current.akamaiTarget,
    }));
  };

  const resetOption = (option: OptionMetadata) => setForm((current) => ({
    ...current,
    config: setOptionValue(
      current.config,
      option.json_path,
      getOptionValue(baseline.config, option.json_path) ?? option.default,
    ),
    explicitFlags: restoreFlagMembership(current.explicitFlags, [option.name]),
  }));

  const applyPreset = (id: string | null) => {
    setSelectedPreset(id);
    const preset = presets.find((entry) => entry.id === id);
    if (!preset) return;
    const config = normalizeConfig(preset.options);
    setForm((current) => ({
      ...current,
      wordlist: preset.wordlist,
      profile: preset.profile,
      config,
      explicitFlags: new Set(preset.explicit_flags),
      akamaiTarget: hasAkamaiHeaders(config.http.headers),
    }));
  };

  const savePreset = () => {
    const name = presetName.trim();
    if (!name || !onSavePreset) return;
    onSavePreset({
      name,
      description: `Saved from ${form.target.trim() || 'new session settings'}`,
      wordlist: form.wordlist.trim(),
      profile: form.profile,
      options: cloneConfig(form.config),
      explicit_flags: [...form.explicitFlags],
    });
    setPresetName('');
  };

  const applyCLICommand = () => {
    const baseConfig = cloneConfig(defaultConfig);
    baseConfig.http.url = form.target.trim();
    baseConfig.input.wordlists = [form.wordlist.trim()].filter(Boolean);
    const parsed = parseCLICommand(cliInput, baseConfig, options);
    setCLIDiagnostics(parsed.diagnostics);
    setCLIApplied(false);
    if (parsed.diagnostics.some((diagnostic) => diagnostic.kind === 'error')) return;

    setForm((current) => {
      const explicitFlags = new Set(parsed.explicitFlags);
      const target = parsed.explicitFlags.has('u') ? parsed.target : current.target;
      const wordlist = parsed.explicitFlags.has('w') ? parsed.wordlist : current.wordlist;
      parsed.config.http.url = target;
      return {
        ...current,
        target,
        wordlist,
        profile: inferProfile(parsed.config.input.extensions, availableProfiles, current.profile),
        config: parsed.config,
        explicitFlags,
        akamaiTarget: hasAkamaiHeaders(parsed.config.http.headers),
      };
    });
    setCLIApplied(true);
  };

  const copyGeneratedCommand = async () => {
    try {
      await copyText(generatedCommand.command);
      notifications.show({ color: 'green', message: 'CLI command copied.' });
    } catch (error) {
      notifications.show({
        color: 'red',
        title: 'Could not copy command',
        message: error instanceof Error ? error.message : 'Clipboard access is unavailable.',
      });
    }
  };

  const submit = () => {
    if (!canSubmit) return;
    const config = cloneConfig(form.config);
    config.http.url = form.target.trim();
    config.http.headers = config.http.headers.filter((header) => header.trim().length > 0);
    if (!form.explicitFlags.has('w') || config.input.wordlists.length === 0) {
      config.input.wordlists = [form.wordlist.trim()];
    }
    onCreate({
      name: form.name.trim() || undefined,
      target: form.target.trim(),
      wordlist: form.wordlist.trim(),
      profile: form.profile,
      options: config,
      explicit_flags: [...form.explicitFlags],
      auto_start: form.autoStart,
    });
  };

  return (
    <Modal
      opened={opened}
      onClose={onClose}
      fullScreen={fullScreen}
      size="min(1180px, 95vw)"
      padding={0}
      radius="lg"
      withCloseButton={false}
      centered
      classNames={{ content: 'wizardModal', body: 'wizardModalBody' }}
      aria-label="Create a new h3fuzz session"
    >
      <Box component="form" className="wizardBody" onSubmit={(event) => { event.preventDefault(); submit(); }}>
        <Box className="wizardHeader">
          <Group justify="space-between" wrap="nowrap">
            <Box className="wizardIntro">
              <Title order={2}>{initialSession ? `Clone and edit ${initialSession.name}` : 'Configure discovery'}</Title>
              <Text c="dimmed" fz="sm" mt={2}>
                One scrollable form, with every h3fuzz flag available under Advanced options.
              </Text>
            </Box>
            <Button className="wizardCancel" variant="subtle" color="gray" type="button" onClick={onClose}>Cancel</Button>
          </Group>
        </Box>

        <ScrollArea className="wizardContent" type="auto" scrollbarSize={7}>
          <Stack className="wizardSections">
            <FormSection title="Target & wordlist" icon={<IconWorld size={17} />} onReset={() => setForm((current) => ({
              ...current,
              target: baseline.target,
              name: baseline.name,
              wordlist: baseline.wordlist,
              profile: baseline.profile,
              config: { ...current.config, input: cloneConfig(baseline.config).input },
              explicitFlags: restoreFlagMembership(current.explicitFlags, ['u', ...sectionFlags('input')]),
            }))}>
              <Grid className="wizardTargetGrid">
                <Grid.Col span={{ base: 12, md: 8 }}>
                  <TextInput
                    label="Target URL"
                    description="Use FUZZ in the URL, or place a keyword in headers, body, or another input."
                    placeholder="https://target.example/FUZZ"
                    value={form.target}
                    onChange={(event) => setForm({ ...form, target: event.currentTarget.value })}
                    error={form.target && !targetValid ? 'Enter an absolute HTTP or HTTPS URL.' : undefined}
                    leftSection={<IconWorld size={17} />}
                    size="md"
                    required
                    autoFocus
                  />
                </Grid.Col>
                <Grid.Col span={{ base: 12, md: 4 }}>
                  <TextInput
                    label="Session name"
                    description="Optional. Defaults to the target host."
                    placeholder="Production API"
                    value={form.name}
                    onChange={(event) => setForm({ ...form, name: event.currentTarget.value })}
                    size="md"
                  />
                </Grid.Col>
              </Grid>
              {targetValid && !form.target.includes('FUZZ') ? (
                <Alert
                  color={hasFuzzKeyword ? 'blue' : 'red'}
                  variant="light"
                  icon={<IconInfoCircle size={17} />}
                  title={hasFuzzKeyword
                    ? sniperMode ? 'Paired sniper template found' : 'Wordlist keyword found outside the URL'
                    : sniperMode ? 'Paired sniper template required' : 'FUZZ keyword required'}
                >
                  <Group justify="space-between" gap="sm">
                    <Text fz="sm">
                      {hasFuzzKeyword
                        ? sniperMode
                          ? 'The request contains a valid pair of § template markers.'
                          : 'The request contains at least one configured wordlist keyword.'
                        : sniperMode
                          ? 'Use one unnamed wordlist and add paired § markers around the value to replace.'
                          : 'Add FUZZ, or a configured named wordlist keyword, to the URL, a request header, or the request body before starting.'}
                    </Text>
                    {!sniperMode ? <Button
                      size="compact-sm"
                      variant="light"
                      type="button"
                      onClick={() => setForm({ ...form, target: `${form.target.replace(/\/$/, '')}/FUZZ` })}
                    >
                      Append /FUZZ
                    </Button> : null}
                  </Group>
                </Alert>
              ) : null}
              <Grid className="wizardWordlistGrid">
                <Grid.Col span={{ base: 12, md: 8 }}>
                  <TextInput
                    label="Wordlist"
                    description="Single-wordlist mode by default. The path must be readable by the h3fuzz server."
                    value={form.wordlist}
                    onChange={(event) => setPrimaryWordlist(event.currentTarget.value)}
                    leftSection={<IconFileText size={16} />}
                    required
                  />
                  <Group gap="xs" mt={6} className="wordlistInspection">
                    <Badge color={wordlistInspecting || wordlistStale ? 'gray' : allWordlistsReadable ? 'green' : validationUnavailable ? 'yellow' : 'red'} variant="soft">
                      {wordlistInspecting
                        ? `Checking ${wordlistSpecs.length} wordlist${wordlistSpecs.length === 1 ? '' : 's'}…`
                        : wordlistStale
                          ? 'Not validated'
                          : allWordlistsReadable
                            ? wordlistChecks.length === 1
                              ? `${wordlistChecks[0].inspection?.entries.toLocaleString()} entries`
                              : `${wordlistChecks.length} wordlists validated`
                            : validationUnavailable
                              ? 'Validation unavailable'
                              : 'Not readable'}
                    </Badge>
                    {estimatedRequests !== undefined ? <Text fz="xs" c="dimmed">About {estimatedRequests.display} requests after extension expansion</Text> : null}
                    {!wordlistStale ? wordlistChecks.filter((check) => check.inspection?.error || check.error).map((check, index) => (
                      <Text key={`${check.spec.raw}:${index}`} fz="xs" c="red.4">
                        {wordlistChecks.length > 1 ? `${check.spec.raw}: ` : ''}{check.inspection?.error || check.error}
                      </Text>
                    )) : null}
                  </Group>
                  {!sniperMode && !wordlistStale && wordlistKeywordActivity.inactive.length > 0 ? (
                    <Text fz="xs" c="yellow.5" mt="xs">
                      {wordlistKeywordActivity.inactive.map((spec) => spec.keyword || '(empty keyword)').join(', ')} {wordlistKeywordActivity.inactive.length === 1 ? 'is' : 'are'} not present in the request and will be ignored by h3fuzz.
                    </Text>
                  ) : null}
                  {validationUnavailable && !wordlistStale ? (
                    <Checkbox
                      mt="xs"
                      label="Continue without wordlist validation"
                      description="Only continue if the h3fuzz process can read this path. The scan will fail otherwise."
                      checked={allowUnverifiedWordlist}
                      onChange={(event) => setAllowUnverifiedWordlist(event.currentTarget.checked)}
                    />
                  ) : null}
                </Grid.Col>
                <Grid.Col span={{ base: 12, md: 4 }}>
                  <Select
                    label="Input mode"
                    description="Pitchfork is linear for one wordlist."
                    value={form.config.input.input_mode}
                    data={[
                      { value: 'pitchfork', label: 'Single / pitchfork' },
                      { value: 'clusterbomb', label: 'Clusterbomb' },
                      { value: 'sniper', label: 'Sniper (§ markers)' },
                    ]}
                    onChange={(value) => patchConfig('input', { input_mode: value ?? 'pitchfork' })}
                  />
                </Grid.Col>
              </Grid>
              <Box>
                <Text fw={600} fz="sm" mb="xs">Language profile</Text>
                <SimpleGrid className="profileGrid" cols={{ base: 1, sm: 2, md: 5 }} spacing="xs">
                  {availableProfiles.map((profile) => (
                    <Paper
                      key={profile.id}
                      component="button"
                      type="button"
                      withBorder
                      p="sm"
                      className={`profileCard${form.profile === profile.id ? ' profileCardActive' : ''}`}
                      onClick={() => selectProfile(profile.id)}
                      aria-pressed={form.profile === profile.id}
                    >
                      <Group justify="space-between" wrap="nowrap">
                        <Text fw={650} fz="sm" ta="left" lineClamp={1}>{profile.name}</Text>
                        {form.profile === profile.id ? <IconCheck size={14} /> : null}
                      </Group>
                      <Text ff="monospace" fz={9} c="dimmed" ta="left" mt={6} lineClamp={1}>
                        {profile.extensions.join(',')}
                      </Text>
                    </Paper>
                  ))}
                </SimpleGrid>
              </Box>
              {presets.length > 0 || onSavePreset ? (
                <Paper withBorder p="sm" className="wizardPresetBar">
                  <Group align="flex-end" gap="sm" wrap="wrap">
                    <Select
                      label="Named preset"
                      description="Apply a reusable scan configuration"
                      placeholder="Select a preset"
                      searchable
                      clearable
                      value={selectedPreset}
                      data={presets.map((preset) => ({ value: preset.id, label: preset.name }))}
                      onChange={applyPreset}
                      style={{ flex: '1 1 220px' }}
                    />
                    {onSavePreset ? <TextInput label="Save current settings as" placeholder="Authenticated API" value={presetName} onChange={(event) => setPresetName(event.currentTarget.value)} style={{ flex: '1 1 220px' }} /> : null}
                    {onSavePreset ? <Button type="button" variant="outline" disabled={!presetName.trim()} loading={presetSaving} onClick={savePreset}>Save preset</Button> : null}
                    {selectedPreset && onDeletePreset ? <Button type="button" variant="ghost" color="red" leftSection={<IconTrash size={14} />} disabled={presetSaving} onClick={() => { onDeletePreset(selectedPreset); setSelectedPreset(null); }}>Delete</Button> : null}
                  </Group>
                </Paper>
              ) : null}
            </FormSection>

            <FormSection title="CLI command" icon={<IconTerminal2 size={17} />} badge="safe import">
              <Text c="dimmed" fz="sm">
                Paste an h3fuzz or ffuf command to replace this form with the command's effective settings. The command is parsed in the browser and is never executed.
              </Text>
              <SimpleGrid className="wizardCliGrid" cols={{ base: 1, lg: 2 }} spacing="md">
                <Box className="wizardCliPane">
                  <Textarea
                    label="Import a CLI command"
                    description="Quoted values, repeated headers, aliases, and supported h3fuzz flags are converted into form fields."
                    placeholder={'h3fuzz -u https://target.example/FUZZ -w final.txt -D -e "php,html" -mc all -fc 404'}
                    value={cliInput}
                    minRows={5}
                    maxRows={10}
                    ff="monospace"
                    onChange={(event) => {
                      setCLIInput(event.currentTarget.value);
                      setCLIDiagnostics([]);
                      setCLIApplied(false);
                    }}
                  />
                  <Group gap="xs" mt="sm">
                    <Button
                      type="button"
                      variant="soft"
                      leftSection={<IconTerminal2 size={15} />}
                      disabled={!cliInput.trim() || options.length === 0}
                      onClick={applyCLICommand}
                    >
                      Apply command to form
                    </Button>
                    {cliInput ? (
                      <Button
                        type="button"
                        variant="ghost"
                        color="gray"
                        onClick={() => {
                          setCLIInput('');
                          setCLIDiagnostics([]);
                          setCLIApplied(false);
                        }}
                      >
                        Clear
                      </Button>
                    ) : null}
                  </Group>
                  {options.length === 0 ? (
                    <Alert color="yellow" mt="sm">
                      Command import will be available when the server flag registry has loaded.
                    </Alert>
                  ) : null}
                  {cliApplied ? (
                    <Alert color="green" mt="sm" title="Command applied">
                      Review the populated fields, then create the session when ready.
                    </Alert>
                  ) : null}
                  {cliDiagnostics.length > 0 ? (
                    <Stack gap="xs" mt="sm" aria-label="CLI import diagnostics">
                      {cliDiagnostics.map((diagnostic, index) => (
                        <Alert
                          key={`${diagnostic.kind}:${diagnostic.message}:${index}`}
                          color={diagnostic.kind === 'error' ? 'red' : 'yellow'}
                        >
                          {diagnostic.message}
                        </Alert>
                      ))}
                    </Stack>
                  ) : null}
                </Box>
                <Box className="wizardCliPane">
                  <Group justify="space-between" align="flex-end" gap="sm" mb="xs">
                    <Box>
                      <Text fw={600} fz="sm">Generated command</Text>
                      <Text c="dimmed" fz="xs">Updates live as settings change.</Text>
                    </Box>
                    <Button
                      type="button"
                      variant="outline"
                      size="sm"
                      leftSection={<IconCopy size={15} />}
                      onClick={copyGeneratedCommand}
                    >
                      Copy command
                    </Button>
                  </Group>
                  <Box
                    component="pre"
                    className="wizardCliPreview"
                    aria-label="Generated CLI command"
                    tabIndex={0}
                  >
                    {generatedCommand.command}
                  </Box>
                  {generatedCommand.diagnostics.length > 0 ? (
                    <Stack className="wizardCliGenerationNotes" gap={4} mt="xs">
                      {generatedCommand.diagnostics.map((diagnostic, index) => (
                        <Text
                          key={`${diagnostic.kind}:${diagnostic.message}:${index}`}
                          c={diagnostic.kind === 'error' ? 'red' : 'yellow'}
                          fz="xs"
                        >
                          {diagnostic.message}
                        </Text>
                      ))}
                    </Stack>
                  ) : null}
                </Box>
              </SimpleGrid>
            </FormSection>

            <FormSection title="Discovery & matching" icon={<IconAdjustmentsHorizontal size={17} />} onReset={() => {
              resetSection('general'); resetSection('input'); resetSection('matchers'); resetSection('filters');
            }}>
              <SimpleGrid className="wizardPrimaryFields" cols={{ base: 1, md: 4 }} spacing="md">
                <TextInput
                  label="Extensions (-e)"
                  description="Comma-separated suffixes"
                  value={form.config.input.extensions}
                  onChange={(event) => patchConfig('input', { extensions: event.currentTarget.value })}
                />
                <NumberInput
                  label="Threads (-t)"
                  description={form.explicitFlags.has('t')
                    ? 'Configured worker count'
                    : 'Regular HTTP baseline; HTTP/3 is sized after preflight'}
                  min={1}
                  value={form.config.general.threads}
                  onChange={(value) => patchConfig('general', { threads: Number(value) || DEFAULT_WEB_THREAD_COUNT })}
                />
                <NumberInput
                  label="Rate limit (-rate)"
                  description="0 means unlimited"
                  min={0}
                  value={form.config.general.rate}
                  onChange={(value) => patchConfig('general', { rate: Number(value) || 0 })}
                />
                <TextInput
                  label="Request delay (-p)"
                  description="Seconds or a range such as 0.1-2.0"
                  value={form.config.general.delay}
                  onChange={(event) => patchConfig('general', { delay: event.currentTarget.value })}
                />
              </SimpleGrid>

              <SimpleGrid className="wizardConditionGrid" cols={{ base: 1, lg: 2 }} spacing="md">
                <ConditionPanel
                  title="Matchers"
                  prefix="m"
                  value={form.config.matchers}
                  onChange={(patch) => patchConfig('matchers', patch)}
                />
                <ConditionPanel
                  title="Filters"
                  prefix="f"
                  value={form.config.filters}
                  onChange={(patch) => patchConfig('filters', patch)}
                />
              </SimpleGrid>

              <SimpleGrid className="wizardToggleGrid" cols={{ base: 1, sm: 2, lg: 4 }}>
                <ToggleCard
                  label="Automatic HTTP/3 worker sizing"
                  description="Let HTTP/3 resize workers. Off keeps the configured -t count fixed."
                  checked={!form.explicitFlags.has('t')}
                  onChange={setAutomaticWorkerSizing}
                />
                <ToggleCard
                  label="DirSearch compatibility (-D)"
                  description="Expands %EXT% entries from final.txt."
                  checked={form.config.input.dirsearch_compat}
                  onChange={(checked) => patchConfig('input', { dirsearch_compat: checked })}
                />
                <ToggleCard
                  label="Automatic calibration (-ac)"
                  description="Learn recurring baseline responses."
                  checked={form.config.general.autocalibration}
                  onChange={(checked) => patchConfig('general', { autocalibration: checked })}
                />
                <ToggleCard
                  label="Recursive discovery"
                  description="Create child jobs for directories."
                  checked={form.config.http.recursion}
                  onChange={(checked) => patchConfig('http', { recursion: checked })}
                />
                <ToggleCard
                  label="Follow redirects (-r)"
                  description="Follow HTTP redirect responses."
                  checked={form.config.http.follow_redirects}
                  onChange={(checked) => patchConfig('http', { follow_redirects: checked })}
                />
              </SimpleGrid>
            </FormSection>

            <FormSection title="Request headers" icon={<IconShieldCheck size={17} />} onReset={() => setForm((current) => ({
              ...current,
              akamaiTarget: baseline.akamaiTarget,
              config: { ...current.config, http: { ...current.config.http, headers: [...baseline.config.http.headers], data: baseline.config.http.data } },
              explicitFlags: restoreFlagMembership(current.explicitFlags, ['H', 'd']),
            }))}>
              <Paper withBorder p="md" className={form.akamaiTarget ? 'akamaiCard akamaiCardActive' : 'akamaiCard'}>
                <Switch
                  checked={form.akamaiTarget}
                  onChange={(event) => setAkamaiTarget(event.currentTarget.checked)}
                  label="Akamai target"
                  description="Apply a browser-like Chrome 136 request header set. Headers remain visible and editable below."
                />
              </Paper>
              <Textarea
                label="Headers (-H)"
                description="One Name: value header per line. Duplicate names are removed when applying the Akamai preset."
                value={form.config.http.headers.join('\n')}
                autosize
                minRows={5}
                maxRows={16}
                ff="monospace"
                onChange={(event) => {
                  const headers = event.currentTarget.value.split('\n');
                  setForm((current) => ({
                    ...current,
                    akamaiTarget: hasAkamaiHeaders(headers),
                    config: { ...current.config, http: { ...current.config.http, headers } },
                    explicitFlags: addExplicitFlags(current.explicitFlags, 'H'),
                  }));
                }}
              />
              <Textarea
                label="Request body (-d)"
                description="Optional request data. A literal FUZZ here can be used instead of a URL keyword."
                placeholder="username=admin&password=FUZZ"
                value={form.config.http.data}
                autosize
                minRows={3}
                maxRows={12}
                ff="monospace"
                onChange={(event) => patchConfig('http', { data: event.currentTarget.value })}
              />
            </FormSection>

            <FormSection title="Protocol selection & HTTP/3 acceleration" icon={<IconBolt size={17} />} badge="h3fuzz" onReset={() => resetSection('http')}>
              <Box>
                <Group gap="xs" mb="xs"><Text fw={600} fz="sm">Protocol mode</Text><Badge size="xs" color={protocolMode(form.config.http) === 'auto' ? 'cyan' : 'violet'} variant="soft">{protocolMode(form.config.http) === 'auto' ? 'Automatically selected' : 'Configured'}</Badge></Group>
                <SegmentedControl
                  fullWidth
                  value={protocolMode(form.config.http)}
                  data={[
                    { label: 'Automatic', value: 'auto' },
                    { label: 'Force HTTP/3', value: 'http3' },
                    { label: 'Skip HTTP/3', value: 'skip-http3' },
                    { label: 'Force HTTP/1', value: 'http1' },
                  ]}
                  onChange={(value) => setProtocolMode(value as ProtocolMode)}
                />
                <Text c="dimmed" fz="xs" mt="xs">
                  {protocolModeDescription(protocolMode(form.config.http))}
                </Text>
              </Box>
              <SimpleGrid cols={{ base: 1, sm: 2 }}>
                <ToggleCard
                  label="Automatic HEAD and Range"
                  description="Qualify reduced-response strategies before scanning."
                  checked={!form.config.http.no_response_optimization}
                  onChange={(checked) => patchConfig('http', { no_response_optimization: !checked })}
                />
                <Paper withBorder p="md">
                  <Group gap="xs" wrap="nowrap" align="flex-start">
                    <ThemeIcon variant="light" color="cyan" size={30}><IconBolt size={16} /></ThemeIcon>
                    <Box>
                      <Text fw={600} fz="sm">Automatic fingerprint</Text>
                      <Text c="dimmed" fz="xs" mt={2}>HTTPS targets are probed before the session chooses its transport.</Text>
                    </Box>
                  </Group>
                </Paper>
              </SimpleGrid>
              <SimpleGrid className="http3NumericGrid" cols={{ base: 2, md: 4 }}>
                <NumberInput
                  label="Fixed connections"
                  description="0 adapts"
                  min={0}
                  max={256}
                  value={form.config.http.http3_connections}
                  onChange={(value) => patchConfig('http', { http3_connections: Number(value) || 0 })}
                />
                <NumberInput
                  label="Maximum connections"
                  description="Adaptive pool ceiling"
                  min={1}
                  max={256}
                  value={form.config.http.http3_max_connections}
                  onChange={(value) => patchConfig('http', { http3_max_connections: Number(value) || 64 })}
                />
                <NumberInput
                  label="Maximum workers"
                  description="Adaptive worker ceiling"
                  min={1}
                  max={65536}
                  value={form.config.http.http3_max_workers}
                  onChange={(value) => patchConfig('http', { http3_max_workers: Number(value) || 65536 })}
                />
                <NumberInput
                  label="Streams / connection"
                  description="Per QUIC connection"
                  min={1}
                  max={4096}
                  value={form.config.http.http3_streams}
                  onChange={(value) => patchConfig('http', { http3_streams: Number(value) || 256 })}
                />
                <NumberInput
                  label="Probe timeout"
                  description="Fingerprint deadline"
                  suffix=" seconds"
                  min={1}
                  max={60}
                  value={form.config.http.http3_probe_timeout}
                  onChange={(value) => patchConfig('http', { http3_probe_timeout: Number(value) || 3 })}
                />
              </SimpleGrid>
              <Http3CapacitySummary
                config={form.config}
                automaticWorkers={!form.explicitFlags.has('t')}
              />
            </FormSection>

            <Accordion variant="separated" radius="md" className="advancedAccordion">
              <Accordion.Item value="advanced">
                <Accordion.Control icon={<IconAdjustmentsHorizontal size={17} />}>
                  <Group gap="xs">
                    <Text fw={650}>Advanced options</Text>
                    <Badge size="xs" color="gray" variant="light">{filteredOptions.length} flags</Badge>
                  </Group>
                </Accordion.Control>
                <Accordion.Panel>
                  <Stack gap="lg">
                    <Text c="dimmed" fz="sm">
                      Generated from this binary's flag registry. Session-incompatible flags stay visible with an explanation.
                    </Text>
                    <Group align="flex-end">
                      <TextInput
                        label="Find an option"
                        placeholder="Search flags and descriptions"
                        value={advancedSearch}
                        onChange={(event) => setAdvancedSearch(event.currentTarget.value)}
                        style={{ flex: 1 }}
                      />
                      <Checkbox
                        label="Compatibility flags"
                        checked={showCompatibility}
                        onChange={(event) => setShowCompatibility(event.currentTarget.checked)}
                        mb={8}
                      />
                      <Checkbox
                        label="Changed options only"
                        checked={changedOnly}
                        onChange={(event) => setChangedOnly(event.currentTarget.checked)}
                        mb={8}
                      />
                      <Button type="button" variant="ghost" color="gray" size="sm" leftSection={<IconRefresh size={14} />} onClick={() => setForm((current) => ({ ...current, config: cloneConfig(baseline.config), explicitFlags: new Set(baseline.explicitFlags), akamaiTarget: baseline.akamaiTarget }))}>Reset all options</Button>
                    </Group>
                    {options.length === 0 ? (
                      <Alert color="yellow">The server's option registry could not be loaded.</Alert>
                    ) : null}
                    {[...optionGroups.entries()].map(([section, fields]) => (
                      <Box key={section}>
                        <Group gap="xs" mb="sm">
                          <Text fw={700} tt="capitalize">{section}</Text>
                          <Badge size="xs" color="gray" variant="light">{fields.length}</Badge>
                        </Group>
                        <SimpleGrid cols={{ base: 1, md: 2 }} spacing="md">
                          {fields.map((option) => (
                            <OptionField
                              key={`${option.name}:${option.json_path}`}
                              option={option}
                              config={form.config}
                              baselineConfig={baseline.config}
                              onChange={(next) => setConfig(next, option.name)}
                              onReset={() => resetOption(option)}
                            />
                          ))}
                        </SimpleGrid>
                        <Divider my="xl" />
                      </Box>
                    ))}
                  </Stack>
                </Accordion.Panel>
              </Accordion.Item>
            </Accordion>

            <Paper withBorder p="md" className="wizardStartCard">
              <Switch
                checked={form.autoStart}
                onChange={(event) => setForm({ ...form, autoStart: event.currentTarget.checked })}
                label="Start immediately"
                description="When disabled, this persisted session opens as Idle and can be started later."
              />
            </Paper>
          </Stack>
        </ScrollArea>

        <Box className="wizardFooter">
          <Group justify="space-between">
            <Text c="dimmed" fz="xs">
              {wordlistStale || wordlistInspecting
                ? 'Validating the wordlist with the h3fuzz server'
                : !wordlistReady
                ? 'Choose wordlists readable by the h3fuzz server'
                : !hasFuzzKeyword
                ? sniperMode
                  ? 'Add one paired § template to enable session creation'
                  : 'Add a FUZZ keyword to enable session creation'
                : estimatedRequests !== undefined
                  ? `${estimatedRequests.display} estimated requests`
                : form.config.input.input_mode === 'pitchfork'
                  ? 'Single-wordlist linear mode'
                  : form.config.input.input_mode}
            </Text>
            <Group gap="xs">
              <Button variant="default" type="button" onClick={onClose}>Cancel</Button>
              <Button
                type="submit"
                leftSection={<IconRocket size={16} />}
                disabled={!canSubmit}
                loading={submitting}
              >
                {form.autoStart ? 'Create & run' : 'Create session'}
              </Button>
            </Group>
          </Group>
        </Box>
      </Box>
    </Modal>
  );
}

function FormSection({
  title,
  icon,
  badge,
  onReset,
  children,
}: {
  title: string;
  icon: React.ReactNode;
  badge?: string;
  onReset?: () => void;
  children: React.ReactNode;
}) {
  return (
    <Paper withBorder p="md" className="formSection">
      <Group className="formSectionHeader" gap="xs">
        <ThemeIcon variant="light" size={30}>{icon}</ThemeIcon>
        <Title order={3}>{title}</Title>
        {badge ? <Badge variant="light" color="cyan">{badge}</Badge> : null}
        {onReset ? <Button className="formSectionReset" type="button" variant="ghost" color="gray" size="compact-sm" leftSection={<IconRefresh size={13} />} onClick={onReset}>Reset section</Button> : null}
      </Group>
      <Stack className="formSectionBody" gap="md">{children}</Stack>
    </Paper>
  );
}

function ToggleCard({
  label,
  description,
  checked,
  onChange,
}: {
  label: string;
  description: string;
  checked: boolean;
  onChange: (checked: boolean) => void;
}) {
  return (
    <Paper withBorder p="md" className="toggleCard">
      <Switch
        checked={checked}
        onChange={(event) => onChange(event.currentTarget.checked)}
        label={label}
        description={description}
      />
    </Paper>
  );
}

function ConditionPanel({
  title,
  prefix,
  value,
  onChange,
}: {
  title: string;
  prefix: 'm' | 'f';
  value: ConfigOptions['matchers'];
  onChange: (patch: Partial<ConfigOptions['matchers']>) => void;
}) {
  return (
    <Paper withBorder p="md" className="conditionPanel">
      <Group className="conditionPanelHeader" justify="space-between" mb="sm">
        <Text fw={700}>{title}</Text>
        <Select
          aria-label={`${title} set operator`}
          value={value.mode}
          data={['or', 'and']}
          onChange={(mode) => onChange({ mode: mode ?? 'or' })}
          w={92}
          size="xs"
        />
      </Group>
      <SimpleGrid cols={{ base: 1, sm: 2 }} spacing="sm">
        <TextInput
          label={`Status (-${prefix}c)`}
          placeholder={prefix === 'm' ? 'all' : '404'}
          value={value.status}
          onChange={(event) => onChange({ status: event.currentTarget.value })}
        />
        <TextInput
          label={`Size (-${prefix}s)`}
          placeholder="123,200-299"
          value={value.size}
          onChange={(event) => onChange({ size: event.currentTarget.value })}
        />
        <TextInput
          label={`Words (-${prefix}w)`}
          placeholder="42,100-200"
          value={value.words}
          onChange={(event) => onChange({ words: event.currentTarget.value })}
        />
        <TextInput
          label={`Lines (-${prefix}l)`}
          placeholder="10,20-30"
          value={value.lines}
          onChange={(event) => onChange({ lines: event.currentTarget.value })}
        />
        <TextInput
          label={`Time (-${prefix}t)`}
          placeholder=">100 or <100"
          value={value.time}
          onChange={(event) => onChange({ time: event.currentTarget.value })}
        />
        <TextInput
          label={`Regexp (-${prefix}r)`}
          placeholder="pattern"
          value={value.regexp}
          onChange={(event) => onChange({ regexp: event.currentTarget.value })}
        />
      </SimpleGrid>
    </Paper>
  );
}
