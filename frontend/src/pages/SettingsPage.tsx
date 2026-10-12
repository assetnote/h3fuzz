import {
  Alert,
  Badge,
  Box,
  Button,
  Group,
  NumberInput,
  Paper,
  Radio,
  SegmentedControl,
  SimpleGrid,
  Stack,
  Switch,
  Tabs,
  Text,
  TextInput,
  Title,
  useMediaQuery,
} from '../components/ui';
import { IconAdjustments, IconBolt, IconDeviceFloppy, IconLanguage, IconPlus, IconSettings, IconTrash } from '../components/ui/icons';
import { useEffect, useMemo, useRef, useState } from 'react';
import type { ConfigOptions, OptionMetadata, Preset, Profile, ProfileID, Settings } from '../types/api';
import {
  bundledProfiles,
  cloneConfig,
  defaultConfig,
  DEFAULT_WEB_EXPLICIT_FLAGS,
  DEFAULT_WEB_THREAD_COUNT,
} from '../utils/config';
import { addExplicitFlags, canonicalPatchFlag } from '../utils/explicitFlags';
import {
  protocolExplicitFlags,
  protocolFlagNames,
  protocolMode,
  protocolModeDescription,
  protocolModePatch,
  type ProtocolMode,
} from '../utils/protocolMode';
import { OptionField } from '../components/OptionField';
import { Http3CapacitySummary } from '../components/Http3CapacitySummary';

interface SettingsPageProps {
  settings?: Settings;
  profiles?: Profile[];
  options?: OptionMetadata[];
  presets?: Preset[];
  saving?: boolean;
  presetSaving?: boolean;
  onSave: (settings: Settings) => void;
  onCreatePreset?: (preset: Omit<Preset, 'id' | 'created_at' | 'updated_at'>) => void;
  onUpdatePreset?: (preset: Preset) => void;
  onDeletePreset?: (id: string) => void;
}

