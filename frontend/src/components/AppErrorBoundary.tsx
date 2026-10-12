import { ExclamationTriangleIcon, ReloadIcon } from '@radix-ui/react-icons';
import { Box, Button, Card, Code, Flex, Heading, Text } from '@radix-ui/themes';
import { Component, type ErrorInfo, type ReactNode } from 'react';

interface Props {
  children: ReactNode;
}

interface State {
  error?: Error;
}

export class AppErrorBoundary extends Component<Props, State> {
  state: State = {};

  static getDerivedStateFromError(error: Error): State {
    return { error };
  }

  componentDidCatch(error: Error, info: ErrorInfo) {
    console.error('h3fuzz console render failure', error, info.componentStack);
  }

  render() {
    if (!this.state.error) return this.props.children;
    return (
      <Box className="centerScreen errorBoundary" role="alert">
        <Card size="4" variant="surface" className="welcomeCard errorBoundaryCard">
          <Flex direction="column" align="start" gap="3">
            <span className="errorBoundaryIcon" data-accent-color="red" aria-hidden="true">
              <ExclamationTriangleIcon width="22" height="22" />
            </span>
            <Heading as="h2" size="6" className="errorBoundaryTitle">
              The console could not render this data
            </Heading>
            <Text as="p" color="gray" size="3" className="errorBoundaryDescription">
              Scan sessions continue on the server. Reload the console to fetch a fresh persisted snapshot.
            </Text>
            <Code color="red" size="2" variant="soft" className="breakAnywhere errorBoundaryMessage">
              {this.state.error.message || 'Unknown rendering error'}
            </Code>
            <Button size="2" onClick={() => window.location.reload()}>
              <ReloadIcon width="16" height="16" aria-hidden="true" />
              Reload console
            </Button>
          </Flex>
        </Card>
      </Box>
    );
  }
}
