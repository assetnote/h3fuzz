import {
  Alert,
  Badge,
  Button,
  Collapse,
  Group,
  Paper,
  SimpleGrid,
  Stack,
  Text,
  TextInput,
} from './ui';
import { IconFilter, IconInfoCircle, IconPlus, IconX } from './ui/icons';
import { type FormEvent, useEffect, useMemo, useState } from 'react';
import type { Session, SessionFilterPatch } from '../types/api';

interface SessionFiltersProps {
  session: Session;
  saving?: boolean;
  onAdd: (filters: SessionFilterPatch) => Promise<void>;
}

type FilterKey = Exclude<keyof SessionFilterPatch, 'mode'>;

const FILTER_FIELDS: Array<{
  key: FilterKey;
  flag: string;
  label: string;
  placeholder: string;
  description: string;
}> = [
  {
    key: 'status',
    flag: '-fc',
    label: 'Status codes',
    placeholder: '404,400-499',
    description: 'Comma-separated codes or ranges',
  },
  {
    key: 'size',
    flag: '-fs',
    label: 'Response sizes',
    placeholder: '0,42,100-200',
    description: 'Response byte counts or ranges',
  },
  {
    key: 'words',
    flag: '-fw',
    label: 'Word counts',
    placeholder: '656',
    description: 'Response word counts or ranges',
  },
  {
    key: 'lines',
    flag: '-fl',
    label: 'Line counts',
    placeholder: '90',
    description: 'Response line counts or ranges',
  },
  {
    key: 'time',
    flag: '-ft',
    label: 'Response time',
    placeholder: '>100 or <100',
    description: 'Milliseconds, using > or <',
  },
  {
    key: 'regexp',
    flag: '-fr',
    label: 'Regular expression',
    placeholder: 'Not found|Access denied',
    description: 'Response body or headers',
  },
];

const EMPTY_FILTERS: Record<FilterKey, string> = {
  status: '',
  size: '',
  words: '',
  lines: '',
  time: '',
  regexp: '',
};

export function SessionFilters({ session, saving = false, onAdd }: SessionFiltersProps) {
  const [opened, setOpened] = useState(false);
  const [values, setValues] = useState(EMPTY_FILTERS);
  const [validationError, setValidationError] = useState('');
  const existing = session.options.filters;
  const regexpAvailable = session.status === 'idle';
  const effectiveMode = existing.mode || 'or';
  const additionUnavailableReason = session.status === 'starting'
    ? 'Filters cannot be changed while the scan is starting. Wait until it is running, then try again.'
    : session.status !== 'idle' && effectiveMode === 'and'
      ? 'AND-mode filters cannot be expanded after a scan starts because earlier responses may already have been discarded.'
      : '';

  useEffect(() => {
    setOpened(false);
    setValues(EMPTY_FILTERS);
    setValidationError('');
  }, [session.id]);

  const active = useMemo(
    () => FILTER_FIELDS.flatMap(({ key, flag }) => {
      const value = existing[key]?.trim();
      return value ? [{ key, flag, value }] : [];
    }),
    [existing],
  );

  const submit = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    if (additionUnavailableReason) {
      setValidationError(additionUnavailableReason);
      return;
    }
    const additions = Object.fromEntries(
      FILTER_FIELDS.map(({ key }) => [key, values[key].trim()]).filter(([, value]) => Boolean(value)),
    ) as SessionFilterPatch;
    if (!Object.keys(additions).length) {
      setValidationError('Enter at least one filter value.');
      return;
    }
    setValidationError('');
    try {
      await onAdd(additions);
      setValues(EMPTY_FILTERS);
      setOpened(false);
    } catch {
      // The parent mutation presents the server's validation message.
    }
  };

  return (
    <Paper withBorder radius="md" p="md" mb="md" aria-label="Result filters">
      <Group justify="space-between" align="flex-start" gap="md">
        <Stack gap={6} style={{ minWidth: 0 }}>
          <Group gap="xs">
            <IconFilter size={16} />
            <Text fw={650} fz="sm">Result filters</Text>
            {active.length ? <Badge size="xs" variant="light">{active.length} active</Badge> : null}
          </Group>
          {active.length ? (
            <Group gap={6}>
              {active.map(({ key, flag, value }) => (
                <Badge key={key} variant="outline" color="violet" ff="monospace" tt="none">
                  {flag} {value}
                </Badge>
              ))}
              {existing.mode && existing.mode !== 'or' ? (
                <Badge variant="outline" color="gray" ff="monospace" tt="none">
                  -fmode {existing.mode}
                </Badge>
              ) : null}
            </Group>
          ) : (
            <Text c="dimmed" fz="xs">No response filters are active.</Text>
          )}
          {additionUnavailableReason ? (
            <Text c="yellow.4" fz="xs">{additionUnavailableReason}</Text>
          ) : null}
        </Stack>
        <Button
          size="xs"
          variant={opened ? 'default' : 'light'}
          leftSection={opened ? <IconX size={14} /> : <IconPlus size={14} />}
          disabled={Boolean(additionUnavailableReason)}
          onClick={() => {
            setOpened((current) => !current);
            setValidationError('');
          }}
        >
          {opened ? 'Close' : 'Add filters'}
        </Button>
      </Group>

      <Collapse in={opened}>
        <form onSubmit={(event) => void submit(event)}>
          <Stack gap="md" mt="md">
            <Alert color="blue" variant="light" icon={<IconInfoCircle size={16} />}>
              <Text fz="xs">
                Filters are additive. Existing findings are re-evaluated and the same rules are applied to new responses.
              </Text>
            </Alert>
            <SimpleGrid cols={{ base: 1, sm: 2, lg: 3 }} spacing="sm">
              {FILTER_FIELDS.map(({ key, flag, label, placeholder, description }) => {
                const disabled = key === 'regexp' && !regexpAvailable;
                return (
                  <TextInput
                    key={key}
                    label={`${label} (${flag})`}
                    description={disabled ? 'Only available before a scan starts because response bodies are not retained.' : description}
                    placeholder={placeholder}
                    value={values[key]}
                    disabled={disabled || saving || Boolean(additionUnavailableReason)}
                    onChange={(event) => {
                      const value = event.currentTarget.value;
                      setValues((current) => ({ ...current, [key]: value }));
                      setValidationError('');
                    }}
                  />
                );
              })}
            </SimpleGrid>
            {validationError ? <Text c="red.4" fz="xs" role="alert">{validationError}</Text> : null}
            <Group justify="flex-end">
              <Button
                type="submit"
                size="sm"
                loading={saving}
                disabled={Boolean(additionUnavailableReason)}
                leftSection={<IconFilter size={15} />}
              >
                Apply filters
              </Button>
            </Group>
          </Stack>
        </form>
      </Collapse>
    </Paper>
  );
}