export function SettingsPage({ settings, profiles, options = [], presets = [], saving, presetSaving, onSave, onCreatePreset, onUpdatePreset, onDeletePreset }: SettingsPageProps) {
  const compactTabs = useMediaQuery('(max-width: 74em)');
  const scrollRef = useRef<HTMLElement>(null);
  const availableProfiles = profiles?.length ? profiles : bundledProfiles;
  const [draft, setDraft] = useState<Settings>(() =>
    settings ?? {
      defaults: {
        wordlist: 'final.txt',
        profile: 'dir',
        options: cloneConfig(defaultConfig),
        explicit_flags: [...DEFAULT_WEB_EXPLICIT_FLAGS],
      },
    },
  );
  const [presetName, setPresetName] = useState('');
  const [presetDescription, setPresetDescription] = useState('');

  useEffect(() => {
    if (settings) setDraft({ ...settings, defaults: { ...settings.defaults, options: cloneConfig(settings.defaults.options) } });
  }, [settings]);

  const groupedOptions = useMemo(() => {
    const groups = new Map<string, OptionMetadata[]>();
    const seenPaths = new Set<string>();
    [...options]
      .sort((left, right) => Number(right.type === 'object_array') - Number(left.type === 'object_array'))
      // The protocol selector is the sole editor for mutually exclusive modes.
      // Duplicating its raw booleans here permits invalid combinations that the
      // selector cannot faithfully display.
      .filter((option) => !(protocolFlagNames as readonly string[]).includes(option.name))
      .filter((option) => !option.hidden)
      .filter((option) => {
        const pathKey = option.json_path || `$flag:${option.name}`;
        if (seenPaths.has(pathKey)) return false;
        seenPaths.add(pathKey);
        return true;
      })
      .forEach((option) => groups.set(option.section, [...(groups.get(option.section) ?? []), option]));
    return groups;
  }, [options]);

  const setConfig = (next: ConfigOptions, flagName?: string) =>
    setDraft((current) => ({
      ...current,
      defaults: {
        ...current.defaults,
        options: next,
        explicit_flags: flagName
          ? [...addExplicitFlags(current.defaults.explicit_flags ?? [], flagName)]
          : current.defaults.explicit_flags,
      },
    }));
  const patchConfig = <S extends keyof ConfigOptions>(section: S, patch: Partial<ConfigOptions[S]>) => {
    const key = Object.keys(patch)[0];
    setConfig({
      ...draft.defaults.options,
      [section]: { ...draft.defaults.options[section], ...patch },
    }, canonicalPatchFlag(section, key));
  };
  const setProtocolMode = (mode: ProtocolMode) => {
    setDraft((current) => ({
      ...current,
      defaults: {
        ...current.defaults,
        options: {
          ...current.defaults.options,
          http: { ...current.defaults.options.http, ...protocolModePatch(mode) },
        },
        explicit_flags: [...protocolExplicitFlags(current.defaults.explicit_flags ?? [], mode)],
      },
    }));
  };
  const setAutomaticWorkerSizing = (enabled: boolean) => {
    setDraft((current) => {
      const explicitFlags = new Set(current.defaults.explicit_flags ?? []);
      if (enabled) explicitFlags.delete('t');
      else explicitFlags.add('t');
      return {
        ...current,
        defaults: {
          ...current.defaults,
          explicit_flags: [...explicitFlags],
          options: {
            ...current.defaults.options,
            general: {
              ...current.defaults.options.general,
              threads: current.defaults.options.general.threads > 0
                ? current.defaults.options.general.threads
                : DEFAULT_WEB_THREAD_COUNT,
            },
          },
        },
      };
    });
  };

  return (
    <Box className="settingsPage">
      <Box className="settingsHeader">
        <Group justify="space-between" align="flex-start">
          <Box>
            <Group gap="xs">
              <IconSettings size={20} />
              <Title order={2}>Defaults & settings</Title>
            </Group>
            <Text c="dimmed" mt={5}>
              New sessions inherit these values. Existing sessions remain unchanged.
            </Text>
          </Box>
          <Button
            leftSection={<IconDeviceFloppy size={16} />}
            loading={saving}
            onClick={() => onSave(draft)}
          >
            Save defaults
          </Button>
        </Group>
      </Box>

      <Box className="settingsScroll" ref={scrollRef}>
        <Tabs
          defaultValue="discovery"
          keepMounted={false}
          orientation={compactTabs ? 'horizontal' : 'vertical'}
          variant="pills"
          className="settingsTabs"
          onChange={() => {
            if (scrollRef.current) scrollRef.current.scrollTop = 0;
          }}
        >
          <Tabs.List>
            <Tabs.Tab value="discovery" leftSection={<IconLanguage size={16} />}>
              Discovery
            </Tabs.Tab>
            <Tabs.Tab value="http3" leftSection={<IconBolt size={16} />}>
              Protocols
            </Tabs.Tab>
            <Tabs.Tab value="advanced" leftSection={<IconAdjustments size={16} />}>
              All options
            </Tabs.Tab>
            <Tabs.Tab value="presets" leftSection={<IconDeviceFloppy size={16} />}>
              Presets
            </Tabs.Tab>
          </Tabs.List>

          <Tabs.Panel value="discovery">
            <SettingsSection title="New session defaults" description="Applied when the new session form opens.">
              <TextInput
                label="Default wordlist"
                value={draft.defaults.wordlist}
                onChange={(event) =>
                  setDraft({ ...draft, defaults: { ...draft.defaults, wordlist: event.currentTarget.value } })
                }
              />
              <Radio.Group
                label="Default language profile"
                value={draft.defaults.profile}
                onChange={(value) =>
                  setDraft({ ...draft, defaults: { ...draft.defaults, profile: value as ProfileID } })
                }
              >
                <SimpleGrid className="settingsProfileGrid" mt="xs">
                  {availableProfiles.map((profile) => (
                    <Paper key={profile.id} withBorder p="sm">
                      <Radio value={profile.id} label={profile.name} description={profile.extensions.join(', ')} />
                    </Paper>
                  ))}
                </SimpleGrid>
              </Radio.Group>
              <SimpleGrid className="settingsDefaultsGrid">
                <TextInput
                  label="Match status"
                  description="Default -mc value"
                  value={draft.defaults.options.matchers.status}
                  onChange={(event) => patchConfig('matchers', { status: event.currentTarget.value })}
                />
                <TextInput
                  label="Filter status"
                  description="Default -fc value"
                  value={draft.defaults.options.filters.status}
                  onChange={(event) => patchConfig('filters', { status: event.currentTarget.value })}
                />
                <NumberInput
                  label="Threads (-t)"
                  description="Regular HTTP baseline; fixed HTTP/3 count when automatic sizing is off"
                  min={1}
                  value={draft.defaults.options.general.threads}
                  onChange={(value) => patchConfig('general', { threads: Number(value) || DEFAULT_WEB_THREAD_COUNT })}
                />
                <Paper withBorder p="sm" className="settingsToggleCard">
                  <Switch
                    label="Automatic HTTP/3 worker sizing"
                    description="Allow HTTP/3 preflight to override this baseline for new sessions."
                    checked={!draft.defaults.explicit_flags?.includes('t')}
                    onChange={(event) => setAutomaticWorkerSizing(event.currentTarget.checked)}
                  />
                </Paper>
              </SimpleGrid>
              <Paper withBorder p="md">
                <Switch
                  label="DirSearch compatibility"
                  description="Required for %EXT% expansion in final.txt."
                  checked={draft.defaults.options.input.dirsearch_compat}
                  onChange={(event) => patchConfig('input', { dirsearch_compat: event.currentTarget.checked })}
                />
              </Paper>
            </SettingsSection>
          </Tabs.Panel>

          <Tabs.Panel value="http3">
            <SettingsSection
              title="Protocol acceleration"
              description="Automatic fingerprinting is the recommended default for HTTPS targets."
            >
              <Box>
                <Text fw={600} fz="sm" mb="xs">
                  Default protocol mode
                </Text>
                <SegmentedControl
                  fullWidth
                  value={protocolMode(draft.defaults.options.http)}
                  data={[
                    { label: 'Automatic', value: 'auto' },
                    { label: 'Force HTTP/3', value: 'http3' },
                    { label: 'Skip HTTP/3', value: 'skip-http3' },
                    { label: 'Force HTTP/1', value: 'http1' },
                  ]}
                  onChange={(value) => setProtocolMode(value as ProtocolMode)}
                />
                <Text c="dimmed" fz="xs" mt="xs">
                  {protocolModeDescription(protocolMode(draft.defaults.options.http))}
                </Text>
              </Box>
              <Paper withBorder p="md">
                <Switch
                  label="Automatic HEAD and Range optimization"
                  description="Qualify bandwidth-saving request methods before each scan."
                  checked={!draft.defaults.options.http.no_response_optimization}
                  onChange={(event) =>
                    patchConfig('http', { no_response_optimization: !event.currentTarget.checked })
                  }
                />
              </Paper>
              <SimpleGrid className="settingsProtocolGrid">
                <NumberInput
                  label="Max connections"
                  min={1}
                  max={256}
                  value={draft.defaults.options.http.http3_max_connections}
                  onChange={(value) => patchConfig('http', { http3_max_connections: Number(value) || 64 })}
                />
                <NumberInput
                  label="Max adaptive workers"
                  description="Upper bound for automatic sizing"
                  min={1}
                  max={65536}
                  value={draft.defaults.options.http.http3_max_workers}
                  onChange={(value) => patchConfig('http', { http3_max_workers: Number(value) || 65536 })}
                />
                <NumberInput
                  label="Streams per connection"
                  min={1}
                  max={4096}
                  value={draft.defaults.options.http.http3_streams}
                  onChange={(value) => patchConfig('http', { http3_streams: Number(value) || 256 })}
                />
                <NumberInput
                  label="Probe timeout"
                  min={1}
                  max={60}
                  suffix=" seconds"
                  value={draft.defaults.options.http.http3_probe_timeout}
                  onChange={(value) => patchConfig('http', { http3_probe_timeout: Number(value) || 3 })}
                />
              </SimpleGrid>
              <Http3CapacitySummary
                config={draft.defaults.options}
                automaticWorkers={!draft.defaults.explicit_flags?.includes('t')}
              />
            </SettingsSection>
          </Tabs.Panel>

          <Tabs.Panel value="advanced">
            <Stack gap="xl">
              <Box>
                <Title order={3}>All inherited options</Title>
                <Text c="dimmed" mt={3}>
                  Generated from this h3fuzz binary's flag registry.
                </Text>
              </Box>
              {options.length === 0 ? (
                <Alert color="yellow">The server's option registry could not be loaded.</Alert>
              ) : null}
              {[...groupedOptions.entries()].map(([section, fields]) => (
                <Box key={section}>
                  <Group gap="xs" mb="md">
                    <Text fw={700} tt="capitalize">
                      {section}
                    </Text>
                    <Badge color="gray" variant="light" size="sm">
                      {fields.length}
                    </Badge>
                  </Group>
                  <SimpleGrid className="settingsOptionsGrid" spacing="md">
                    {fields.map((option) => (
                      <OptionField
                        key={`${option.name}:${option.json_path}`}
                        option={option}
                        config={draft.defaults.options}
                        onChange={setConfig}
                      />
                    ))}
                  </SimpleGrid>
                </Box>
              ))}
            </Stack>
          </Tabs.Panel>

          <Tabs.Panel value="presets">
            <SettingsSection title="Named presets" description="Save and reuse wordlists, headers, extensions, matching, filters, and protocol settings.">
              <Paper withBorder p="md">
                <Group align="flex-end" gap="sm" wrap="wrap">
                  <TextInput label="Preset name" placeholder="Authenticated API" value={presetName} onChange={(event) => setPresetName(event.currentTarget.value)} style={{ flex: '1 1 220px' }} />
                  <TextInput label="Description" placeholder="Optional context for the team" value={presetDescription} onChange={(event) => setPresetDescription(event.currentTarget.value)} style={{ flex: '2 1 320px' }} />
                  <Button
                    leftSection={<IconPlus size={15} />}
                    disabled={!presetName.trim() || !onCreatePreset}
                    loading={presetSaving}
                    onClick={() => {
                      onCreatePreset?.({
                        name: presetName.trim(),
                        description: presetDescription.trim(),
                        wordlist: draft.defaults.wordlist,
                        profile: draft.defaults.profile,
                        options: cloneConfig(draft.defaults.options),
                        explicit_flags: [...draft.defaults.explicit_flags],
                      });
                      setPresetName('');
                      setPresetDescription('');
                    }}
                  >
                    Save current defaults
                  </Button>
                </Group>
              </Paper>
              {presets.length === 0 ? <Alert color="gray">No custom presets yet. Bundled language profiles remain available in every scan form.</Alert> : null}
              <Stack gap="sm">
                {presets.map((preset) => (
                  <Paper key={preset.id} withBorder p="md" className="settingsPresetCard">
                    <Group justify="space-between" align="flex-start" gap="md" wrap="wrap">
                      <Box style={{ flex: '1 1 260px' }}>
                        <Group gap="xs"><Text fw={700}>{preset.name}</Text><Badge color="gray" variant="soft">{preset.profile}</Badge></Group>
                        {preset.description ? <Text c="dimmed" fz="sm" mt={3}>{preset.description}</Text> : null}
                        <Text c="dimmed" fz="xs" ff="monospace" mt="xs">{preset.wordlist} · {preset.options.input.extensions || 'no extensions'}</Text>
                      </Box>
                      <Group gap="xs">
                        <Button variant="outline" size="sm" onClick={() => setDraft((current) => ({ ...current, defaults: { wordlist: preset.wordlist, profile: preset.profile, options: cloneConfig(preset.options), explicit_flags: [...preset.explicit_flags] } }))}>Use as defaults</Button>
                        <Button variant="ghost" size="sm" disabled={!onUpdatePreset || presetSaving} onClick={() => onUpdatePreset?.({ ...preset, wordlist: draft.defaults.wordlist, profile: draft.defaults.profile, options: cloneConfig(draft.defaults.options), explicit_flags: [...draft.defaults.explicit_flags] })}>Replace contents</Button>
                        <Button variant="ghost" color="red" size="sm" leftSection={<IconTrash size={14} />} disabled={!onDeletePreset || presetSaving} onClick={() => onDeletePreset?.(preset.id)}>Delete</Button>
                      </Group>
                    </Group>
                  </Paper>
                ))}
              </Stack>
            </SettingsSection>
          </Tabs.Panel>
        </Tabs>
      </Box>
    </Box>
  );
}

function SettingsSection({
  title,
  description,
  children,
}: {
  title: string;
  description: string;
  children: React.ReactNode;
}) {
  return (
    <Stack gap="lg">
      <Box>
        <Title order={3}>{title}</Title>
        <Text c="dimmed" mt={3}>
          {description}
        </Text>
      </Box>
      {children}
    </Stack>
  );
}
