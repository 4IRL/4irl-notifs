import { cleanup, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { ApiError } from '../api/client';
import type { TestNotifyParams, TestNotifyResult, UserSummary } from '../api/client';
import { strings } from '../strings';
import { SendTestNotification } from './SendTestNotification';

afterEach(() => {
  cleanup();
});

const aliceHash = '76gzqgp4byjl6dje';
const bobHash = '4x2k9m7pqrs1twvz';
const daveHash = '9df3hh0aa5ee2bbc';
// Deliberately absent from emailByPersonHash to exercise the raw-identifier fallback.
const carolHash = 'c4r0lnoemailhash';
const aliceEmail = 'alice@example.com';
const bobEmail = 'bob@example.com';
const daveEmail = 'dave@example.com';

function user(userId: string, apps: string[]): UserSummary {
  return { userId, apps, topicPatterns: apps.map((app) => `${app}-*`) };
}

// Two subscribers on `urls4irl` and one on `zapp` (sorts after urls4irl), so the
// first-sorted app option is deterministically `urls4irl`.
const defaultUsers: UserSummary[] = [
  user(`u_${aliceHash}`, ['urls4irl']),
  user(`u_${bobHash}`, ['urls4irl']),
  user(`u_${daveHash}`, ['zapp']),
];

const emailByPersonHash = new Map<string, string>([
  [aliceHash, aliceEmail],
  [bobHash, bobEmail],
  [daveHash, daveEmail],
]);

function renderSection(
  overrides: {
    users?: UserSummary[];
    loading?: boolean;
    onSendTest?: (params: TestNotifyParams) => Promise<TestNotifyResult>;
  } = {},
) {
  const onSendTest = overrides.onSendTest ?? vi.fn().mockResolvedValue({ results: [] });
  const utils = render(
    <SendTestNotification
      users={overrides.users ?? defaultUsers}
      loading={overrides.loading ?? false}
      emailByPersonHash={emailByPersonHash}
      onSendTest={onSendTest}
    />,
  );
  return { ...utils, onSendTest };
}

describe('SendTestNotification', () => {
  it('renders an app option per subscriber app derived from users', () => {
    renderSection();

    expect(screen.getByRole('heading', { name: strings.sendTestHeading })).toBeInTheDocument();
    expect(screen.getByRole('option', { name: 'urls4irl' })).toBeInTheDocument();
    expect(screen.getByRole('option', { name: 'zapp' })).toBeInTheDocument();
  });

  it('scopes the rows to the selected target app', async () => {
    const person = userEvent.setup();
    renderSection();

    // Defaults to the first sorted app (urls4irl): alice + bob visible, dave not.
    expect(screen.getByText(aliceEmail)).toBeInTheDocument();
    expect(screen.getByText(bobEmail)).toBeInTheDocument();
    expect(screen.queryByText(daveEmail)).not.toBeInTheDocument();

    await person.selectOptions(screen.getByLabelText(strings.sendTestTargetAppLabel), 'zapp');

    expect(screen.getByText(daveEmail)).toBeInTheDocument();
    expect(screen.queryByText(aliceEmail)).not.toBeInTheDocument();
    expect(screen.queryByText(bobEmail)).not.toBeInTheDocument();
  });

  it('toggles selection and updates the selected count', async () => {
    const person = userEvent.setup();
    renderSection();

    expect(screen.getByText(strings.sendTestSelectedCount({ count: 0 }))).toBeInTheDocument();

    await person.click(
      screen.getByRole('checkbox', { name: strings.sendTestSelectAria({ who: aliceEmail }) }),
    );
    expect(screen.getByText(strings.sendTestSelectedCount({ count: 1 }))).toBeInTheDocument();

    await person.click(
      screen.getByRole('checkbox', { name: strings.sendTestSelectAria({ who: aliceEmail }) }),
    );
    expect(screen.getByText(strings.sendTestSelectedCount({ count: 0 }))).toBeInTheDocument();
  });

  it('disables Send when nothing is selected', () => {
    renderSection();

    expect(screen.getByRole('button', { name: strings.sendTestAction })).toBeDisabled();
  });

  it('enables Send once at least one recipient is selected', async () => {
    const person = userEvent.setup();
    renderSection();

    await person.click(
      screen.getByRole('checkbox', { name: strings.sendTestSelectAria({ who: aliceEmail }) }),
    );
    expect(screen.getByRole('button', { name: strings.sendTestAction })).not.toBeDisabled();
  });

  it('disables the picker controls, row checkboxes, and Send while a send is in flight', async () => {
    const person = userEvent.setup();
    let resolveSend: (value: TestNotifyResult) => void = () => {};
    const pending = new Promise<TestNotifyResult>((resolve) => {
      resolveSend = resolve;
    });
    const onSendTest = vi.fn().mockReturnValue(pending);
    renderSection({ onSendTest });

    await person.click(
      screen.getByRole('checkbox', { name: strings.sendTestSelectAria({ who: aliceEmail }) }),
    );
    await person.click(screen.getByRole('button', { name: strings.sendTestAction }));

    expect(await screen.findByRole('button', { name: strings.sendTestSending })).toBeDisabled();
    expect(screen.getByLabelText(strings.sendTestTargetAppLabel)).toBeDisabled();
    expect(screen.getByLabelText(strings.sendTestChannelLabel)).toBeDisabled();
    expect(screen.getByLabelText(strings.sendTestMessageLabel)).toBeDisabled();
    for (const checkbox of screen.getAllByRole('checkbox')) {
      expect(checkbox).toBeDisabled();
    }

    resolveSend({ results: [] });
    await waitFor(() =>
      expect(screen.getByRole('button', { name: strings.sendTestAction })).not.toBeDisabled(),
    );
  });

  it('calls onSendTest with the mapped app, person-hash recipients, channel, and message', async () => {
    const person = userEvent.setup();
    const { onSendTest } = renderSection();

    await person.click(
      screen.getByRole('checkbox', { name: strings.sendTestSelectAria({ who: aliceEmail }) }),
    );
    await person.click(
      screen.getByRole('checkbox', { name: strings.sendTestSelectAria({ who: bobEmail }) }),
    );
    await person.click(screen.getByRole('button', { name: strings.sendTestAction }));

    await waitFor(() => {
      expect(onSendTest).toHaveBeenCalledTimes(1);
    });
    const call = (onSendTest as ReturnType<typeof vi.fn>).mock.calls[0][0];
    expect(call.appId).toBe('urls4irl');
    expect(call.channel).toBe(strings.sendTestDefaultChannel);
    expect(call.message).toBe(strings.sendTestDefaultMessage);
    expect(call.recipients).toEqual(expect.arrayContaining([aliceHash, bobHash]));
    expect(call.recipients).toHaveLength(2);
  });

  it('blocks send and shows a role=alert error when the channel is invalid', async () => {
    const person = userEvent.setup();
    const { onSendTest } = renderSection();

    await person.click(
      screen.getByRole('checkbox', { name: strings.sendTestSelectAria({ who: aliceEmail }) }),
    );
    const channelInput = screen.getByLabelText(strings.sendTestChannelLabel);
    await person.clear(channelInput);
    await person.type(channelInput, 'Bad-Channel');
    await person.click(screen.getByRole('button', { name: strings.sendTestAction }));

    const alert = await screen.findByRole('alert');
    expect(alert).toHaveTextContent(strings.sendTestInvalidChannel);
    expect(onSendTest).not.toHaveBeenCalled();
  });

  it('surfaces a rejected onSendTest in the same role=alert element', async () => {
    const person = userEvent.setup();
    const onSendTest = vi
      .fn()
      .mockRejectedValue(new ApiError({ status: 500, message: 'publisher not configured' }));
    renderSection({ onSendTest });

    await person.click(
      screen.getByRole('checkbox', { name: strings.sendTestSelectAria({ who: aliceEmail }) }),
    );
    await person.click(screen.getByRole('button', { name: strings.sendTestAction }));

    const alert = await screen.findByRole('alert');
    expect(alert).toHaveTextContent('publisher not configured');
  });

  it('does not dispatch an empty batch when Enter is pressed with nothing selected', async () => {
    const person = userEvent.setup();
    const { onSendTest } = renderSection();

    // Nothing selected; press Enter from within the message input (inside the
    // form) — the disabled Send button only blocks clicks, so the handler's own
    // empty-selection guard must prevent the dispatch.
    const messageInput = screen.getByLabelText(strings.sendTestMessageLabel);
    await person.type(messageInput, '{Enter}');

    expect(onSendTest).not.toHaveBeenCalled();
  });

  it('clears a prior error when the target app changes', async () => {
    const person = userEvent.setup();
    const onSendTest = vi
      .fn()
      .mockRejectedValue(new ApiError({ status: 500, message: 'publisher not configured' }));
    renderSection({ onSendTest });

    await person.click(
      screen.getByRole('checkbox', { name: strings.sendTestSelectAria({ who: aliceEmail }) }),
    );
    await person.click(screen.getByRole('button', { name: strings.sendTestAction }));
    expect(await screen.findByRole('alert')).toHaveTextContent('publisher not configured');

    await person.selectOptions(screen.getByLabelText(strings.sendTestTargetAppLabel), 'zapp');

    expect(screen.queryByRole('alert')).not.toBeInTheDocument();
  });

  it('renders the summary plus green Delivered and red Failed pills for a mixed result set', async () => {
    const person = userEvent.setup();
    const onSendTest = vi.fn().mockResolvedValue({
      results: [
        {
          recipient: aliceHash,
          userId: `u_${aliceHash}`,
          topic: `urls4irl-${aliceHash}-alerts`,
          ok: true,
          messageId: 'VkT2p9wQ',
          error: '',
        },
        {
          recipient: bobHash,
          userId: `u_${bobHash}`,
          topic: `urls4irl-${bobHash}-alerts`,
          ok: false,
          messageId: '',
          error: 'ntfy publish failed (503)',
        },
      ],
    });
    renderSection({ onSendTest });

    await person.click(
      screen.getByRole('checkbox', { name: strings.sendTestSelectAria({ who: aliceEmail }) }),
    );
    await person.click(
      screen.getByRole('checkbox', { name: strings.sendTestSelectAria({ who: bobEmail }) }),
    );
    await person.click(screen.getByRole('button', { name: strings.sendTestAction }));

    const status = await screen.findByRole('status');
    expect(status).toHaveTextContent(
      strings.sendTestResultsSummary({ sent: 1, total: 2, failed: 1 }),
    );
    expect(within(status).getByText(strings.sendTestDelivered)).toBeInTheDocument();
    expect(within(status).getByText(strings.sendTestFailed)).toBeInTheDocument();
    expect(within(status).getByText('ntfy publish failed (503)')).toBeInTheDocument();
  });

  it('wraps the results in an aria-live polite region', async () => {
    const person = userEvent.setup();
    const onSendTest = vi.fn().mockResolvedValue({
      results: [
        {
          recipient: aliceHash,
          userId: `u_${aliceHash}`,
          topic: `urls4irl-${aliceHash}-alerts`,
          ok: true,
          messageId: 'VkT2p9wQ',
          error: '',
        },
      ],
    });
    renderSection({ onSendTest });

    await person.click(
      screen.getByRole('checkbox', { name: strings.sendTestSelectAria({ who: aliceEmail }) }),
    );
    await person.click(screen.getByRole('button', { name: strings.sendTestAction }));

    const status = await screen.findByRole('status');
    expect(status).toHaveAttribute('aria-live', 'polite');
  });

  it('clears the selection and results when the target app changes', async () => {
    const person = userEvent.setup();
    const onSendTest = vi.fn().mockResolvedValue({
      results: [
        {
          recipient: aliceHash,
          userId: `u_${aliceHash}`,
          topic: `urls4irl-${aliceHash}-alerts`,
          ok: true,
          messageId: 'VkT2p9wQ',
          error: '',
        },
      ],
    });
    renderSection({ onSendTest });

    await person.click(
      screen.getByRole('checkbox', { name: strings.sendTestSelectAria({ who: aliceEmail }) }),
    );
    await person.click(screen.getByRole('button', { name: strings.sendTestAction }));
    expect(await screen.findByRole('status')).toBeInTheDocument();

    await person.selectOptions(screen.getByLabelText(strings.sendTestTargetAppLabel), 'zapp');

    expect(screen.getByText(strings.sendTestSelectedCount({ count: 0 }))).toBeInTheDocument();
    expect(screen.queryByRole('status')).not.toBeInTheDocument();
  });

  it('drops a stale selection when the selected user is deprovisioned from the target app', async () => {
    const person = userEvent.setup();
    const { rerender } = renderSection();

    await person.click(
      screen.getByRole('checkbox', { name: strings.sendTestSelectAria({ who: aliceEmail }) }),
    );
    expect(screen.getByText(strings.sendTestSelectedCount({ count: 1 }))).toBeInTheDocument();

    // Per-app deprovision: alice still exists in `users` but no longer carries
    // `urls4irl`; bob keeps urls4irl so the target app stays valid.
    rerender(
      <SendTestNotification
        users={[
          user(`u_${aliceHash}`, []),
          user(`u_${bobHash}`, ['urls4irl']),
          user(`u_${daveHash}`, ['zapp']),
        ]}
        loading={false}
        emailByPersonHash={emailByPersonHash}
        onSendTest={vi.fn().mockResolvedValue({ results: [] })}
      />,
    );

    await waitFor(() =>
      expect(screen.getByText(strings.sendTestSelectedCount({ count: 0 }))).toBeInTheDocument(),
    );
  });

  it('defaults the target app to the first sorted option once users load asynchronously', async () => {
    const { rerender } = renderSection({ users: [], loading: true });

    expect(screen.queryByRole('option', { name: 'urls4irl' })).not.toBeInTheDocument();

    rerender(
      <SendTestNotification
        users={defaultUsers}
        loading={false}
        emailByPersonHash={emailByPersonHash}
        onSendTest={vi.fn().mockResolvedValue({ results: [] })}
      />,
    );

    await waitFor(() =>
      expect(
        (screen.getByLabelText(strings.sendTestTargetAppLabel) as HTMLSelectElement).value,
      ).toBe('urls4irl'),
    );
  });

  it('shows the loading status and no table while loading', () => {
    renderSection({ loading: true });

    expect(screen.getByText(strings.sendTestLoading)).toBeInTheDocument();
    expect(screen.queryByText(strings.sendTestNoUsersForApp)).not.toBeInTheDocument();
    expect(screen.queryByRole('table')).not.toBeInTheDocument();
  });

  it('shows the empty state (and hides the select hint) when no users are scoped to the app', () => {
    // Only a publisher identity present — no `u_`-prefixed subscribers — so no
    // app options and no scoped rows.
    renderSection({
      users: [user('urls4irl-publisher', ['urls4irl'])],
      loading: false,
    });

    expect(screen.getByText(strings.sendTestNoUsersForApp)).toBeInTheDocument();
    expect(screen.queryByText(strings.sendTestSelectHint)).not.toBeInTheDocument();
  });

  it('falls back to the raw userId in a scoped row when the personHash has no email mapping', () => {
    // A subscriber on urls4irl whose personHash is absent from emailByPersonHash;
    // the row must display the raw `u_`-prefixed identifier rather than blank
    // (exercises the `emailByPersonHash.get(personHash) ?? userId` fallback).
    renderSection({
      users: [user(`u_${carolHash}`, ['urls4irl'])],
    });

    expect(screen.getByText(`u_${carolHash}`)).toBeInTheDocument();
  });

  it('falls back to the raw userId in the results panel when the personHash has no email mapping', async () => {
    const person = userEvent.setup();
    // Interaction uses alice (mapped); the returned result carries an unmapped
    // personHash, so the results row must fall back to the raw `result.userId`
    // (exercises the `emailByPersonHash.get(result.userId.slice(2)) ?? result.userId` fallback).
    const onSendTest = vi.fn().mockResolvedValue({
      results: [
        {
          recipient: carolHash,
          userId: `u_${carolHash}`,
          topic: `urls4irl-${carolHash}-alerts`,
          ok: true,
          messageId: 'VkT2p9wQ',
          error: '',
        },
      ],
    });
    renderSection({ onSendTest });

    await person.click(
      screen.getByRole('checkbox', { name: strings.sendTestSelectAria({ who: aliceEmail }) }),
    );
    await person.click(screen.getByRole('button', { name: strings.sendTestAction }));

    const status = await screen.findByRole('status');
    expect(within(status).getByText(`u_${carolHash}`)).toBeInTheDocument();
  });
});
