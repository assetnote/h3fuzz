import {
  Badge as RadixBadge,
  Box as RadixBox,
  Button as RadixButton,
  Callout,
  Flex,
  Heading,
  IconButton,
  Progress as RadixProgress,
  ScrollArea as RadixScrollArea,
  Separator,
  Spinner,
  Text as RadixText,
  Tooltip as RadixTooltip,
} from '@radix-ui/themes';
import {
  createElement,
  forwardRef,
  type ComponentProps,
  type CSSProperties,
  type ElementType,
  type MouseEventHandler,
  type ReactNode,
} from 'react';
import {
  compatStyle,
  cx,
  dataColor,
  responsiveCSSVariables,
  toCSSLength,
  toLength,
  type CompatColor,
  type CompatStyleProps,
  type Responsive,
  type WithChildren,
} from './compat';

type NativeExtras = {
  type?: 'button' | 'submit' | 'reset' | string;
  disabled?: boolean;
  href?: string;
  target?: string;
  rel?: string;
  download?: boolean | string;
};

export interface BoxProps extends CompatStyleProps, WithChildren {
  component?: ElementType;
  ref?: React.Ref<HTMLElement>;
  onClick?: MouseEventHandler<HTMLElement>;
  onSubmit?: React.FormEventHandler<HTMLElement>;
  onKeyDown?: React.KeyboardEventHandler<HTMLElement>;
  onPointerDown?: React.PointerEventHandler<HTMLElement>;
  role?: string;
  tabIndex?: number;
  id?: string;
  title?: string;
  type?: NativeExtras['type'];
  disabled?: boolean;
  ta?: CSSProperties['textAlign'];
  [key: `aria-${string}`]: string | number | boolean | undefined;
}

function nativeProps(props: CompatStyleProps & NativeExtras & Record<string, unknown>) {
  const {
    p,
    px,
    py,
    pt,
    pr,
    pb,
    pl,
    m,
    mx,
    my,
    mt,
    mr,
    mb,
    ml,
    w,
    h,
    maw,
    mih,
    className,
    style,
    ...rest
  } = props;
  return {
    ...rest,
    className: cx('uiResponsive', typeof className === 'string' ? className : undefined),
    style: compatStyle({ p, px, py, pt, pr, pb, pl, m, mx, my, mt, mr, mb, ml, w, h, maw, mih, style }),
  };
}

export const Box = forwardRef<HTMLElement, BoxProps>(function Box(
  { component, children, ta, ...props },
  ref,
) {
  const renderedProps = nativeProps({ ...props, ref, style: { textAlign: ta, ...props.style } } as CompatStyleProps & NativeExtras & Record<string, unknown>);
  if (component) return createElement(component, renderedProps, children);
  return (
    <RadixBox {...(renderedProps as ComponentProps<typeof RadixBox>)}>
      {children}
    </RadixBox>
  );
});

export interface GroupProps extends CompatStyleProps, WithChildren {
  gap?: Responsive<string | number>;
  align?: CSSProperties['alignItems'];
  justify?: CSSProperties['justifyContent'];
  wrap?: CSSProperties['flexWrap'];
}

function responsiveGapStyle(gap: Responsive<string | number> | undefined): CSSProperties {
  if (gap === undefined) return {};
  if (typeof gap !== 'object' || gap === null) return { gap: toLength(gap) };
  return {
    gap: 'var(--ui-gap-active)',
    ...responsiveCSSVariables('ui-gap', gap, toCSSLength),
  };
}

export function Group({ children, gap, align = 'center', justify, wrap = 'wrap', className, ...props }: GroupProps) {
  return (
    <Flex
      className={cx('uiResponsive', 'uiGroup', className)}
      style={{
        ...responsiveGapStyle(gap),
        alignItems: align,
        justifyContent: justify,
        flexWrap: wrap,
        ...compatStyle(props),
      }}
    >
      {children}
    </Flex>
  );
}

