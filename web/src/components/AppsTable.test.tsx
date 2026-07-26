import type { ComponentProps } from 'react';
import { cleanup, render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';

import type { AppSummary } from '../api/personClient';
import { strings } from '../strings';
import { AppsTable } from './AppsTable';

afterEach(() => {
  cleanup();
});

const apps: AppSummary[] = [
  {
    appId: 'urls4irl',
    displayName: 'URLs4IRL',
    description: 'Shared URL app',
    createdAt: '2026-07-25T10:00:00Z',
  },
];

function renderTable(overrides: Partial<ComponentProps<typeof AppsTable>> = {}) {
  const props: ComponentProps<typeof AppsTable> = {
    apps,
    subscriberCountByApp: new Map([['urls4irl', 3]]),
    loading: false,
    error: null,
    onUpdateApp: vi.fn().mockResolvedValue(undefined),
    onRemintToken: vi.fn().mockResolvedValue({
      appId: 'urls4irl',
      publisherUserId: 'urls4irl-publisher',
      topicPattern: 'urls4irl-*',
      token: 'tk_fresh',
    }),
    onRemoveApp: vi.fn(),
    ...overrides,
  };
  render(<AppsTable {...props} />);
  return props;
}

describe('AppsTable', () => {
  it('shows the loading copy and no table when loading', () => {
    renderTable({ loading: true });
    expect(screen.getByText(strings.appsLoading)).toBeInTheDocument();
    expect(screen.queryByRole('table')).not.toBeInTheDocument();
  });

  it('shows the empty-state copy when there are no apps', () => {
    renderTable({ apps: [] });
    expect(screen.getByText(strings.appsEmpty)).toBeInTheDocument();
  });

  it('shows an alert when loading fails', () => {
    renderTable({ error: strings.appsLoadError });
    expect(screen.getByRole('alert')).toHaveTextContent(strings.appsLoadError);
  });

  it('renders a row per app with the subscriber count', () => {
    renderTable();
    expect(screen.getByText('URLs4IRL')).toBeInTheDocument();
    expect(screen.getByText('urls4irl', { selector: '.apps-table__id' })).toBeInTheDocument();
    expect(screen.getByText('Shared URL app')).toBeInTheDocument();
    expect(screen.getByText('3')).toBeInTheDocument();
  });

  it('defaults the subscriber count to 0 when an app has no entry', () => {
    renderTable({ subscriberCountByApp: new Map() });
    const row = screen.getByText('URLs4IRL').closest('tr');
    expect(within(row!).getByText('0')).toBeInTheDocument();
  });

  it('toggles the inline edit form when Edit is clicked', async () => {
    const user = userEvent.setup();
    renderTable();

    expect(screen.queryByRole('button', { name: strings.saveAction })).not.toBeInTheDocument();

    await user.click(screen.getByRole('button', { name: `${strings.editAction} URLs4IRL` }));
    expect(screen.getByRole('button', { name: strings.saveAction })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: strings.remintTokenAction })).toBeInTheDocument();
  });

  it('removes an app only after confirmation', async () => {
    const user = userEvent.setup();
    const { onRemoveApp } = renderTable();

    await user.click(screen.getByRole('button', { name: `${strings.removeAction} URLs4IRL` }));
    expect(onRemoveApp).not.toHaveBeenCalled();

    await user.click(
      screen.getByRole('button', { name: `${strings.confirmRemoveAction} URLs4IRL` }),
    );
    expect(onRemoveApp).toHaveBeenCalledWith({ appId: 'urls4irl' });
  });
});
