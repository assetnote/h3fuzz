import type { CSSProperties, ReactNode } from 'react';

export type Responsive<T> =
  | T
  | {
      base?: T;
      xs?: T;
      sm?: T;
      md?: T;
      lg?: T;
      xl?: T;
    };

export const RESPONSIVE_BREAKPOINTS = {
  xs: '36em',
  sm: '48em',
  md: '62em',
  lg: '75em',
  xl: '88em',
} as const;

const responsiveKeys = ['base', 'xs', 'sm', 'md', 'lg', 'xl'] as const;
type ResponsiveObject<T> = {
  base?: T;
  xs?: T;
  sm?: T;
  md?: T;
  lg?: T;
  xl?: T;
};

export type CompatColor =
  | 'gray'
  | 'violet'
  | 'blue'
  | 'cyan'
  | 'green'
  | 'yellow'
  | 'orange'
  | 'red'
  | string;

export interface CompatStyleProps {
  className?: string;
  style?: CSSProperties;
  p?: Responsive<string | number>;
  px?: Responsive<string | number>;
  py?: Responsive<string | number>;
  pt?: Responsive<string | number>;
  pr?: Responsive<string | number>;
  pb?: Responsive<string | number>;
  pl?: Responsive<string | number>;
  m?: Responsive<string | number>;
  mx?: Responsive<string | number>;
  my?: Responsive<string | number>;
  mt?: Responsive<string | number>;
  mr?: Responsive<string | number>;
  mb?: Responsive<string | number>;
  ml?: Responsive<string | number>;
  w?: Responsive<string | number>;
  h?: Responsive<string | number>;
  maw?: Responsive<string | number>;
  mih?: Responsive<string | number>;
}

export interface WithChildren {
  children?: ReactNode;
}

const spacing: Record<string, string> = {
  0: '0px',
  xs: 'var(--space-1, 4px)',
  sm: 'var(--space-2, 8px)',
  md: 'var(--space-3, 12px)',
  lg: 'var(--space-4, 16px)',
  xl: 'var(--space-5, 24px)',
};

export function cx(...values: Array<string | false | null | undefined>) {
  return values.filter(Boolean).join(' ');
}

export function toLength(value: string | number | undefined): string | number | undefined {
  if (value === undefined) return undefined;
  if (typeof value === 'number') return value;
  return spacing[value] ?? value;
}

function isResponsiveObject<T>(value: Responsive<T> | undefined): value is ResponsiveObject<T> {
  return value !== undefined && value !== null && typeof value === 'object';
}

export function responsiveInitial<T>(value: Responsive<T> | undefined): T | undefined {
  return isResponsiveObject(value) ? value.base : value as T | undefined;
}

export function toCSSLength(value: string | number): string {
  if (typeof value === 'number') return `${value}px`;
  return spacing[value] ?? value;
}

export function responsiveCSSVariables<T extends string | number>(
  prefix: string,
  value: Responsive<T> | undefined,
  transform: (entry: T) => string | number = (entry) => entry,
): CSSProperties {
  if (value === undefined) return {};

  const variables: Record<string, string | number> = {};
  if (!isResponsiveObject(value)) {
    variables[`--${prefix}-base`] = transform(value as T);
    return variables as CSSProperties;
  }

  for (const key of responsiveKeys) {
    const entry = value[key];
    if (entry !== undefined) variables[`--${prefix}-${key}`] = transform(entry);
  }
  return variables as CSSProperties;
}

function responsiveLengthStyle(
  cssProperty: keyof CSSProperties,
  variable: string,
  value: Responsive<string | number> | undefined,
): CSSProperties {
  if (value === undefined) return {};
  if (!isResponsiveObject(value)) return { [cssProperty]: toLength(value) } as CSSProperties;

  const variables = responsiveCSSVariables(`ui-${variable}`, value, toCSSLength);
  if (Object.keys(variables).length === 0) return {};
  return {
    [cssProperty]: `var(--ui-${variable}-active)`,
    ...variables,
  } as CSSProperties;
}

export function compatStyle(props: CompatStyleProps): CSSProperties {
  return {
    ...responsiveLengthStyle('padding', 'p', props.p),
    ...responsiveLengthStyle('paddingInline', 'px', props.px),
    ...responsiveLengthStyle('paddingBlock', 'py', props.py),
    ...responsiveLengthStyle('paddingTop', 'pt', props.pt),
    ...responsiveLengthStyle('paddingRight', 'pr', props.pr),
    ...responsiveLengthStyle('paddingBottom', 'pb', props.pb),
    ...responsiveLengthStyle('paddingLeft', 'pl', props.pl),
    ...responsiveLengthStyle('margin', 'm', props.m),
    ...responsiveLengthStyle('marginInline', 'mx', props.mx),
    ...responsiveLengthStyle('marginBlock', 'my', props.my),
    ...responsiveLengthStyle('marginTop', 'mt', props.mt),
    ...responsiveLengthStyle('marginRight', 'mr', props.mr),
    ...responsiveLengthStyle('marginBottom', 'mb', props.mb),
    ...responsiveLengthStyle('marginLeft', 'ml', props.ml),
    ...responsiveLengthStyle('width', 'w', props.w),
    ...responsiveLengthStyle('height', 'h', props.h),
    ...responsiveLengthStyle('maxWidth', 'maw', props.maw),
    ...responsiveLengthStyle('minHeight', 'mih', props.mih),
    ...props.style,
  };
}

export function dataColor(color: CompatColor | undefined) {
  if (!color) return undefined;
  const [name] = color.split('.');
  return name === 'yellow' ? 'amber' : name;
}

export function fieldDescriptionID(id: string | undefined, suffix: string) {
  return id ? `${id}-${suffix}` : undefined;
}
