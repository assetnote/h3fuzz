import {
  Badge,
  Box,
  Button,
  Checkbox as RadixCheckbox,
  DropdownMenu,
  Flex,
  IconButton,
  RadioGroup,
  SegmentedControl as RadixSegmentedControl,
  Select as RadixSelect,
  Switch as RadixSwitch,
  TextArea as RadixTextArea,
  TextField,
} from '@radix-ui/themes';
import { CheckIcon, ChevronDownIcon, Cross2Icon } from '@radix-ui/react-icons';
import {
  createContext,
  useContext,
  useId,
  useMemo,
  useState,
  type ChangeEvent,
  type ChangeEventHandler,
  type ComponentProps,
  type KeyboardEvent,
  type ReactNode,
} from 'react';
import { compatStyle, cx, type CompatStyleProps } from './compat';

type Option = string | { value: string; label: ReactNode; disabled?: boolean };

function options(data: readonly Option[]) {
  return data.map((item) => typeof item === 'string' ? { value: item, label: item, disabled: false } : item);
}

interface FieldShellProps extends CompatStyleProps {
  children: ReactNode;
  label?: ReactNode;
  description?: ReactNode;
  error?: ReactNode;
  required?: boolean;
  inputID: string;
}

function FieldShell({ children, label, description, error, required, inputID, className, ...props }: FieldShellProps) {
  return (
    <Box className={cx('uiField', className)} style={compatStyle(props)}>
      {label ? (
        <label className="uiFieldLabel" htmlFor={inputID}>
          {label}{required ? <span aria-hidden="true"> *</span> : null}
        </label>
      ) : null}
      {description ? <div className="uiFieldDescription" id={`${inputID}-description`}>{description}</div> : null}
      {children}
      {error ? <div className="uiFieldError" id={`${inputID}-error`} role="alert">{error}</div> : null}
    </Box>
  );
}

function describedBy(id: string, description: ReactNode, error: ReactNode) {
  return [description ? `${id}-description` : '', error ? `${id}-error` : ''].filter(Boolean).join(' ') || undefined;
}

function controlSize(value: string | undefined): ComponentProps<typeof TextField.Root>['size'] {
  if (value === 'xs' || value === 'compact-xs' || value === 'compact-sm') return '1';
  if (value === 'lg' || value === 'xl') return '3';
  return '2';
}

export interface TextInputProps extends CompatStyleProps {
  id?: string;
  label?: ReactNode;
  description?: ReactNode;
  error?: ReactNode;
  required?: boolean;
  disabled?: boolean;
  autoFocus?: boolean;
  placeholder?: string;
  value?: string | number;
  onChange?: ChangeEventHandler<HTMLInputElement>;
  leftSection?: ReactNode;
  rightSection?: ReactNode;
  size?: string;
  type?: string;
  name?: string;
  'aria-label'?: string;
}

export function TextInput({
  id: suppliedID,
  label,
  description,
  error,
  required,
  leftSection,
  rightSection,
  size,
  className,
  ...props
}: TextInputProps) {
  const generatedID = useId();
  const id = suppliedID ?? generatedID;
  const input = (
    <div className="uiControlWithSections" data-left={Boolean(leftSection)} data-right={Boolean(rightSection)}>
      {leftSection ? <span className="uiLeftSection">{leftSection}</span> : null}
      <TextField.Root
        id={id}
        value={props.value}
        onChange={props.onChange}
        placeholder={props.placeholder}
        disabled={props.disabled}
        required={required}
        autoFocus={props.autoFocus}
        type={props.type as ComponentProps<typeof TextField.Root>['type']}
        name={props.name}
        size={controlSize(size)}
        aria-label={props['aria-label']}
        aria-invalid={Boolean(error) || undefined}
        aria-describedby={describedBy(id, description, error)}
      />
      {rightSection ? <span className="uiRightSection">{rightSection}</span> : null}
    </div>
  );
  return <FieldShell inputID={id} label={label} description={description} error={error} required={required} className={className} {...props}>{input}</FieldShell>;
}

export interface TextareaProps extends CompatStyleProps {
  id?: string;
  label?: ReactNode;
  description?: ReactNode;
  placeholder?: string;
  value?: string;
  onChange?: ChangeEventHandler<HTMLTextAreaElement>;
  autosize?: boolean;
  minRows?: number;
  maxRows?: number;
  ff?: string;
}

