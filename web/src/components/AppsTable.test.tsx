import type { ComponentProps } from 'react';
import { act, cleanup, render, screen, waitFor, within } from '@testing-library/react';
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
      createdAt: '2026-11-03T10:00:00Z',
      updatedAt: '2026-11-03T11:00:00Z',
    }),
    onRemoveApp: vi.fn().mockResolvedValue(true),
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
      onRemoveApp: vi.fn().mockResolvedValue(true),
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
    expect((document.querySelector('.apps-table__status') as HTMLElement).textContent).toBe('');
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

  it('is busy during a re-mint, then a clean form closes at once on Escape, X and Close', async () => {
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

    resolveMint({
      appId: 'urls4irl',
      publisherUserId: 'p',
      topicPattern: 't',
      createdAt: '2026-11-03T10:00:00Z',
      updatedAt: '2026-11-03T11:00:00Z',
    });
    const dialogStatus = within(screen.getByRole('dialog')).getByRole('status');
    await waitFor(() =>
      expect(dialogStatus).toHaveTextContent(
        strings.publisherRemintedResult({ appId: 'urls4irl' }),
      ),
    );
    expect(onRemintToken).toHaveBeenCalledWith({ appId: 'urls4irl', rotate: false });

    // The re-mint status does not guard a close: the form is clean.
    await user.keyboard('{Escape}');
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument();
    expect(trigger).toHaveFocus();

    await user.click(trigger);
    await user.click(document.querySelector('.modal__close') as HTMLElement);
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument();

    await user.click(trigger);
    await user.click(formClose());
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument();
    expect(screen.queryByText(strings.discardChangesMessage)).not.toBeInTheDocument();
  });

  it('still guards a dirty form after a re-mint', async () => {
    const user = userEvent.setup();
    renderTable();

    await user.click(screen.getByRole('button', { name: `${strings.editAction} URLs4IRL` }));
    await user.click(screen.getByRole('button', { name: strings.remintTokenAction }));
    await waitFor(() =>
      expect(within(screen.getByRole('dialog')).getByRole('status')).not.toBeEmptyDOMElement(),
    );
    await user.type(screen.getByLabelText(strings.appDisplayNameLabel), '2');
    await user.keyboard('{Escape}');

    expect(screen.getByText(strings.discardChangesMessage)).toBeInTheDocument();
  });

  it('closes on a resolved save even after a re-mint and announces Saved in the table', async () => {
    const user = userEvent.setup();
    const { onUpdateApp } = renderTable();

    await user.click(screen.getByRole('button', { name: `${strings.editAction} URLs4IRL` }));
    await user.click(screen.getByRole('button', { name: strings.remintTokenAction }));
    await waitFor(() =>
      expect(within(screen.getByRole('dialog')).getByRole('status')).not.toBeEmptyDOMElement(),
    );
    await user.type(screen.getByLabelText(strings.appDisplayNameLabel), '2');
    await user.click(screen.getByRole('button', { name: strings.saveAction }));

    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument());
    expect(onUpdateApp).toHaveBeenCalledTimes(1);
    await waitFor(() =>
      expect(screen.getByRole('status')).toHaveTextContent(
        strings.appSavedResult({ appId: 'urls4irl' }),
      ),
    );
  });

  it('keeps the dialog open after a failed mint, with the alert and no status text', async () => {
    const user = userEvent.setup();
    renderTable({ onRemintToken: vi.fn().mockRejectedValue(new Error('mint down')) });

    await user.click(screen.getByRole('button', { name: `${strings.editAction} URLs4IRL` }));
    await user.click(screen.getByRole('button', { name: strings.remintTokenAction }));

    const dialog = screen.getByRole('dialog');
    await waitFor(() =>
      expect(within(dialog).getByRole('alert')).toHaveTextContent(strings.genericError),
    );
    expect(within(dialog).getByRole('status')).toBeEmptyDOMElement();
    expect(screen.getByRole('dialog')).toBeInTheDocument();
    // The form is still clean, so Escape closes it at once.
    await user.keyboard('{Escape}');
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument();
  });

  it('confirms Revoke & re-mint inside the dialog, keeps it open and announces the rotation', async () => {
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
    await waitFor(() =>
      expect(within(screen.getByRole('dialog')).getByRole('status')).toHaveTextContent(
        strings.publisherRotatedResult({ appId: 'urls4irl' }),
      ),
    );
    expect(screen.getByRole('dialog')).toBeInTheDocument();
    expect(revoke).toHaveFocus();
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

  it('shows the cascade notice in the compact remove confirm', async () => {
    setCompactViewport(true);
    const user = userEvent.setup();
    renderTable();

    await user.click(
      screen.getByRole('button', { name: strings.rowActionsFor({ row: 'URLs4IRL' }) }),
    );
    await user.click(screen.getByRole('menuitem', { name: `${strings.removeAction} URLs4IRL` }));

    const dialog = screen.getByRole('dialog', { name: strings.removeAppDialogTitle });
    expect(within(dialog).getByText(strings.removeAppNotice)).toBeInTheDocument();
    expect(strings.removeAppNotice).toMatch(/checks, routes and destinations/);
  });

  describe('removal feedback', () => {
    it('puts the wide Remove button in its busy state while the app is removing', () => {
      renderTable({ removingAppIds: new Set(['urls4irl']) });

      const busy = screen.getByRole('button', { name: 'Removing URLs4IRL' });
      expect(busy).toHaveTextContent(/^Removing$/);
      expect(busy).toHaveAttribute('aria-disabled', 'true');
    });

    it('disables the compact Remove item and marks the kebab busy while removing', async () => {
      setCompactViewport(true);
      const user = userEvent.setup();
      renderTable({ removingAppIds: new Set(['urls4irl']) });

      const kebab = screen.getByRole('button', {
        name: strings.rowActionsFor({ row: 'URLs4IRL' }),
      });
      expect(kebab).toHaveAttribute('aria-busy', 'true');
      await user.click(kebab);
      const item = screen.getByRole('menuitem', { name: 'Removing URLs4IRL' });
      expect(item).toHaveTextContent(/^Removing$/);
      expect(item).toHaveAttribute('aria-disabled', 'true');
    });

    it('leaves the kebab without aria-busy when nothing is removing', () => {
      setCompactViewport(true);
      renderTable();

      expect(
        screen.getByRole('button', { name: strings.rowActionsFor({ row: 'URLs4IRL' }) }),
      ).not.toHaveAttribute('aria-busy');
    });

    async function confirmRemove(user: ReturnType<typeof userEvent.setup>) {
      await user.click(screen.getByRole('button', { name: `${strings.removeAction} URLs4IRL` }));
      await user.click(
        screen.getByRole('button', { name: `${strings.confirmRemoveAction} URLs4IRL` }),
      );
    }

    it('announces Removing while pending, then Removed with focus on the heading first', async () => {
      const user = userEvent.setup();
      let resolveRemoval: (value: boolean) => void = () => undefined;
      const onRemoveApp = vi.fn().mockReturnValue(
        new Promise<boolean>((resolve) => {
          resolveRemoval = resolve;
        }),
      );
      const props: ComponentProps<typeof AppsTable> = {
        apps,
        subscriberCountByApp: new Map(),
        loading: false,
        error: null,
        onUpdateApp: vi.fn(),
        onRemintToken: vi.fn(),
        onRemoveApp,
      };
      const { rerender } = render(<AppsTable {...props} />);

      await confirmRemove(user);
      expect(screen.getByRole('status')).toHaveTextContent('Removing urls4irl…');

      // Focus-order spy: what the status read at the moment the heading took focus.
      const heading = screen.getByRole('heading', { name: strings.appsHeading });
      let statusAtFocus: string | null = null;
      heading.addEventListener('focus', () => {
        statusAtFocus = screen.getByRole('status').textContent;
      });

      await act(async () => {
        resolveRemoval(true);
      });
      // The row's control unmounts with the refreshed list; focus is lost to <body>.
      (document.activeElement as HTMLElement | null)?.blur();
      rerender(<AppsTable {...props} apps={[]} />);

      await waitFor(() =>
        expect(screen.getByRole('status')).toHaveTextContent('Removed urls4irl.'),
      );
      expect(heading).toHaveFocus();
      expect(statusAtFocus).toBe('Removing urls4irl…');
    });

    it('clears the in-flight text on failure and writes no Removed message', async () => {
      const user = userEvent.setup();
      const onRemoveApp = vi.fn().mockResolvedValue(false);
      renderTable({ onRemoveApp });

      await confirmRemove(user);

      await waitFor(() => expect(onRemoveApp).toHaveBeenCalledTimes(1));
      await waitFor(() => expect(screen.getByRole('status')).toBeEmptyDOMElement());
      expect(screen.getByRole('status')).not.toHaveTextContent('Removed');
    });

    it('does not move focus after a failure while the row remains', async () => {
      const user = userEvent.setup();
      renderTable({ onRemoveApp: vi.fn().mockResolvedValue(false) });

      await confirmRemove(user);

      await waitFor(() => expect(screen.getByRole('status')).toBeEmptyDOMElement());
      expect(screen.getByRole('heading', { name: strings.appsHeading })).not.toHaveFocus();
    });

    it('moves focus to the heading when a failed removal refresh drops the row', async () => {
      const user = userEvent.setup();
      const onRemoveApp = vi.fn().mockResolvedValue(false);
      const props: ComponentProps<typeof AppsTable> = {
        apps,
        subscriberCountByApp: new Map(),
        loading: false,
        error: null,
        onUpdateApp: vi.fn(),
        onRemintToken: vi.fn(),
        onRemoveApp,
      };
      const { rerender } = render(<AppsTable {...props} />);

      await confirmRemove(user);
      await waitFor(() => expect(onRemoveApp).toHaveBeenCalledTimes(1));
      (document.activeElement as HTMLElement | null)?.blur();
      rerender(<AppsTable {...props} apps={[]} />);

      await waitFor(() =>
        expect(screen.getByRole('heading', { name: strings.appsHeading })).toHaveFocus(),
      );
      expect(screen.getByRole('status')).not.toHaveTextContent('Removed');
    });

    it('does not steal focus later when the refresh failed and the row stayed', async () => {
      const user = userEvent.setup();
      const props: ComponentProps<typeof AppsTable> = {
        apps,
        subscriberCountByApp: new Map(),
        loading: false,
        error: null,
        onUpdateApp: vi.fn(),
        onRemintToken: vi.fn(),
        onRemoveApp: vi.fn().mockResolvedValue(true),
      };
      const { rerender } = render(<AppsTable {...props} />);

      await confirmRemove(user);
      // The row stays (a fresh list that still has it) and focus stays on its control.
      const control = screen.getByRole('button', { name: `${strings.removeAction} URLs4IRL` });
      control.focus();
      rerender(<AppsTable {...props} apps={[...apps]} />);
      await waitFor(() =>
        expect(screen.getByRole('status')).toHaveTextContent('Removed urls4irl.'),
      );
      const heading = screen.getByRole('heading', { name: strings.appsHeading });
      expect(heading).not.toHaveFocus();
      expect(control).toHaveFocus();
      // The armed state is cleared: a later refresh with focus on <body> must not focus the heading.
      control.blur();
      rerender(<AppsTable {...props} apps={[...apps]} />);
      expect(heading).not.toHaveFocus();
    });

    it('replaces the previous Removed message with the next in-flight text', async () => {
      const user = userEvent.setup();
      const twoApps: AppSummary[] = [
        ...apps,
        {
          appId: 'chores4irl',
          displayName: 'Chores4IRL',
          description: null,
          createdAt: '2026-07-26T10:00:00Z',
        },
      ];
      const onRemoveApp = vi
        .fn()
        .mockResolvedValueOnce(true)
        .mockReturnValueOnce(new Promise<boolean>(() => undefined));
      const props: ComponentProps<typeof AppsTable> = {
        apps: twoApps,
        subscriberCountByApp: new Map(),
        loading: false,
        error: null,
        onUpdateApp: vi.fn(),
        onRemintToken: vi.fn(),
        onRemoveApp,
      };
      const { rerender } = render(<AppsTable {...props} />);

      await confirmRemove(user);
      // The post-removal refresh lands with a fresh list that still has the other app.
      rerender(<AppsTable {...props} apps={[twoApps[1]]} />);
      await waitFor(() =>
        expect(screen.getByRole('status')).toHaveTextContent('Removed urls4irl.'),
      );
      await user.click(screen.getByRole('button', { name: 'Remove Chores4IRL' }));
      await user.click(screen.getByRole('button', { name: 'Confirm remove Chores4IRL' }));

      expect(screen.getByRole('status')).toHaveTextContent('Removing chores4irl…');
      expect(screen.getByRole('status')).not.toHaveTextContent('Removed');
    });
  });
});
