import { cleanup, render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import type { ReactElement } from 'react';
import { afterEach, describe, expect, it, vi } from 'vitest';

import type { DeliveryApiClient, ReadinessResult } from '../api/deliveryClient';
import { strings } from '../strings';
import { setCompactViewport } from '../test-viewport';
import { AppIdentities } from './AppIdentities';
import { DeliveryLog } from './DeliveryLog';
import { DeliveryStatusCard } from './DeliveryStatusCard';
import { Heartbeats } from './Heartbeats';
import { PendingSends } from './PendingSends';
import { RefreshButton } from './RefreshButton';

afterEach(() => {
  cleanup();
});

describe('RefreshButton', () => {
  it('roomy: renders the visible label with the caller class', () => {
    render(<RefreshButton label="Refresh x" className="ghost" onClick={() => undefined} />);

    const button = screen.getByRole('button', { name: 'Refresh x' });
    expect(button).toHaveTextContent('Refresh x');
    expect(button).toHaveClass('ghost');
    expect(button.querySelector('svg')).toBeNull();
  });

  it('compact: renders an icon-only button with the same accessible name, title and hidden svg', () => {
    setCompactViewport(true);
    render(<RefreshButton label="Refresh x" className="ghost" onClick={() => undefined} />);

    const button = screen.getByRole('button', { name: 'Refresh x' });
    expect(button).toHaveAttribute('aria-label', 'Refresh x');
    expect(button).toHaveAttribute('title', 'Refresh x');
    expect(button.textContent).toBe('');
    expect(button).not.toHaveClass('ghost');
    expect(button.querySelector('svg')).toHaveAttribute('aria-hidden', 'true');
    expect(button).not.toHaveAttribute('aria-busy');
    expect(button).not.toHaveAttribute('data-dimmed');
  });

  it.each([false, true])(
    'busy (compact=%s): aria-disabled, aria-busy, not native disabled, clicks ignored, focus kept',
    async (compact) => {
      setCompactViewport(compact);
      const onClick = vi.fn();
      const user = userEvent.setup();
      render(<RefreshButton label="Refresh x" busy onClick={onClick} />);

      const button = screen.getByRole('button', { name: 'Refresh x' });
      button.focus();
      expect(button).toHaveAttribute('aria-disabled', 'true');
      expect(button).toHaveAttribute('aria-busy', 'true');
      expect(button).not.toBeDisabled();
      if (compact) {
        expect(button).toHaveAttribute('data-dimmed', 'true');
      }

      await user.click(button);

      expect(onClick).not.toHaveBeenCalled();
      expect(button).toHaveFocus();
    },
  );

  it.each([false, true])('disabled (compact=%s): native disabled, no aria-disabled', (compact) => {
    setCompactViewport(compact);
    render(<RefreshButton label="Refresh x" disabled onClick={() => undefined} />);

    const button = screen.getByRole('button', { name: 'Refresh x' });
    expect(button).toBeDisabled();
    expect(button).not.toHaveAttribute('aria-disabled');
    if (compact) {
      expect(button).toHaveAttribute('data-dimmed', 'true');
    }
  });

  it.each([false, true])('idle (compact=%s): click calls onClick', async (compact) => {
    setCompactViewport(compact);
    const onClick = vi.fn();
    const user = userEvent.setup();
    render(<RefreshButton label="Refresh x" onClick={onClick} />);

    await user.click(screen.getByRole('button', { name: 'Refresh x' }));

    expect(onClick).toHaveBeenCalledTimes(1);
  });
});

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
    createHeartbeat: vi.fn(),
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

const READY: ReadinessResult = {
  kind: 'ok',
  schemaVersion: 4,
  expectedSchemaVersion: 4,
  heartbeatChecker: { lastTickAt: '2026-10-19T09:00:00Z', stale: false },
  lastMigrationFailure: null,
};

interface Site {
  name: string;
  label: string;
  /** The client method the panel loads through; counted to see a refresh fire. */
  method: keyof DeliveryApiClient;
  /** What a settled load resolves to. */
  settled: unknown;
  element: (client: DeliveryApiClient) => ReactElement;
  /** What the first load resolves to when the refresh must visibly change the status region. */
  firstLoad?: unknown;
  /** The text the site's status region announces once the refresh settles. */
  announced?: string;
}

const PENDING_REQUEST = {
  id: 1,
  appId: 'app_1',
  githubRepository: '4IRL/app_1',
  githubRepositoryId: 812345601,
  workflowPath: '.github/workflows/prod-deploy.yml',
  gitRef: 'refs/heads/main',
  environment: 'production',
  githubRunId: '14022',
  status: 'pending',
  createdAt: '2026-10-21T18:04:00Z',
  updatedAt: '2026-10-21T18:04:00Z',
};

const UP_CHECK = {
  id: 1,
  appId: 'urls4irl',
  name: 'check_1',
  periodSeconds: 86400,
  graceSeconds: 7200,
  paused: false,
  state: 'up',
  lastPingAt: '2026-10-18T08:01:00Z',
  lastPingKind: 'success',
  downSince: null,
  deadlineAt: '2099-01-01T10:00:00Z',
  destinations: [{ name: 'ops_discord', type: 'discord', enabled: true }],
  ownerKind: 'operator',
  ownerAppId: null,
  downReason: null,
  probe: null,
  createdAt: '2026-10-17T10:00:00Z',
  updatedAt: '2026-10-18T08:01:00Z',
};

const SITES: Site[] = [
  {
    name: 'DeliveryStatusCard',
    label: strings.deliveryStatusRefresh,
    method: 'getReadiness',
    settled: READY,
    firstLoad: { kind: 'unavailable' },
    announced: strings.deliveryStatusReady,
    element: (client) => <DeliveryStatusCard client={client} />,
  },
  {
    name: 'AppIdentities',
    label: strings.identitiesRefresh,
    method: 'listIdentityRequests',
    settled: [PENDING_REQUEST],
    firstLoad: [],
    announced: strings.identitiesPendingSummary({ count: 1 }),
    element: (client) => <AppIdentities client={client} />,
  },
  {
    name: 'Heartbeats',
    label: strings.heartbeatsRefresh,
    method: 'listHeartbeats',
    settled: [UP_CHECK],
    firstLoad: [],
    announced: strings.heartbeatsDownSummary({ down: 0, total: 1 }),
    element: (client) => (
      <Heartbeats client={client} destinationsState={{ status: 'ready', destinations: [] }} />
    ),
  },
  {
    name: 'PendingSends',
    label: strings.pendingSendsRefresh,
    method: 'listScheduledMessages',
    settled: { items: [], total: 0 },
    element: (client) => <PendingSends client={client} />,
  },
  {
    name: 'DeliveryLog',
    label: strings.deliveryLogRefresh,
    method: 'listDeliveries',
    settled: [],
    element: (client) => <DeliveryLog client={client} />,
  },
];

describe.each(SITES)('$name refresh control', (site) => {
  it.each([false, true])(
    'is aria-disabled and ignores clicks while loading (compact=%s)',
    async (compact) => {
      setCompactViewport(compact);
      const load = vi.fn(() => new Promise<never>(() => undefined));
      const client = buildClient({ [site.method]: load } as Partial<DeliveryApiClient>);
      const user = userEvent.setup();
      render(site.element(client));

      const button = screen.getByRole('button', { name: site.label });
      expect(button).toHaveAttribute('aria-disabled', 'true');
      expect(button).not.toBeDisabled();
      button.focus();
      const callsBefore = load.mock.calls.length;

      await user.click(button);

      expect(load.mock.calls.length).toBe(callsBefore);
      expect(button).toHaveFocus();
    },
  );

  it('roomy: shows its text and refreshes once settled, with a status region present', async () => {
    const load = vi.fn().mockResolvedValue(site.settled);
    const client = buildClient({ [site.method]: load } as Partial<DeliveryApiClient>);
    const user = userEvent.setup();
    render(site.element(client));

    const button = screen.getByRole('button', { name: site.label });
    expect(button).toHaveTextContent(site.label);
    await waitFor(() => {
      expect(button).not.toHaveAttribute('aria-disabled');
    });
    expect(screen.getAllByRole('status').length).toBeGreaterThan(0);
    const callsBefore = load.mock.calls.length;

    await user.click(button);

    await waitFor(() => {
      expect(load.mock.calls.length).toBe(callsBefore + 1);
    });
  });

  it('compact: is icon-only with the same name, title and a hidden svg', async () => {
    setCompactViewport(true);
    const load = vi.fn().mockResolvedValue(site.settled);
    const client = buildClient({ [site.method]: load } as Partial<DeliveryApiClient>);
    render(site.element(client));

    const button = screen.getByRole('button', { name: site.label });
    expect(button.textContent).toBe('');
    expect(button).toHaveAttribute('title', site.label);
    expect(button.querySelector('svg')).toHaveAttribute('aria-hidden', 'true');
    await waitFor(() => {
      expect(button).not.toHaveAttribute('aria-busy');
    });
  });
});

// PendingSends and DeliveryLog announce only cancel and replay results in their status regions,
// not a refresh, so they have no refresh announcement to assert here.
const ANNOUNCING_SITES = SITES.flatMap((site) =>
  site.announced === undefined ? [] : [{ ...site, announced: site.announced }],
);

describe.each(ANNOUNCING_SITES)('$name refresh announcement', (site) => {
  it('compact: the existing status region announces the refresh result', async () => {
    setCompactViewport(true);
    const load = vi.fn().mockResolvedValueOnce(site.firstLoad).mockResolvedValue(site.settled);
    const client = buildClient({ [site.method]: load } as Partial<DeliveryApiClient>);
    const user = userEvent.setup();
    render(site.element(client));
    const button = screen.getByRole('button', { name: site.label });
    await waitFor(() => {
      expect(button).not.toHaveAttribute('aria-busy');
    });
    expect(screen.queryByText(site.announced)).not.toBeInTheDocument();

    await user.click(button);

    const region = await screen.findByText(site.announced);
    expect(region.closest('[role="status"]')).not.toBeNull();
  });
});