export function Textarea({ id: suppliedID, label, description, autosize, minRows, maxRows, ff, className, ...props }: TextareaProps) {
  const generatedID = useId();
  const id = suppliedID ?? generatedID;
  return (
    <FieldShell inputID={id} label={label} description={description} className={className} {...props}>
      <RadixTextArea
        id={id}
        value={props.value}
        onChange={props.onChange}
        placeholder={props.placeholder}
        rows={minRows}
        aria-describedby={description ? `${id}-description` : undefined}
        style={{
          resize: autosize ? 'none' : undefined,
          minHeight: minRows ? `${minRows * 1.5 + 1}em` : undefined,
          maxHeight: maxRows ? `${maxRows * 1.5 + 1}em` : undefined,
          overflowY: maxRows ? 'auto' : undefined,
          fontFamily: ff === 'monospace' ? 'var(--code-font-family)' : ff,
        }}
      />
    </FieldShell>
  );
}

export interface NumberInputProps extends CompatStyleProps {
  id?: string;
  label?: ReactNode;
  description?: ReactNode;
  value?: string | number;
  onChange?: (value: string | number) => void;
  min?: number;
  max?: number;
  allowDecimal?: boolean;
  suffix?: string;
  disabled?: boolean;
}

export function NumberInput({ id: suppliedID, label, description, onChange, allowDecimal = true, suffix, className, ...props }: NumberInputProps) {
  const generatedID = useId();
  const id = suppliedID ?? generatedID;
  const handleChange: ChangeEventHandler<HTMLInputElement> = (event) => {
    const raw = event.currentTarget.value;
    if (raw === '' || raw === '-' || raw.endsWith('.')) onChange?.(raw);
    else {
      const numeric = Number(raw);
      onChange?.(Number.isFinite(numeric) ? numeric : raw);
    }
  };
  return (
    <FieldShell inputID={id} label={label} description={description} className={className} {...props}>
      <TextField.Root
        id={id}
        type="number"
        value={props.value}
        onChange={handleChange}
        min={props.min}
        max={props.max}
        step={allowDecimal ? 'any' : 1}
        disabled={props.disabled}
        aria-describedby={description ? `${id}-description` : undefined}
      >
        {suffix ? <TextField.Slot side="right">{suffix}</TextField.Slot> : null}
      </TextField.Root>
    </FieldShell>
  );
}

export interface SelectProps extends CompatStyleProps {
  id?: string;
  label?: ReactNode;
  description?: ReactNode;
  data: readonly Option[];
  value?: string | null;
  onChange?: (value: string | null) => void;
  placeholder?: string;
  clearable?: boolean;
  searchable?: boolean;
  disabled?: boolean;
  size?: string;
  'aria-label'?: string;
}

export function Select({ id: suppliedID, label, description, data, value, onChange, placeholder, clearable, size, className, ...props }: SelectProps) {
  const generatedID = useId();
  const id = suppliedID ?? generatedID;
  return (
    <FieldShell inputID={id} label={label} description={description} className={className} {...props}>
      <Flex gap="1" align="center">
        <RadixSelect.Root
          value={value ?? ''}
          onValueChange={(next) => onChange?.(next || null)}
          disabled={props.disabled}
          size={controlSize(size)}
        >
          <RadixSelect.Trigger
            id={id}
            placeholder={placeholder}
            aria-label={props['aria-label']}
            aria-describedby={description ? `${id}-description` : undefined}
            style={{ flex: 1, minWidth: 0 }}
          />
          <RadixSelect.Content position="popper">
            {options(data).map((item) => (
              <RadixSelect.Item key={item.value} value={item.value} disabled={item.disabled}>{item.label}</RadixSelect.Item>
            ))}
          </RadixSelect.Content>
        </RadixSelect.Root>
        {clearable && value ? (
          <IconButton type="button" size={controlSize(size)} variant="ghost" aria-label="Clear selection" onClick={() => onChange?.(null)}>
            <Cross2Icon />
          </IconButton>
        ) : null}
      </Flex>
    </FieldShell>
  );
}

