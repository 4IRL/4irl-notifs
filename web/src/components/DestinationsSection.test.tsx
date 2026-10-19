import { cleanup, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { ApiError } from '../api/client';
import type { DeliveryApiClient, DestinationSummary } from '../api/deliveryClient';
import { strings } from '../strings';
import { DestinationsSection } from './DestinationsSection';

afterEach(() => {
  cleanup();
});

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (reason: unknown) => void;
  const promise = new Promise<T>((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, resolve, reject };
}

function buildDestination(
  overrides: Partial<DestinationSummary> & { name: string },
): DestinationSummary {
  return {
    type: 'ntfy',
    enabled: true,
    disabledReason: null,
    config: { topic: 'family_alerts' },
    secrets: {},
    createdAt: '2026-10-01T00:00:00Z',
    updatedAt: '2026-10-01T00:00:00Z',
    ...overrides,
  };
}

function buildWebhook(
  overrides: Partial<DestinationSummary> = {},
  name = 'chores_hook',
): DestinationSummary {
  return buildDestination({
    name,
    type: 'webhook',
    config: {},
    secrets: { url: 'https://chores.4irl.app/****', signing_secret: '****' },
    ...overrides,
  });
}

function buildClient(overrides: Partial<DeliveryApiClient> = {}): DeliveryApiClient {
  return {
    getReadiness: vi.fn(),
    listDestinations: vi.fn().mockResolvedValue([]),
    createDestination: vi.fn(),
    updateDestination: vi.fn().mockResolvedValue(undefined),
    deleteDestination: vi.fn().mockResolvedValue(undefined),
    rotateSecret: vi.fn(),
    sendTest: vi.fn(),
    listRoutes: vi.fn().mockResolvedValue([]),
    createRoute: vi.fn(),
    updateRoute: vi.fn(),
    deleteRoute: vi.fn().mockResolvedValue(undefined),
    listDeliveries: vi.fn().mockResolvedValue([]),
    listAttempts: vi.fn().mockResolvedValue([]),
    replayDelivery: vi.fn(),
    listScheduledMessages: vi.fn().mockResolvedValue({ items: [], total: 0 }),
    cancelScheduledMessage: vi.fn(),
    ...overrides,
  };
}

function renderSection(client: DeliveryApiClient) {
  return render(<DestinationsSection client={client} />);
}

function button(name: string) {
  return screen.getByRole('button', { name });
}

describe('DestinationsSection states', () => {
  it('shows loading, then the empty state', async () => {
    renderSection(buildClient());

    expect(screen.getByText(strings.destinationsLoading)).toBeInTheDocument();
    expect(await screen.findByText(strings.destinationsEmpty)).toBeInTheDocument();
  });

  it('shows a load error in the section', async () => {
    const client = buildClient({
      listDestinations: vi.fn().mockRejectedValue(new ApiError({ status: 502, message: 'bad gw' })),
    });
    renderSection(client);

    expect(await screen.findByRole('alert')).toHaveTextContent('bad gw');
  });

  it('renders targets, types, status pills and the disabled reason', async () => {
    const client = buildClient({
      listDestinations: vi.fn().mockResolvedValue([
        buildDestination({
          name: 'ops_discord',
          type: 'discord',
          config: {},
          secrets: { webhook_url: 'https://discord.com/****' },
        }),
        buildDestination({ name: 'family_ntfy' }),
        buildWebhook({
          enabled: false,
          disabledReason: 'gone: receiver returned 410',
        }),
      ]),
    });
    renderSection(client);

    expect(await screen.findByText('https://discord.com/****')).toBeInTheDocument();
    expect(screen.getByText('topic: family_alerts')).toBeInTheDocument();
    expect(screen.getByText('https://chores.4irl.app/****')).toBeInTheDocument();
    expect(screen.getByText('gone: receiver returned 410')).toBeInTheDocument();
    expect(screen.getAllByText(strings.destinationEnabled)).toHaveLength(2);
    expect(screen.getAllByText(strings.destinationDisabled)).toHaveLength(1);
  });
});

describe('send test', () => {
  it('shows the delivery state and HTTP status in a live region', async () => {
    const user = userEvent.setup();
    const client = buildClient({
      listDestinations: vi.fn().mockResolvedValue([buildDestination({ name: 'ops' })]),
      sendTest: vi.fn().mockResolvedValue({
        messageId: 'm1',
        status: 'delivered',
        deliveries: [
          {
            id: 1,
            destination: 'ops',
            state: 'delivered',
            deadReason: null,
            attemptCount: 1,
            lastStatusCode: 204,
            nextAttemptAt: null,
            deliveredAt: null,
          },
        ],
      }),
    });
    renderSection(client);

    await user.click(await screen.findByRole('button', { name: 'Send test to ops' }));

    const status = await screen.findByText(strings.testStateDelivered);
    expect(status.closest('[role="status"]')).toHaveTextContent('HTTP 204');
    expect(client.sendTest).toHaveBeenCalledWith({ destination: 'ops' });
  });

  it('shows the dead reason when there is no status code', async () => {
    const user = userEvent.setup();
    const client = buildClient({
      listDestinations: vi.fn().mockResolvedValue([buildDestination({ name: 'ops' })]),
      sendTest: vi.fn().mockResolvedValue({
        messageId: 'm1',
        status: 'dead',
        deliveries: [
          {
            id: 1,
            destination: 'ops',
            state: 'dead',
            deadReason: 'timeout',
            attemptCount: 3,
            lastStatusCode: null,
            nextAttemptAt: null,
            deliveredAt: null,
          },
        ],
      }),
    });
    renderSection(client);

    await user.click(await screen.findByRole('button', { name: 'Send test to ops' }));

    const pill = await screen.findByText(strings.testStateDead);
    expect(pill.closest('[role="status"]')).toHaveTextContent('timeout');
  });

  it('sends no second request while a test is in flight', async () => {
    const user = userEvent.setup();
    const pending = deferred<never>();
    const client = buildClient({
      listDestinations: vi.fn().mockResolvedValue([buildDestination({ name: 'ops' })]),
      sendTest: vi.fn().mockReturnValue(pending.promise),
    });
    renderSection(client);

    await user.click(await screen.findByRole('button', { name: 'Send test to ops' }));
    const busy = screen.getByRole('button', { name: strings.sendTestToSending });
    expect(busy).toHaveAttribute('aria-disabled', 'true');
    await user.click(busy);

    expect(client.sendTest).toHaveBeenCalledTimes(1);
  });

  it('cannot be triggered for a disabled destination and announces the reason', async () => {
    const user = userEvent.setup();
    const client = buildClient({
      listDestinations: vi
        .fn()
        .mockResolvedValue([buildWebhook({ enabled: false, disabledReason: 'gone: 410' })]),
    });
    renderSection(client);

    const trigger = await screen.findByRole('button', { name: 'Send test to chores_hook' });
    expect(trigger).toHaveAttribute('aria-disabled', 'true');
    expect(trigger).not.toBeDisabled();
    expect(trigger).toHaveAccessibleDescription('gone: 410');
    await user.click(trigger);

    expect(client.sendTest).not.toHaveBeenCalled();
  });

  it('shows a server error message instead of swallowing it', async () => {
    const user = userEvent.setup();
    const client = buildClient({
      listDestinations: vi.fn().mockResolvedValue([buildDestination({ name: 'ops' })]),
      sendTest: vi
        .fn()
        .mockRejectedValue(new ApiError({ status: 409, message: 'destination disabled' })),
    });
    renderSection(client);

    await user.click(await screen.findByRole('button', { name: 'Send test to ops' }));

    expect(await screen.findByRole('alert')).toHaveTextContent('destination disabled');
  });
});

describe('enable, disable and delete', () => {
  it('disables with a confirm, shows the progress label and focuses Enable afterwards', async () => {
    const user = userEvent.setup();
    const update = deferred<undefined>();
    const listDestinations = vi
      .fn()
      .mockResolvedValueOnce([buildDestination({ name: 'ops' })])
      .mockResolvedValue([
        buildDestination({ name: 'ops', enabled: false, disabledReason: 'by operator' }),
      ]);
    const client = buildClient({
      listDestinations,
      updateDestination: vi.fn().mockReturnValue(update.promise),
    });
    renderSection(client);

    await user.click(await screen.findByRole('button', { name: 'Disable ops' }));
    await user.click(button('Confirm disable ops'));

    const busy = screen.getByRole('button', { name: strings.disablingDestination });
    expect(busy).toHaveAttribute('aria-disabled', 'true');
    expect(busy).toHaveFocus();
    await user.click(busy);
    expect(client.updateDestination).toHaveBeenCalledTimes(1);
    expect(client.updateDestination).toHaveBeenCalledWith({ name: 'ops', enabled: false });

    update.resolve(undefined);
    await waitFor(() => expect(button('Enable ops')).toHaveFocus());
  });

  it('enables in one click', async () => {
    const user = userEvent.setup();
    const client = buildClient({
      listDestinations: vi
        .fn()
        .mockResolvedValue([buildDestination({ name: 'ops', enabled: false })]),
    });
    renderSection(client);

    await user.click(await screen.findByRole('button', { name: 'Enable ops' }));

    expect(client.updateDestination).toHaveBeenCalledWith({ name: 'ops', enabled: true });
  });

  it('deletes and moves focus to the next row, then to the heading when none remain', async () => {
    const user = userEvent.setup();
    const listDestinations = vi
      .fn()
      .mockResolvedValueOnce([
        buildDestination({ name: 'a_one' }),
        buildDestination({ name: 'b_two' }),
      ])
      .mockResolvedValueOnce([buildDestination({ name: 'b_two' })])
      .mockResolvedValue([]);
    const client = buildClient({ listDestinations });
    renderSection(client);

    await user.click(await screen.findByRole('button', { name: 'Delete a_one' }));
    await user.click(button('Confirm delete a_one'));
    await waitFor(() => expect(button('Send test to b_two')).toHaveFocus());

    await user.click(button('Delete b_two'));
    await user.click(button('Confirm delete b_two'));
    await waitFor(() =>
      expect(screen.getByRole('heading', { name: strings.destinationsHeading })).toHaveFocus(),
    );
    expect(await screen.findByText(strings.destinationsEmpty)).toBeInTheDocument();
  });

  it('keeps the row and shows the server error when a destination is in use', async () => {
    const user = userEvent.setup();
    const client = buildClient({
      listDestinations: vi.fn().mockResolvedValue([buildDestination({ name: 'ops' })]),
      deleteDestination: vi
        .fn()
        .mockRejectedValue(
          new ApiError({ status: 409, message: 'destination is used by a route' }),
        ),
    });
    renderSection(client);

    await user.click(await screen.findByRole('button', { name: 'Delete ops' }));
    await user.click(button('Confirm delete ops'));

    expect(await screen.findByRole('alert')).toHaveTextContent('destination is used by a route');
    expect(button('Delete ops')).toBeInTheDocument();
    expect(button('Delete ops')).toHaveFocus();
  });
});

describe('unreadable secrets', () => {
  it('offers only send-test, disable/enable and delete', async () => {
    const client = buildClient({
      listDestinations: vi.fn().mockResolvedValue([buildWebhook({ secrets: { error: '****' } })]),
    });
    renderSection(client);

    expect(await screen.findByText(strings.destinationSecretsUnreadable)).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /^Edit / })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /^Rotate / })).not.toBeInTheDocument();
    expect(button('Send test to chores_hook')).toBeInTheDocument();
    expect(button('Disable chores_hook')).toBeInTheDocument();
    expect(button('Delete chores_hook')).toBeInTheDocument();
  });
});

