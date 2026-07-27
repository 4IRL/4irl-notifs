import type { JSX } from 'react';

import { strings } from '../strings';
import './UnregisteredApps.css';

/** Props for UnregisteredApps. */
interface UnregisteredAppsProps {
  /** Sorted app_ids that are in use (referenced by a user/topic) but have no registry row. */
  appIds: string[];
  /** app_id -> live subscriber count (shared with the Apps table). */
  subscriberCountByApp: Map<string, number>;
  /** True while the underlying users/apps are still loading — the empty state
   *  ("None…") is only meaningful once both have resolved. */
  loading: boolean;
}

/**
 * Read-only view of "unprovisioned" apps — app_ids with users/topics but no
 * registry entry (the ghost-app case). Shows each with its subscriber count and
 * instructional copy on how to onboard it (the app registers itself via
 * POST /v1/provision-app, or the operator adds it via "Add an app").
 */
export function UnregisteredApps({
  appIds,
  subscriberCountByApp,
  loading,
}: UnregisteredAppsProps): JSX.Element {
  return (
    <section className="unregistered-apps">
      <h2>{strings.unregisteredAppsHeading}</h2>
      <p className="unregistered-apps__hint">{strings.unregisteredAppsHint}</p>
      {loading ? (
        <p className="unregistered-apps__status">{strings.unregisteredAppsLoading}</p>
      ) : appIds.length === 0 ? (
        <p className="unregistered-apps__status">{strings.unregisteredAppsEmpty}</p>
      ) : (
        <table className="unregistered-apps__table">
          <thead>
            <tr>
              <th scope="col">{strings.columnAppId}</th>
              <th scope="col">{strings.columnSubscribers}</th>
            </tr>
          </thead>
          <tbody>
            {appIds.map((appId) => (
              <tr key={appId}>
                <td className="unregistered-apps__id">{appId}</td>
                <td>{subscriberCountByApp.get(appId) ?? 0}</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </section>
  );
}
