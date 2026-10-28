import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { ApiError } from '../api/client';
import type {
  CreateHeartbeatParams,
  DeliveryApiClient,
  DestinationSummary,
  HeartbeatCheck,
} from '../api/deliveryClient';
import { strings } from '../strings';
import { Heartbeats } from './Heartbeats';
import type { DestinationsLoadState } from './RoutesSection';

afterEach(cleanup);

type User = ReturnType<typeof userEvent.setup>;

function destination(name: string): DestinationSummary {
  return {
    name,
    type: 'discord',
    enabled: true,
    disabledReason: null,
    config: {},
    secrets: {},
    ownerKind: 'operator',
    ownerAppId: null,
    createdAt: '2026-10-01T00:00:00Z',
    updatedAt: '2026-10-01T00:00:00Z',
  } as DestinationSummary;
}

const DESTINATIONS: DestinationsLoadState = {
  status: 'ready',
  destinations: [destination('ops_discord'), destination('ops_other')],
};

const CREATED = {
  id: 1,
  appId: 'zqapp',
  name: 'site',
  periodSeconds: 60,
  graceSeconds: 30,
  paused: false,
  state: 'up',
  lastPingAt: null,
  lastPingKind: null,
  downSince: null,
  deadlineAt: '2099-01-01T10:00:00Z',
  destinations: [{ name: 'ops_discord', type: 'discord', enabled: true }],
  ownerKind: 'operator',
  ownerAppId: null,
  downReason: null,
  probe: null,
  createdAt: '2026-10-17T10:00:00Z',
  updatedAt: '2026-10-18T08:01:00Z',
} as HeartbeatCheck;

function buildClient(overrides: Partial<DeliveryApiClient> = {}): DeliveryApiClient {
  return {
    getReadiness: vi.fn(),
    listDestinations: vi.fn().mockResolvedValue([]),
    createDestination: vi.fn(),
    updateDestination: vi.fn(),
    deleteDestination: vi.fn(),
    rotateSecret: vi.fn(),
    sendTest: vi.fn(),
    listRoutes: vi.fn().mockResolvedValue([]),
    createRoute: vi.fn(),
    updateRoute: vi.fn(),
    deleteRoute: vi.fn(),
    listDeliveries: vi.fn().mockResolvedValue([]),
    listAttempts: vi.fn().mockResolvedValue([]),
    replayDelivery: vi.fn(),
    listScheduledMessages: vi.fn().mockResolvedValue({ items: [], total: 0 }),
    cancelScheduledMessage: vi.fn(),
    listHeartbeats: vi.fn().mockResolvedValue([]),
    createHeartbeat: vi.fn().mockResolvedValue(CREATED),
    updateHeartbeat: vi.fn(),
    deleteHeartbeat: vi.fn(),
    pauseHeartbeat: vi.fn(),
    resumeHeartbeat: vi.fn(),
    listIdentityRequests: vi.fn().mockResolvedValue([]),
    approveIdentityRequest: vi.fn(),
    rejectIdentityRequest: vi.fn(),
    listIdentityBindings: vi.fn().mockResolvedValue([]),
    revokeIdentityKey: vi.fn(),
    unbindApp: vi.fn(),
    ...overrides,
  };
}

async function openAddForm(client: DeliveryApiClient): Promise<{ user: User; form: HTMLElement }> {
  const user = userEvent.setup();
  render(<Heartbeats client={client} destinationsState={DESTINATIONS} />);
  await user.click(await screen.findByRole('button', { name: strings.addHeartbeatAction }));
  return { user, form: screen.getByRole('form', { name: strings.addHeartbeatHeading }) };
}

function submit(form: HTMLElement): HTMLElement {
  const button = within(form)
    .getAllByRole('button', { name: strings.addHeartbeatAction })
    .find((element) => element.getAttribute('type') === 'submit');
  if (button === undefined) {
    throw new Error('submit button not found');
  }
  return button;
}

function field(form: HTMLElement, label: string): HTMLInputElement {
  return within(form).getByLabelText(label) as HTMLInputElement;
}

function radio(form: HTMLElement, label: string): HTMLInputElement {
  return within(form).getByRole('radio', { name: label });
}

async function fillCommon({ user, form }: { user: User; form: HTMLElement }) {
  await user.type(field(form, strings.heartbeatAppIdLabel), 'zqapp');
  await user.type(field(form, strings.heartbeatNameLabel), 'site');
  await user.click(within(form).getByRole('checkbox', { name: /^ops_discord/ }));
}