describe('add destination', () => {
  it('focuses the name field when the form opens', async () => {
    const user = userEvent.setup();
    renderSection(buildClient());

    await user.click(await screen.findByRole('button', { name: strings.addDestinationAction }));

    expect(screen.getByLabelText(strings.destNameLabel)).toHaveFocus();
  });

  it('creates an ntfy destination with trimmed non-secret inputs', async () => {
    const user = userEvent.setup();
    const created = buildDestination({ name: 'family_ntfy' });
    const client = buildClient({
      listDestinations: vi.fn().mockResolvedValueOnce([]).mockResolvedValue([created]),
      createDestination: vi.fn().mockResolvedValue(created),
    });
    renderSection(client);

    await user.click(await screen.findByRole('button', { name: strings.addDestinationAction }));
    await user.type(screen.getByLabelText(strings.destNameLabel), ' family_ntfy ');
    await user.type(screen.getByLabelText(strings.destTopicLabel), ' family_alerts ');
    const form = screen.getByRole('region', { name: strings.addDestinationHeading });
    await user.click(within(form).getByRole('button', { name: strings.addDestinationAction }));

    await waitFor(() =>
      expect(client.createDestination).toHaveBeenCalledWith({
        name: 'family_ntfy',
        type: 'ntfy',
        config: { topic: 'family_alerts' },
        secrets: {},
      }),
    );
    expect(await screen.findByText('topic: family_alerts')).toBeInTheDocument();
    expect(screen.queryByRole('region', { name: strings.addDestinationHeading })).toBeNull();
  });

  it('shows an inline error for a bad name and for a secret with surrounding whitespace', async () => {
    const user = userEvent.setup();
    const client = buildClient();
    renderSection(client);

    await user.click(await screen.findByRole('button', { name: strings.addDestinationAction }));
    const form = screen.getByRole('region', { name: strings.addDestinationHeading });
    await user.selectOptions(screen.getByLabelText(strings.destTypeLabel), 'discord');
    await user.type(screen.getByLabelText(strings.destNameLabel), 'Bad Name');
    await user.click(within(form).getByRole('button', { name: strings.addDestinationAction }));
    expect(within(form).getByRole('alert')).toHaveTextContent(strings.invalidDestinationName);

    await user.clear(screen.getByLabelText(strings.destNameLabel));
    await user.type(screen.getByLabelText(strings.destNameLabel), 'ops_discord');
    await user.type(
      screen.getByLabelText(strings.destDiscordUrlLabel),
      ' https://discord.com/api/webhooks/1/abc',
    );
    await user.click(within(form).getByRole('button', { name: strings.addDestinationAction }));

    expect(within(form).getByRole('alert')).toHaveTextContent('leading or trailing whitespace');
    expect(client.createDestination).not.toHaveBeenCalled();
  });

  it('masks secret inputs and shows the server error inline', async () => {
    const user = userEvent.setup();
    const client = buildClient({
      createDestination: vi
        .fn()
        .mockRejectedValue(new ApiError({ status: 409, message: 'destination already exists' })),
    });
    renderSection(client);

    await user.click(await screen.findByRole('button', { name: strings.addDestinationAction }));
    const form = screen.getByRole('region', { name: strings.addDestinationHeading });
    await user.selectOptions(screen.getByLabelText(strings.destTypeLabel), 'discord');
    const url = screen.getByLabelText(strings.destDiscordUrlLabel);
    expect(url).toHaveAttribute('type', 'password');
    expect(url).toHaveAttribute('autocomplete', 'new-password');
    await user.type(screen.getByLabelText(strings.destNameLabel), 'ops_discord');
    await user.type(url, 'https://discord.com/api/webhooks/1/abc');
    await user.click(within(form).getByRole('button', { name: strings.addDestinationAction }));

    expect(await within(form).findByRole('alert')).toHaveTextContent('destination already exists');
  });

  it('keeps a generated signing secret visible, reveals it once after create and dismisses it', async () => {
    const user = userEvent.setup();
    const created = buildWebhook();
    const client = buildClient({
      listDestinations: vi.fn().mockResolvedValueOnce([]).mockResolvedValue([created]),
      createDestination: vi.fn().mockResolvedValue(created),
    });
    renderSection(client);

    await user.click(await screen.findByRole('button', { name: strings.addDestinationAction }));
    const form = screen.getByRole('region', { name: strings.addDestinationHeading });
    await user.selectOptions(screen.getByLabelText(strings.destTypeLabel), 'webhook');
    await user.type(screen.getByLabelText(strings.destNameLabel), 'chores_hook');
    await user.type(
      screen.getByLabelText(strings.destWebhookUrlLabel),
      'https://chores.4irl.app/hook',
    );
    const signing = screen.getByLabelText(strings.destSigningSecretLabel);
    expect(signing).toHaveAttribute('type', 'password');

    await user.click(screen.getByRole('button', { name: strings.destGenerateAction }));
    expect(signing).toHaveAttribute('type', 'text');
    const generated = (signing as HTMLInputElement).value;
    expect(generated).toHaveLength(43);

    await user.click(within(form).getByRole('button', { name: strings.addDestinationAction }));

    await waitFor(() =>
      expect(client.createDestination).toHaveBeenCalledWith({
        name: 'chores_hook',
        type: 'webhook',
        config: {},
        secrets: { url: 'https://chores.4irl.app/hook', signing_secret: generated },
      }),
    );
    const secret = await screen.findByText(generated);
    expect(secret.closest('[tabindex="-1"]')).toHaveFocus();

    await user.click(button(strings.revealDismiss));
    expect(screen.queryByText(generated)).not.toBeInTheDocument();
  });

  it('rejects a typed signing secret that is too short', async () => {
    const user = userEvent.setup();
    const client = buildClient();
    renderSection(client);

    await user.click(await screen.findByRole('button', { name: strings.addDestinationAction }));
    const form = screen.getByRole('region', { name: strings.addDestinationHeading });
    await user.selectOptions(screen.getByLabelText(strings.destTypeLabel), 'webhook');
    await user.type(screen.getByLabelText(strings.destNameLabel), 'chores_hook');
    await user.type(screen.getByLabelText(strings.destWebhookUrlLabel), 'https://x.example/hook');
    await user.type(screen.getByLabelText(strings.destSigningSecretLabel), 'short');
    await user.click(within(form).getByRole('button', { name: strings.addDestinationAction }));

    expect(within(form).getByRole('alert')).toHaveTextContent(strings.invalidSigningSecret);
    expect(client.createDestination).not.toHaveBeenCalled();
  });

  it('cancel returns focus to the Add trigger', async () => {
    const user = userEvent.setup();
    renderSection(buildClient());

    await user.click(await screen.findByRole('button', { name: strings.addDestinationAction }));
    await user.click(button(strings.cancelAction));

    expect(button(strings.addDestinationAction)).toHaveFocus();
  });
});