export interface MultiSelectProps extends CompatStyleProps {
  data: readonly Option[];
  value: string[];
  onChange: (value: string[]) => void;
  placeholder?: string;
  clearable?: boolean;
  searchable?: boolean;
  disabled?: boolean;
  'aria-label'?: string;
}

export function MultiSelect({ data, value, onChange, placeholder, clearable, searchable, disabled, className, ...props }: MultiSelectProps) {
  const [query, setQuery] = useState('');
  const normalized = options(data);
  const visible = normalized.filter((item) => !query || String(item.label).toLowerCase().includes(query.toLowerCase()));
  const selectedLabels = normalized.filter((item) => value.includes(item.value)).map((item) => item.label).join(', ');
  function toggle(itemValue: string, checked: boolean) {
    onChange(checked ? [...new Set([...value, itemValue])] : value.filter((item) => item !== itemValue));
  }
  return (
    <Box className={className} style={compatStyle(props)}>
      <DropdownMenu.Root>
        <DropdownMenu.Trigger>
          <Button className="uiMultiSelectTrigger" variant="surface" disabled={disabled} aria-label={props['aria-label']}>
            <span className="uiMultiSelectValue">{selectedLabels || placeholder || 'Select'}</span>
            <ChevronDownIcon aria-hidden="true" />
          </Button>
        </DropdownMenu.Trigger>
        <DropdownMenu.Content align="start" style={{ minWidth: 'var(--radix-dropdown-menu-trigger-width)', maxHeight: 320 }}>
          {searchable ? (
            <Box p="2" onKeyDown={(event) => event.stopPropagation()}>
              <TextField.Root aria-label="Search options" placeholder="Search" value={query} onChange={(event) => setQuery(event.currentTarget.value)} />
            </Box>
          ) : null}
          {clearable && value.length ? <DropdownMenu.Item onSelect={() => onChange([])}>Clear selection</DropdownMenu.Item> : null}
          {visible.map((item) => (
            <DropdownMenu.CheckboxItem
              key={item.value}
              checked={value.includes(item.value)}
              disabled={item.disabled}
              onCheckedChange={(checked) => toggle(item.value, checked)}
            >
              {value.includes(item.value) ? <CheckIcon /> : null}
              {item.label}
            </DropdownMenu.CheckboxItem>
          ))}
        </DropdownMenu.Content>
      </DropdownMenu.Root>
    </Box>
  );
}

export interface TagsInputProps extends CompatStyleProps {
  id?: string;
  label?: ReactNode;
  description?: ReactNode;
  value: string[];
  onChange: (value: string[]) => void;
  placeholder?: string;
  splitChars?: string[];
  clearable?: boolean;
}

export function TagsInput({ id: suppliedID, label, description, value, onChange, placeholder, splitChars = [','], clearable, className, ...props }: TagsInputProps) {
  const generatedID = useId();
  const id = suppliedID ?? generatedID;
  const [draft, setDraft] = useState('');
  const separators = useMemo(() => new Set(splitChars), [splitChars]);
  const displayValues = value.filter((item) => item.trim().length > 0);
  function commit(raw: string) {
    const additions = raw.split(new RegExp(`[${splitChars.map((char) => `\\${char}`).join('')}]`)).map((item) => item.trim()).filter(Boolean);
    if (additions.length) onChange([...new Set([...value, ...additions])]);
    setDraft('');
  }
  function keyDown(event: KeyboardEvent<HTMLInputElement>) {
    if (event.key === 'Enter' || separators.has(event.key)) {
      event.preventDefault();
      commit(draft);
    } else if (event.key === 'Backspace' && !draft && value.length) {
      onChange(value.slice(0, -1));
    }
  }
  return (
    <FieldShell inputID={id} label={label} description={description} className={className} {...props}>
      <div className="uiTagsControl">
        {displayValues.map((item) => (
          <Badge className="uiTag" key={item} variant="soft">
            <span className="uiTagLabel" title={item}>{item}</span>
            <button type="button" className="uiTagRemove" aria-label={`Remove ${item}`} onClick={() => onChange(value.filter((entry) => entry !== item))}>
              <Cross2Icon width="11" height="11" />
            </button>
          </Badge>
        ))}
        <input
          id={id}
          className="uiTagsInput"
          value={draft}
          placeholder={!displayValues.length ? placeholder : undefined}
          onChange={(event) => setDraft(event.currentTarget.value)}
          onKeyDown={keyDown}
          onBlur={() => draft.trim() && commit(draft)}
          aria-describedby={description ? `${id}-description` : undefined}
        />
        {clearable && displayValues.length ? (
          <IconButton type="button" variant="ghost" size="1" aria-label="Clear all values" onClick={() => onChange([])}>
            <Cross2Icon />
          </IconButton>
        ) : null}
      </div>
    </FieldShell>
  );
}