async function chooseProbe({ user, form }: { user: User; form: HTMLElement }) {
  await user.click(radio(form, strings.heartbeatKindProbe));
}

async function fillProbe({ user, form }: { user: User; form: HTMLElement }) {
  await user.type(field(form, strings.heartbeatProbeUrlLabel), 'https://host.example/health');
}

function lastCreate(client: DeliveryApiClient): CreateHeartbeatParams {
  const mock = vi.mocked(client.createHeartbeat);
  return mock.mock.calls[mock.mock.calls.length - 1][0];
}

describe('add form: check type', () => {
  it('defaults to push and sends the unchanged push body', async () => {
    const client = buildClient();
    const { user, form } = await openAddForm(client);
    expect(radio(form, strings.heartbeatKindPush)).toBeChecked();
    expect(within(form).queryByRole('group', { name: strings.heartbeatCredentialsCaption })).toBe(
      null,
    );
    await fillCommon({ user, form });
    await user.click(submit(form));

    await waitFor(() => expect(client.createHeartbeat).toHaveBeenCalledTimes(1));
    expect(lastCreate(client)).toEqual({
      kind: 'push',
      appId: 'zqapp',
      name: 'site',
      periodSeconds: 86_400,
      graceSeconds: 7_200,
      destinations: ['ops_discord'],
    });
  });

  it('sends a probe create with integer seconds and no push fields or optional keys', async () => {
    const client = buildClient();
    const { user, form } = await openAddForm(client);
    await chooseProbe({ user, form });
    await fillCommon({ user, form });
    await fillProbe({ user, form });
    await user.click(submit(form));

    await waitFor(() => expect(client.createHeartbeat).toHaveBeenCalledTimes(1));
    const sent = lastCreate(client);
    expect(sent).toEqual({
      kind: 'probe',
      appId: 'zqapp',
      name: 'site',
      destinations: ['ops_discord'],
      probe: {
        url: 'https://host.example/health',
        expectedStatuses: [200],
        bodyMatch: null,
        timeoutSeconds: 5,
        intervalSeconds: 300,
      },
    });
    expect('accessToken' in sent).toBe(false);
  });

  it('sends body match and the credential pair when both are filled', async () => {
    const client = buildClient();
    const { user, form } = await openAddForm(client);
    await chooseProbe({ user, form });
    await fillCommon({ user, form });
    await fillProbe({ user, form });
    await user.type(field(form, strings.heartbeatProbeBodyMatchLabel), 'ok');
    await user.type(field(form, strings.heartbeatCredentialsClientIdLabel), 'id1');
    await user.type(field(form, strings.heartbeatCredentialsClientSecretLabel), 'sec1');
    await user.click(submit(form));

    await waitFor(() => expect(client.createHeartbeat).toHaveBeenCalledTimes(1));
    expect(lastCreate(client)).toMatchObject({
      probe: { bodyMatch: 'ok' },
      accessToken: { clientId: 'id1', clientSecret: 'sec1' },
    });
  });

  it('flags a half-filled credential pair on the empty half and sends nothing', async () => {
    const client = buildClient();
    const { user, form } = await openAddForm(client);
    await chooseProbe({ user, form });
    await fillCommon({ user, form });
    await fillProbe({ user, form });
    await user.type(field(form, strings.heartbeatCredentialsClientIdLabel), 'id1');
    await user.click(submit(form));

    const secret = field(form, strings.heartbeatCredentialsClientSecretLabel);
    expect(within(form).getByText(strings.invalidProbeCredentialPair)).toBeInTheDocument();
    expect(secret).toHaveAttribute('aria-invalid', 'true');
    expect(secret).toHaveFocus();
    expect(client.createHeartbeat).not.toHaveBeenCalled();
  });

  it('keeps typed values across a kind switch, keeps focus, and drops the credentials', async () => {
    const client = buildClient();
    const { user, form } = await openAddForm(client);
    await user.clear(field(form, strings.heartbeatPeriodLabel));
    await user.type(field(form, strings.heartbeatPeriodLabel), '12');
    await chooseProbe({ user, form });
    expect(radio(form, strings.heartbeatKindProbe)).toHaveFocus();
    expect(within(form).queryByLabelText(strings.heartbeatPeriodLabel)).toBeNull();
    await fillProbe({ user, form });
    await user.type(field(form, strings.heartbeatCredentialsClientIdLabel), 'id1');
    await user.type(field(form, strings.heartbeatCredentialsClientSecretLabel), 'sec1');

    await user.click(radio(form, strings.heartbeatKindPush));
    expect(radio(form, strings.heartbeatKindPush)).toHaveFocus();
    expect(field(form, strings.heartbeatPeriodLabel).value).toBe('12');

    await chooseProbe({ user, form });
    expect(field(form, strings.heartbeatProbeUrlLabel).value).toBe('https://host.example/health');
    expect(field(form, strings.heartbeatCredentialsClientIdLabel).value).toBe('');
    expect(field(form, strings.heartbeatCredentialsClientSecretLabel).value).toBe('');
  });

  it('clears the hidden kind errors on a switch', async () => {
    const client = buildClient();
    const { user, form } = await openAddForm(client);
    await user.clear(field(form, strings.heartbeatPeriodLabel));
    await user.click(submit(form));
    expect(within(form).getByText(strings.invalidHeartbeatPeriod)).toBeInTheDocument();

    await chooseProbe({ user, form });
    await user.click(radio(form, strings.heartbeatKindPush));
    expect(within(form).queryByText(strings.invalidHeartbeatPeriod)).toBeNull();
  });

  it('shows the credentials hint with the switch note and no status region', async () => {
    const { user, form } = await openAddForm(buildClient());
    await chooseProbe({ user, form });
    const group = within(form).getByRole('group', { name: strings.heartbeatCredentialsCaption });
    expect(group).toHaveTextContent('Cleared if you switch to Push.');
    for (const label of [
      strings.heartbeatCredentialsClientIdLabel,
      strings.heartbeatCredentialsClientSecretLabel,
    ]) {
      expect(field(form, label)).toHaveAttribute(
        'aria-describedby',
        'heartbeat-credentials-hint-new',
      );
    }
    expect(within(form).queryByRole('status')).toBeNull();
  });
});

