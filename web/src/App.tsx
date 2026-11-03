import { useCallback, useEffect, useRef, useState } from 'react';

import { ApiError, isAppNotBound } from './api/client';
import type {
  ApiClient,
  AppUserPair,
  ProvisionAppResult,
  ProvisionParams,
  ProvisionResult,
  ResetPasswordParams,
  TestNotifyParams,
  UserSummary,
} from './api/client';
import type {
  AppSummary,
  CreateAppParams,
  PersonApiClient,
  PersonSummary,
  UpdateAppParams,
} from './api/personClient';
import type { DeliveryApiClient } from './api/deliveryClient';
import { AppTabs } from './components/AppTabs';
import { DeliveryStatusCard } from './components/DeliveryStatusCard';
import { DeliveryLog } from './components/DeliveryLog';
import { AppIdentities } from './components/AppIdentities';
import { DeliveryRegistry } from './components/DeliveryRegistry';
import { Heartbeats } from './components/Heartbeats';
import { PendingSends } from './components/PendingSends';
import type { DestinationsLoadState } from './components/RoutesSection';
import { ProvisionForm } from './components/ProvisionForm';
import { SendTestNotification } from './components/SendTestNotification';
import { UsersTable } from './components/UsersTable';
import { PeopleTable } from './components/PeopleTable';
import { AddAppForm, type AddAppResult } from './components/AddAppForm';
import { AppsTable } from './components/AppsTable';
import { UnregisteredApps } from './components/UnregisteredApps';
import { strings } from './strings';
import './App.css';

/**
 * Maps the delivery-api "app has no approved binding" 404 to the neutral
 * not-bound copy; any other rejection (including any other 404) is returned
 * untouched so the caller rethrows it as is.
 */
function mapNotBoundRejection({ rejection, appId }: { rejection: unknown; appId: string }): unknown {
  if (isAppNotBound(rejection)) {
    return new ApiError({ status: 404, message: strings.appNotBoundRetryError({ appId }) });
  }
  return rejection;
}

/**
 * Props for App. The API client is injected so tests can supply a double.
 * personClient is optional: locally there is no person-service Worker, so the
 * people/apps views must not render at all when it is absent. appsEnabled
 * additionally gates the Apps section (VITE_APPS_ENABLED); like the People
 * view, it also requires a personClient since the app registry lives in the
 * person-service.
 */
interface AppProps {
  client: ApiClient;
  personClient?: PersonApiClient;
  appsEnabled?: boolean;
  /** delivery-api client; the Delivery tab renders only with it AND a flag on. */
  deliveryClient?: DeliveryApiClient;
  /** VITE_DESTINATIONS_ENABLED: status card, Destinations and Routes. */
  destinationsEnabled?: boolean;
  /** VITE_DELIVERY_LOG_ENABLED: the Delivery Log. */
  deliveryLogEnabled?: boolean;
  /** VITE_PENDING_SENDS_ENABLED: the Pending sends (scheduled messages) section. */
  pendingSendsEnabled?: boolean;
  /** VITE_HEARTBEATS_ENABLED (and destinationsEnabled): the Heartbeats section. */
  heartbeatsEnabled?: boolean;
  /** VITE_APP_IDENTITIES_ENABLED: the App identities section. */
  appIdentitiesEnabled?: boolean;
}

