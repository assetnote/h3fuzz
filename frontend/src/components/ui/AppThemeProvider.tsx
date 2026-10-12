import { Theme } from '@radix-ui/themes';
import type { ReactNode } from 'react';
import { appTheme } from '../../theme';

export function AppThemeProvider({ children }: { children: ReactNode }) {
  return (
    <Theme {...appTheme} className="h3Theme">
      {children}
    </Theme>
  );
}
