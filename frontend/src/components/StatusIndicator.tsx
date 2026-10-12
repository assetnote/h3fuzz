import {
  CheckCircledIcon,
  DotFilledIcon,
  ExclamationTriangleIcon,
  PauseIcon,
  PlayIcon,
  StopIcon,
  UpdateIcon,
} from '@radix-ui/react-icons';
import { Badge, Flex, Text, Tooltip, type BadgeProps } from '@radix-ui/themes';
import type { SessionStatus } from '../types/api';

type StatusColor = NonNullable<BadgeProps['color']>;

const statusConfig = {
  idle: { label: 'Idle', color: 'gray', icon: DotFilledIcon },
  starting: { label: 'Starting', color: 'violet', icon: PlayIcon },
  running: { label: 'Running', color: 'green', icon: PlayIcon },
  paused: { label: 'Paused', color: 'amber', icon: PauseIcon },
  stopping: { label: 'Stopping', color: 'orange', icon: UpdateIcon },
  completed: { label: 'Completed', color: 'blue', icon: CheckCircledIcon },
  stopped: { label: 'Stopped', color: 'gray', icon: StopIcon },
  failed: { label: 'Failed', color: 'red', icon: ExclamationTriangleIcon },
} satisfies Record<SessionStatus, { label: string; color: StatusColor; icon: typeof DotFilledIcon }>;

export function StatusIndicator({ status, compact = false }: { status: SessionStatus; compact?: boolean }) {
  const config = statusConfig[status];
  const Icon = config.icon;
  const animated = status === 'running' || status === 'stopping';
  const icon = (
    <span className={animated ? 'statusIcon statusPulse' : 'statusIcon'} aria-hidden="true">
      <Icon width="13" height="13" />
    </span>
  );
  if (compact) {
    return (
      <Tooltip content={config.label} delayDuration={400}>
        <span
          className="statusCompact"
          data-accent-color={config.color}
          aria-label={config.label}
          role="status"
        >
          {icon}
        </span>
      </Tooltip>
    );
  }
  return (
    <Badge color={config.color} variant="soft" size="2" radius="full" role="status">
      {icon}
      {config.label}
    </Badge>
  );
}

export function ConnectionIndicator({ connected }: { connected: boolean }) {
  return (
    <Flex gap="2" align="center" wrap="nowrap" role="status" aria-live="polite">
      <span className={connected ? 'connectionDot connectionDotOnline' : 'connectionDot'} aria-hidden="true" />
      <Text as="span" color="gray" size="1">
        {connected ? 'Live' : 'Reconnecting'}
      </Text>
    </Flex>
  );
}