function App({
  client,
  personClient,
  appsEnabled = false,
  deliveryClient,
  destinationsEnabled = false,
  deliveryLogEnabled = false,
  pendingSendsEnabled = false,
  heartbeatsEnabled = false,
  appIdentitiesEnabled = false,
}: AppProps) {
  // The destinations list as the Destinations section last reported it. Lifted
  // here so DeliveryRegistry (Routes) and Heartbeats share the one fetch.
  const [destinationsState, setDestinationsState] = useState<DestinationsLoadState>({
    status: 'loading',
    destinations: [],
  });
  const [users, setUsers] = useState<UserSummary[]>([]);
  const [loading, setLoading] = useState(true);
  // Bumped when a removal may have deleted checks/routes/destinations; the Delivery sections reload on change.
  const [appRemovedSignal, setAppRemovedSignal] = useState(0);
  // appIds with a removal running: the ref dedupes synchronously, the state drives rendering.
  const inFlightRemovalsRef = useRef<Set<string>>(new Set());
  const [removingAppIds, setRemovingAppIds] = useState<ReadonlySet<string>>(new Set());
  const [error, setError] = useState<string | null>(null);
  const [people, setPeople] = useState<PersonSummary[]>([]);
  const [peopleLoading, setPeopleLoading] = useState(personClient !== undefined);
  const [peopleError, setPeopleError] = useState<string | null>(null);
  const appsSectionEnabled = personClient !== undefined && appsEnabled;
  const [apps, setApps] = useState<AppSummary[]>([]);
  const [appsLoading, setAppsLoading] = useState(appsSectionEnabled);
  const [appsError, setAppsError] = useState<string | null>(null);

  // `loading` starts true and flips off after the first load; later refreshes
  // (post-provision/deprovision/delete) update the list in place without
  // re-entering the loading state. Written as a promise chain (not async/await)
  // so every setState is inside a promise callback and therefore provably
  // asynchronous to the react-hooks/set-state-in-effect lint rule.
  const refreshUsers = useCallback(
    (): Promise<void> =>
      client
        .listUsers()
        .then((nextUsers) => {
          setUsers(nextUsers);
        })
        .catch((rejection: unknown) => {
          setError(rejection instanceof ApiError ? rejection.message : strings.genericError);
        })
        .finally(() => {
          setLoading(false);
        }),
    [client],
  );

  useEffect(() => {
    void refreshUsers();
  }, [refreshUsers]);

  // Mirrors refreshUsers' promise-chain style (see the lint-rule comment
  // above). A no-op when personClient is undefined so App never touches the
  // people state absent a configured person service. Failures set peopleError
  // only — never the shared `error` state — so a person-service outage never
  // blocks user management.
  const refreshPeople = useCallback((): Promise<void> => {
    if (personClient === undefined) {
      return Promise.resolve();
    }
    return personClient
      .listPeople()
      .then((nextPeople) => {
        setPeople(nextPeople);
        setPeopleError(null);
      })
      .catch((rejection: unknown) => {
        setPeopleError(rejection instanceof ApiError ? rejection.message : strings.peopleLoadError);
      })
      .finally(() => {
        setPeopleLoading(false);
      });
  }, [personClient]);

  useEffect(() => {
    void refreshPeople();
  }, [refreshPeople]);

  // Same promise-chain style and same isolation as refreshPeople: an apps-load
  // failure sets only appsError, never the shared banner. No-op unless the Apps
  // section is enabled (requires personClient + the build flag).
  const refreshApps = useCallback((): Promise<void> => {
    if (personClient === undefined || !appsEnabled) {
      return Promise.resolve();
    }
    return personClient
      .listApps()
      .then((nextApps) => {
        setApps(nextApps);
        setAppsError(null);
      })
      .catch((rejection: unknown) => {
        setAppsError(rejection instanceof ApiError ? rejection.message : strings.appsLoadError);
      })
      .finally(() => {
        setAppsLoading(false);
      });
  }, [personClient, appsEnabled]);

  useEffect(() => {
    void refreshApps();
  }, [refreshApps]);

  // Join key: a ntfy userId is "u_" + personHash. Built fresh each render from
  // `people`; empty when there is no personClient, so UsersTable always falls
  // back to displaying the raw userId in that case.
  const emailByPersonHash = new Map(people.map((person) => [person.personHash, person.email]));

  // app_id -> registry display name, for the test-notification title. Empty
  // without a registry (no personClient), so the title falls back to the app_id.
  const displayNameByAppId = new Map(apps.map((app) => [app.appId, app.displayName]));

  // Live subscriber count per app, keyed over every app_id in play (registered
  // AND merely in-use), so both the Apps table and the Unprovisioned-apps
  // section can read it. Only "u_"-prefixed users (real subscribers) count;
  // publisher identities surface via the app-wide wildcard and are excluded.
  const registeredAppIds = new Set(apps.map((app) => app.appId));
  const inUseAppIds = new Set(users.flatMap((user) => user.apps));
  const subscriberCountByApp = new Map<string, number>();
  for (const appId of new Set([...registeredAppIds, ...inUseAppIds])) {
    const count = users.filter(
      (user) => user.userId.startsWith('u_') && user.apps.includes(appId),
    ).length;
    subscriberCountByApp.set(appId, count);
  }

  // "Unprovisioned" apps: an app_id that is in use (a user/topic references it)
  // but has no registry row — the ghost-app case. Sorted for stable display.
  const unregisteredAppIds = [...inUseAppIds]
    .filter((appId) => !registeredAppIds.has(appId))
    .sort();

  const handleProvision = useCallback(
    async (params: ProvisionParams): Promise<ProvisionResult> => {
      let result: ProvisionResult;
      try {
        result = await client.provision(params);
      } catch (rejection: unknown) {
        throw mapNotBoundRejection({ rejection, appId: params.appId });
      }
      await refreshUsers();
      await refreshPeople();
      return result;
    },
    [client, refreshUsers, refreshPeople],
  );

  const handleDeprovision = useCallback(
    ({ userId, appId }: AppUserPair) => {
      void client
        .deprovision({ userId, appId })
        .then(() => {
          setError(null);
          return refreshUsers();
        })
        .catch((rejection: unknown) => {
          if (rejection instanceof ApiError && rejection.status === 404) {
            // 404 covers both a vanished user and one with no grant in this app.
            setError(strings.deprovisionNotFound);
            return;
          }
          setError(rejection instanceof ApiError ? rejection.message : strings.genericError);
        });
    },
    [client, refreshUsers],
  );

  // Thin pass-through, like handleTestNotify: UsersTable owns the reveal and
  // failure state, and the password is stored nowhere else.
  const handleResetPassword = useCallback(
    (params: ResetPasswordParams) => client.resetPassword(params),
    [client],
  );

  const handleDelete = useCallback(
    ({ userId }: { userId: string }) => {
      void client
        .deleteUser({ userId })
        .then(() => {
          setError(null);
          return refreshUsers();
        })
        .catch((rejection: unknown) => {
          setError(rejection instanceof ApiError ? rejection.message : strings.genericError);
        });
    },
    [client, refreshUsers],
  );

  // People-row delete = full teardown. The People row carries the personHash;
  // the ntfy user id is "u_" + personHash. Deleting that user tears down every
  // app grant + token AND (server-side dual-delete) the person row, so refresh
  // both lists afterward.
  const handleDeletePerson = useCallback(
    ({ personHash }: { personHash: string }) => {
      void client
        .deleteUser({ userId: `u_${personHash}` })
        .then(() => {
          setError(null);
          return Promise.all([refreshUsers(), refreshPeople()]);
        })
        .then(() => undefined)
        .catch((rejection: unknown) => {
          setError(rejection instanceof ApiError ? rejection.message : strings.genericError);
        });
    },
    [client, refreshUsers, refreshPeople],
  );

  // Add app = mint first, then register. Any mint error aborts before the app
  // row is touched, so an unbound app (no approved delivery key) never leaves a
  // row with no publisher. After a successful mint, a 409 on createApp means
  // approval already created the row, so update it instead. A row-step failure
  // after the mint is prefixed so the operator knows the mint happened; a
  // retry mints one more token (earlier ones live until Revoke & re-mint).
  const handleAddApp = useCallback(
    async ({ appId, displayName, description }: CreateAppParams): Promise<AddAppResult> => {
      if (personClient === undefined) {
        throw new Error('person service not configured');
      }
      let result: ProvisionAppResult;
      try {
        result = await client.provisionApp({ appId });
      } catch (rejection: unknown) {
        if (isAppNotBound(rejection)) {
          throw new ApiError({ status: 404, message: strings.appNotBoundError({ appId }) });
        }
        throw rejection;
      }
      let alreadyRegistered = false;
      try {
        try {
          await personClient.createApp({ appId, displayName, description });
        } catch (rejection: unknown) {
          // 409: approval already created the row. Apply the operator's values.
          if (!(rejection instanceof ApiError && rejection.status === 409)) {
            throw rejection;
          }
          alreadyRegistered = true;
          await personClient.updateApp({ appId, displayName, description });
        }
      } catch (rejection: unknown) {
        const detail = rejection instanceof Error ? rejection.message : String(rejection);
        await refreshApps();
        throw new ApiError({
          status: rejection instanceof ApiError ? rejection.status : 0,
          message: `${strings.publisherMintedButRegisterFailed}${detail}`,
        });
      }
      await refreshUsers();
      await refreshApps();
      return alreadyRegistered ? { ...result, alreadyRegistered: true } : result;
    },
    [personClient, client, refreshApps, refreshUsers],
  );

  const handleUpdateApp = useCallback(
    async (params: UpdateAppParams): Promise<void> => {
      if (personClient === undefined) {
        return;
      }
      await personClient.updateApp(params);
      await refreshApps();
    },
    [personClient, refreshApps],
  );

  // Thin pass-through to the API client. SendTestNotification owns its own
  // selection/send/results state (mirroring ProvisionForm), so this never
  // touches the shared `error` banner or refreshes any list.
  const handleTestNotify = useCallback(
    async (params: TestNotifyParams) => {
      try {
        return await client.testNotify(params);
      } catch (rejection: unknown) {
        throw mapNotBoundRejection({ rejection, appId: params.appId });
      }
    },
    [client],
  );

  // Re-mint / rotate the publisher token. The token is never returned; the
  // edit form only announces success. Nothing in the tables changes.
  const handleRemintToken = useCallback(
    async ({ appId, rotate }: { appId: string; rotate: boolean }): Promise<ProvisionAppResult> => {
      try {
        return await client.provisionApp({ appId, rotate });
      } catch (rejection: unknown) {
        throw mapNotBoundRejection({ rejection, appId });
      }
    },
    [client],
  );

  // Remove app = full cascade (server-side). Refresh apps (row gone) and users
  // (grants gone). The cascade is not atomic, so a 409/503 may follow committed
  // phases: show the message and refresh too. 400/502/other change nothing.
  // Resolves true on success, false on any failure (never rejects).
  const handleRemoveApp = useCallback(
    async ({ appId }: { appId: string }): Promise<boolean> => {
      if (inFlightRemovalsRef.current.has(appId)) {
        return false;
      }
      inFlightRemovalsRef.current.add(appId);
      setRemovingAppIds(new Set(inFlightRemovalsRef.current));
      // The id stays in flight through the refresh, so Remove is not re-enabled early.
      try {
        try {
          await client.deprovisionApp({ appId });
        } catch (rejection: unknown) {
          if (
            rejection instanceof ApiError &&
            (rejection.status === 409 || rejection.status === 503)
          ) {
            setAppRemovedSignal((value) => value + 1);
            await Promise.all([refreshApps(), refreshUsers()]);
          }
          // After the refresh, so a refresh failure cannot overwrite the server's reason.
          setError(rejection instanceof ApiError ? rejection.message : strings.genericError);
          return false;
        }
        setError(null);
        setAppRemovedSignal((value) => value + 1);
        await Promise.all([refreshApps(), refreshUsers()]);
        return true;
      } finally {
        inFlightRemovalsRef.current.delete(appId);
        setRemovingAppIds(new Set(inFlightRemovalsRef.current));
      }
    },
    [client, refreshApps, refreshUsers],
  );

  // The Provisioning content. With tabs it is the Provisioning panel's body
  // (the shared error banner is its first child, so it never shows on the
  // Delivery tab); without tabs it is the page's <main> content, unchanged.
  const provisioningContent = (
    <>
      {error !== null && (
        <p role="alert" className="app__error">
          {error}
        </p>
      )}
      <div className="app__forms">
        <ProvisionForm onProvision={handleProvision} apps={apps} />
        {appsSectionEnabled && <AddAppForm onAddApp={handleAddApp} />}
      </div>
      <SendTestNotification
        users={users}
        loading={loading}
        emailByPersonHash={emailByPersonHash}
        displayNameByAppId={displayNameByAppId}
        onSendTest={handleTestNotify}
      />
      <UsersTable
        users={users}
        loading={loading}
        emailByPersonHash={emailByPersonHash}
        onDeprovision={handleDeprovision}
        onDelete={handleDelete}
        onResetPassword={handleResetPassword}
      />
      {appsSectionEnabled && (
        <AppsTable
          apps={apps}
          subscriberCountByApp={subscriberCountByApp}
          loading={appsLoading}
          error={appsError}
          onUpdateApp={handleUpdateApp}
          onRemintToken={handleRemintToken}
          onRemoveApp={handleRemoveApp}
          removingAppIds={removingAppIds}
        />
      )}
      {appsSectionEnabled && (
        <UnregisteredApps
          appIds={unregisteredAppIds}
          subscriberCountByApp={subscriberCountByApp}
          loading={loading || appsLoading}
        />
      )}
      {personClient !== undefined && (
        <PeopleTable
          people={people}
          loading={peopleLoading}
          error={peopleError}
          onDelete={handleDeletePerson}
        />
      )}
    </>
  );

  // Heartbeats alert through registered destinations, so it never mounts without
  // the Destinations section (mirrors appsSectionEnabled's gating above).
  const heartbeatsSectionEnabled = destinationsEnabled && heartbeatsEnabled;
  const tabsEnabled =
    deliveryClient !== undefined &&
    (destinationsEnabled ||
      deliveryLogEnabled ||
      pendingSendsEnabled ||
      heartbeatsSectionEnabled ||
      appIdentitiesEnabled);

  return (
    <div className="app">
      <header className="app__header">
        <h1>{strings.appTitle}</h1>
        <span className="app__badge">{strings.headerBadge}</span>
      </header>
      {tabsEnabled ? (
        <AppTabs
          provisioning={provisioningContent}
          delivery={
            <>
              {destinationsEnabled && <DeliveryStatusCard client={deliveryClient} />}
              {appIdentitiesEnabled && (
                <AppIdentities
                  client={deliveryClient}
                  refreshSignal={appRemovedSignal}
                  onBindingApproved={() => {
                    void refreshApps();
                  }}
                />
              )}
              {destinationsEnabled && (
                <DeliveryRegistry
                  client={deliveryClient}
                  destinationsState={destinationsState}
                  onDestinationsStateChange={setDestinationsState}
                  refreshSignal={appRemovedSignal}
                />
              )}
              {heartbeatsSectionEnabled && (
                <Heartbeats
                  client={deliveryClient}
                  destinationsState={destinationsState}
                  refreshSignal={appRemovedSignal}
                />
              )}
              {pendingSendsEnabled && <PendingSends client={deliveryClient} />}
              {deliveryLogEnabled && <DeliveryLog client={deliveryClient} />}
            </>
          }
        />
      ) : (
        <main className="app__main">{provisioningContent}</main>
      )}
    </div>
  );
}

export default App;
