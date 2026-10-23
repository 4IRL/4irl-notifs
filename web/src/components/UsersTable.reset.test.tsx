import { cleanup, render, screen, within } from '@testing-library/react';
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

const originalClipboard = Object.getOwnPropertyDescriptor(navigator, 'clipboard');

afterEach(() => {
  cleanup();
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
  onResetPassword?: (params: { appId: string; email: string }) => Promise<ResetPasswordResult>;
  emailByPersonHash?: Map<string, string>;
}) {
  const view = render(
    <UsersTable
      users={users}
      loading={false}
      emailByPersonHash={emailByPersonHash}
      onDeprovision={vi.fn()}
      onDelete={vi.fn()}
      onResetPassword={onResetPassword}
    />,
  );
  return {
    ...view,
    rerenderWith: (nextUsers: UserSummary[]) =>
      view.rerender(
        <UsersTable
          users={nextUsers}
          loading={false}
          emailByPersonHash={emailByPersonHash}
          onDeprovision={vi.fn()}
          onDelete={vi.fn()}
          onResetPassword={onResetPassword}
        />,
      ),
  };
}

function resetTrigger(who: string) {
  return screen.getByRole('button', { name: strings.resetPasswordTriggerNamed({ user: who }) });
}

function resetOk(): (params: { appId: string; email: string }) => Promise<ResetPasswordResult> {
  return vi.fn().mockResolvedValue({ userId: 'u_x', appId: 'urls4irl', password: PASSWORD });
}

async function resetThrough(user: ReturnType<typeof userEvent.setup>, who: string) {
  await user.click(resetTrigger(who));
  await user.click(
    screen.getByRole('button', { name: strings.resetPasswordConfirmNamed({ user: who }) }),
  );
}

