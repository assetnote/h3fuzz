import type { ConfigOptions } from '../types/api';
import { http3Capacity } from '../utils/config';
import { protocolMode } from '../utils/protocolMode';
import { Badge, Box, Group, Paper, SimpleGrid, Text } from './ui';

interface Http3CapacitySummaryProps {
  config: ConfigOptions;
  automaticWorkers: boolean;
}

function number(value: number): string {
  return new Intl.NumberFormat('en-US').format(value);
}

export function Http3CapacitySummary({ config, automaticWorkers }: Http3CapacitySummaryProps) {
  const capacity = http3Capacity(config, automaticWorkers);
  const mode = protocolMode(config.http);
  const automaticProtocol = mode === 'auto';
  const retainedCapacityDefaults = automaticProtocol
    && !config.http.no_response_optimization
    && capacity.adaptiveConnections
    && capacity.automaticWorkers
    && capacity.maximumConnections === 64
    && capacity.streamsPerConnection === 256
    && config.http.http3_max_workers === 65_536
    && config.general.rate === 0;
  const protocol = config.http.http3
    ? 'HTTP/3 required'
    : config.http.http1
      ? 'HTTP/1 required'
    : config.http.http2
      ? 'HTTP/2 preferred'
      : config.http.no_http3
        ? 'HTTP/3 disabled'
        : 'HTTP/3 → HTTP/2 → HTTP/1';

  return (
    <Paper withBorder p="md" className="http3CapacitySummary">
      <Group justify="space-between" align="flex-start" gap="sm">
        <Box>
          <Text fw={650} fz="sm">Effective HTTP/3 capacity</Text>
          <Text c="dimmed" fz="xs" mt={2}>
            {retainedCapacityDefaults
              ? 'Retained adaptive capacity defaults are configured without forcing HTTP/3.'
              : 'Current configured limits are shown below.'}
          </Text>
        </Box>
        <Badge color={automaticProtocol ? 'cyan' : 'violet'} variant="soft">{protocol}</Badge>
      </Group>

      <SimpleGrid cols={{ base: 2, sm: 4 }} spacing="sm" mt="md">
        <Box>
          <Text c="dimmed" fz="xs">Connections</Text>
          <Text fw={650} fz="sm">
            {capacity.adaptiveConnections
              ? `${capacity.initialConnections} initial / ${capacity.maximumConnections} max`
              : `${capacity.maximumConnections} fixed`}
          </Text>
        </Box>
        <Box>
          <Text c="dimmed" fz="xs">Streams</Text>
          <Text fw={650} fz="sm">{number(capacity.streamsPerConnection)} per connection</Text>
        </Box>
        <Box>
          <Text c="dimmed" fz="xs">Workers</Text>
          <Text fw={650} fz="sm">
            {capacity.automaticWorkers
              ? `${number(capacity.initialWorkers)} initial / ${number(capacity.maximumWorkers)} max`
              : `${number(capacity.maximumWorkers)} fixed`}
          </Text>
        </Box>
        <Box>
          <Text c="dimmed" fz="xs">Rate limit</Text>
          <Text fw={650} fz="sm">
            {config.general.rate === 0 ? 'Unlimited' : `${number(config.general.rate)} req/s`}
          </Text>
        </Box>
      </SimpleGrid>

      <Text c="dimmed" fz="xs" mt="md">
        Automatic mode tries HTTP/3 after a successful probe, then HTTP/2, then HTTP/1.
        HEAD or Range is qualified per target. Body-dependent filters, response scrapers, or Akamai
        encoded-response headers can require GET instead. Capacity is not a throughput guarantee,
        and short scans may finish before adaptation converges.
      </Text>
    </Paper>
  );
}
