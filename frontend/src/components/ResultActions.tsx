import { ActionIcon, Box, Tooltip, notifications } from './ui';
import { IconArrowUpRight, IconBraces, IconCopy, IconRoute } from './ui/icons';
import { copyText } from '../utils/clipboard';
import { resultCurl } from '../utils/resultView';

interface ResultActionsProps {
  uri: string;
  url: string;
  openURL?: string;
  label?: string;
}

export function ResultActions({ uri, url, openURL, label = uri }: ResultActionsProps) {
  const copy = (value: string, kind: 'path' | 'full URL' | 'cURL command') => {
    void copyText(value)
      .then(() => notifications.show({ color: 'green', message: `${kind} copied` }))
      .catch((error: unknown) => notifications.show({
        color: 'red',
        title: `Could not copy ${kind}`,
        message: error instanceof Error ? error.message : 'Clipboard access is unavailable.',
      }));
  };

  return (
    <Box
      role="group"
      className="resultActions"
      aria-label={`Actions for ${label}`}
      onClick={(event) => event.stopPropagation()}
    >
      <Tooltip label="Copy path">
        <ActionIcon
          size="sm"
          variant="ghost"
          color="gray"
          aria-label={`Copy path for ${label}`}
          onClick={() => copy(uri, 'path')}
        >
          <IconRoute size={14} />
        </ActionIcon>
      </Tooltip>
      <Tooltip label="Copy full URL">
        <ActionIcon
          size="sm"
          variant="ghost"
          color="gray"
          aria-label={`Copy full URL for ${label}`}
          onClick={() => copy(url, 'full URL')}
        >
          <IconCopy size={14} />
        </ActionIcon>
      </Tooltip>
      <Tooltip label="Copy cURL command">
        <ActionIcon
          size="sm"
          variant="ghost"
          color="gray"
          aria-label={`Copy cURL for ${label}`}
          onClick={() => copy(resultCurl(url), 'cURL command')}
        >
          <IconBraces size={14} />
        </ActionIcon>
      </Tooltip>
      <Tooltip label={openURL ? 'Open URL in a new tab' : 'Only HTTP and HTTPS URLs can be opened'}>
        {openURL ? (
          <ActionIcon
            component="a"
            href={openURL}
            target="_blank"
            rel="noopener noreferrer"
            size="sm"
            variant="ghost"
            color="gray"
            aria-label={`Open URL for ${label}`}
          >
            <IconArrowUpRight size={14} />
          </ActionIcon>
        ) : (
          <ActionIcon size="sm" variant="ghost" color="gray" disabled aria-label={`Open URL for ${label}`}>
            <IconArrowUpRight size={14} />
          </ActionIcon>
        )}
      </Tooltip>
    </Box>
  );
}