describe('UsersTable operator password reset', () => {
  it('offers Reset only on person rows whose email is known, and hints on the rest', () => {
    renderTable({
      users: [aliceRow, publisherRow, { ...bobRow, userId: 'u_cccccccccccccccc' }],
      onResetPassword: resetOk(),
    });

    expect(resetTrigger(ALICE)).toBeInTheDocument();
    expect(screen.getAllByRole('button', { name: /^Reset password for/ })).toHaveLength(1);
    // The unknown-email person row says why, the publisher row says nothing.
    expect(screen.getAllByText(strings.resetPasswordUnavailable)).toHaveLength(1);
  });

  it('offers no Reset action without the handler', () => {
    renderTable({});

    expect(screen.queryByRole('button', { name: /Reset password/ })).not.toBeInTheDocument();
    expect(screen.queryByRole('status')).not.toBeInTheDocument();
  });

  it('opens a confirm with the warning and focus on Confirm, and Cancel returns focus without a call', async () => {
    const user = userEvent.setup();
    const onResetPassword = resetOk();
    renderTable({ onResetPassword });

    await user.click(resetTrigger(ALICE));

    const confirm = screen.getByRole('button', {
      name: strings.resetPasswordConfirmNamed({ user: ALICE }),
    });
    expect(confirm).toHaveFocus();
    expect(screen.getByText(strings.resetPasswordNotice)).toBeInTheDocument();
    expect(onResetPassword).not.toHaveBeenCalled();

    await user.click(
      screen.getByRole('button', {
        name: strings.cancelActionNamed({
          subject: strings.resetPasswordTriggerNamed({ user: ALICE }),
        }),
      }),
    );

    expect(onResetPassword).not.toHaveBeenCalled();
    expect(resetTrigger(ALICE)).toHaveFocus();
  });

  it('calls reset once with the first app and the email after Confirm, and reveals the password', async () => {
    const user = userEvent.setup();
    const onResetPassword = resetOk();
    renderTable({ onResetPassword });

    await resetThrough(user, ALICE);

    expect(onResetPassword).toHaveBeenCalledTimes(1);
    expect(onResetPassword).toHaveBeenCalledWith({ appId: 'urls4irl', email: ALICE });
    const status = await screen.findByRole('status');
    expect(status).toHaveTextContent(strings.resetPasswordRevealLead({ user: ALICE }));
    expect(status).toHaveAttribute('aria-atomic', 'false');
    const group = screen.getByRole('group', { name: strings.passwordGroupHeading });
    const field = within(group).getByLabelText(strings.passwordLabel);
    expect(field).toHaveValue(PASSWORD);
    expect(field).toHaveAttribute('readonly');
    expect(field).toHaveAttribute('autocomplete', 'off');
    expect(field).toHaveAttribute('spellcheck', 'false');
    expect(field).not.toHaveAttribute('title');
    expect(field).not.toHaveAttribute('aria-label');
    // The password is never inside the live region.
    expect(status).not.toHaveTextContent(PASSWORD);
    // The reveal does not steal focus: it stays on the row's trigger.
    expect(resetTrigger(ALICE)).toHaveFocus();
  });

  it('has exactly one status region, present and empty before any reset', () => {
    renderTable({ onResetPassword: resetOk() });

    expect(screen.getAllByRole('status')).toHaveLength(1);
    expect(screen.getByRole('status')).toBeEmptyDOMElement();
  });

  it('marks the trigger busy while the request runs and ignores a second reset', async () => {
    const user = userEvent.setup();
    let resolveReset: (result: ResetPasswordResult) => void = () => undefined;
    const onResetPassword = vi.fn().mockReturnValue(
      new Promise<ResetPasswordResult>((resolve) => {
        resolveReset = resolve;
      }),
    );
    renderTable({ onResetPassword });

    await resetThrough(user, ALICE);

    const busy = screen.getByRole('button', {
      name: strings.resetPasswordBusyNamed({ user: ALICE }),
    });
    expect(busy).toHaveAttribute('aria-disabled', 'true');
    expect(busy).toHaveTextContent(strings.resettingPasswordAction);
    await user.click(resetTrigger(BOB));
    expect(
      screen.queryByRole('button', { name: strings.resetPasswordConfirmNamed({ user: BOB }) }),
    ).not.toBeInTheDocument();
    expect(onResetPassword).toHaveBeenCalledTimes(1);

    resolveReset({ userId: 'u_x', appId: 'urls4irl', password: PASSWORD });
    expect(await screen.findByLabelText(strings.passwordLabel)).toHaveValue(PASSWORD);
  });

  it('copies the password, announces a fresh Copied node per click, and clears it on Done', async () => {
    const user = userEvent.setup();
    const writeText = vi.fn().mockResolvedValue(undefined);
    renderTable({ onResetPassword: resetOk() });
    setClipboard({ writeText });
    await resetThrough(user, ALICE);
    await screen.findByLabelText(strings.passwordLabel);

    const copyButton = screen.getByRole('button', { name: strings.copyPassword });
    expect(copyButton).toHaveAttribute('type', 'button');
    await user.click(copyButton);
    const first = await screen.findByText(strings.passwordCopied);
    await user.click(copyButton);
    await vi.waitFor(() => {
      expect(screen.getByText(strings.passwordCopied)).not.toBe(first);
    });
    expect(writeText).toHaveBeenCalledWith(PASSWORD);
    expect(within(screen.getByRole('status')).getByText(strings.passwordCopied)).toBeVisible();

    await user.click(
      screen.getByRole('button', { name: strings.resetPasswordDoneNamed({ user: ALICE }) }),
    );

    expect(screen.queryByLabelText(strings.passwordLabel)).not.toBeInTheDocument();
    expect(screen.queryByText(PASSWORD)).not.toBeInTheDocument();
    expect(screen.getByRole('status')).toBeEmptyDOMElement();
    expect(resetTrigger(ALICE)).toHaveFocus();
  });

  it('ignores a clipboard write that settles after Done', async () => {
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
    await screen.findByLabelText(strings.passwordLabel);

    await user.click(screen.getByRole('button', { name: strings.copyPassword }));
    await user.click(
      screen.getByRole('button', { name: strings.resetPasswordDoneNamed({ user: ALICE }) }),
    );
    settle();
    await Promise.resolve();

    expect(screen.queryByText(strings.passwordCopied)).not.toBeInTheDocument();
  });

  it('clears an earlier reveal when the next reset fails', async () => {
    const user = userEvent.setup();
    const onResetPassword = vi
      .fn()
      .mockResolvedValueOnce({ userId: 'u_a', appId: 'urls4irl', password: PASSWORD })
      .mockRejectedValueOnce(new ApiError({ status: 500, message: 'boom' }));
    renderTable({ onResetPassword });
    await resetThrough(user, ALICE);
    await screen.findByDisplayValue(PASSWORD);

    await resetThrough(user, ALICE);

    expect(await screen.findByText(strings.resetPasswordFailed)).toBeInTheDocument();
    expect(screen.queryByDisplayValue(PASSWORD)).not.toBeInTheDocument();
  });

  it.each([
    ['missing', undefined],
    ['rejecting', { writeText: vi.fn().mockRejectedValue(new Error('denied')) }],
  ])('shows the fallback when the Clipboard API is %s', async (_label, clipboard) => {
    const user = userEvent.setup();
    renderTable({ onResetPassword: resetOk() });
    setClipboard(clipboard);
    await resetThrough(user, ALICE);
    await screen.findByLabelText(strings.passwordLabel);

    await user.click(screen.getByRole('button', { name: strings.copyPassword }));

    expect(await screen.findByText(strings.passwordCopyUnavailable)).toBeInTheDocument();
    expect(screen.getByLabelText(strings.passwordLabel)).toHaveValue(PASSWORD);
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

  it('replaces the first reveal when another person is reset', async () => {
    const user = userEvent.setup();
    const onResetPassword = vi
      .fn()
      .mockResolvedValueOnce({ userId: 'u_a', appId: 'urls4irl', password: PASSWORD })
      .mockResolvedValueOnce({ userId: 'u_b', appId: 'urls4irl', password: 'zzzz-yyyy-xxxx-wwww' });
    renderTable({ onResetPassword });
    await resetThrough(user, ALICE);
    await screen.findByLabelText(strings.passwordLabel);

    await resetThrough(user, BOB);

    expect(await screen.findByDisplayValue('zzzz-yyyy-xxxx-wwww')).toBeInTheDocument();
    expect(screen.queryByDisplayValue(PASSWORD)).not.toBeInTheDocument();
    expect(screen.getByRole('status')).toHaveTextContent(
      strings.resetPasswordRevealLead({ user: BOB }),
    );
  });

  it('drops the reveal when its row leaves the list', async () => {
    const user = userEvent.setup();
    const { rerenderWith } = renderTable({ onResetPassword: resetOk() });
    await resetThrough(user, ALICE);
    await screen.findByLabelText(strings.passwordLabel);

    rerenderWith([bobRow]);

    expect(screen.queryByLabelText(strings.passwordLabel)).not.toBeInTheDocument();
    expect(screen.queryByDisplayValue(PASSWORD)).not.toBeInTheDocument();
  });

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
  ])('shows the right message and no password on %s', async (_label, rejection, expected) => {
    const user = userEvent.setup();
    renderTable({ onResetPassword: vi.fn().mockRejectedValue(rejection) });

    await resetThrough(user, ALICE);

    const status = await screen.findByRole('status');
    const message =
      expected === 'notfound'
        ? strings.resetPasswordNotFound
        : expected === 'failed'
          ? strings.resetPasswordFailed
          : strings.resetPasswordTooMany({ seconds: Number(expected) });
    await vi.waitFor(() => {
      expect(status).toHaveTextContent(message);
    });
    expect(screen.queryByLabelText(strings.passwordLabel)).not.toBeInTheDocument();
    expect(screen.queryByRole('group', { name: strings.passwordGroupHeading })).toBeNull();
    // The trigger is usable again for a retry.
    expect(resetTrigger(ALICE)).not.toHaveAttribute('aria-disabled');
  });

  it('never sends the password to console or browser storage', async () => {
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
  });
});