function changeEvent(checked: boolean): ChangeEvent<HTMLInputElement> {
  return { target: { checked }, currentTarget: { checked } } as ChangeEvent<HTMLInputElement>;
}

interface CheckProps extends CompatStyleProps {
  checked?: boolean;
  onChange?: ChangeEventHandler<HTMLInputElement>;
  label?: ReactNode;
  description?: ReactNode;
  disabled?: boolean;
}

export function Checkbox({ checked, onChange, label, description, disabled, className, ...props }: CheckProps) {
  const id = useId();
  return (
    <label className={cx('uiCheckLabel', className)} style={compatStyle(props)} htmlFor={id}>
      <RadixCheckbox id={id} checked={checked} disabled={disabled} onCheckedChange={(next) => onChange?.(changeEvent(next === true))} />
      <span className="uiCheckCopy"><span>{label}</span>{description ? <span className="uiFieldDescription">{description}</span> : null}</span>
    </label>
  );
}

export function Switch({ checked, onChange, label, description, disabled, className, ...props }: CheckProps) {
  const id = useId();
  return (
    <label className={cx('uiCheckLabel', className)} style={compatStyle(props)} htmlFor={id}>
      <RadixSwitch id={id} checked={checked} disabled={disabled} onCheckedChange={(next) => onChange?.(changeEvent(next))} />
      <span className="uiCheckCopy"><span>{label}</span>{description ? <span className="uiFieldDescription">{description}</span> : null}</span>
    </label>
  );
}

interface RadioContextValue {
  value?: string;
}
const CompatRadioContext = createContext<RadioContextValue>({});

export interface RadioGroupProps extends CompatStyleProps {
  children?: ReactNode;
  value?: string;
  onChange?: (value: string) => void;
  label?: ReactNode;
}

function CompatRadioGroup({ children, value, onChange, label, className, ...props }: RadioGroupProps) {
  return (
    <div className={cx('uiField', className)} style={compatStyle(props)}>
      {label ? <span className="uiFieldLabel">{label}</span> : null}
      <RadioGroup.Root value={value} onValueChange={onChange}>
        <CompatRadioContext.Provider value={{ value }}>{children}</CompatRadioContext.Provider>
      </RadioGroup.Root>
    </div>
  );
}

export interface RadioProps {
  value: string;
  label?: ReactNode;
  description?: ReactNode;
  disabled?: boolean;
}

export function RadioItem({ value, label, description, disabled }: RadioProps) {
  useContext(CompatRadioContext);
  const id = useId();
  return (
    <label className="uiRadioLabel" htmlFor={id}>
      <RadioGroup.Item id={id} value={value} disabled={disabled} />
      <span className="uiCheckCopy"><span>{label}</span>{description ? <span className="uiFieldDescription">{description}</span> : null}</span>
    </label>
  );
}

export const Radio = Object.assign(RadioItem, { Group: CompatRadioGroup });

export interface SegmentedControlProps extends CompatStyleProps {
  data: readonly Option[];
  value?: string;
  onChange?: (value: string) => void;
  fullWidth?: boolean;
}

export function SegmentedControl({ data, value, onChange, fullWidth, className, ...props }: SegmentedControlProps) {
  return (
    <RadixSegmentedControl.Root
      className={className}
      value={value}
      onValueChange={onChange}
      style={{ width: fullWidth ? '100%' : undefined, ...compatStyle(props) }}
    >
      {options(data).map((item) => <RadixSegmentedControl.Item key={item.value} value={item.value}>{item.label}</RadixSegmentedControl.Item>)}
    </RadixSegmentedControl.Root>
  );
}