describe('edit destination', () => {
  it('prefills config, focuses the first field and keeps one editor open at a time', async () => {
    const user = userEvent.setup();
    const client = buildClient({
      listDestinations: vi
        .fn()
        .mockResolvedValue([
          buildDestination({ name: 'a_one', config: { topic: 'topic_one' } }),
          buildDestination({ name: 'b_two', config: { topic: 'topic_two' } }),
        ]),
    });
    renderSection(client);

    await user.click(await screen.findByRole('button', { name: 'Edit a_one' }));
    const topic = screen.getByLabelText(strings.destTopicLabel);
    expect(topic).toHaveValue('topic_one');
    expect(topic).toHaveFocus();
    expect(button('Close a_one')).toHaveAttribute('aria-expanded', 'true');

    await user.click(button('Edit b_two'));

    expect(screen.getAllByLabelText(strings.destTopicLabel)).toHaveLength(1);
    expect(screen.getByLabelText(strings.destTopicLabel)).toHaveValue('topic_two');
    expect(button('Edit a_one')).toHaveAttribute('aria-expanded', 'false');
  });

  it('closes without a request on an untouched Save and focuses the Edit trigger', async () => {
    const user = userEvent.setup();
    const client = buildClient({
      listDestinations: vi.fn().mockResolvedValue([buildDestination({ name: 'ops' })]),
    });
    renderSection(client);

    await user.click(await screen.findByRole('button', { name: 'Edit ops' }));
    await user.click(button('Save ops'));

    expect(client.updateDestination).not.toHaveBeenCalled();
    expect(screen.queryByLabelText(strings.destTopicLabel)).not.toBeInTheDocument();
    await waitFor(() => expect(button('Edit ops')).toHaveFocus());
  });

  it('sends only the changed config keys and non-empty secrets', async () => {
    const user = userEvent.setup();
    const client = buildClient({
      listDestinations: vi.fn().mockResolvedValue([
        buildDestination({
          name: 'ops',
          config: { topic: 'old_topic' },
          secrets: { access_token: '****' },
        }),
      ]),
    });
    renderSection(client);

    await user.click(await screen.findByRole('button', { name: 'Edit ops' }));
    const token = screen.getByLabelText(strings.destAccessTokenLabel);
    expect(token).toHaveAttribute('placeholder', `**** ${strings.destLeaveBlank}`);
    await user.clear(screen.getByLabelText(strings.destTopicLabel));
    await user.type(screen.getByLabelText(strings.destTopicLabel), 'new_topic');
    await user.click(button('Save ops'));

    await waitFor(() =>
      expect(client.updateDestination).toHaveBeenCalledWith({
        name: 'ops',
        config: { topic: 'new_topic' },
        secrets: undefined,
      }),
    );
    await waitFor(() => expect(button('Edit ops')).toHaveFocus());
  });

  it('runs the same validators on edit, including the whitespace rule', async () => {
    const user = userEvent.setup();
    const client = buildClient({
      listDestinations: vi.fn().mockResolvedValue([buildDestination({ name: 'ops' })]),
    });
    renderSection(client);

    await user.click(await screen.findByRole('button', { name: 'Edit ops' }));
    await user.type(screen.getByLabelText(strings.destAccessTokenLabel), ' tk_abc');
    await user.click(button('Save ops'));

    expect(await screen.findByRole('alert')).toHaveTextContent('leading or trailing whitespace');
    expect(client.updateDestination).not.toHaveBeenCalled();
  });

  it('shows a save failure inline and keeps the editor open', async () => {
    const user = userEvent.setup();
    const client = buildClient({
      listDestinations: vi.fn().mockResolvedValue([buildDestination({ name: 'ops' })]),
      updateDestination: vi
        .fn()
        .mockRejectedValue(new ApiError({ status: 400, message: 'invalid config.topic' })),
    });
    renderSection(client);

    await user.click(await screen.findByRole('button', { name: 'Edit ops' }));
    await user.type(screen.getByLabelText(strings.destTopicLabel), '_x');
    await user.click(button('Save ops'));

    expect(await screen.findByRole('alert')).toHaveTextContent('invalid config.topic');
    expect(screen.getByLabelText(strings.destTopicLabel)).toBeInTheDocument();
  });

  it('does not overwrite typed values when the list refreshes mid-edit', async () => {
    const user = userEvent.setup();
    const listDestinations = vi
      .fn()
      .mockResolvedValueOnce([
        buildDestination({ name: 'a_one', config: { topic: 'topic_one' } }),
        buildDestination({ name: 'b_two' }),
      ])
      .mockResolvedValue([
        buildDestination({ name: 'a_one', config: { topic: 'changed_elsewhere' } }),
        buildDestination({ name: 'b_two', enabled: false }),
      ]);
    const client = buildClient({ listDestinations });
    renderSection(client);

    await user.click(await screen.findByRole('button', { name: 'Edit a_one' }));
    await user.type(screen.getByLabelText(strings.destTopicLabel), '_typed');
    await user.click(button('Disable b_two'));
    await user.click(button('Confirm disable b_two'));
    await screen.findByRole('button', { name: 'Enable b_two' });

    expect(screen.getByLabelText(strings.destTopicLabel)).toHaveValue('topic_one_typed');
  });
});