describe('add form: https scheme', () => {
  it('prepends https:// on blur and sends the prefixed URL', async () => {
    const client = buildClient();
    const { user, form } = await openAddForm(client);
    await chooseProbe({ user, form });
    await fillCommon({ user, form });
    const url = field(form, strings.heartbeatProbeUrlLabel);
    await user.type(url, 'example.com');
    await user.tab();
    expect(url.value).toBe('https://example.com');
    await user.click(submit(form));
    await waitFor(() => expect(client.createHeartbeat).toHaveBeenCalledTimes(1));
    expect(lastCreate(client)).toMatchObject({ probe: { url: 'https://example.com' } });
  });

  it('keeps an explicit http:// on blur and flags it at submit', async () => {
    const client = buildClient();
    const { user, form } = await openAddForm(client);
    await chooseProbe({ user, form });
    await fillCommon({ user, form });
    const url = field(form, strings.heartbeatProbeUrlLabel);
    await user.type(url, 'http://x');
    await user.tab();
    expect(url.value).toBe('http://x');
    await user.click(submit(form));
    expect(client.createHeartbeat).not.toHaveBeenCalled();
    expect(form).toHaveTextContent(strings.invalidProbeUrlScheme);
  });

  it('adds nothing on a blank blur', async () => {
    const client = buildClient();
    const { user, form } = await openAddForm(client);
    await chooseProbe({ user, form });
    const url = field(form, strings.heartbeatProbeUrlLabel);
    await user.click(url);
    await user.tab();
    expect(url.value).toBe('');
  });

  it('Enter in the URL field normalises at submit without a blur', async () => {
    const client = buildClient();
    const { user, form } = await openAddForm(client);
    await chooseProbe({ user, form });
    await fillCommon({ user, form });
    const url = field(form, strings.heartbeatProbeUrlLabel);
    await user.type(url, 'example.com{Enter}');
    await waitFor(() => expect(client.createHeartbeat).toHaveBeenCalledTimes(1));
    expect(form).not.toHaveTextContent(strings.invalidProbeUrlScheme);
    expect(lastCreate(client)).toMatchObject({ probe: { url: 'https://example.com' } });
    expect(url.value).toBe('https://example.com');
  });
});