export interface StackProps extends CompatStyleProps, WithChildren {
  gap?: Responsive<string | number>;
  align?: CSSProperties['alignItems'];
  justify?: CSSProperties['justifyContent'];
  role?: string;
  'aria-label'?: string;
}

export function Stack({ children, gap, align, justify, className, ...props }: StackProps) {
  return (
    <Flex
      direction="column"
      className={cx('uiResponsive', className)}
      gap={undefined}
      style={{
        ...responsiveGapStyle(gap),
        alignItems: align,
        justifyContent: justify,
        ...compatStyle(props),
      }}
      role={props.role}
      aria-label={props['aria-label']}
    >
      {children}
    </Flex>
  );
}

type ResponsiveCount = Responsive<number>;

export interface SimpleGridProps extends CompatStyleProps, WithChildren {
  cols?: ResponsiveCount;
  spacing?: Responsive<string | number>;
}

export function SimpleGrid({ children, cols = 1, spacing = 'md', className, style, ...props }: SimpleGridProps) {
  return (
    <RadixBox
      className={cx('uiResponsive', 'uiSimpleGrid', className)}
      style={{
        ...responsiveCSSVariables('ui-cols', cols),
        ...responsiveCSSVariables('ui-grid-gap', spacing, toCSSLength),
        ...compatStyle({ ...props, style }),
      } as CSSProperties}
    >
      {children}
    </RadixBox>
  );
}

export interface GridProps extends CompatStyleProps, WithChildren {}
export interface GridColProps extends CompatStyleProps, WithChildren {
  span?: ResponsiveCount;
}

export function GridRoot({ children, className, ...props }: GridProps) {
  return (
    <RadixBox className={cx('uiResponsive', 'uiGrid', className)} style={compatStyle(props)}>
      {children}
    </RadixBox>
  );
}

export function GridCol({ children, span = 12, className, style, ...props }: GridColProps) {
  return (
    <RadixBox
      className={cx('uiResponsive', 'uiGridCol', className)}
      style={{ ...responsiveCSSVariables('ui-span', span), ...compatStyle({ ...props, style }) }}
    >
      {children}
    </RadixBox>
  );
}

export const Grid = Object.assign(GridRoot, { Col: GridCol });

export interface PaperProps extends CompatStyleProps, WithChildren {
  component?: ElementType;
  withBorder?: boolean;
  radius?: string | number;
  onClick?: MouseEventHandler<HTMLElement>;
  type?: NativeExtras['type'];
  'aria-label'?: string;
}

export function Paper({ component, children, withBorder, radius, className, ...props }: PaperProps) {
  const renderedProps = nativeProps({
    ...props,
    className,
    style: {
      border: withBorder ? '1px solid var(--gray-a6)' : undefined,
      borderRadius: radius ? `var(--radius-${radius === 'lg' ? '4' : radius === 'md' ? '3' : '2'})` : undefined,
      ...props.style,
    },
  } as CompatStyleProps & NativeExtras & Record<string, unknown>);
  if (component) return createElement(component, renderedProps, children);
  return <RadixBox {...(renderedProps as ComponentProps<typeof RadixBox>)}>{children}</RadixBox>;
}

export interface ScrollAreaProps extends WithChildren {
  className?: string;
  type?: 'auto' | 'always' | 'scroll' | 'hover';
  scrollbarSize?: number;
}

export function ScrollArea({ children, className, type = 'hover', scrollbarSize }: ScrollAreaProps) {
  return (
    <RadixScrollArea
      className={className}
      type={type === 'scroll' ? 'always' : type}
      scrollbars="vertical"
      style={{ '--scrollarea-scrollbar-size': scrollbarSize ? `${scrollbarSize}px` : undefined } as CSSProperties}
    >
      {children}
    </RadixScrollArea>
  );
}

function textColor(color: string | undefined) {
  if (!color || color === 'inherit') return color;
  if (color === 'dimmed') return 'var(--gray-10)';
  const [name, shade] = color.split('.');
  if (!shade) return `var(--${name}-11, ${color})`;
  const scale = Math.max(1, Math.min(12, Number(shade) + 4));
  return `var(--${name === 'yellow' ? 'amber' : name}-${scale})`;
}

