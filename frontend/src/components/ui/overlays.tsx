import * as AccordionPrimitive from '@radix-ui/react-accordion';
import * as CollapsiblePrimitive from '@radix-ui/react-collapsible';
import {
  Dialog,
  DropdownMenu,
  IconButton,
  Tabs as RadixTabs,
  VisuallyHidden,
} from '@radix-ui/themes';
import { ChevronDownIcon, Cross2Icon } from '@radix-ui/react-icons';
import {
  createContext,
  useContext,
  useState,
  type CSSProperties,
  type MouseEventHandler,
  type ReactElement,
  type ReactNode,
} from 'react';
import { compatStyle, cx, toLength, type CompatStyleProps } from './compat';

type ClassNames = Partial<Record<'content' | 'body' | 'header' | 'overlay', string>>;

export interface ModalProps {
  opened: boolean;
  onClose: () => void;
  children?: ReactNode;
  title?: ReactNode;
  centered?: boolean;
  fullScreen?: boolean;
  size?: string | number;
  padding?: string | number;
  radius?: string | number;
  withCloseButton?: boolean;
  classNames?: ClassNames;
  'aria-label'?: string;
}

function modalWidth(size: string | number | undefined) {
  if (typeof size === 'number') return size;
  if (!size) return undefined;
  return ({ xs: 320, sm: 420, md: 560, lg: 720, xl: 900 } as Record<string, string | number>)[size] ?? size;
}

export function Modal({
  opened,
  onClose,
  children,
  title,
  fullScreen,
  size,
  padding,
  radius,
  withCloseButton = true,
  classNames,
  'aria-label': ariaLabel,
}: ModalProps) {
  return (
    <Dialog.Root open={opened} onOpenChange={(next) => { if (!next) onClose(); }}>
      <Dialog.Content
        className={cx('uiModalContent', classNames?.content)}
        data-full-screen={fullScreen}
        aria-label={ariaLabel}
        style={{
          width: fullScreen ? undefined : modalWidth(size),
          maxWidth: fullScreen ? undefined : 'calc(100vw - 2rem)',
          padding: toLength(padding),
          borderRadius: typeof radius === 'number' ? radius : radius === 'lg' ? 'var(--radius-4)' : undefined,
        }}
      >
        {title || withCloseButton ? (
          <div className={cx('uiDialogHeader', classNames?.header)}>
            {title ? <Dialog.Title>{title}</Dialog.Title> : (
              <VisuallyHidden asChild>
                <Dialog.Title>{ariaLabel ?? 'Dialog'}</Dialog.Title>
              </VisuallyHidden>
            )}
            {withCloseButton ? (
              <Dialog.Close>
                <IconButton className="uiDialogClose" type="button" variant="ghost" aria-label="Close">
                  <Cross2Icon />
                </IconButton>
              </Dialog.Close>
            ) : null}
          </div>
        ) : (
          <VisuallyHidden asChild>
            <Dialog.Title>{ariaLabel ?? 'Dialog'}</Dialog.Title>
          </VisuallyHidden>
        )}
        <div className={classNames?.body}>{children}</div>
      </Dialog.Content>
    </Dialog.Root>
  );
}

export interface DrawerProps {
  opened: boolean;
  onClose: () => void;
  children?: ReactNode;
  title?: ReactNode;
  position?: 'left' | 'right';
  size?: string | number;
  padding?: string | number;
  withCloseButton?: boolean;
  classNames?: ClassNames;
  overlayProps?: { backgroundOpacity?: number; blur?: number };
}

export function Drawer({
  opened,
  onClose,
  children,
  title,
  position = 'right',
  size = 'md',
  padding,
  withCloseButton = true,
  classNames,
}: DrawerProps) {
  const drawerSize = typeof size === 'string' && size in { sm: 1, md: 1, lg: 1, xl: 1 }
    ? ({ sm: 320, md: 400, lg: 520, xl: 640 } as Record<string, number>)[size]
    : size;
  return (
    <Dialog.Root open={opened} onOpenChange={(next) => { if (!next) onClose(); }}>
      <Dialog.Content
        className={cx('uiDrawerContent', classNames?.content)}
        data-position={position}
        style={{ '--ui-drawer-size': toLength(drawerSize), padding: toLength(padding) } as CSSProperties}
      >
        {title || withCloseButton ? (
          <div className={cx('uiDialogHeader', classNames?.header)}>
            {title ? <Dialog.Title>{title}</Dialog.Title> : (
              <VisuallyHidden asChild>
                <Dialog.Title>Drawer</Dialog.Title>
              </VisuallyHidden>
            )}
            {withCloseButton ? (
              <Dialog.Close>
                <IconButton type="button" variant="ghost" aria-label="Close"><Cross2Icon /></IconButton>
              </Dialog.Close>
            ) : null}
          </div>
        ) : (
          <VisuallyHidden asChild>
            <Dialog.Title>Drawer</Dialog.Title>
          </VisuallyHidden>
        )}
        <div className={classNames?.body}>{children}</div>
      </Dialog.Content>
    </Dialog.Root>
  );
}

interface MenuContextValue {
  align: 'start' | 'center' | 'end';
}
const MenuContext = createContext<MenuContextValue>({ align: 'start' });

export interface MenuProps {
  children?: ReactNode;
  position?: string;
  shadow?: string;
  withinPortal?: boolean;
}

