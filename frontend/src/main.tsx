import '@radix-ui/themes/styles.css';
import './styles.css';

import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { StrictMode } from 'react';
import { createRoot } from 'react-dom/client';
import App from './App';
import { AppErrorBoundary } from './components/AppErrorBoundary';
import { AppThemeProvider } from './components/ui/AppThemeProvider';
import { ToastProvider } from './components/ui/ToastProvider';

const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      refetchOnWindowFocus: false,
      staleTime: 30_000,
    },
  },
});

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <AppThemeProvider>
      <AppErrorBoundary>
        <QueryClientProvider client={queryClient}>
          <ToastProvider limit={4}>
            <App />
          </ToastProvider>
        </QueryClientProvider>
      </AppErrorBoundary>
    </AppThemeProvider>
  </StrictMode>,
);