function textSize(value: string | number | undefined) {
  if (typeof value === 'number') return value;
  if (!value) return undefined;
  return ({ xs: 'var(--font-size-1)', sm: 'var(--font-size-2)', md: 'var(--font-size-3)', lg: 'var(--font-size-5)', xl: 'var(--font-size-7)' } as Record<string, string>)[value] ?? value;
}

export interface TextProps extends CompatStyleProps, WithChildren {
  component?: ElementType;
  c?: string;
  fz?: string | number;
  fw?: string | number;
  ff?: string;
  lh?: string | number;
  lts?: string | number;
  ta?: CSSProperties['textAlign'];
  tt?: CSSProperties['textTransform'];
  lineClamp?: number;
  role?: string;
}

export function Text({ component = 'div', children, c, fz, fw, ff, lh, lts, ta, tt, lineClamp, className, ...props }: TextProps) {
  const style: CSSProperties = {
    color: textColor(c),
    fontSize: textSize(fz),
    fontWeight: fw,
    fontFamily: ff === 'monospace' ? 'var(--code-font-family)' : ff,
    lineHeight: lh,
    letterSpacing: lts,
    textAlign: ta,
    textTransform: tt,
    WebkitLineClamp: lineClamp,
    ...compatStyle(props),
  };
  return (
    <RadixText asChild>
      {createElement(component, { className: cx('uiResponsive', lineClamp ? 'uiLineClamp' : undefined, className), style, role: props.role }, children)}
    </RadixText>
  );
}

export interface TitleProps extends CompatStyleProps, WithChildren {
  order?: 1 | 2 | 3 | 4 | 5 | 6;
}

export function Title({ order = 1, children, className, ...props }: TitleProps) {
  return (
    <Heading as={`h${order}` as ComponentProps<typeof Heading>['as']} className={cx('uiResponsive', className)} style={compatStyle(props)}>
      {children}
    </Heading>
  );
}

export interface BadgeProps extends CompatStyleProps, WithChildren {
  color?: CompatColor;
  variant?: 'filled' | 'light' | 'outline' | 'dot' | 'default' | 'soft';
  size?: string | number;
  tt?: CSSProperties['textTransform'];
  ff?: string;
}

function variant(value: string | undefined): 'solid' | 'soft' | 'outline' | 'surface' | 'ghost' | undefined {
  return ({ filled: 'solid', light: 'soft', default: 'surface', subtle: 'ghost' } as Record<string, 'solid' | 'soft' | 'outline' | 'surface' | 'ghost'>)[value ?? ''] ?? value as 'solid' | 'soft' | 'outline' | 'surface' | 'ghost' | undefined;
}

function radixSize(value: string | number | undefined): '1' | '2' | '3' | '4' | undefined {
  if (value === undefined) return undefined;
  if (typeof value === 'number') return value <= 16 ? '1' : value <= 24 ? '2' : value <= 32 ? '3' : '4';
  return ({ xs: '1', sm: '1', md: '2', lg: '3', xl: '4', 'compact-xs': '1', 'compact-sm': '1' } as const)[value as 'xs'] ?? '2';
}

export function Badge({ children, color, variant: visualVariant, size, tt, ff, className, ...props }: BadgeProps) {
  return (
    <RadixBadge
      className={cx('uiResponsive', className)}
      color={dataColor(color) as ComponentProps<typeof RadixBadge>['color']}
      variant={variant(visualVariant) as ComponentProps<typeof RadixBadge>['variant']}
      size={radixSize(size) as ComponentProps<typeof RadixBadge>['size']}
      style={{ textTransform: tt, fontFamily: ff === 'monospace' ? 'var(--code-font-family)' : ff, ...compatStyle(props) }}
    >
      {children}
    </RadixBadge>
  );
}

export interface ThemeIconProps extends WithChildren {
  color?: CompatColor;
  variant?: string;
  size?: string | number;
  radius?: string | number;
}