function MenuRoot({ children, position }: MenuProps) {
  const align = position?.endsWith('end') ? 'end' : position?.endsWith('start') ? 'start' : 'center';
  return (
    <MenuContext.Provider value={{ align }}>
      <DropdownMenu.Root>{children}</DropdownMenu.Root>
    </MenuContext.Provider>
  );
}

function MenuTarget({ children }: { children: ReactElement }) {
  return <DropdownMenu.Trigger>{children}</DropdownMenu.Trigger>;
}

function MenuDropdown({ children }: { children?: ReactNode }) {
  const { align } = useContext(MenuContext);
  return <DropdownMenu.Content align={align}>{children}</DropdownMenu.Content>;
}

function MenuItem({ children, leftSection, onClick, disabled }: { children?: ReactNode; leftSection?: ReactNode; onClick?: MouseEventHandler<HTMLDivElement>; disabled?: boolean }) {
  return (
    <DropdownMenu.Item disabled={disabled} onClick={onClick}>
      {leftSection ? <span aria-hidden="true">{leftSection}</span> : null}
      {children}
    </DropdownMenu.Item>
  );
}

function MenuLabel({ children }: { children?: ReactNode }) {
  return <DropdownMenu.Label>{children}</DropdownMenu.Label>;
}

function MenuDivider() {
  return <DropdownMenu.Separator />;
}

export const Menu = Object.assign(MenuRoot, {
  Target: MenuTarget,
  Dropdown: MenuDropdown,
  Item: MenuItem,
  Label: MenuLabel,
  Divider: MenuDivider,
});

interface TabsContextValue {
  keepMounted: boolean;
  activeValue?: string;
}
const TabsContext = createContext<TabsContextValue>({ keepMounted: true });

export interface TabsProps {
  children?: ReactNode;
  className?: string;
  defaultValue?: string;
  value?: string;
  onChange?: (value: string | null) => void;
  keepMounted?: boolean;
  orientation?: 'horizontal' | 'vertical';
  variant?: string;
}

function TabsRoot({ children, keepMounted = true, defaultValue, value, onChange, ...props }: TabsProps) {
  const [uncontrolledValue, setUncontrolledValue] = useState(defaultValue);
  const activeValue = value ?? uncontrolledValue;
  return (
    <TabsContext.Provider value={{ keepMounted, activeValue }}>
      <RadixTabs.Root
        {...props}
        value={activeValue}
        onValueChange={(next) => {
          if (value === undefined) setUncontrolledValue(next);
          onChange?.(next);
        }}
      >
        {children}
      </RadixTabs.Root>
    </TabsContext.Provider>
  );
}

function TabsList({ children }: { children?: ReactNode }) {
  return <RadixTabs.List className="uiTabsList">{children}</RadixTabs.List>;
}

function TabsTab({
  children,
  value,
  leftSection,
  'aria-label': ariaLabel,
}: {
  children?: ReactNode;
  value: string;
  leftSection?: ReactNode;
  'aria-label'?: string;
}) {
  return <RadixTabs.Trigger value={value} aria-label={ariaLabel}>{leftSection}{children}</RadixTabs.Trigger>;
}

function TabsPanel({ children, value, className, ...props }: CompatStyleProps & { children?: ReactNode; value: string }) {
  const { keepMounted, activeValue } = useContext(TabsContext);
  return (
    <RadixTabs.Content
      className={cx('uiTabsPanel', className)}
      value={value}
      forceMount={keepMounted || undefined}
      hidden={activeValue !== value}
      style={compatStyle(props)}
    >
      {children}
    </RadixTabs.Content>
  );
}

export const Tabs = Object.assign(TabsRoot, { List: TabsList, Tab: TabsTab, Panel: TabsPanel });

export interface AccordionProps {
  children?: ReactNode;
  className?: string;
  variant?: string;
  radius?: string | number;
  defaultValue?: string;
  value?: string;
  onChange?: (value: string | null) => void;
}

function AccordionRoot({ children, value, defaultValue, onChange, className }: AccordionProps) {
  return (
    <AccordionPrimitive.Root
      className={className}
      type="single"
      collapsible
      value={value}
      defaultValue={defaultValue}
      onValueChange={(next) => onChange?.(next || null)}
    >
      {children}
    </AccordionPrimitive.Root>
  );
}

function AccordionItem({ children, value }: { children?: ReactNode; value: string }) {
  return <AccordionPrimitive.Item className="uiAccordionItem" value={value}>{children}</AccordionPrimitive.Item>;
}

function AccordionControl({ children, icon }: { children?: ReactNode; icon?: ReactNode }) {
  return (
    <AccordionPrimitive.Header>
      <AccordionPrimitive.Trigger className="uiAccordionTrigger">
        {icon}{children}<ChevronDownIcon aria-hidden="true" />
      </AccordionPrimitive.Trigger>
    </AccordionPrimitive.Header>
  );
}

function AccordionPanel({ children }: { children?: ReactNode }) {
  return <AccordionPrimitive.Content className="uiAccordionPanel">{children}</AccordionPrimitive.Content>;
}

export const Accordion = Object.assign(AccordionRoot, { Item: AccordionItem, Control: AccordionControl, Panel: AccordionPanel });

export function Collapse({ in: opened, children }: { in: boolean; children?: ReactNode }) {
  return (
    <CollapsiblePrimitive.Root open={opened}>
      <CollapsiblePrimitive.Content className="uiCollapse" forceMount>{children}</CollapsiblePrimitive.Content>
    </CollapsiblePrimitive.Root>
  );
}
