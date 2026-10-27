import { act, cleanup, render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { ApiError } from '../api/client';
import type { ResetPasswordResult, UserSummary } from '../api/client';
import { strings } from '../strings';
import { UsersTable } from './UsersTable';

const PASSWORD = 'abcd-efgh-ijkm-npqr';
const ALICE = 'alice@example.com';
const BOB = 'bob@example.com';

const aliceRow: UserSummary = {
  userId: 'u_aaaaaaaaaaaaaaaa',
  apps: ['urls4irl', 'chores4irl'],
  topicPatterns: ['t-alice'],
};
const bobRow: UserSummary = { userId: 'u_bbbbbbbbbbbbbbbb', apps: ['urls4irl'], topicPatterns: [] };
const publisherRow: UserSummary = { userId: 'pub_urls4irl', apps: ['urls4irl'], topicPatterns: [] };
const emails = new Map([
  ['aaaaaaaaaaaaaaaa', ALICE],
  ['bbbbbbbbbbbbbbbb', BOB],
]);

type ResetFn = (params: { appId: string; email: string }) => Promise<ResetPasswordResult>;

const originalClipboard = Object.getOwnPropertyDescriptor(navigator, 'clipboard');

afterEach(() => {
  cleanup();
  vi.useRealTimers();
  vi.restoreAllMocks();
  if (originalClipboard) {
    Object.defineProperty(navigator, 'clipboard', originalClipboard);
  } else {
    Reflect.deleteProperty(navigator, 'clipboard');
  }
});

function setClipboard(value: unknown) {
  Object.defineProperty(navigator, 'clipboard', { configurable: true, value });
}

function renderTable({
  users = [aliceRow, bobRow],
  onResetPassword,
  emailByPersonHash = emails,
}: {
  users?: UserSummary[];
  onResetPassword?: ResetFn;
  emailByPersonHash?: Map<string, string>;
}) {
  const build = (nextUsers: UserSummary[], map: Map<string, string>) => (
    <div>
      <button type="button" id="app-tab-provisioning">
        Provisioning
      </button>
      <div data-testid="panel">
        <UsersTable
          users={nextUsers}
          loading={false}
          emailByPersonHash={map}
          onDeprovision={vi.fn()}
          onDelete={vi.fn()}
          onResetPassword={onResetPassword}
        />
      </div>
    </div>
  );
  const view = render(build(users, emailByPersonHash));
  return {
    ...view,
    rerenderWith: (nextUsers: UserSummary[], map: Map<string, string> = emailByPersonHash) =>
      view.rerender(build(nextUsers, map)),
    panel: () => screen.getByTestId('panel'),
  };
}

function resetTrigger(who: string) {
  return screen.getByRole('button', { name: strings.resetPasswordTriggerNamed({ user: who }) });
}

function dialog() {
  return screen.getByRole('dialog');
}

/** The failed phase's Close button; the header X shares the accessible name "Close". */
function closeActionButton() {
  return within(dialog()).getByText(strings.closeDialog, { selector: 'button' });
}

function confirmButton(who: string) {
  return within(dialog()).getByRole('button', {
    name: strings.resetPasswordConfirmNamed({ user: who }),
  });
}

function cancelButton(who: string) {
  return within(dialog()).getByRole('button', {
    name: strings.cancelActionNamed({ subject: strings.resetPasswordTriggerNamed({ user: who }) }),
  });
}

function resetOk(): ResetFn {
  return vi.fn().mockResolvedValue({ userId: 'u_x', appId: 'urls4irl', password: PASSWORD });
}

function pendingReset() {
  let resolve: (result: ResetPasswordResult) => void = () => undefined;
  let reject: (reason: unknown) => void = () => undefined;
  const promise = new Promise<ResetPasswordResult>((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { fn: vi.fn().mockReturnValue(promise) as unknown as ResetFn, resolve, reject };
}

async function resetThrough(user: ReturnType<typeof userEvent.setup>, who: string) {
  await user.click(resetTrigger(who));
  await user.click(confirmButton(who));
}

describe('UsersTable operator password reset', () => {
  it('offers Reset only on person rows whose email is known, and hints on the rest', () => {
    renderTable({
      users: [aliceRow, publisherRow, { ...bobRow, userId: 'u_cccccccccccccccc' }],
      onResetPassword: resetOk(),
    });

    expect(resetTrigger(ALICE)).toBeInTheDocument();
    expect(screen.getAllByRole('button', { name: /^Reset password for/ })).toHaveLength(1);
    expect(screen.getAllByText(strings.resetPasswordUnavailable)).toHaveLength(1);
  });

  it('offers no Reset action and no dialog without the handler', () => {
    renderTable({});

    expect(screen.queryByRole('button', { name: /Reset password/ })).not.toBeInTheDocument();
    expect(screen.queryByRole('status')).not.toBeInTheDocument();
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument();
  });

  describe('confirm phase', () => {
    it('opens a dialog naming the action, user and consequence, with focus on Cancel', async () => {
      const user = userEvent.setup();
      const onResetPassword = resetOk();
      renderTable({ onResetPassword });

      await user.click(resetTrigger(ALICE));

      expect(dialog()).toHaveAccessibleName(strings.resetPasswordDialogTitle);
      expect(within(dialog()).getByText(strings.dialogItemUser)).toBeInTheDocument();
      expect(within(dialog()).getByText(ALICE)).toBeInTheDocument();
      expect(within(dialog()).getByText(strings.resetPasswordNotice)).toBeInTheDocument();
      expect(cancelButton(ALICE)).toHaveFocus();
      expect(onResetPassword).not.toHaveBeenCalled();
    });

    it.each([
      [
        'Cancel',
        async (user: ReturnType<typeof userEvent.setup>) => user.click(cancelButton(ALICE)),
      ],
      [
        'the X',
        async (user: ReturnType<typeof userEvent.setup>) =>
          user.click(within(dialog()).getByRole('button', { name: strings.closeDialog })),
      ],
      ['Escape', async (user: ReturnType<typeof userEvent.setup>) => user.keyboard('{Escape}')],
      [
        'the backdrop',
        async (user: ReturnType<typeof userEvent.setup>) =>
          user.click(dialog().parentElement as HTMLElement),
      ],
    ])('%s closes it with no call and focus back on the trigger', async (_label, exit) => {
      const user = userEvent.setup();
      const onResetPassword = resetOk();
      renderTable({ onResetPassword });
      await user.click(resetTrigger(ALICE));

      await exit(user);

      expect(screen.queryByRole('dialog')).not.toBeInTheDocument();
      expect(onResetPassword).not.toHaveBeenCalled();
      expect(resetTrigger(ALICE)).toHaveFocus();
    });

    it('closes without firing when its row leaves the list, focusing the Users heading', async () => {
      const user = userEvent.setup();
      const onResetPassword = resetOk();
      const { rerenderWith } = renderTable({ onResetPassword });
      await user.click(resetTrigger(ALICE));

      rerenderWith([bobRow]);

      expect(screen.queryByRole('dialog')).not.toBeInTheDocument();
      expect(onResetPassword).not.toHaveBeenCalled();
      expect(screen.getByRole('heading', { name: strings.usersHeading })).toHaveFocus();
    });

    it('closes without firing when the row stops being reset-eligible', async () => {
      const user = userEvent.setup();
      const onResetPassword = resetOk();
      const { rerenderWith } = renderTable({ onResetPassword });
      await user.click(resetTrigger(ALICE));

      rerenderWith([{ ...aliceRow, apps: [] }, bobRow]);

      expect(screen.queryByRole('dialog')).not.toBeInTheDocument();
      expect(onResetPassword).not.toHaveBeenCalled();
      expect(screen.getByRole('heading', { name: strings.usersHeading })).toHaveFocus();
    });

    it('focuses the Provisioning tab when the row leaves while the panel is hidden', async () => {
      const user = userEvent.setup();
      const { rerenderWith, panel } = renderTable({ onResetPassword: resetOk() });
      await user.click(resetTrigger(ALICE));

      panel().hidden = true;
      rerenderWith([bobRow]);

      expect(screen.queryByRole('dialog')).not.toBeInTheDocument();
      expect(screen.getByRole('button', { name: 'Provisioning' })).toHaveFocus();
    });

    it('resolves appId and email from the live row at Confirm, not from open time', async () => {
      const user = userEvent.setup();
      const onResetPassword = resetOk();
      const { rerenderWith } = renderTable({ onResetPassword });
      await user.click(resetTrigger(ALICE));

      rerenderWith([{ ...aliceRow, apps: ['chores4irl'] }, bobRow]);
      await user.click(confirmButton(ALICE));

      expect(onResetPassword).toHaveBeenCalledWith({ appId: 'chores4irl', email: ALICE });
    });
  });

  describe('busy phase', () => {
    it('calls reset once with the first app and the email and keeps the dialog open and busy', async () => {
      const user = userEvent.setup();
      const { fn } = pendingReset();
      renderTable({ onResetPassword: fn });

      await user.click(resetTrigger(ALICE));
      const confirm = confirmButton(ALICE);
      await user.dblClick(confirm);

      expect(fn).toHaveBeenCalledTimes(1);
      expect(fn).toHaveBeenCalledWith({ appId: 'urls4irl', email: ALICE });
      expect(dialog()).toHaveAttribute('aria-busy', 'true');
      const busyConfirm = within(dialog()).getByRole('button', {
        name: strings.resetPasswordBusyNamed({ user: ALICE }),
      });
      expect(busyConfirm).toHaveTextContent(strings.resettingPasswordAction);
      expect(busyConfirm).toHaveAttribute('aria-disabled', 'true');
      expect(cancelButton(ALICE)).toHaveAttribute('aria-disabled', 'true');
      expect(
        within(dialog()).queryByRole('button', { name: strings.closeDialog }),
      ).not.toBeInTheDocument();
    });

    it('ignores Escape, Cancel and the backdrop while the request is in flight', async () => {
      const user = userEvent.setup();
      const { fn } = pendingReset();
      renderTable({ onResetPassword: fn });
      await resetThrough(user, ALICE);

      await user.keyboard('{Escape}');
      await user.click(cancelButton(ALICE));
      await user.click(dialog().parentElement as HTMLElement);

      expect(dialog()).toBeInTheDocument();
      expect(dialog()).toHaveAttribute('aria-busy', 'true');
    });

    it('disables every Reset trigger and ignores a second reset while busy', async () => {
      const user = userEvent.setup();
      const { fn, resolve } = pendingReset();
      renderTable({ onResetPassword: fn });
      await resetThrough(user, ALICE);
      const rowTrigger = screen.getAllByRole('button', {
        name: strings.resetPasswordBusyNamed({ user: ALICE }),
      })[0];

      expect(rowTrigger).toHaveAttribute('aria-disabled', 'true');
      expect(resetTrigger(BOB)).toHaveAttribute('aria-disabled', 'true');
      await act(async () => {
        resetTrigger(BOB).click();
      });
      expect(within(dialog()).getByText(ALICE)).toBeInTheDocument();
      expect(fn).toHaveBeenCalledTimes(1);

      await act(async () => {
        resolve({ userId: 'u_x', appId: 'urls4irl', password: PASSWORD });
      });
    });

    it('keeps one persistent status node across phases and never puts the password in it', async () => {
      const user = userEvent.setup();
      const { fn, resolve } = pendingReset();
      renderTable({ onResetPassword: fn });
      await user.click(resetTrigger(ALICE));
      const statusNode = within(dialog()).getByRole('status');
      expect(statusNode).toBeEmptyDOMElement();

      await user.click(confirmButton(ALICE));
      expect(within(dialog()).getByRole('status')).toBe(statusNode);
      expect(statusNode).toBeEmptyDOMElement();

      await act(async () => {
        resolve({ userId: 'u_x', appId: 'urls4irl', password: PASSWORD });
      });
      expect(within(dialog()).getAllByRole('status')[0]).toBe(statusNode);
      expect(statusNode).toHaveTextContent(strings.resetPasswordRevealLead({ user: ALICE }));
      expect(statusNode).not.toHaveTextContent(PASSWORD);
    });
  });

  describe('revealed phase', () => {
    it('shows the password in the dialog with Copy focused and no password outside it', async () => {
      const user = userEvent.setup();
      renderTable({ onResetPassword: resetOk() });

      await resetThrough(user, ALICE);

      expect(await within(dialog()).findByLabelText(strings.passwordLabel)).toHaveValue(PASSWORD);
      expect(dialog()).toHaveAccessibleName(strings.passwordGroupHeading);
      const field = within(dialog()).getByLabelText(strings.passwordLabel);
      expect(field).toHaveAttribute('readonly');
      expect(field).toHaveAttribute('autocomplete', 'off');
      expect(field).toHaveAttribute('spellcheck', 'false');
      expect(screen.getByRole('button', { name: strings.copyPassword })).toHaveFocus();
      expect(screen.getAllByDisplayValue(PASSWORD)).toHaveLength(1);
    });

    it('ignores the backdrop, and X, Escape and Done close it and clear the password', async () => {
      const user = userEvent.setup();
      renderTable({ onResetPassword: resetOk() });
      await resetThrough(user, ALICE);
      await within(dialog()).findByLabelText(strings.passwordLabel);

      await user.click(dialog().parentElement as HTMLElement);
      expect(dialog()).toBeInTheDocument();

      await user.click(
        within(dialog()).getByRole('button', {
          name: strings.resetPasswordDoneNamed({ user: ALICE }),
        }),
      );
      expect(screen.queryByRole('dialog')).not.toBeInTheDocument();
      expect(screen.queryByDisplayValue(PASSWORD)).not.toBeInTheDocument();
      expect(resetTrigger(ALICE)).toHaveFocus();
    });

    it.each([
      [
        'the X',
        async (user: ReturnType<typeof userEvent.setup>) =>
          user.click(within(dialog()).getByRole('button', { name: strings.closeDialog })),
      ],
      ['Escape', async (user: ReturnType<typeof userEvent.setup>) => user.keyboard('{Escape}')],
    ])('%s closes it, drops the password and refocuses the trigger', async (_label, exit) => {
      const user = userEvent.setup();
      renderTable({ onResetPassword: resetOk() });
      await resetThrough(user, ALICE);
      await within(dialog()).findByLabelText(strings.passwordLabel);

      await exit(user);

      expect(screen.queryByRole('dialog')).not.toBeInTheDocument();
      expect(screen.queryByDisplayValue(PASSWORD)).not.toBeInTheDocument();
      expect(resetTrigger(ALICE)).toHaveFocus();
    });

    it('shows no password when the same user is reset again from the confirm step', async () => {
      const user = userEvent.setup();
      const { fn } = pendingReset();
      const onResetPassword = vi
        .fn()
        .mockResolvedValueOnce({ userId: 'u_a', appId: 'urls4irl', password: PASSWORD })
        .mockImplementationOnce(fn);
      renderTable({ onResetPassword });
      await resetThrough(user, ALICE);
      await within(dialog()).findByLabelText(strings.passwordLabel);
      await user.keyboard('{Escape}');

      await user.click(resetTrigger(ALICE));

      expect(screen.queryByDisplayValue(PASSWORD)).not.toBeInTheDocument();
      expect(screen.queryByText(PASSWORD)).not.toBeInTheDocument();
    });

    it('copies the password, announces a fresh Copied node per click, and clears it on close', async () => {
      const user = userEvent.setup();
      const writeText = vi.fn().mockResolvedValue(undefined);
      renderTable({ onResetPassword: resetOk() });
      setClipboard({ writeText });
      await resetThrough(user, ALICE);
      await within(dialog()).findByLabelText(strings.passwordLabel);

      const copyButton = screen.getByRole('button', { name: strings.copyPassword });
      expect(copyButton).toHaveAttribute('type', 'button');
      await user.click(copyButton);
      const first = await within(dialog()).findByText(strings.passwordCopied);
      await user.click(copyButton);
      await vi.waitFor(() => {
        expect(within(dialog()).getByText(strings.passwordCopied)).not.toBe(first);
      });
      expect(writeText).toHaveBeenCalledWith(PASSWORD);
      expect(first.closest('[role="status"]')).not.toBeNull();

      await user.keyboard('{Escape}');
      await user.click(resetTrigger(ALICE));
      expect(screen.queryByText(strings.passwordCopied)).not.toBeInTheDocument();
    });

    it('ignores a clipboard write that settles after the dialog closes', async () => {
      const user = userEvent.setup();
      let settle: () => void = () => undefined;
      const writeText = vi.fn().mockReturnValue(
        new Promise<void>((resolve) => {
          settle = resolve;
        }),
      );
      renderTable({ onResetPassword: resetOk() });
      setClipboard({ writeText });
      await resetThrough(user, ALICE);
      await within(dialog()).findByLabelText(strings.passwordLabel);

      await user.click(screen.getByRole('button', { name: strings.copyPassword }));
      await user.keyboard('{Escape}');
      await act(async () => {
        settle();
        await new Promise((done) => setTimeout(done, 0));
      });

      expect(screen.queryByText(strings.passwordCopied)).not.toBeInTheDocument();
    });

    it.each([
      ['missing', undefined],
      ['rejecting', { writeText: vi.fn().mockRejectedValue(new Error('denied')) }],
    ])('shows the fallback when the Clipboard API is %s', async (_label, clipboard) => {
      const user = userEvent.setup();
      renderTable({ onResetPassword: resetOk() });
      setClipboard(clipboard);
      await resetThrough(user, ALICE);
      await within(dialog()).findByLabelText(strings.passwordLabel);

      await user.click(screen.getByRole('button', { name: strings.copyPassword }));

      expect(await screen.findByText(strings.passwordCopyUnavailable)).toBeInTheDocument();
      expect(within(dialog()).getByLabelText(strings.passwordLabel)).toHaveValue(PASSWORD);
    });

    it('selects the whole password when its field is focused', async () => {
      const user = userEvent.setup();
      renderTable({ onResetPassword: resetOk() });
      await resetThrough(user, ALICE);
      const field = (await screen.findByLabelText(strings.passwordLabel)) as HTMLInputElement;

      await user.click(field);

      expect(field.selectionStart).toBe(0);
      expect(field.selectionEnd).toBe(PASSWORD.length);
    });

    it('stays open with the password copyable when its row leaves the list', async () => {
      const user = userEvent.setup();
      const { rerenderWith } = renderTable({ onResetPassword: resetOk() });
      await resetThrough(user, ALICE);
      await within(dialog()).findByLabelText(strings.passwordLabel);

      rerenderWith([bobRow]);

      expect(screen.getByDisplayValue(PASSWORD)).toBeInTheDocument();
      expect(screen.getByRole('button', { name: strings.copyPassword })).toBeInTheDocument();

      await user.keyboard('{Escape}');
      expect(screen.queryByRole('dialog')).not.toBeInTheDocument();
      expect(screen.getByRole('heading', { name: strings.usersHeading })).toHaveFocus();
    });

    it('closes onto the Provisioning tab when the panel is hidden, then the heading once visible', async () => {
      const user = userEvent.setup();
      const { panel } = renderTable({ onResetPassword: resetOk() });
      await resetThrough(user, ALICE);
      await within(dialog()).findByLabelText(strings.passwordLabel);

      panel().hidden = true;
      await user.keyboard('{Escape}');
      expect(screen.getByRole('button', { name: 'Provisioning' })).toHaveFocus();

      panel().hidden = false;
      await user.click(resetTrigger(ALICE));
      await user.click(confirmButton(ALICE));
      await within(dialog()).findByLabelText(strings.passwordLabel);
      const trigger = resetTrigger(ALICE);
      trigger.remove();
      await user.keyboard('{Escape}');
      expect(screen.getByRole('heading', { name: strings.usersHeading })).toHaveFocus();
    });
  });

  describe('failed phase', () => {
    it.each([
      ['404', new ApiError({ status: 404, message: 'user does not exist' }), 'notfound'],
      [
        '429 with Retry-After',
        new ApiError({ status: 429, message: 'too many', retryAfterSeconds: 42 }),
        '42',
      ],
      ['429 without Retry-After', new ApiError({ status: 429, message: 'too many' }), '60'],
      ['500', new ApiError({ status: 500, message: 'boom' }), 'failed'],
      ['a network error', new TypeError('network down'), 'failed'],
    ])('shows the right alert and no password on %s', async (_label, rejection, expected) => {
      const user = userEvent.setup();
      renderTable({ onResetPassword: vi.fn().mockRejectedValue(rejection) });

      await resetThrough(user, ALICE);

      const message =
        expected === 'notfound'
          ? strings.resetPasswordNotFound
          : expected === 'failed'
            ? strings.resetPasswordFailed
            : strings.resetPasswordTooMany({ seconds: Number(expected) });
      const alert = await within(dialog()).findByRole('alert');
      expect(alert).toHaveTextContent(message);
      expect(screen.queryByLabelText(strings.passwordLabel)).not.toBeInTheDocument();
      const close = closeActionButton();
      expect(close).toHaveFocus();
      expect(document.activeElement).not.toBe(document.body);
      expect(screen.getAllByRole('button', { name: /^Reset password for/ })[0]).not.toHaveAttribute(
        'aria-disabled',
      );
    });

    it('closes via Close, X or Escape with focus back on the trigger', async () => {
      const user = userEvent.setup();
      renderTable({
        onResetPassword: vi.fn().mockRejectedValue(new ApiError({ status: 500, message: 'x' })),
      });
      await resetThrough(user, ALICE);
      await within(dialog()).findByRole('alert');

      await user.click(closeActionButton());

      expect(screen.queryByRole('dialog')).not.toBeInTheDocument();
      expect(resetTrigger(ALICE)).toHaveFocus();

      await resetThrough(user, ALICE);
      await within(dialog()).findByRole('alert');
      await user.keyboard('{Escape}');
      expect(screen.queryByRole('dialog')).not.toBeInTheDocument();
      expect(resetTrigger(ALICE)).toHaveFocus();

      await resetThrough(user, ALICE);
      await within(dialog()).findByRole('alert');
      await user.click(within(dialog()).getByText('×', { selector: 'button' }));
      expect(screen.queryByRole('dialog')).not.toBeInTheDocument();
      expect(resetTrigger(ALICE)).toHaveFocus();
    });

    it('persists with its message when the row leaves the list', async () => {
      const user = userEvent.setup();
      const { rerenderWith } = renderTable({
        onResetPassword: vi.fn().mockRejectedValue(new ApiError({ status: 500, message: 'x' })),
      });
      await resetThrough(user, ALICE);
      await within(dialog()).findByRole('alert');

      rerenderWith([bobRow]);

      expect(within(dialog()).getByRole('alert')).toHaveTextContent(strings.resetPasswordFailed);
    });

    it('focuses the Provisioning tab on Close when the panel is hidden', async () => {
      const user = userEvent.setup();
      const { panel } = renderTable({
        onResetPassword: vi.fn().mockRejectedValue(new ApiError({ status: 500, message: 'x' })),
      });
      await resetThrough(user, ALICE);
      await within(dialog()).findByRole('alert');

      panel().hidden = true;
      await user.click(closeActionButton());

      expect(screen.getByRole('button', { name: 'Provisioning' })).toHaveFocus();
    });
  });

  describe('timeout', () => {
    it('fails after 30s with the timed-out message, ignores a late success and allows a new reset', async () => {
      vi.useFakeTimers({ shouldAdvanceTime: true });
      const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime });
      const { fn, resolve } = pendingReset();
      renderTable({ onResetPassword: fn });
      await resetThrough(user, ALICE);
      expect(dialog()).toHaveAttribute('aria-busy', 'true');

      await act(async () => {
        vi.advanceTimersByTime(20_000);
      });
      expect(dialog()).toHaveAttribute('aria-busy', 'true');
      await act(async () => {
        vi.advanceTimersByTime(10_000);
      });

      expect(within(dialog()).getByRole('alert')).toHaveTextContent(strings.resetPasswordTimedOut);
      expect(closeActionButton()).toHaveFocus();
      expect(resetTrigger(ALICE)).not.toHaveAttribute('aria-disabled');

      await act(async () => {
        resolve({ userId: 'u_x', appId: 'urls4irl', password: PASSWORD });
      });
      expect(screen.queryByDisplayValue(PASSWORD)).not.toBeInTheDocument();
      expect(screen.queryByText(PASSWORD)).not.toBeInTheDocument();
      expect(within(dialog()).getByRole('alert')).toHaveTextContent(strings.resetPasswordTimedOut);

      await user.click(closeActionButton());
      await user.click(resetTrigger(ALICE));
      expect(within(dialog()).getByText(strings.resetPasswordNotice)).toBeInTheDocument();
    });

    it('a timed-out request settling late cannot touch a newer reset in flight', async () => {
      vi.useFakeTimers({ shouldAdvanceTime: true });
      const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime });
      const first = pendingReset();
      const second = pendingReset();
      const onResetPassword = vi
        .fn()
        .mockImplementationOnce(first.fn)
        .mockImplementationOnce(second.fn);
      renderTable({ onResetPassword });
      await resetThrough(user, ALICE);
      await act(async () => {
        vi.advanceTimersByTime(30_000);
      });
      await user.click(closeActionButton());
      await resetThrough(user, ALICE);
      expect(dialog()).toHaveAttribute('aria-busy', 'true');

      await act(async () => {
        first.resolve({ userId: 'u_x', appId: 'urls4irl', password: PASSWORD });
      });

      expect(dialog()).toHaveAttribute('aria-busy', 'true');
      expect(resetTrigger(BOB)).toHaveAttribute('aria-disabled', 'true');
      expect(screen.queryByDisplayValue(PASSWORD)).not.toBeInTheDocument();
    });

    it('clears the timer when the request settles first', async () => {
      vi.useFakeTimers({ shouldAdvanceTime: true });
      const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime });
      const { fn, resolve } = pendingReset();
      renderTable({ onResetPassword: fn });
      const baselineTimers = vi.getTimerCount();
      await resetThrough(user, ALICE);

      await act(async () => {
        resolve({ userId: 'u_x', appId: 'urls4irl', password: PASSWORD });
      });
      await vi.waitFor(() => {
        expect(vi.getTimerCount()).toBe(baselineTimers);
      });
      await act(async () => {
        vi.advanceTimersByTime(60_000);
      });

      expect(screen.getByDisplayValue(PASSWORD)).toBeInTheDocument();
      expect(screen.queryByRole('alert')).not.toBeInTheDocument();
    });

    it('clears the timer when the table unmounts', async () => {
      vi.useFakeTimers({ shouldAdvanceTime: true });
      const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime });
      const { fn } = pendingReset();
      const { unmount } = renderTable({ onResetPassword: fn });
      await resetThrough(user, ALICE);
      expect(vi.getTimerCount()).toBeGreaterThan(0);

      unmount();

      expect(vi.getTimerCount()).toBe(0);
    });
  });

  describe('navigation and leaks', () => {
    it.each(['confirm', 'busy', 'revealed', 'failed'])(
      'a hashchange does not close the dialog in the %s phase',
      async (phase) => {
        const user = userEvent.setup();
        const pending = pendingReset();
        const onResetPassword =
          phase === 'failed'
            ? vi.fn().mockRejectedValue(new ApiError({ status: 500, message: 'x' }))
            : phase === 'revealed'
              ? resetOk()
              : pending.fn;
        renderTable({ onResetPassword });
        await user.click(resetTrigger(ALICE));
        if (phase !== 'confirm') {
          await user.click(confirmButton(ALICE));
        }
        if (phase === 'revealed') {
          await within(dialog()).findByLabelText(strings.passwordLabel);
        }
        if (phase === 'failed') {
          await within(dialog()).findByRole('alert');
        }

        act(() => {
          window.dispatchEvent(new HashChangeEvent('hashchange'));
        });

        expect(dialog()).toBeInTheDocument();
      },
    );

    it('never sends the password to console, storage or any status/alert text', async () => {
      const user = userEvent.setup();
      const sinks = [
        vi.spyOn(console, 'log').mockImplementation(() => undefined),
        vi.spyOn(console, 'info').mockImplementation(() => undefined),
        vi.spyOn(console, 'warn').mockImplementation(() => undefined),
        vi.spyOn(console, 'error').mockImplementation(() => undefined),
        vi.spyOn(console, 'debug').mockImplementation(() => undefined),
        vi.spyOn(Storage.prototype, 'setItem'),
      ];
      renderTable({ onResetPassword: resetOk() });
      setClipboard({ writeText: vi.fn().mockResolvedValue(undefined) });

      await resetThrough(user, ALICE);
      expect(await screen.findByDisplayValue(PASSWORD)).toBeInTheDocument();
      await user.click(screen.getByRole('button', { name: strings.copyPassword }));
      await screen.findByText(strings.passwordCopied);

      for (const sink of sinks) {
        expect(JSON.stringify(sink.mock.calls)).not.toContain(PASSWORD);
      }
      for (const node of [...screen.queryAllByRole('status'), ...screen.queryAllByRole('alert')]) {
        expect(node).not.toHaveTextContent(PASSWORD);
      }
    });
  });
});
