import type { ComponentProps } from 'react';
import { cleanup, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';

import type { AppSummary } from '../api/personClient';
import { strings } from '../strings';
import { useTimeZone } from '../test-timezone';
import { setCompactViewport } from '../test-viewport';
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

/** The form's own Close button (the dialog's X shares its accessible name). */
function formClose(): HTMLElement {
  return within(screen.getByRole('dialog')).getAllByRole('button', {
    name: strings.closeEditAction,
  })[1];
}

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

describe('AppsTable created time in a non-UTC zone', () => {
  useTimeZone('America/Los_Angeles');

  it('shows createdAt as a local time element with the UTC instant as its title', () => {
    renderTable();

    const created = screen.getByText('2026-07-25 03:00 PDT');
    expect(created.tagName).toBe('TIME');
    expect(created).toHaveAttribute('datetime', '2026-07-25T10:00:00.000Z');
    expect(created).toHaveAttribute('title', '2026-07-25T10:00:00.000Z');
  });

  it('shows the same local time element in the compact layout', () => {
    setCompactViewport(true);
    renderTable();

    expect(screen.getByText('2026-07-25 03:00 PDT').tagName).toBe('TIME');
  });
});

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

  it('opens the edit form in a dialog when Edit is clicked', async () => {
    const user = userEvent.setup();
    renderTable();

    expect(screen.queryByRole('button', { name: strings.saveAction })).not.toBeInTheDocument();

    const trigger = screen.getByRole('button', { name: `${strings.editAction} URLs4IRL` });
    expect(trigger).toHaveAttribute('aria-haspopup', 'dialog');
    await user.click(trigger);

    const dialog = screen.getByRole('dialog', {
      name: strings.editAppNamed({ appId: 'urls4irl' }),
    });
    expect(dialog).toHaveAttribute('data-owner-row-attribute', 'data-app-id');
    expect(dialog).toHaveAttribute('data-owner-row-id', 'urls4irl');
    expect(within(dialog).getByRole('button', { name: strings.saveAction })).toBeInTheDocument();
    expect(
      within(dialog).getByRole('button', { name: strings.remintTokenAction }),
    ).toBeInTheDocument();
    expect(within(dialog).getByLabelText(strings.appDisplayNameLabel)).toHaveFocus();
    // The row does not grow an inline editor row.
    expect(screen.getAllByRole('row')).toHaveLength(2);
  });

  it('Close returns focus to the Edit button with no prompt when nothing changed', async () => {
    const user = userEvent.setup();
    renderTable();

    const trigger = screen.getByRole('button', { name: `${strings.editAction} URLs4IRL` });
    await user.click(trigger);
    await user.click(formClose());

    expect(screen.queryByRole('dialog')).not.toBeInTheDocument();
    expect(trigger).toHaveFocus();
  });

  it('asks before discarding typed edits', async () => {
    const user = userEvent.setup();
    const { onUpdateApp } = renderTable();

    await user.click(screen.getByRole('button', { name: `${strings.editAction} URLs4IRL` }));
    await user.type(screen.getByLabelText(strings.appDisplayNameLabel), 'x');
    await user.keyboard('{Escape}');
    expect(screen.getByText(strings.discardChangesMessage)).toBeInTheDocument();

    await user.click(screen.getByRole('button', { name: strings.discardChanges }));
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument();
    expect(onUpdateApp).not.toHaveBeenCalled();
  });

  it('closes after a save with no token shown and announces the result once the dialog is gone', async () => {
    const user = userEvent.setup();
    const root = document.createElement('div');
    root.id = 'root';
    document.body.appendChild(root);
    const props = {
      apps,
      subscriberCountByApp: new Map([['urls4irl', 3]]),
      loading: false,
      error: null,
      onUpdateApp: vi.fn().mockResolvedValue(undefined),
      onRemintToken: vi.fn(),
      onRemoveApp: vi.fn(),
    };
    render(<AppsTable {...props} />, { container: root });

    const trigger = screen.getByRole('button', { name: `${strings.editAction} URLs4IRL` });
    await user.click(trigger);
    expect(root).toHaveAttribute('inert');
    await user.type(screen.getByLabelText(strings.appDisplayNameLabel), '2');
    await user.click(screen.getByRole('button', { name: strings.saveAction }));

    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument());
    expect(props.onUpdateApp).toHaveBeenCalledWith({
      appId: 'urls4irl',
      displayName: 'URLs4IRL2',
      description: 'Shared URL app',
    });
    // The announcement is written after the close, when `#root` is no longer inert.
    expect(root).not.toHaveAttribute('inert');
    await waitFor(() =>
      expect(screen.getByRole('status')).toHaveTextContent(
        strings.appSavedResult({ appId: 'urls4irl' }),
      ),
    );
    expect(trigger).toHaveFocus();

    // The next dialog clears it.
    await user.click(trigger);
    expect(screen.getByRole('status', { hidden: true })).toBeEmptyDOMElement();
    root.remove();
  });

  it('closes an untouched Save with no request and no result', async () => {
    const user = userEvent.setup();
    const { onUpdateApp } = renderTable();

    await user.click(screen.getByRole('button', { name: `${strings.editAction} URLs4IRL` }));
    await user.click(screen.getByRole('button', { name: strings.saveAction }));

    expect(screen.queryByRole('dialog')).not.toBeInTheDocument();
    expect(onUpdateApp).not.toHaveBeenCalled();
    expect(screen.getByRole('status')).toBeEmptyDOMElement();
  });

  it('ignores Escape, X and the backdrop while a save is in flight, and closes once it succeeds', async () => {
    const user = userEvent.setup();
    let resolveSave!: () => void;
    const pending = new Promise<void>((resolve) => {
      resolveSave = resolve;
    });
    renderTable({ onUpdateApp: vi.fn().mockReturnValue(pending) });

    await user.click(screen.getByRole('button', { name: `${strings.editAction} URLs4IRL` }));
    await user.type(screen.getByLabelText(strings.appDisplayNameLabel), '2');
    await user.click(screen.getByRole('button', { name: strings.saveAction }));

    await user.keyboard('{Escape}');
    expect(document.querySelector('.modal__close')).toBeNull();
    const backdrop = document.querySelector('.modal__backdrop') as HTMLElement;
    await user.pointer([{ keys: '[MouseLeft>]', target: backdrop }, { keys: '[/MouseLeft]' }]);
    expect(screen.getByRole('dialog')).toBeInTheDocument();
    expect(screen.queryByText(strings.discardChangesMessage)).not.toBeInTheDocument();

    resolveSave();
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument());
  });

  it('is busy during a re-mint, and a token on screen guards every dismissal path', async () => {
    const user = userEvent.setup();
    let resolveMint!: (value: unknown) => void;
    const pending = new Promise((resolve) => {
      resolveMint = resolve;
    });
    const { onRemintToken } = renderTable({ onRemintToken: vi.fn().mockReturnValue(pending) });

    const trigger = screen.getByRole('button', { name: `${strings.editAction} URLs4IRL` });
    await user.click(trigger);
    await user.click(screen.getByRole('button', { name: strings.remintTokenAction }));
    await user.keyboard('{Escape}');
    expect(screen.getByRole('dialog')).toBeInTheDocument();
    expect(screen.queryByText(strings.tokenShownOnceGuard)).not.toBeInTheDocument();

    resolveMint({ appId: 'urls4irl', publisherUserId: 'p', topicPattern: 't', token: 'tk_fresh' });
    expect(await screen.findByText('tk_fresh')).toBeInTheDocument();
    expect(onRemintToken).toHaveBeenCalledWith({ appId: 'urls4irl', rotate: false });

    // Escape, then X, then the Close button: each asks with the token wording.
    await user.keyboard('{Escape}');
    expect(screen.getByText(strings.tokenShownOnceGuard)).toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: strings.keepOpen }));
    expect(screen.queryByText(strings.tokenShownOnceGuard)).not.toBeInTheDocument();

    await user.click(document.querySelector('.modal__close') as HTMLElement);
    expect(screen.getByText(strings.tokenShownOnceGuard)).toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: strings.keepOpen }));

    await user.click(formClose());
    expect(screen.getByText(strings.tokenShownOnceGuard)).toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: strings.closeAnyway }));

    expect(screen.queryByRole('dialog')).not.toBeInTheDocument();
    expect(trigger).toHaveFocus();
  });

  it('keeps the dialog open on a save while a token is shown, announces inside it and is clean after', async () => {
    const user = userEvent.setup();
    const { onUpdateApp } = renderTable();

    await user.click(screen.getByRole('button', { name: `${strings.editAction} URLs4IRL` }));
    await user.click(screen.getByRole('button', { name: strings.remintTokenAction }));
    await screen.findByText('tk_fresh');
    await user.type(screen.getByLabelText(strings.appDisplayNameLabel), '2');
    await user.click(screen.getByRole('button', { name: strings.saveAction }));

    const dialog = screen.getByRole('dialog');
    expect(await within(dialog).findByRole('status')).toHaveTextContent(
      strings.appSavedResult({ appId: 'urls4irl' }),
    );
    expect(onUpdateApp).toHaveBeenCalledTimes(1);
    expect(screen.getByText('tk_fresh')).toBeInTheDocument();
    // The table's own line queued nothing.
    expect(
      within(document.querySelector('.apps-table') as HTMLElement).getAllByRole('status')[0],
    ).toBeEmptyDOMElement();

    // Only the token guards the close now: no "discard changes" wording.
    await user.keyboard('{Escape}');
    expect(screen.getByText(strings.tokenShownOnceGuard)).toBeInTheDocument();
    expect(screen.queryByText(strings.discardChangesMessage)).not.toBeInTheDocument();
  });

  it('confirms Revoke & re-mint inside the dialog, keeps it open and shows the token under the guard', async () => {
    const user = userEvent.setup();
    const { onRemintToken } = renderTable();

    const trigger = screen.getByRole('button', { name: `${strings.editAction} URLs4IRL` });
    await user.click(trigger);
    const revoke = screen.getByRole('button', { name: strings.rotateTokenAction });
    await user.click(revoke);

    expect(screen.getAllByRole('dialog')).toHaveLength(1);
    expect(onRemintToken).not.toHaveBeenCalled();
    await user.keyboard('{Escape}');
    expect(screen.getAllByRole('dialog')).toHaveLength(1);
    expect(revoke).toHaveFocus();

    await user.click(revoke);
    await user.click(screen.getByRole('button', { name: strings.confirmRotateTokenAction }));
    expect(onRemintToken).toHaveBeenCalledWith({ appId: 'urls4irl', rotate: true });
    expect(await screen.findByText('tk_fresh')).toBeInTheDocument();
    expect(screen.getByRole('dialog')).toBeInTheDocument();
    expect(revoke).toHaveFocus();

    await user.keyboard('{Escape}');
    expect(screen.getByText(strings.tokenShownOnceGuard)).toBeInTheDocument();
  });

  it("names each row's own app in its remove dialog and keeps the per-row cancel name", async () => {
    const user = userEvent.setup();
    renderTable({
      apps: [
        ...apps,
        {
          appId: 'chores4irl',
          displayName: 'Chores4IRL',
          description: 'Chores app',
          createdAt: '2026-07-26T10:00:00Z',
        },
      ],
    });

    for (const displayName of ['URLs4IRL', 'Chores4IRL']) {
      const subject = `${strings.removeAction} ${displayName}`;
      await user.click(screen.getByRole('button', { name: subject }));
      const dialog = screen.getByRole('dialog', { name: strings.removeAppDialogTitle });
      expect(within(dialog).getByText(displayName)).toBeInTheDocument();
      expect(within(dialog).getByText(strings.removeAppNotice)).toBeInTheDocument();
      const dismiss = within(dialog).getByRole('button', {
        name: strings.cancelActionNamed({ subject }),
      });
      expect(dismiss).toHaveTextContent(strings.cancelAction);
      await user.click(dismiss);
      expect(screen.queryByRole('dialog')).toBeNull();
    }
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
