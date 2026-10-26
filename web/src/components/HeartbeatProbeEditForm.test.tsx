import { act, cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { ApiError } from '../api/client';
import type {
  DeliveryApiClient,
  DestinationSummary,
  HeartbeatCheck,
  HeartbeatProbe,
  HeartbeatProbeResult,
} from '../api/deliveryClient';
import { strings } from '../strings';
import { Heartbeats } from './Heartbeats';
import type { DestinationsLoadState } from './RoutesSection';

afterEach(() => {
  cleanup();
  vi.useRealTimers();
});

type User = ReturnType<typeof userEvent.setup>;

const NOW_ISO = '2026-10-26T12:00:00Z';
const TICK = 30_000;
const LABEL = 'zqapp/site';

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

function result(overrides: Partial<HeartbeatProbeResult> = {}): HeartbeatProbeResult {
  return { at: '2026-10-26T11:59:00Z', status: 200, reason: null, ...overrides };
}

function pushCheck(): HeartbeatCheck {
  return {
    id: 7,
    appId: 'zqapp',
    name: 'site',
    periodSeconds: 3600,
    graceSeconds: 600,
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
}

function probeCheck({
  check = {},
  probe = {},
}: { check?: Partial<HeartbeatCheck>; probe?: Partial<HeartbeatProbe> } = {}): HeartbeatCheck {
  return {
    ...pushCheck(),
    periodSeconds: 300,
    graceSeconds: 600,
    probe: {
      url: 'https://host.example/health',
      expectedStatuses: [200, 204],
      bodyMatch: null,
      timeoutSeconds: 5,
      intervalSeconds: 300,
      consecutiveFailures: 0,
      failureThreshold: 3,
      hasAccessToken: false,
      lastResult: result(),
      ...probe,
    },
    ...check,
  };
}

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
    listHeartbeats: vi.fn().mockResolvedValue([probeCheck()]),
    createHeartbeat: vi.fn(),
    updateHeartbeat: vi.fn().mockResolvedValue(probeCheck()),
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

async function openEditor({
  check = probeCheck(),
  client = {},
}: { check?: HeartbeatCheck; client?: Partial<DeliveryApiClient> } = {}): Promise<{
  user: User;
  form: HTMLElement;
  api: DeliveryApiClient;
}> {
  const api = buildClient({ listHeartbeats: vi.fn().mockResolvedValue([check]), ...client });
  const user = userEvent.setup();
  render(<Heartbeats client={api} destinationsState={DESTINATIONS} />);
  await user.click(await screen.findByRole('button', { name: `Edit ${LABEL}` }));
  return { user, form: screen.getByRole('form', { name: `Edit ${LABEL}` }), api };
}

function field(form: HTMLElement, label: string): HTMLInputElement {
  return within(form).getByLabelText(label) as HTMLInputElement;
}

function control(form: HTMLElement, name: string): HTMLElement {
  return within(form).getByRole('button', { name });
}

async function replaceText({
  user,
  input,
  text,
}: {
  user: User;
  input: HTMLInputElement;
  text: string;
}) {
  await user.clear(input);
  if (text !== '') {
    await user.type(input, text);
  }
}

function save(user: User, form: HTMLElement) {
  return user.click(control(form, strings.saveHeartbeat));
}

function lastUpdate(api: DeliveryApiClient): unknown {
  const mock = vi.mocked(api.updateHeartbeat);
  return mock.mock.calls[mock.mock.calls.length - 1][0];
}

function credentialStatus(form: HTMLElement): HTMLElement {
  return within(form).getByRole('status');
}

describe('probe edit form: layout', () => {
  it('shows the type as text, saved values, no radio and no period or grace', async () => {
    const { form } = await openEditor({
      check: probeCheck({ probe: { bodyMatch: 'ok', timeoutSeconds: 4 } }),
    });
    expect(form).toHaveTextContent('Type: Website probe.');
    expect(within(form).queryByRole('radio')).toBeNull();
    expect(within(form).queryByLabelText(strings.heartbeatPeriodLabel)).toBeNull();
    expect(within(form).queryByLabelText(strings.heartbeatGraceLabel)).toBeNull();
    expect(field(form, strings.heartbeatProbeUrlLabel).value).toBe('https://host.example/health');
    expect(field(form, strings.heartbeatProbeStatusesLabel).value).toBe('200, 204');
    expect(field(form, strings.heartbeatProbeBodyMatchLabel).value).toBe('ok');
    expect(field(form, strings.heartbeatProbeIntervalLabel).value).toBe('5');
    expect(field(form, strings.heartbeatProbeTimeoutLabel).value).toBe('4');
  });

  it('focuses the URL on a probe edit and Period on a push edit', async () => {
    const probe = await openEditor();
    expect(field(probe.form, strings.heartbeatProbeUrlLabel)).toHaveFocus();
    cleanup();
    const push = await openEditor({ check: pushCheck() });
    expect(field(push.form, strings.heartbeatPeriodLabel)).toHaveFocus();
  });

  it('shows the restart note on a probe edit only', async () => {
    const probe = await openEditor();
    expect(probe.form).toHaveTextContent(strings.heartbeatProbeEditRestartNote);
    cleanup();
    const push = await openEditor({ check: pushCheck() });
    expect(push.form).not.toHaveTextContent(strings.heartbeatProbeEditRestartNote);
    expect(push.form).not.toHaveTextContent('Type: Website probe.');
  });

  it.each([
    [3, 'Down after 3 failed probes in a row.', true],
    [null, 'Down after', false],
    [0, 'Down after', false],
    [-1, 'Down after', false],
  ])('interval hint for failureThreshold %s', async (threshold, text, present) => {
    const { form } = await openEditor({
      check: probeCheck({ probe: { failureThreshold: threshold } }),
    });
    if (present) {
      expect(form).toHaveTextContent(text);
    } else {
      expect(form).not.toHaveTextContent(text);
    }
  });
});

describe('probe edit form: diff', () => {
  it('sends only the changed interval', async () => {
    const { user, form, api } = await openEditor();
    await replaceText({
      user,
      input: field(form, strings.heartbeatProbeIntervalLabel),
      text: '10',
    });
    await save(user, form);
    await waitFor(() => expect(api.updateHeartbeat).toHaveBeenCalledTimes(1));
    expect(lastUpdate(api)).toEqual({ id: 7, probe: { intervalSeconds: 600 } });
  });

  it('closes without a request when nothing changed, reordered statuses included', async () => {
    const { user, form, api } = await openEditor();
    await replaceText({
      user,
      input: field(form, strings.heartbeatProbeStatusesLabel),
      text: '204, 200',
    });
    await save(user, form);
    expect(api.updateHeartbeat).not.toHaveBeenCalled();
    expect(screen.queryByRole('form', { name: `Edit ${LABEL}` })).toBeNull();
  });

  it('sends a changed URL and statuses and a cleared body match as null', async () => {
    const { user, form, api } = await openEditor({
      check: probeCheck({ probe: { bodyMatch: 'ok' } }),
    });
    await replaceText({
      user,
      input: field(form, strings.heartbeatProbeUrlLabel),
      text: 'https://other.example/x',
    });
    await replaceText({
      user,
      input: field(form, strings.heartbeatProbeStatusesLabel),
      text: '200',
    });
    await replaceText({ user, input: field(form, strings.heartbeatProbeBodyMatchLabel), text: '' });
    await save(user, form);
    await waitFor(() => expect(api.updateHeartbeat).toHaveBeenCalledTimes(1));
    expect(lastUpdate(api)).toEqual({
      id: 7,
      probe: { url: 'https://other.example/x', expectedStatuses: [200], bodyMatch: null },
    });
  });

  it('an unchanged empty body match sends nothing', async () => {
    const { user, form, api } = await openEditor();
    await replaceText({
      user,
      input: field(form, strings.heartbeatProbeTimeoutLabel),
      text: '4',
    });
    await save(user, form);
    await waitFor(() => expect(api.updateHeartbeat).toHaveBeenCalledTimes(1));
    expect(lastUpdate(api)).toEqual({ id: 7, probe: { timeoutSeconds: 4 } });
  });

  it('errors on a changed out-of-range interval and timeout and sends nothing', async () => {
    const { user, form, api } = await openEditor();
    await user.selectOptions(
      within(form).getByLabelText(strings.heartbeatProbeIntervalUnitLabel),
      strings.heartbeatUnitSeconds,
    );
    await replaceText({
      user,
      input: field(form, strings.heartbeatProbeIntervalLabel),
      text: '30',
    });
    await replaceText({
      user,
      input: field(form, strings.heartbeatProbeTimeoutLabel),
      text: '99',
    });
    await save(user, form);
    expect(api.updateHeartbeat).not.toHaveBeenCalled();
    expect(form).toHaveTextContent(strings.invalidProbeInterval);
    expect(form).toHaveTextContent(strings.invalidProbeTimeout);
    expect(field(form, strings.heartbeatProbeIntervalLabel)).toHaveFocus();
  });

  it('validates only changed probe fields: a stored whitespace body match never blocks', async () => {
    const check = probeCheck({ probe: { bodyMatch: '   ' } });
    const first = await openEditor({ check });
    await replaceText({
      user: first.user,
      input: field(first.form, strings.heartbeatProbeIntervalLabel),
      text: '10',
    });
    await save(first.user, first.form);
    await waitFor(() => expect(first.api.updateHeartbeat).toHaveBeenCalledTimes(1));
    expect(lastUpdate(first.api)).toEqual({ id: 7, probe: { intervalSeconds: 600 } });
    cleanup();

    const second = await openEditor({ check });
    await replaceText({
      user: second.user,
      input: field(second.form, strings.heartbeatProbeBodyMatchLabel),
      text: '  ',
    });
    await save(second.user, second.form);
    expect(second.api.updateHeartbeat).not.toHaveBeenCalled();
    expect(second.form).toHaveTextContent(strings.invalidProbeBodyMatch);
  });

  it('never sends period or grace on a probe edit, and a push edit never sends probe', async () => {
    const probe = await openEditor();
    await replaceText({
      user: probe.user,
      input: field(probe.form, strings.heartbeatProbeTimeoutLabel),
      text: '3',
    });
    await save(probe.user, probe.form);
    await waitFor(() => expect(probe.api.updateHeartbeat).toHaveBeenCalledTimes(1));
    const sent = lastUpdate(probe.api) as Record<string, unknown>;
    expect(sent.periodSeconds).toBeUndefined();
    expect(sent.graceSeconds).toBeUndefined();
    cleanup();

    const push = await openEditor({ check: pushCheck() });
    await replaceText({
      user: push.user,
      input: field(push.form, strings.heartbeatPeriodLabel),
      text: '2',
    });
    await save(push.user, push.form);
    await waitFor(() => expect(push.api.updateHeartbeat).toHaveBeenCalledTimes(1));
    expect((lastUpdate(push.api) as Record<string, unknown>).probe).toBeUndefined();
  });

  it('shows a server 400 in the alert and keeps the form and values', async () => {
    const updateHeartbeat = vi
      .fn()
      .mockRejectedValue(new ApiError({ status: 400, message: 'invalid probe.url' }));
    const { user, form } = await openEditor({
      check: probeCheck({ probe: { hasAccessToken: true } }),
      client: { updateHeartbeat },
    });
    await user.click(control(form, strings.heartbeatCredentialsReplace));
    await user.type(field(form, strings.heartbeatCredentialsClientIdLabel), 'zq-id');
    await user.type(field(form, strings.heartbeatCredentialsClientSecretLabel), 'zq-secret');
    await save(user, form);
    expect(await within(form).findByRole('alert')).toHaveTextContent('invalid probe.url');
    expect(within(form).getByRole('alert').textContent).not.toContain('zq-secret');
    expect(field(form, strings.heartbeatCredentialsClientIdLabel).value).toBe('zq-id');
    expect(credentialStatus(form)).toHaveTextContent(strings.heartbeatCredentialsReplacing);
  });
});

describe('probe edit form: stored credentials', () => {
  const stored = () => probeCheck({ probe: { hasAccessToken: true } });

  it('sends no access token while untouched', async () => {
    const { user, form, api } = await openEditor({ check: stored() });
    expect(credentialStatus(form)).toHaveTextContent(strings.heartbeatCredentialsStored);
    await replaceText({
      user,
      input: field(form, strings.heartbeatProbeTimeoutLabel),
      text: '4',
    });
    await save(user, form);
    await waitFor(() => expect(api.updateHeartbeat).toHaveBeenCalledTimes(1));
    expect(lastUpdate(api)).toEqual({ id: 7, probe: { timeoutSeconds: 4 } });
  });

  it('Clear sends access_token null, with focus on Undo and an Undo back to untouched', async () => {
    const { user, form, api } = await openEditor({ check: stored() });
    await user.click(control(form, strings.heartbeatCredentialsClear));
    expect(control(form, strings.heartbeatCredentialsUndo)).toHaveFocus();
    expect(credentialStatus(form)).toHaveTextContent(strings.heartbeatCredentialsClearing);
    await user.click(control(form, strings.heartbeatCredentialsUndo));
    expect(control(form, strings.heartbeatCredentialsClear)).toHaveFocus();
    await save(user, form);
    expect(api.updateHeartbeat).not.toHaveBeenCalled();
  });

  it('Clear then Save sends a null access token', async () => {
    const { user, form, api } = await openEditor({ check: stored() });
    await user.click(control(form, strings.heartbeatCredentialsClear));
    await save(user, form);
    await waitFor(() => expect(api.updateHeartbeat).toHaveBeenCalledTimes(1));
    expect(lastUpdate(api)).toEqual({ id: 7, accessToken: null });
  });

  it('Replace sends the pair, moves focus, and never echoes the typed values', async () => {
    const { user, form, api } = await openEditor({ check: stored() });
    await user.click(control(form, strings.heartbeatCredentialsReplace));
    const clientId = field(form, strings.heartbeatCredentialsClientIdLabel);
    expect(clientId).toHaveFocus();
    expect(field(form, strings.heartbeatCredentialsClientSecretLabel)).toHaveAttribute(
      'type',
      'password',
    );
    await user.type(clientId, 'zq-id');
    await user.type(field(form, strings.heartbeatCredentialsClientSecretLabel), 'zq-secret');
    expect(credentialStatus(form).textContent).toBe(strings.heartbeatCredentialsReplacing);
    await save(user, form);
    await waitFor(() => expect(api.updateHeartbeat).toHaveBeenCalledTimes(1));
    expect(lastUpdate(api)).toEqual({
      id: 7,
      accessToken: { clientId: 'zq-id', clientSecret: 'zq-secret' },
    });
  });

  it('Cancel replace discards typed values, returns focus to Replace and sends nothing', async () => {
    const { user, form, api } = await openEditor({ check: stored() });
    await user.click(control(form, strings.heartbeatCredentialsReplace));
    await user.type(field(form, strings.heartbeatCredentialsClientIdLabel), 'zq-id');
    await user.click(control(form, strings.heartbeatCredentialsCancelReplace));
    expect(control(form, strings.heartbeatCredentialsReplace)).toHaveFocus();
    await user.click(control(form, strings.heartbeatCredentialsReplace));
    expect(field(form, strings.heartbeatCredentialsClientIdLabel).value).toBe('');
    await user.click(control(form, strings.heartbeatCredentialsCancelReplace));
    await save(user, form);
    expect(api.updateHeartbeat).not.toHaveBeenCalled();
  });

  it('a blank pair while replacing errors on both halves and stays in replacing', async () => {
    const { user, form, api } = await openEditor({ check: stored() });
    await user.click(control(form, strings.heartbeatCredentialsReplace));
    await save(user, form);
    expect(api.updateHeartbeat).not.toHaveBeenCalled();
    expect(within(form).getAllByText(strings.invalidProbeCredentialPair)).toHaveLength(2);
    expect(field(form, strings.heartbeatCredentialsClientIdLabel)).toHaveFocus();
    expect(credentialStatus(form)).toHaveTextContent(strings.heartbeatCredentialsReplacing);

    await user.click(control(form, strings.heartbeatCredentialsCancelReplace));
    expect(within(form).queryByText(strings.invalidProbeCredentialPair)).toBeNull();
    expect(form.querySelector('[aria-invalid="true"]')).toBeNull();
    await save(user, form);
    expect(api.updateHeartbeat).not.toHaveBeenCalled();
    await waitFor(() => expect(screen.queryByRole('form', { name: `Edit ${LABEL}` })).toBeNull());
  });

  it('a half pair errors on the empty half', async () => {
    const { user, form, api } = await openEditor({ check: stored() });
    await user.click(control(form, strings.heartbeatCredentialsReplace));
    await user.type(field(form, strings.heartbeatCredentialsClientIdLabel), 'zq-id');
    await save(user, form);
    expect(api.updateHeartbeat).not.toHaveBeenCalled();
    expect(within(form).getAllByText(strings.invalidProbeCredentialPair)).toHaveLength(1);
    expect(field(form, strings.heartbeatCredentialsClientSecretLabel)).toHaveFocus();
  });

  it('focuses an earlier invalid field before a blank pair', async () => {
    const { user, form } = await openEditor({ check: stored() });
    await user.click(control(form, strings.heartbeatCredentialsReplace));
    await replaceText({
      user,
      input: field(form, strings.heartbeatProbeTimeoutLabel),
      text: '99',
    });
    await save(user, form);
    expect(field(form, strings.heartbeatProbeTimeoutLabel)).toHaveFocus();
  });

  it('focuses the client id before the destinations when both are invalid', async () => {
    const { user, form } = await openEditor({ check: stored() });
    await user.click(control(form, strings.heartbeatCredentialsReplace));
    await user.click(within(form).getByRole('checkbox', { name: /^ops_discord/ }));
    await save(user, form);
    expect(field(form, strings.heartbeatCredentialsClientIdLabel)).toHaveFocus();
  });

  it('shows the optional pair with no Replace or Clear when nothing is stored', async () => {
    const { user, form, api } = await openEditor();
    expect(within(form).queryByRole('button', { name: strings.heartbeatCredentialsReplace })).toBe(
      null,
    );
    expect(within(form).queryByRole('button', { name: strings.heartbeatCredentialsClear })).toBe(
      null,
    );
    expect(within(form).queryByRole('status')).toBeNull();
    expect(form).not.toHaveTextContent('Cleared if you switch to Push.');
    await user.type(field(form, strings.heartbeatCredentialsClientIdLabel), 'zq-id');
    await user.type(field(form, strings.heartbeatCredentialsClientSecretLabel), 'zq-secret');
    await save(user, form);
    await waitFor(() => expect(api.updateHeartbeat).toHaveBeenCalledTimes(1));
    expect(lastUpdate(api)).toEqual({
      id: 7,
      accessToken: { clientId: 'zq-id', clientSecret: 'zq-secret' },
    });
  });

  it('blank optional pair with nothing stored sends nothing', async () => {
    const { user, form, api } = await openEditor();
    await save(user, form);
    expect(api.updateHeartbeat).not.toHaveBeenCalled();
  });

  it('makes the controls inert while a save is in flight', async () => {
    const updateHeartbeat = vi.fn().mockReturnValue(new Promise(() => undefined));
    const { user, form } = await openEditor({ check: stored(), client: { updateHeartbeat } });
    await replaceText({
      user,
      input: field(form, strings.heartbeatProbeTimeoutLabel),
      text: '4',
    });
    await save(user, form);
    await waitFor(() => expect(updateHeartbeat).toHaveBeenCalledTimes(1));
    const replace = control(form, strings.heartbeatCredentialsReplace);
    const clear = control(form, strings.heartbeatCredentialsClear);
    expect(replace).toHaveAttribute('aria-disabled', 'true');
    expect(clear).toHaveAttribute('aria-disabled', 'true');
    await user.click(replace);
    await user.click(clear);
    expect(credentialStatus(form)).toHaveTextContent(strings.heartbeatCredentialsStored);
  });

  it('adds exactly one status region only with stored credentials', async () => {
    const withStored = await openEditor({ check: stored() });
    expect(screen.getAllByRole('status')).toHaveLength(3);
    expect(screen.getAllByRole('status')[2]).toBe(credentialStatus(withStored.form));
    cleanup();
    await openEditor();
    expect(screen.getAllByRole('status')).toHaveLength(2);
  });
});

describe('probe edit form: credentials hint', () => {
  const HINT_REJECTED = strings.heartbeatCredentialsHintTokenRejected;
  const HINT_MISSING = strings.heartbeatCredentialsHintTokenMissing;
  const HINT_CONFIG_STORED = strings.heartbeatCredentialsHintConfigStored;
  const HINT_CONFIG_MISSING = strings.heartbeatCredentialsHintConfigMissing;
  const ALL = [HINT_REJECTED, HINT_MISSING, HINT_CONFIG_STORED, HINT_CONFIG_MISSING];

  async function hintsShown({
    hasAccessToken,
    lastResult,
    downReason = null,
  }: {
    hasAccessToken: boolean;
    lastResult: HeartbeatProbeResult | null;
    downReason?: string | null;
  }): Promise<string[]> {
    const { form } = await openEditor({
      check: probeCheck({
        check: downReason === null ? {} : { state: 'down', downReason },
        probe: { hasAccessToken, lastResult },
      }),
    });
    const text = form.textContent ?? '';
    cleanup();
    return ALL.filter((hint) => text.includes(hint));
  }

  it('shows the rejected hint for a 403 with a stored token', async () => {
    expect(
      await hintsShown({
        hasAccessToken: true,
        lastResult: result({ status: 403, reason: 'http_status' }),
      }),
    ).toEqual([HINT_REJECTED]);
  });

  it('shows the missing hint for a 403 without credentials, with no Replace', async () => {
    expect(
      await hintsShown({
        hasAccessToken: false,
        lastResult: result({ status: 403, reason: 'http_status' }),
      }),
    ).toEqual([HINT_MISSING]);
    expect(HINT_MISSING).not.toContain('Replace');
  });

  it('shows the config hint by stored credentials', async () => {
    expect(
      await hintsShown({
        hasAccessToken: true,
        lastResult: result({ status: null, reason: 'config' }),
      }),
    ).toEqual([HINT_CONFIG_STORED]);
    expect(
      await hintsShown({ hasAccessToken: false, lastResult: null, downReason: 'config' }),
    ).toEqual([HINT_CONFIG_MISSING]);
    expect(HINT_CONFIG_MISSING).not.toContain('Replace');
  });

  it('a 403 hint wins over a config down reason', async () => {
    const lastResult = result({ status: 403, reason: 'http_status' });
    expect(await hintsShown({ hasAccessToken: true, lastResult, downReason: 'config' })).toEqual([
      HINT_REJECTED,
    ]);
    expect(await hintsShown({ hasAccessToken: false, lastResult, downReason: 'config' })).toEqual([
      HINT_MISSING,
    ]);
  });

  it.each([
    ['401', result({ status: 401, reason: 'http_status' })],
    ['500', result({ status: 500, reason: 'http_status' })],
    ['a success', result()],
    ['no result', null],
  ])('shows no hint for %s', async (_name, lastResult) => {
    expect(await hintsShown({ hasAccessToken: true, lastResult })).toEqual([]);
  });
});

describe('probe edit form: quiet reload', () => {
  it('keeps the staged state, typed values and hint when a reload flips hasAccessToken', async () => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date(NOW_ISO));
    const initial = probeCheck({
      check: { state: 'down', downReason: 'config' },
      probe: { hasAccessToken: true },
    });
    const flipped = probeCheck({
      check: { state: 'down', downReason: 'config' },
      probe: { hasAccessToken: false },
    });
    const listHeartbeats = vi.fn().mockResolvedValueOnce([initial]).mockResolvedValue([flipped]);
    render(
      <Heartbeats client={buildClient({ listHeartbeats })} destinationsState={DESTINATIONS} />,
    );
    await act(async () => {
      await vi.advanceTimersByTimeAsync(0);
    });
    fireEvent.click(screen.getByRole('button', { name: `Edit ${LABEL}` }));
    const form = screen.getByRole('form', { name: `Edit ${LABEL}` });
    fireEvent.click(control(form, strings.heartbeatCredentialsReplace));
    fireEvent.change(field(form, strings.heartbeatCredentialsClientIdLabel), {
      target: { value: 'zq-id' },
    });
    fireEvent.change(field(form, strings.heartbeatCredentialsClientSecretLabel), {
      target: { value: 'zq-secret' },
    });

    await act(async () => {
      await vi.advanceTimersByTimeAsync(TICK);
    });
    expect(listHeartbeats.mock.calls.length).toBeGreaterThan(1);

    const same = screen.getByRole('form', { name: `Edit ${LABEL}` });
    expect(field(same, strings.heartbeatCredentialsClientIdLabel).value).toBe('zq-id');
    expect(field(same, strings.heartbeatCredentialsClientSecretLabel).value).toBe('zq-secret');
    expect(credentialStatus(same)).toHaveTextContent(strings.heartbeatCredentialsReplacing);
    expect(same).toHaveTextContent(strings.heartbeatCredentialsHintConfigStored);
    expect(same).not.toHaveTextContent(strings.heartbeatCredentialsHintConfigMissing);
  });
});
