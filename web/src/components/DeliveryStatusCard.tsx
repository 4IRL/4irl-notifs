import { useCallback, useEffect, useState } from 'react';

import type {
  DeliveryApiClient,
  HeartbeatCheckerStatus,
  ReadinessResult,
} from '../api/deliveryClient';
import { useLatestRequest } from '../hooks/useLatestRequest';
import { strings } from '../strings';
import { formatRelativePast } from './formatRelative';
import { formatDateTimeZone } from './formatLocalTime';
import { RefreshButton } from './RefreshButton';
import './DeliveryStatusCard.css';

interface DeliveryStatusCardProps {
  client: DeliveryApiClient;
}

function pillFor(result: ReadinessResult | null): { label: string; modifier: string } {
  if (result === null) {
    return { label: strings.deliveryStatusChecking, modifier: 'neutral' };
  }
  switch (result.kind) {
    case 'ok':
      return { label: strings.deliveryStatusReady, modifier: 'ok' };
    case 'schema_mismatch':
      return { label: strings.deliveryStatusSchemaMismatch, modifier: 'fail' };
    case 'unavailable':
      return { label: strings.deliveryStatusUnavailable, modifier: 'fail' };
    default:
      return { label: strings.deliveryStatusUnreachable, modifier: 'fail' };
  }
}

function heartbeatCheckerFact({
  checker,
  now,
}: {
  checker: HeartbeatCheckerStatus;
  now: number;
}): string {
  const lastRun =
    checker.lastTickAt === null
      ? strings.heartbeatCheckerNever
      : formatRelativePast({ at: checker.lastTickAt, now });
  return checker.stale
    ? strings.heartbeatCheckerStalled({ lastRun })
    : strings.heartbeatCheckerRunning({ lastRun });
}

/**
 * delivery-api readiness card. Mounted by the Delivery tab on its first
 * activation, so it fetches once there; only the manual Refresh reloads. A
 * delivery-api outage is rendered here only and never touches the shared
 * provisioning error banner.
 */
export function DeliveryStatusCard({ client }: DeliveryStatusCardProps) {
  // null means a check is in flight.
  const [result, setResult] = useState<ReadinessResult | null>(null);
  // When the result was fetched; "last run" is relative to it, not to render time.
  const [loadedAt, setLoadedAt] = useState(() => Date.now());
  const track = useLatestRequest();

  // Promise-chain style (see App.tsx): every setState runs inside a promise
  // callback, asynchronous to the react-hooks/set-state-in-effect rule.
  const load = useCallback(
    (): Promise<void> =>
      track(client.getReadiness()).then((outcome) => {
        if (outcome.stale) {
          return;
        }
        setResult(
          outcome.ok ? outcome.value : { kind: 'unreachable', message: strings.genericError },
        );
        setLoadedAt(Date.now());
      }),
    [client, track],
  );

  useEffect(() => {
    void load();
  }, [load]);

  const checking = result === null;
  const pill = pillFor(result);
  const hasSchema = result !== null && (result.kind === 'ok' || result.kind === 'schema_mismatch');

  return (
    <section className="status-card" aria-labelledby="delivery-status-heading">
      <div className="status-card__row">
        <h2 id="delivery-status-heading" className="status-card__heading">
          {strings.deliveryStatusHeading}
        </h2>
        <div role="status" className="status-card__summary">
          <span className={`status-card__pill status-card__pill--${pill.modifier}`}>
            {pill.label}
          </span>
          {hasSchema && (
            <dl className="status-card__facts">
              <div>
                <dt>{strings.deliveryStatusSchemaLabel}</dt>
                <dd>
                  {strings.deliveryStatusSchemaValue({
                    current: result.schemaVersion,
                    expected: result.expectedSchemaVersion,
                  })}
                </dd>
              </div>
              <div>
                <dt>{strings.deliveryStatusMigrationLabel}</dt>
                <dd>
                  {result.lastMigrationFailure === null
                    ? strings.deliveryStatusNone
                    : strings.deliveryStatusFailureValue({
                        version: result.lastMigrationFailure.version,
                        failedAt: formatDateTimeZone({
                          timestamp: result.lastMigrationFailure.failedAt,
                        }),
                      })}
                </dd>
              </div>
              <div>
                <dt>{strings.heartbeatCheckerLabel}</dt>
                <dd className={result.heartbeatChecker.stale ? 'status-card__stalled' : undefined}>
                  {heartbeatCheckerFact({ checker: result.heartbeatChecker, now: loadedAt })}
                </dd>
              </div>
            </dl>
          )}
          {result !== null && result.kind === 'unavailable' && (
            <dl className="status-card__facts">
              <div>
                <dt>{strings.deliveryStatusSchemaLabel}</dt>
                <dd>{strings.noValue}</dd>
              </div>
            </dl>
          )}
        </div>
        <RefreshButton
          label={strings.deliveryStatusRefresh}
          className="status-card__refresh"
          busy={checking}
          onClick={() => {
            setResult(null);
            void load();
          }}
        />
      </div>
      {hasSchema && result.heartbeatChecker.stale && (
        <p className="status-card__warn">{strings.heartbeatCheckerStalledWarning}</p>
      )}
      {result !== null && result.kind === 'ok' && result.lastMigrationFailure !== null && (
        <p className="status-card__warn">
          {strings.deliveryStatusFailedNewerLead({ version: result.lastMigrationFailure.version })}
          <span className="status-card__mono">{result.lastMigrationFailure.error}</span>
          {strings.deliveryStatusFailedNewerTail({ schemaVersion: result.schemaVersion })}
        </p>
      )}
      {result !== null && result.kind === 'schema_mismatch' && (
        <p role="alert" className="status-card__error">
          {result.lastMigrationFailure === null ? (
            strings.deliveryStatusMismatchNoFailure
          ) : (
            <>
              {strings.deliveryStatusMismatchLead({ version: result.lastMigrationFailure.version })}
              <span className="status-card__mono">{result.lastMigrationFailure.error}</span>
              {strings.deliveryStatusMismatchTail}
            </>
          )}
        </p>
      )}
      {result !== null && result.kind === 'unavailable' && (
        <p role="alert" className="status-card__error">
          {strings.deliveryStatusUnavailableDetail}
        </p>
      )}
      {result !== null && result.kind === 'unreachable' && (
        <p role="alert" className="status-card__error">
          {strings.deliveryStatusUnreachableDetail({ message: result.message })}
        </p>
      )}
    </section>
  );
}