export function ThemeIcon({ children, color = 'gray', variant: visualVariant = 'light', size = 32, radius }: ThemeIconProps) {
  return (
    <RadixBox
      data-accent-color={dataColor(color)}
      data-variant={variant(visualVariant)}
      style={{
        display: 'inline-grid',
        width: toLength(size),
        height: toLength(size),
        flex: '0 0 auto',
        placeItems: 'center',
        borderRadius: radius === 'xl' ? 'var(--radius-full)' : `var(--radius-${radius === 'lg' ? '4' : '3'})`,
        color: `var(--${dataColor(color)}-11)`,
        background: visualVariant === 'filled' ? `var(--${dataColor(color)}-9)` : `var(--${dataColor(color)}-a3)`,
      }}
    >
      {children}
    </RadixBox>
  );
}

export interface AlertProps extends CompatStyleProps, WithChildren {
  color?: CompatColor;
  icon?: ReactNode;
  title?: ReactNode;
  variant?: string;
}

export function Alert({ children, color = 'blue', icon, title, className, ...props }: AlertProps) {
  return (
    <Callout.Root role="alert" className={cx('uiResponsive', className)} color={dataColor(color) as ComponentProps<typeof Callout.Root>['color']} style={compatStyle(props)}>
      {icon ? <Callout.Icon>{icon}</Callout.Icon> : null}
      <RadixBox>
        {title ? <RadixText as="div" weight="bold">{title}</RadixText> : null}
        {children}
      </RadixBox>
    </Callout.Root>
  );
}

export interface TooltipProps extends WithChildren {
  label: ReactNode;
  disabled?: boolean;
  multiline?: boolean;
  maw?: string | number;
  openDelay?: number;
}

export function Tooltip({ children, label, disabled, multiline, maw, openDelay }: TooltipProps) {
  if (disabled) return <>{children}</>;
  return (
    <RadixTooltip
      content={<span style={{ display: 'block', maxWidth: toLength(maw), whiteSpace: multiline ? 'normal' : undefined }}>{label}</span>}
      delayDuration={openDelay}
    >
      <span style={{ display: 'inline-flex' }}>{children}</span>
    </RadixTooltip>
  );
}

export interface ProgressProps extends CompatStyleProps {
  value: number;
  color?: CompatColor;
  size?: string | number;
  animated?: boolean;
}

export function Progress({ value, color, size, animated, className, ...props }: ProgressProps) {
  return (
    <RadixProgress
      className={cx('uiResponsive', animated ? 'uiProgressAnimated' : undefined, className)}
      value={value}
      color={dataColor(color) as ComponentProps<typeof RadixProgress>['color']}
      size={radixSize(size) as ComponentProps<typeof RadixProgress>['size']}
      style={compatStyle(props)}
    />
  );
}

export function Loader({ size }: { size?: string | number }) {
  return <Spinner size={radixSize(size) as ComponentProps<typeof Spinner>['size']} />;
}

export interface DividerProps extends CompatStyleProps {
  color?: string;
}

export function Divider({ color, className, ...props }: DividerProps) {
  return <Separator className={cx('uiResponsive', className)} size="4" style={{ backgroundColor: textColor(color), ...compatStyle(props) }} />;
}

export interface ButtonProps extends CompatStyleProps, WithChildren {
  component?: ElementType;
  type?: NativeExtras['type'];
  href?: string;
  target?: string;
  rel?: string;
  download?: boolean | string;
  disabled?: boolean;
  loading?: boolean;
  fullWidth?: boolean;
  leftSection?: ReactNode;
  rightSection?: ReactNode;
  justify?: CSSProperties['justifyContent'];
  size?: string | number;
  variant?: string;
  color?: CompatColor;
  onClick?: MouseEventHandler<HTMLElement>;
  'aria-label'?: string;
  'aria-pressed'?: boolean;
  'aria-expanded'?: boolean;
  'aria-controls'?: string;
}

