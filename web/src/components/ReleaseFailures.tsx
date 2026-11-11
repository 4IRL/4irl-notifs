import type { JSX } from 'react';

import type { HeldFailuresList } from '../api/deliveryClient';
import { strings } from '../strings';
import { LocalTime } from './LocalTime';
import { shortMessageId } from './shortMessageId';

interface ReleaseFailuresProps {
  loading: boolean;
  unavailable: boolean;
  failures: HeldFailuresList | null;
}

const REASON_INVALID = 'release_invalid';

/**
 * The "Release failed" branch of the Delivery log: held messages that could not be sent once a
 * route appeared. Renders only status, truncation and the table; the parent owns the hint.
 */
export function ReleaseFailures({
  loading,
  unavailable,
  failures,
}: ReleaseFailuresProps): JSX.Element | null {
  if (loading) {
    return <p className="delivery-log__status">{strings.releaseFailuresLoading}</p>;
  }
  if (unavailable || failures === null) {
    return <p className="delivery-log__status">{strings.releaseFailuresUnavailable}</p>;
  }
  if (failures.items.length === 0) {
    return <p className="delivery-log__status">{strings.releaseFailuresEmpty}</p>;
  }
  return (
    <>
      {failures.items.length < failures.total && (
        <p className="delivery-log__hint">
          {strings.releaseFailuresTruncated({
            shown: failures.items.length,
            total: failures.total,
          })}
        </p>
      )}
      <div className="delivery-log__scroll">
        <table className="delivery-log__table" aria-label={strings.releaseFailuresTableLabel}>
          <thead>
            <tr>
              <th scope="col">{strings.columnLogAppChannel}</th>
              <th scope="col">{strings.columnReleaseReason}</th>
              <th scope="col">{strings.columnReleaseFailed}</th>
            </tr>
          </thead>
          <tbody>
            {failures.items.map((failure) => (
              <tr key={`${failure.messageId}:${failure.channel}:${failure.failedAt}`}>
                <td>
                  <span className="delivery-log__mono">{failure.appId}</span>
                  {strings.separator}
                  <span className="delivery-log__mono">{failure.channel}</span>
                  <span className="delivery-log__sub delivery-log__mono">
                    {strings.deliveryMessageId({ id: shortMessageId(failure.messageId) })}
                  </span>
                </td>
                <td>
                  {failure.reason === REASON_INVALID ? (
                    <>
                      {strings.releaseReasonInvalid}
                      <span className="delivery-log__sub delivery-log__mono">{failure.reason}</span>
                    </>
                  ) : (
                    <span className="delivery-log__mono delivery-log__wrap">{failure.reason}</span>
                  )}
                </td>
                <td className="delivery-log__mono">
                  <LocalTime timestamp={failure.failedAt} />
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </>
  );
}