describe('rotate signing secret', () => {
  function rotated(overrides: Partial<DestinationSummary> = {}): DestinationSummary {
    return buildWebhook({
      config: { previous_secret_expires_at: '2099-01-02T03:04:05Z' },
      ...overrides,
    });
  }

  it('rotates with the default overlap and reveals the secret once with the previous expiry', async () => {
    const user = userEvent.setup();
    const client = buildClient({
      listDestinations: vi.fn().mockResolvedValue([buildWebhook()]),
      rotateSecret: vi
        .fn()
        .mockResolvedValue({ destination: rotated(), signingSecret: 'NEW_SECRET_VALUE' }),
    });
    renderSection(client);

    await user.click(await screen.findByRole('button', { name: 'Edit chores_hook' }));
    await user.click(button('Rotate chores_hook'));
    expect(screen.queryByText(strings.rotateInvalidatesPrevious)).not.toBeInTheDocument();
    await user.click(button('Confirm rotate chores_hook'));

    expect(client.rotateSecret).toHaveBeenCalledWith({ name: 'chores_hook', overlapHours: 24 });
    const secret = await screen.findByText('NEW_SECRET_VALUE');
    expect(secret.closest('[tabindex="-1"]')).toHaveFocus();
    expect(
      screen.getByText('Previous secret valid until 2099-01-02 03:04 UTC.'),
    ).toBeInTheDocument();
  });

  it('sends the chosen overlap', async () => {
    const user = userEvent.setup();
    const client = buildClient({
      listDestinations: vi.fn().mockResolvedValue([buildWebhook()]),
      rotateSecret: vi
        .fn()
        .mockResolvedValue({ destination: rotated(), signingSecret: 'NEW_SECRET_VALUE' }),
    });
    renderSection(client);

    await user.click(await screen.findByRole('button', { name: 'Edit chores_hook' }));
    await user.selectOptions(screen.getByLabelText(strings.rotateOverlapLabel), '168');
    await user.click(button('Rotate chores_hook'));
    await user.click(button('Confirm rotate chores_hook'));

    expect(client.rotateSecret).toHaveBeenCalledWith({ name: 'chores_hook', overlapHours: 168 });
  });

  it('warns inside the confirm only while a previous secret is still valid', async () => {
    const user = userEvent.setup();
    const client = buildClient({
      listDestinations: vi.fn().mockResolvedValue([rotated()]),
    });
    renderSection(client);

    await user.click(await screen.findByRole('button', { name: 'Edit chores_hook' }));
    expect(screen.queryByText(strings.rotateInvalidatesPrevious)).not.toBeInTheDocument();
    await user.click(button('Rotate chores_hook'));

    expect(screen.getByText(strings.rotateInvalidatesPrevious)).toBeInTheDocument();
  });

  it('shows no warning when the previous secret has expired', async () => {
    const user = userEvent.setup();
    const client = buildClient({
      listDestinations: vi
        .fn()
        .mockResolvedValue([
          buildWebhook({ config: { previous_secret_expires_at: '2020-01-01T00:00:00Z' } }),
        ]),
    });
    renderSection(client);

    await user.click(await screen.findByRole('button', { name: 'Edit chores_hook' }));
    await user.click(button('Rotate chores_hook'));

    expect(screen.queryByText(strings.rotateInvalidatesPrevious)).not.toBeInTheDocument();
  });

  it('keeps other typed values, clears the typed signing secret, and Save never resends it', async () => {
    const user = userEvent.setup();
    const client = buildClient({
      listDestinations: vi.fn().mockResolvedValue([buildWebhook()]),
      rotateSecret: vi
        .fn()
        .mockResolvedValue({ destination: rotated(), signingSecret: 'NEW_SECRET_VALUE' }),
    });
    renderSection(client);

    await user.click(await screen.findByRole('button', { name: 'Edit chores_hook' }));
    await user.type(screen.getByLabelText(strings.destWebhookUrlLabel), 'https://new.example/hook');
    await user.type(screen.getByLabelText(strings.destSigningSecretLabel), 'x'.repeat(40));
    await user.click(button('Rotate chores_hook'));
    await user.click(button('Confirm rotate chores_hook'));
    await screen.findByText('NEW_SECRET_VALUE');

    expect(screen.getByLabelText(strings.destWebhookUrlLabel)).toHaveValue(
      'https://new.example/hook',
    );
    expect(screen.getByLabelText(strings.destSigningSecretLabel)).toHaveValue('');

    await user.click(button('Save chores_hook'));
    await waitFor(() =>
      expect(client.updateDestination).toHaveBeenCalledWith({
        name: 'chores_hook',
        config: undefined,
        secrets: { url: 'https://new.example/hook' },
      }),
    );
  });

  it('persists the reveal across a list refresh until dismissed, and never for a re-created name', async () => {
    const user = userEvent.setup();
    const listDestinations = vi
      .fn()
      .mockResolvedValueOnce([buildWebhook(), buildDestination({ name: 'zz_other' })])
      .mockResolvedValueOnce([rotated(), buildDestination({ name: 'zz_other' })])
      .mockResolvedValue([rotated(), buildDestination({ name: 'zz_other', enabled: false })]);
    const client = buildClient({
      listDestinations,
      rotateSecret: vi
        .fn()
        .mockResolvedValue({ destination: rotated(), signingSecret: 'NEW_SECRET_VALUE' }),
    });
    renderSection(client);

    await user.click(await screen.findByRole('button', { name: 'Edit chores_hook' }));
    await user.click(button('Rotate chores_hook'));
    await user.click(button('Confirm rotate chores_hook'));
    await screen.findByText('NEW_SECRET_VALUE');

    // A refresh (triggered by disabling another row) leaves the reveal in place.
    await user.click(button('Disable zz_other'));
    await user.click(button('Confirm disable zz_other'));
    await screen.findByRole('button', { name: 'Enable zz_other' });
    expect(screen.getByText('NEW_SECRET_VALUE')).toBeInTheDocument();

    // Closing the editor clears it; it does not come back from list data.
    await user.click(button('Close chores_hook'));
    expect(screen.queryByText('NEW_SECRET_VALUE')).not.toBeInTheDocument();
  });

  it('clears a reveal when its destination is deleted', async () => {
    const user = userEvent.setup();
    const listDestinations = vi
      .fn()
      .mockResolvedValueOnce([buildWebhook()])
      .mockResolvedValueOnce([rotated()])
      .mockResolvedValue([]);
    const client = buildClient({
      listDestinations,
      rotateSecret: vi
        .fn()
        .mockResolvedValue({ destination: rotated(), signingSecret: 'NEW_SECRET_VALUE' }),
    });
    renderSection(client);

    await user.click(await screen.findByRole('button', { name: 'Edit chores_hook' }));
    await user.click(button('Rotate chores_hook'));
    await user.click(button('Confirm rotate chores_hook'));
    await screen.findByText('NEW_SECRET_VALUE');
    await user.click(button('Delete chores_hook'));
    await user.click(button('Confirm delete chores_hook'));

    await screen.findByText(strings.destinationsEmpty);
    expect(screen.queryByText('NEW_SECRET_VALUE')).not.toBeInTheDocument();
  });

  it('shows the progress label and sends no second request while rotating', async () => {
    const user = userEvent.setup();
    const pending = deferred<never>();
    const client = buildClient({
      listDestinations: vi.fn().mockResolvedValue([buildWebhook()]),
      rotateSecret: vi.fn().mockReturnValue(pending.promise),
    });
    renderSection(client);

    await user.click(await screen.findByRole('button', { name: 'Edit chores_hook' }));
    await user.click(button('Rotate chores_hook'));
    await user.click(button('Confirm rotate chores_hook'));

    const busy = screen.getByRole('button', { name: strings.rotatingDestination });
    expect(busy).toHaveAttribute('aria-disabled', 'true');
    expect(busy).toHaveFocus();
    await user.click(busy);

    expect(client.rotateSecret).toHaveBeenCalledTimes(1);
  });

  it('is not reverted by a stale in-flight list response that resolves after the rotate', async () => {
    const user = userEvent.setup();
    const staleList = deferred<DestinationSummary[]>();
    const listDestinations = vi
      .fn()
      .mockResolvedValueOnce([buildWebhook()])
      .mockReturnValueOnce(staleList.promise)
      .mockResolvedValue([rotated()]);
    const client = buildClient({
      listDestinations,
      rotateSecret: vi
        .fn()
        .mockResolvedValue({ destination: rotated(), signingSecret: 'NEW_SECRET_VALUE' }),
    });
    renderSection(client);

    await user.click(await screen.findByRole('button', { name: 'Edit chores_hook' }));
    // Starts a list refresh that stays in flight.
    await user.click(button('Disable chores_hook'));
    await user.click(button('Confirm disable chores_hook'));
    await waitFor(() => expect(listDestinations).toHaveBeenCalledTimes(2));

    await user.click(button('Rotate chores_hook'));
    await user.click(button('Confirm rotate chores_hook'));
    await screen.findByText('NEW_SECRET_VALUE');
    await waitFor(() => expect(listDestinations).toHaveBeenCalledTimes(3));

    staleList.resolve([buildWebhook()]);
    await staleList.promise;

    await user.click(button('Rotate chores_hook'));
    expect(screen.getByText(strings.rotateInvalidatesPrevious)).toBeInTheDocument();
  });

  it('shows a rotate failure inline', async () => {
    const user = userEvent.setup();
    const client = buildClient({
      listDestinations: vi.fn().mockResolvedValue([buildWebhook()]),
      rotateSecret: vi
        .fn()
        .mockRejectedValue(
          new ApiError({ status: 422, message: 'destination type does not support rotation' }),
        ),
    });
    renderSection(client);

    await user.click(await screen.findByRole('button', { name: 'Edit chores_hook' }));
    await user.click(button('Rotate chores_hook'));
    await user.click(button('Confirm rotate chores_hook'));

    expect(await screen.findByRole('alert')).toHaveTextContent(
      'destination type does not support rotation',
    );
  });
});

