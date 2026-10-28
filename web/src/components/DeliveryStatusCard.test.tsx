import { act, render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { StrictMode } from 'react';
import { afterEach, describe, expect, it, vi } from 'vitest';

import type { DeliveryApiClient, ReadinessResult } from '../api/deliveryClient';
import { strings } from '../strings';
import { useTimeZone } from '../test-timezone';
import { DeliveryStatusCard } from './DeliveryStatusCard';

function clientReturning(getReadiness: DeliveryApiClient['getReadiness']): DeliveryApiClient {
  return { getReadiness } as DeliveryApiClient;
}

function clientWith(result: ReadinessResult): DeliveryApiClient {
  return clientReturning(vi.fn().mockResolvedValue(result));
}

describe('DeliveryStatusCard', () => {
  it('shows Checking with Refresh disabled until the first result settles', async () => {
    render(
      <DeliveryStatusCard
        client={clientReturning(vi.fn(() => new Promise<ReadinessResult>(() => undefined)))}
      />,
    );

    expect(screen.getByText(strings.deliveryStatusChecking)).toBeInTheDocument();
    const refresh = screen.getByRole('button', { name: strings.deliveryStatusRefresh });
    expect(refresh).toHaveAttribute('aria-disabled', 'true');
    expect(refresh).not.toBeDisabled();
  });

  it('renders ready with no migration failure', async () => {
    render(
      <DeliveryStatusCard
        client={clientWith({
          kind: 'ok',
          schemaVersion: 4,
          expectedSchemaVersion: 4,
          heartbeatChecker: { lastTickAt: '2026-10-19T09:00:00Z', stale: false },
          lastMigrationFailure: null,
        })}
      />,
    );

    expect(await screen.findByText(strings.deliveryStatusReady)).toBeInTheDocument();
    expect(screen.getByText('4 of 4')).toBeInTheDocument();
    expect(screen.getByText(strings.deliveryStatusNone)).toBeInTheDocument();
    expect(screen.queryByRole('alert')).not.toBeInTheDocument();
    expect(screen.getByRole('button', { name: strings.deliveryStatusRefresh })).toBeEnabled();
  });

  describe('heartbeat checker fact', () => {
    function readyWith(heartbeatChecker: { lastTickAt: string | null; stale: boolean }) {
      return clientWith({
        kind: 'ok',
        schemaVersion: 4,
        expectedSchemaVersion: 4,
        lastMigrationFailure: null,
        heartbeatChecker,
      });
    }

    const NOW = '2026-10-19T09:00:00Z';

    // Only Date is faked (timers stay real), so the card's "last run" is exact.
    function freezeClock() {
      vi.useFakeTimers({ toFake: ['Date'] });
      vi.setSystemTime(new Date(NOW));
    }

    afterEach(() => {
      vi.useRealTimers();
    });

    it('shows Running with the relative last run and no warning while fresh', async () => {
      freezeClock();
      render(
        <DeliveryStatusCard
          client={readyWith({ lastTickAt: '2026-10-19T08:59:48Z', stale: false })}
        />,
      );

      expect(await screen.findByText(strings.heartbeatCheckerLabel)).toBeInTheDocument();
      expect(screen.getByText('Running · last run 12 s ago')).toBeInTheDocument();
      expect(screen.queryByText(strings.heartbeatCheckerStalledWarning)).not.toBeInTheDocument();
    });

    it('shows Stalled in the accent colour plus the warning, while the pill still says Ready', async () => {
      freezeClock();
      render(
        <DeliveryStatusCard
          client={readyWith({ lastTickAt: '2026-10-19T08:53:00Z', stale: true })}
        />,
      );

      const fact = await screen.findByText('Stalled · last run 7 min ago');
      expect(fact).toHaveClass('status-card__stalled');
      expect(screen.getByText(strings.heartbeatCheckerStalledWarning)).toBeInTheDocument();
      expect(screen.getByText(strings.deliveryStatusReady)).toBeInTheDocument();
      expect(screen.queryByRole('alert')).not.toBeInTheDocument();
    });

    it('shows never for a checker that has not ticked', async () => {
      render(<DeliveryStatusCard client={readyWith({ lastTickAt: null, stale: true })} />);

      expect(await screen.findByText('Stalled · last run never')).toBeInTheDocument();
      expect(screen.getByText(strings.heartbeatCheckerStalledWarning)).toBeInTheDocument();
    });

    it('shows the fact on a schema mismatch too', async () => {
      render(
        <DeliveryStatusCard
          client={clientWith({
            kind: 'schema_mismatch',
            schemaVersion: 3,
            expectedSchemaVersion: 4,
            lastMigrationFailure: null,
            heartbeatChecker: { lastTickAt: null, stale: true },
          })}
        />,
      );

      expect(await screen.findByText('Stalled · last run never')).toBeInTheDocument();
    });

    it('omits the fact when the service is unavailable', async () => {
      render(<DeliveryStatusCard client={clientWith({ kind: 'unavailable' })} />);

      await screen.findByText(strings.deliveryStatusUnavailable);
      expect(screen.queryByText(strings.heartbeatCheckerLabel)).not.toBeInTheDocument();
    });
  });

  it('renders ready with a failed newer migration as a warning, not an alert', async () => {
    render(
      <DeliveryStatusCard
        client={clientWith({
          kind: 'ok',
          schemaVersion: 4,
          expectedSchemaVersion: 4,
          heartbeatChecker: { lastTickAt: '2026-10-19T09:00:00Z', stale: false },
          lastMigrationFailure: {
            version: 5,
            error: 'ERROR: column "x" already exists',
            failedAt: '2026-10-17T06:00:00Z',
          },
        })}
      />,
    );

    expect(await screen.findByText('v5 · 2026-10-17 06:00 UTC')).toBeInTheDocument();
    expect(screen.getByText('ERROR: column "x" already exists')).toBeInTheDocument();
    expect(screen.getByText(/A newer migration \(v5\) failed/)).toBeInTheDocument();
    expect(screen.getByText(/The service still runs on schema 4/)).toBeInTheDocument();
    expect(screen.queryByRole('alert')).not.toBeInTheDocument();
  });

  describe('in a non-UTC zone', () => {
    useTimeZone('America/Los_Angeles');

    it('words the failed migration time in the viewer zone with its abbreviation', async () => {
      render(
        <DeliveryStatusCard
          client={clientWith({
            kind: 'ok',
            schemaVersion: 4,
            expectedSchemaVersion: 4,
            heartbeatChecker: { lastTickAt: '2026-10-19T09:00:00Z', stale: false },
            lastMigrationFailure: {
              version: 5,
              error: 'ERROR: column "x" already exists',
              failedAt: '2026-10-17T06:00:00Z',
            },
          })}
        />,
      );

      expect(await screen.findByText('v5 · 2026-10-16 23:00 PDT')).toBeInTheDocument();
    });
  });

  it('renders a schema mismatch as a danger alert naming the failed migration', async () => {
    render(
      <DeliveryStatusCard
        client={clientWith({
          kind: 'schema_mismatch',
          schemaVersion: 3,
          expectedSchemaVersion: 4,
          heartbeatChecker: { lastTickAt: '2026-10-19T09:00:00Z', stale: false },
          lastMigrationFailure: {
            version: 4,
            error: 'ERROR: relation "app_publishers" already exists',
            failedAt: '2026-10-17T06:00:00Z',
          },
        })}
      />,
    );

    expect(await screen.findByText(strings.deliveryStatusSchemaMismatch)).toBeInTheDocument();
    expect(screen.getByText('3 of 4')).toBeInTheDocument();
    const alert = screen.getByRole('alert');
    expect(alert).toHaveTextContent('Migration v4 failed');
    expect(alert).toHaveTextContent('ERROR: relation "app_publishers" already exists');
  });

  it('renders a schema mismatch without a recorded failure', async () => {
    render(
      <DeliveryStatusCard
        client={clientWith({
          kind: 'schema_mismatch',
          schemaVersion: 3,
          expectedSchemaVersion: 4,
          heartbeatChecker: { lastTickAt: '2026-10-19T09:00:00Z', stale: false },
          lastMigrationFailure: null,
        })}
      />,
    );

    expect(await screen.findByRole('alert')).toHaveTextContent(
      strings.deliveryStatusMismatchNoFailure,
    );
  });

  it('renders unavailable', async () => {
    render(<DeliveryStatusCard client={clientWith({ kind: 'unavailable' })} />);

    expect(await screen.findByText(strings.deliveryStatusUnavailable)).toBeInTheDocument();
    expect(screen.getByRole('alert')).toHaveTextContent(strings.deliveryStatusUnavailableDetail);
    expect(screen.getByText(strings.noValue)).toBeInTheDocument();
  });

  it('renders unreachable with the message', async () => {
    render(
      <DeliveryStatusCard
        client={clientWith({ kind: 'unreachable', message: 'upstream auth failed' })}
      />,
    );

    expect(await screen.findByText(strings.deliveryStatusUnreachable)).toBeInTheDocument();
    expect(screen.getByRole('alert')).toHaveTextContent(
      'Could not reach delivery-api: upstream auth failed',
    );
  });

  it('falls back to unreachable if getReadiness unexpectedly rejects', async () => {
    render(
      <DeliveryStatusCard client={clientReturning(vi.fn().mockRejectedValue(new Error('x')))} />,
    );

    expect(await screen.findByText(strings.deliveryStatusUnreachable)).toBeInTheDocument();
  });

  it('announces the settled state through a status region', async () => {
    render(<DeliveryStatusCard client={clientWith({ kind: 'unavailable' })} />);

    const region = screen.getByRole('status');
    expect(region).toHaveTextContent(strings.deliveryStatusChecking);
    await waitFor(() => expect(region).toHaveTextContent(strings.deliveryStatusUnavailable));
  });

  it('fetches once on mount and only reloads on Refresh', async () => {
    const user = userEvent.setup();
    const getReadiness = vi.fn().mockResolvedValue({ kind: 'unavailable' });
    render(<DeliveryStatusCard client={clientReturning(getReadiness)} />);

    await screen.findByText(strings.deliveryStatusUnavailable);
    expect(getReadiness).toHaveBeenCalledTimes(1);

    await user.click(screen.getByRole('button', { name: strings.deliveryStatusRefresh }));
    await waitFor(() => expect(getReadiness).toHaveBeenCalledTimes(2));
    await screen.findByText(strings.deliveryStatusUnavailable);
  });

  it('never renders a superseded readiness response', async () => {
    let resolveFirst!: (value: ReadinessResult) => void;
    const first = new Promise<ReadinessResult>((resolve) => {
      resolveFirst = resolve;
    });
    const getReadiness = vi
      .fn()
      .mockReturnValueOnce(first)
      .mockResolvedValueOnce({
        kind: 'ok',
        schemaVersion: 4,
        expectedSchemaVersion: 4,
        heartbeatChecker: { lastTickAt: '2026-10-19T09:00:00Z', stale: false },
        lastMigrationFailure: null,
      });
    // StrictMode runs the mount effect twice, so the first request is
    // superseded by the second while it is still in flight.
    render(
      <StrictMode>
        <DeliveryStatusCard client={clientReturning(getReadiness)} />
      </StrictMode>,
    );

    expect(await screen.findByText(strings.deliveryStatusReady)).toBeInTheDocument();
    resolveFirst({ kind: 'unavailable' });
    await act(async () => {
      await first;
    });

    expect(screen.getByText(strings.deliveryStatusReady)).toBeInTheDocument();
    expect(screen.queryByText(strings.deliveryStatusUnavailable)).not.toBeInTheDocument();
    expect(getReadiness).toHaveBeenCalledTimes(2);
  });
});
