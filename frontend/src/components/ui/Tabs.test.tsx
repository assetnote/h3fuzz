import { screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { describe, expect, it } from 'vitest';
import { render } from '../../test/render';
import { Tabs } from './index';

describe('Tabs', () => {
  it('keeps inactive mounted panels hidden and switches them accessibly', async () => {
    const user = userEvent.setup();
    render(
      <Tabs defaultValue="discovery" keepMounted orientation="vertical">
        <Tabs.List>
          <Tabs.Tab value="discovery">Discovery</Tabs.Tab>
          <Tabs.Tab value="http3">HTTP/3</Tabs.Tab>
        </Tabs.List>
        <Tabs.Panel value="discovery">Discovery settings</Tabs.Panel>
        <Tabs.Panel value="http3">HTTP/3 settings</Tabs.Panel>
      </Tabs>,
    );

    const discovery = screen.getByText('Discovery settings').closest('[role="tabpanel"]');
    const http3 = screen.getByText('HTTP/3 settings').closest('[role="tabpanel"]');
    expect(discovery).toBeVisible();
    expect(http3).not.toBeVisible();

    await user.click(screen.getByRole('tab', { name: /HTTP\/3/ }));
    expect(discovery).not.toBeVisible();
    expect(http3).toBeVisible();
  });
});