describe('overlapping refreshes', () => {
  it('never renders a superseded destinations response', async () => {
    const user = userEvent.setup();
    const initial = deferred<DestinationSummary[]>();
    const second = deferred<DestinationSummary[]>();
    const created = buildDestination({ name: 'fresh_one' });
    const listDestinations = vi
      .fn()
      .mockReturnValueOnce(initial.promise)
      .mockReturnValueOnce(second.promise);
    const client = buildClient({
      listDestinations,
      createDestination: vi.fn().mockResolvedValue(created),
    });
    renderSection(client);

    await user.click(button(strings.addDestinationAction));
    await user.type(screen.getByLabelText(strings.destNameLabel), 'fresh_one');
    await user.type(screen.getByLabelText(strings.destTopicLabel), 'fresh_topic');
    const form = screen.getByRole('region', { name: strings.addDestinationHeading });
    await user.click(within(form).getByRole('button', { name: strings.addDestinationAction }));
    await waitFor(() => expect(listDestinations).toHaveBeenCalledTimes(2));

    second.resolve([created]);
    expect(await screen.findByText('topic: family_alerts')).toBeInTheDocument();
    initial.resolve([buildDestination({ name: 'stale_one' })]);
    await initial.promise;

    await waitFor(() => expect(screen.queryByText('stale_one')).not.toBeInTheDocument());
    expect(screen.getByText('fresh_one')).toBeInTheDocument();
  });
});