describe('add form: validation', () => {
  it('shows each invalid probe field error wired to its input and focuses the first', async () => {
    const client = buildClient();
    const { user, form } = await openAddForm(client);
    await chooseProbe({ user, form });
    await fillCommon({ user, form });
    await user.type(field(form, strings.heartbeatProbeUrlLabel), 'http://nope');
    await user.clear(field(form, strings.heartbeatProbeStatusesLabel));
    await user.type(field(form, strings.heartbeatProbeStatusesLabel), '99');
    await user.click(submit(form));

    const url = field(form, strings.heartbeatProbeUrlLabel);
    expect(url).toHaveFocus();
    expect(url).toHaveAttribute('aria-invalid', 'true');
    expect(url.getAttribute('aria-describedby')).toBe(
      'heartbeat-probe-url-new-hint heartbeat-probe-url-new-error',
    );
    expect(within(form).getByText(strings.invalidProbeUrlScheme)).toBeInTheDocument();
    expect(within(form).getByText(strings.invalidProbeStatuses)).toBeInTheDocument();
    expect(client.createHeartbeat).not.toHaveBeenCalled();
  });

  it('flags a credential with a space and a whitespace-only body match', async () => {
    const client = buildClient();
    const { user, form } = await openAddForm(client);
    await chooseProbe({ user, form });
    await fillCommon({ user, form });
    await fillProbe({ user, form });
    await user.type(field(form, strings.heartbeatProbeBodyMatchLabel), '   ');
    await user.type(field(form, strings.heartbeatCredentialsClientIdLabel), 'bad id');
    await user.type(field(form, strings.heartbeatCredentialsClientSecretLabel), 'sec1');
    await user.click(submit(form));

    const bodyMatch = field(form, strings.heartbeatProbeBodyMatchLabel);
    const clientId = field(form, strings.heartbeatCredentialsClientIdLabel);
    expect(within(form).getByText(strings.invalidProbeBodyMatch)).toBeInTheDocument();
    expect(within(form).getByText(strings.invalidProbeCredential)).toBeInTheDocument();
    expect(bodyMatch).toHaveAttribute('aria-invalid', 'true');
    expect(clientId).toHaveAttribute('aria-invalid', 'true');
    expect(field(form, strings.heartbeatCredentialsClientSecretLabel)).not.toHaveAttribute(
      'aria-invalid',
    );
    expect(bodyMatch).toHaveFocus();
    expect(client.createHeartbeat).not.toHaveBeenCalled();
  });

  it.each([
    ['empty', '', strings.invalidProbeUrlEmpty],
    ['tooLong', `https://${'a'.repeat(2050)}.example/`, strings.invalidProbeUrlTooLong],
    ['malformed', 'https://host.example/#frag', strings.invalidProbeUrlMalformed],
  ])('maps a %s URL to its message', async (_name, value, message) => {
    const client = buildClient();
    const { user, form } = await openAddForm(client);
    await chooseProbe({ user, form });
    await fillCommon({ user, form });
    const url = field(form, strings.heartbeatProbeUrlLabel);
    if (value !== '') {
      fireEvent.change(url, { target: { value } });
    }
    await user.click(submit(form));

    expect(within(form).getByText(message)).toBeInTheDocument();
    expect(url).toHaveAttribute('aria-invalid', 'true');
    expect(url).toHaveFocus();
    expect(client.createHeartbeat).not.toHaveBeenCalled();
  });

  it('errors on an interval under a minute and focuses it', async () => {
    const { user, form } = await openAddForm(buildClient());
    await chooseProbe({ user, form });
    await fillCommon({ user, form });
    await fillProbe({ user, form });
    const interval = field(form, strings.heartbeatProbeIntervalLabel);
    await user.clear(interval);
    await user.type(interval, '30');
    await user.selectOptions(
      within(form).getByLabelText(strings.heartbeatProbeIntervalUnitLabel),
      'seconds',
    );
    await user.click(submit(form));
    expect(within(form).getByText(strings.invalidProbeInterval)).toBeInTheDocument();
    expect(interval).toHaveFocus();
    expect(interval).toHaveAttribute('aria-invalid', 'true');
  });

  it('flags an out-of-range timeout', async () => {
    const { user, form } = await openAddForm(buildClient());
    await chooseProbe({ user, form });
    await fillCommon({ user, form });
    await fillProbe({ user, form });
    const timeout = field(form, strings.heartbeatProbeTimeoutLabel);
    await user.clear(timeout);
    await user.type(timeout, '9');
    await user.click(submit(form));
    expect(within(form).getByText(strings.invalidProbeTimeout)).toBeInTheDocument();
    expect(timeout).toHaveFocus();
  });

  it('puts the destinations error on the destinations, not the form alert', async () => {
    const client = buildClient();
    const { user, form } = await openAddForm(client);
    await user.type(field(form, strings.heartbeatAppIdLabel), 'zqapp');
    await user.type(field(form, strings.heartbeatNameLabel), 'site');
    await user.click(submit(form));

    const first = within(form).getByRole('checkbox', { name: /^ops_discord/ });
    const errorElement = within(form).getByText(strings.heartbeatNeedsDestination);
    expect(within(form).queryByRole('alert')).toBeNull();
    expect(first).toHaveFocus();
    expect(first).toHaveAttribute('aria-invalid', 'true');
    expect(first).toHaveAttribute('aria-describedby', errorElement.id);
    const fieldset = within(form).getByRole('group', { name: strings.heartbeatDestinationsLegend });
    expect(fieldset).toHaveAttribute('aria-describedby', errorElement.id);
    expect(fieldset).not.toHaveAttribute('aria-invalid');

    await user.click(first);
    expect(within(form).queryByText(strings.heartbeatNeedsDestination)).toBeNull();
    expect(first).not.toHaveAttribute('aria-invalid');
    expect(first).not.toHaveAttribute('aria-describedby');
    expect(fieldset).not.toHaveAttribute('aria-describedby');
  });

  it('focuses the empty credential half before the destinations in DOM order', async () => {
    const { user, form } = await openAddForm(buildClient());
    await chooseProbe({ user, form });
    await user.type(field(form, strings.heartbeatAppIdLabel), 'zqapp');
    await user.type(field(form, strings.heartbeatNameLabel), 'site');
    await fillProbe({ user, form });
    await user.type(field(form, strings.heartbeatCredentialsClientSecretLabel), 'sec1');
    await user.click(submit(form));
    expect(field(form, strings.heartbeatCredentialsClientIdLabel)).toHaveFocus();
    expect(within(form).getByText(strings.heartbeatNeedsDestination)).toBeInTheDocument();
  });

  it('wires Period error and hint ids on push', async () => {
    const { user, form } = await openAddForm(buildClient());
    const period = field(form, strings.heartbeatPeriodLabel);
    expect(period).toHaveAttribute('aria-describedby', 'heartbeat-period-new-hint');
    await user.clear(period);
    await user.click(submit(form));
    expect(period).toHaveAttribute('aria-invalid', 'true');
    expect(period).toHaveAttribute(
      'aria-describedby',
      'heartbeat-period-new-hint heartbeat-period-new-error',
    );
  });
});

