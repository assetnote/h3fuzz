import type { ThemeProps } from '@radix-ui/themes';

// One application theme keeps primitives and nested Theme scopes predictable.
// Component-specific presentation belongs in the component or its CSS class.
export const appTheme = {
  appearance: 'dark',
  accentColor: 'violet',
  grayColor: 'slate',
  panelBackground: 'translucent',
  radius: 'medium',
  scaling: '100%',
  hasBackground: false,
} satisfies ThemeProps;