export const Button = forwardRef<HTMLButtonElement, ButtonProps>(function Button({
  component,
  children,
  leftSection,
  rightSection,
  loading,
  fullWidth,
  justify,
  size,
  variant: visualVariant,
  color,
  className,
  disabled,
  ...props
}, ref) {
  const content = (
    <>
      {leftSection}
      <span className="uiButtonLabel">{children}</span>
      {rightSection}
    </>
  );
  const common = {
    className: cx('uiResponsive', className),
    disabled,
    loading,
    color: dataColor(color) as ComponentProps<typeof RadixButton>['color'],
    variant: variant(visualVariant) as ComponentProps<typeof RadixButton>['variant'],
    size: radixSize(size) as ComponentProps<typeof RadixButton>['size'],
    style: { width: fullWidth ? '100%' : undefined, justifyContent: justify, ...compatStyle(props) },
  };
  if (component) {
    const childProps = nativeProps({ ...props, disabled, className, ref } as CompatStyleProps & NativeExtras & Record<string, unknown>);
    return (
      <RadixButton {...common} asChild>
        {createElement(component, childProps, content)}
      </RadixButton>
    );
  }
  const { style: _nativeStyle, ...buttonProps } = nativeProps(props as CompatStyleProps & NativeExtras & Record<string, unknown>);
  void _nativeStyle;
  return (
    <RadixButton
      {...(buttonProps as ComponentProps<typeof RadixButton>)}
      {...common}
      ref={ref}
      type={props.type as 'button' | 'submit' | 'reset' | undefined}
      onClick={props.onClick as MouseEventHandler<HTMLButtonElement> | undefined}
    >
      {content}
    </RadixButton>
  );
});

export interface ActionIconProps extends Omit<ButtonProps, 'fullWidth' | 'leftSection' | 'rightSection' | 'justify'> {
  'aria-label'?: string;
  mb?: Responsive<string | number>;
}

export const ActionIcon = forwardRef<HTMLButtonElement, ActionIconProps>(function ActionIcon(
  { component, children, size, variant: visualVariant, color, className, disabled, ...props },
  ref,
) {
  const common = {
    className: cx('uiResponsive', className),
    disabled,
    color: dataColor(color) as ComponentProps<typeof IconButton>['color'],
    variant: variant(visualVariant) as ComponentProps<typeof IconButton>['variant'],
    size: radixSize(size) as ComponentProps<typeof IconButton>['size'],
    style: compatStyle(props),
  };
  if (component) {
    return (
      <IconButton {...common} asChild ref={ref}>
        {createElement(component, nativeProps(props as CompatStyleProps & NativeExtras & Record<string, unknown>), children)}
      </IconButton>
    );
  }
  const { style: _nativeStyle, ...iconButtonProps } = nativeProps(props as CompatStyleProps & NativeExtras & Record<string, unknown>);
  void _nativeStyle;
  return (
    <IconButton
      {...(iconButtonProps as ComponentProps<typeof IconButton>)}
      {...common}
      ref={ref}
      aria-label={props['aria-label']}
      type={props.type as 'button' | 'submit' | 'reset' | undefined}
      onClick={props.onClick as MouseEventHandler<HTMLButtonElement> | undefined}
    >
      {children}
    </IconButton>
  );
});

export interface UnstyledButtonProps extends WithChildren {
  className?: string;
  id?: string;
  title?: string;
  role?: string;
  onClick?: MouseEventHandler<HTMLButtonElement>;
  'aria-selected'?: boolean;
  'aria-current'?: 'page' | boolean;
}

export function UnstyledButton({ children, className, ...props }: UnstyledButtonProps) {
  return (
    <button type="button" {...props} className={cx('uiUnstyledButton', className)}>
      {children}
    </button>
  );
}

export interface BurgerProps {
  opened: boolean;
  onClick?: MouseEventHandler<HTMLButtonElement>;
  size?: string | number;
  'aria-label'?: string;
}

export function Burger({ opened, size = 18, ...props }: BurgerProps) {
  return (
    <IconButton variant="ghost" {...props}>
      <span className="uiBurgerLines" data-opened={opened} style={{ width: toLength(size) }} aria-hidden="true" />
    </IconButton>
  );
}