describe('add form: server errors and in-flight lock', () => {
  it('shows a server 400 in the alert, keeps the values and never echoes the secret', async () => {
    const createHeartbeat = vi
      .fn()
      .mockRejectedValue(new ApiError({ status: 400, message: 'invalid probe.url' }));
    const { user, form } = await openAddForm(buildClient({ createHeartbeat }));
    await chooseProbe({ user, form });
    await fillCommon({ user, form });
    await fillProbe({ user, form });
    await user.type(field(form, strings.heartbeatCredentialsClientIdLabel), 'id1');
    await user.type(field(form, strings.heartbeatCredentialsClientSecretLabel), 'topsecret1');
    await user.click(submit(form));

    const alert = await within(form).findByRole('alert');
    expect(alert).toHaveTextContent('invalid probe.url');
    expect(form).not.toHaveTextContent('topsecret1');
    expect(field(form, strings.heartbeatProbeUrlLabel).value).toBe('https://host.example/health');
    expect(field(form, strings.heartbeatCredentialsClientSecretLabel).value).toBe('topsecret1');
  });

  it('locks the kind radios while the save is in flight', async () => {
    let release: (value: HeartbeatCheck) => void = () => undefined;
    const createHeartbeat = vi.fn().mockReturnValue(
      new Promise<HeartbeatCheck>((resolve) => {
        release = resolve;
      }),
    );
    const { user, form } = await openAddForm(buildClient({ createHeartbeat }));
    await chooseProbe({ user, form });
    await fillCommon({ user, form });
    await fillProbe({ user, form });
    await user.click(submit(form));
    await waitFor(() => expect(createHeartbeat).toHaveBeenCalledTimes(1));

    expect(radio(form, strings.heartbeatKindPush)).toHaveAttribute('aria-disabled', 'true');
    await user.click(radio(form, strings.heartbeatKindPush));
    expect(radio(form, strings.heartbeatKindProbe)).toBeChecked();
    expect(radio(form, strings.heartbeatKindPush)).not.toBeChecked();
    expect(within(form).getByLabelText(strings.heartbeatProbeUrlLabel)).toBeInTheDocument();
    release(CREATED);
    await waitFor(() => expect(screen.queryByRole('form')).toBeNull());
  });
});
