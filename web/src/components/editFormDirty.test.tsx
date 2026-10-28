import { cleanup, render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';

import type {
  DestinationSummary,
  HeartbeatCheck,
  HeartbeatProbe,
  RouteSummary,
} from '../api/deliveryClient';
import { strings } from '../strings';
import { HeartbeatForm } from './HeartbeatForm';
import { RouteForm } from './RouteForm';

afterEach(() => {
  cleanup();
});

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

const DESTINATIONS = [destination('ops_discord'), destination('ops_other')];

const ROUTE: RouteSummary = {
  id: 1,
  appPattern: '*',
  channelPattern: 'alerts*',
  destinations: ['ops_discord'],
  enabled: true,
  ownerKind: 'operator',
  ownerAppId: null,
  createdAt: '2026-10-01T00:00:00Z',
  updatedAt: '2026-10-01T00:00:00Z',
};

function pushCheck(probe: HeartbeatProbe | null = null): HeartbeatCheck {
  return {
    id: 7,
    appId: 'zqapp',
    name: 'site',
    periodSeconds: probe === null ? 3600 : 300,
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
    probe,
    createdAt: '2026-10-17T10:00:00Z',
    updatedAt: '2026-10-18T08:01:00Z',
  } as HeartbeatCheck;
}

function probeCheck(): HeartbeatCheck {
  return pushCheck({
    url: 'https://host.example/health',
    expectedStatuses: [200, 204],
    bodyMatch: null,
    timeoutSeconds: 5,
    intervalSeconds: 300,
    consecutiveFailures: 0,
    failureThreshold: 3,
    hasAccessToken: false,
    lastResult: null,
  } as HeartbeatProbe);
}

/** The last value passed to the spy, or undefined before any call. */
function lastDirty(spy: ReturnType<typeof vi.fn>): unknown {
  return spy.mock.calls[spy.mock.calls.length - 1]?.[0];
}

async function replace({
  user,
  label,
  text,
}: {
  user: ReturnType<typeof userEvent.setup>;
  label: string;
  text: string;
}) {
  const input = screen.getByLabelText(label);
  await user.clear(input);
  await user.type(input, text);
}

function renderHeartbeat({
  check,
  onDirtyChange,
}: {
  check: HeartbeatCheck;
  onDirtyChange: () => void;
}) {
  return render(
    <HeartbeatForm
      check={check}
      destinations={DESTINATIONS}
      destinationsReady
      onSubmit={vi.fn()}
      onClose={vi.fn()}
      onDirtyChange={onDirtyChange}
    />,
  );
}

describe('RouteForm onDirtyChange', () => {
  function renderRoute(onDirtyChange: () => void) {
    return render(
      <RouteForm
        route={ROUTE}
        destinations={DESTINATIONS}
        onSubmit={vi.fn()}
        onClose={vi.fn()}
        onDirtyChange={onDirtyChange}
      />,
    );
  }

  it('is clean on mount, dirty after an edit, clean after reverting, false on unmount', async () => {
    const user = userEvent.setup();
    const spy = vi.fn();
    const view = renderRoute(spy);
    expect(lastDirty(spy)).toBe(false);

    await user.type(screen.getByLabelText(strings.routeAppLabel), 'x');
    expect(lastDirty(spy)).toBe(true);
    await user.click(screen.getByLabelText('ops_other'));
    await user.click(screen.getByLabelText('ops_other'));
    await replace({ user, label: strings.routeAppLabel, text: '*' });
    expect(lastDirty(spy)).toBe(false);

    await user.click(screen.getByLabelText('ops_other'));
    expect(lastDirty(spy)).toBe(true);
    view.unmount();
    expect(lastDirty(spy)).toBe(false);
  });
});

describe('HeartbeatForm (push) onDirtyChange', () => {
  it('is clean on mount, dirty after an edit, clean after reverting, false on unmount', async () => {
    const user = userEvent.setup();
    const spy = vi.fn();
    const view = renderHeartbeat({ check: pushCheck(), onDirtyChange: spy });
    expect(lastDirty(spy)).toBe(false);

    await replace({ user, label: strings.heartbeatGraceLabel, text: '20' });
    expect(lastDirty(spy)).toBe(true);
    await replace({ user, label: strings.heartbeatGraceLabel, text: '10' });
    expect(lastDirty(spy)).toBe(false);

    const other = () => screen.getByRole('checkbox', { name: /^ops_other/ });
    await user.click(other());
    expect(lastDirty(spy)).toBe(true);
    await user.click(other());
    expect(lastDirty(spy)).toBe(false);

    await user.click(other());
    view.unmount();
    expect(lastDirty(spy)).toBe(false);
  });
});

describe('HeartbeatForm (probe) onDirtyChange', () => {
  it('is clean on mount, dirty after an edit, clean after reverting, false on unmount', async () => {
    const user = userEvent.setup();
    const spy = vi.fn();
    const view = renderHeartbeat({ check: probeCheck(), onDirtyChange: spy });
    expect(lastDirty(spy)).toBe(false);

    await replace({ user, label: strings.heartbeatProbeUrlLabel, text: 'https://other.example/x' });
    expect(lastDirty(spy)).toBe(true);
    await replace({
      user,
      label: strings.heartbeatProbeUrlLabel,
      text: 'https://host.example/health',
    });
    expect(lastDirty(spy)).toBe(false);

    // Statuses reordered are the same set.
    await replace({ user, label: strings.heartbeatProbeStatusesLabel, text: '204, 200' });
    expect(lastDirty(spy)).toBe(false);
    await replace({ user, label: strings.heartbeatProbeStatusesLabel, text: '500' });
    expect(lastDirty(spy)).toBe(true);
    await replace({ user, label: strings.heartbeatProbeStatusesLabel, text: '200, 204' });
    expect(lastDirty(spy)).toBe(false);

    await replace({ user, label: strings.heartbeatProbeTimeoutLabel, text: '9' });
    expect(lastDirty(spy)).toBe(true);
    view.unmount();
    expect(lastDirty(spy)).toBe(false);
  });
});
