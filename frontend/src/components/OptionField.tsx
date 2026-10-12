import {
  ActionIcon,
  Box,
  Button,
  Group,
  NumberInput,
  Paper,
  Select,
  Stack,
  Switch,
  TagsInput,
  Text,
  TextInput,
  Textarea,
  Tooltip,
} from './ui';
import { IconInfoCircle, IconPlus, IconTrash } from './ui/icons';
import type { ConfigOptions, FlightConfig, FlightVariable, OptionMetadata } from '../types/api';
import { getOptionValue, normalizeOptionPath, setOptionValue } from '../utils/optionValue';

interface OptionFieldProps {
  option: OptionMetadata;
  config: ConfigOptions;
  baselineConfig?: ConfigOptions;
  onChange: (next: ConfigOptions, flagName?: string) => void;
  onReset?: () => void;
}

export function OptionField({ option, config, baselineConfig, onChange, onReset }: OptionFieldProps) {
  const value = getOptionValue(config, option.json_path) ?? option.default;
  const baselineValue = baselineConfig ? (getOptionValue(baselineConfig, option.json_path) ?? option.default) : undefined;
  const changed = baselineConfig !== undefined && JSON.stringify(value) !== JSON.stringify(baselineValue);
  const isHeaderOption = normalizeOptionPath(option.json_path).join('.') === 'http.headers';
  const flightKind = option.json_path?.endsWith('preflights')
    ? 'preflight / preflight-var / preflight-var-auto'
    : option.json_path?.endsWith('postflights')
      ? 'postflight / postflight-var / postflight-var-auto'
      : undefined;
  const label = (
    <Group gap={5} wrap="nowrap">
      <Text component="span" fz="sm" fw={500}>
        -{flightKind ?? option.name}
      </Text>
      <Tooltip label={option.usage} maw={360} multiline>
        <IconInfoCircle size={13} color="var(--gray-10)" aria-label={option.usage} />
      </Tooltip>
    </Group>
  );
  const update = (next: unknown) => onChange(setOptionValue(config, option.json_path, next), option.name);
  const representedElsewhere = option.name === 'u';
  const unavailable = option.available === false || !option.json_path || representedElsewhere;
  const wrap = (field: React.ReactNode) => (
    <Box className={`optionFieldWrap${changed ? ' optionFieldChanged' : ''}`}>
      <fieldset disabled={unavailable} className={unavailable ? 'optionUnavailable' : 'optionFieldset'}>
        {field}
        {unavailable ? (
          <Text fz={10} c="yellow.4" mt={5}>
            {option.unavailable_reason ||
              (option.name === 'u'
                ? 'Use the Target URL field above.'
                : option.name === 'b'
                ? 'Add a Cookie header in the request headers field.'
                : 'This process-level flag is unavailable in web sessions.')}
          </Text>
        ) : null}
      </fieldset>
      {changed && !unavailable ? (
        <Button
          className="optionReset"
          type="button"
          variant="ghost"
          color="gray"
          size="compact-xs"
          onClick={() => onReset ? onReset() : onChange(setOptionValue(config, option.json_path, baselineValue))}
        >
          Reset
        </Button>
      ) : null}
    </Box>
  );

  if (option.type === 'boolean') {
    return wrap(
      <Paper className="optionSwitch" p="sm" withBorder>
        <Switch
          checked={Boolean(value)}
          onChange={(event) => update(event.currentTarget.checked)}
          label={label}
          description={option.usage}
        />
      </Paper>,
    );
  }

  if (option.type === 'integer') {
    return wrap(
      <NumberInput
        label={label}
        description={option.usage}
        value={typeof value === 'number' ? value : Number(value) || 0}
        min={option.min}
        max={option.max}
        allowDecimal={false}
        onChange={(next) => update(typeof next === 'number' ? next : Number(next) || 0)}
      />,
    );
  }

  if (option.type === 'string_array' && isHeaderOption) {
    const headers = Array.isArray(value) ? value.map(String) : [];
    return wrap(
      <Textarea
        label={label}
        description={option.usage}
        value={headers.join('\n')}
        placeholder="One Name: value header per line"
        autosize
        minRows={3}
        maxRows={12}
        ff="monospace"
        onChange={(event) => update(event.currentTarget.value.split('\n'))}
      />,
    );
  }

  if (option.type === 'string_array') {
    return wrap(
      <TagsInput
        label={label}
        description={option.usage}
        value={Array.isArray(value) ? value.map(String) : []}
        placeholder={option.repeatable ? 'Type a value and press Enter' : 'Comma-separated values'}
        splitChars={[',']}
        onChange={update}
        clearable
      />,
    );
  }

  if (option.type === 'object_array') {
    return wrap(
      <FlightList
        label={label}
        description={option.usage}
        value={Array.isArray(value) ? (value as FlightConfig[]) : []}
        onChange={update}
      />,
    );
  }

  if (option.choices?.length) {
    return wrap(
      <Select
        label={label}
        description={option.usage}
        value={value === undefined ? null : String(value)}
        data={option.choices}
        onChange={(next) => update(next ?? '')}
        searchable
        clearable
      />,
    );
  }

  const multiline = ['data', 'H', 'acc', 'preflight', 'postflight'].includes(option.name);
  const Component = multiline ? Textarea : TextInput;
  return wrap(
    <Component
      label={label}
      description={option.usage}
      value={value === undefined || value === null ? '' : String(value)}
      onChange={(event) => update(event.currentTarget.value)}
      autosize={multiline || undefined}
      minRows={multiline ? 2 : undefined}
    />,
  );
}

function FlightList({
  label,
  description,
  value,
  onChange,
}: {
  label: React.ReactNode;
  description: string;
  value: FlightConfig[];
  onChange: (value: FlightConfig[]) => void;
}) {
  const updateAt = (index: number, patch: Partial<FlightConfig>) => {
    const next = value.map((entry, entryIndex) => (entryIndex === index ? { ...entry, ...patch } : entry));
    onChange(next);
  };
  const updateVariable = (flightIndex: number, variableIndex: number, patch: Partial<FlightVariable>) => {
    const variables = value[flightIndex].vars.map((variable, index) =>
      index === variableIndex ? { ...variable, ...patch } : variable,
    );
    updateAt(flightIndex, { vars: variables });
  };
  return (
    <Stack gap="xs">
      <Box>
        {label}
        <Text fz="xs" c="dimmed">
          {description}
        </Text>
      </Box>
      {value.map((entry, index) => (
        <Paper key={index} withBorder p="sm">
          <Group align="flex-end" wrap="nowrap">
            <TextInput
              label="Raw request file"
              description="Path readable by the h3fuzz server"
              value={entry.request_file}
              placeholder="requests/login.txt"
              onChange={(event) => updateAt(index, { request_file: event.currentTarget.value })}
              style={{ flex: 1 }}
            />
            <ActionIcon
              color="red"
              variant="subtle"
              mb={4}
              onClick={() => onChange(value.filter((_, entryIndex) => entryIndex !== index))}
              aria-label="Remove flight request"
            >
              <IconTrash size={16} />
            </ActionIcon>
          </Group>
          <Stack gap="xs" mt="sm">
            <Group justify="space-between">
              <Text fz="xs" fw={600}>Response variables</Text>
              <Button
                size="compact-xs"
                variant="subtle"
                leftSection={<IconPlus size={13} />}
                onClick={() => updateAt(index, { vars: [...entry.vars, { name: '', regex: '' }] })}
              >
                Add variable
              </Button>
            </Group>
            {entry.vars.map((variable, variableIndex) => {
              const method = variable.source ? variable.source : 'regex';
              return (
                <Paper key={variableIndex} p="xs" withBorder>
                  <Group align="flex-end" wrap="nowrap">
                    <TextInput
                      label="Name"
                      placeholder="CSRF"
                      value={variable.name}
                      onChange={(event) => updateVariable(index, variableIndex, { name: event.currentTarget.value })}
                      w={130}
                    />
                    <Select
                      label="Extract with"
                      value={method}
                      data={[
                        { value: 'regex', label: 'Regular expression' },
                        { value: 'auto', label: 'Auto named value' },
                        { value: 'form', label: 'Form field' },
                        { value: 'meta', label: 'Meta tag' },
                        { value: 'cookie', label: 'Cookie' },
                        { value: 'header', label: 'Header' },
                      ]}
                      onChange={(source) =>
                        updateVariable(index, variableIndex, {
                          source: source === 'regex' ? '' : (source as FlightVariable['source']),
                          regex: source === 'regex' ? variable.regex : '',
                          key: source === 'regex' ? '' : variable.key,
                        })
                      }
                      w={170}
                    />
                    <TextInput
                      label={method === 'regex' ? 'Capture regex' : 'Key'}
                      placeholder={method === 'regex' ? 'token="([^"]+)"' : 'csrf_token'}
                      value={method === 'regex' ? variable.regex : variable.key ?? ''}
                      onChange={(event) =>
                        updateVariable(
                          index,
                          variableIndex,
                          method === 'regex'
                            ? { regex: event.currentTarget.value }
                            : { key: event.currentTarget.value },
                        )
                      }
                      style={{ flex: 1 }}
                    />
                    <ActionIcon
                      color="red"
                      variant="subtle"
                      mb={4}
                      onClick={() =>
                        updateAt(index, { vars: entry.vars.filter((_, itemIndex) => itemIndex !== variableIndex) })
                      }
                      aria-label="Remove response variable"
                    >
                      <IconTrash size={15} />
                    </ActionIcon>
                  </Group>
                </Paper>
              );
            })}
          </Stack>
        </Paper>
      ))}
      <Button
        variant="light"
        color="gray"
        leftSection={<IconPlus size={15} />}
        onClick={() => onChange([...value, { request_file: '', vars: [] }])}
      >
        Add request
      </Button>
    </Stack>
  );
}
