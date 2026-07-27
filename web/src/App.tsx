import { useCallback, useEffect, useState } from 'react';

import { ApiError } from './api/client';
import type {
  ApiClient,
  AppUserPair,
  ProvisionAppResult,
  ProvisionParams,
  ProvisionResult,
  UserSummary,
} from './api/client';
import type {
  AppSummary,
  CreateAppParams,
  PersonApiClient,
  PersonSummary,
  UpdateAppParams,
} from './api/personClient';
import { ProvisionForm } from './components/ProvisionForm';
import { UsersTable } from './components/UsersTable';
import { PeopleTable } from './components/PeopleTable';
import { AddAppForm } from './components/AddAppForm';
import { AppsTable } from './components/AppsTable';
import { UnregisteredApps } from './components/UnregisteredApps';
import { strings } from './strings';
import './App.css';

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
}

function App({ client, personClient, appsEnabled = false }: AppProps) {
  const [users, setUsers] = useState<UserSummary[]>([]);
  const [loading, setLoading] = useState(true);
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
      const result = await client.provision(params);
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
          setError(rejection instanceof ApiError ? rejection.message : strings.genericError);
        });
    },
    [client, refreshUsers],
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

  // Add app = registry-first: register metadata, then mint the publisher token.
  // A failure after registration leaves a visible registry row with no token,
  // recoverable via Re-mint. Returns the token result for the form's reveal.
  const handleAddApp = useCallback(
    async ({ appId, displayName, description }: CreateAppParams): Promise<ProvisionAppResult> => {
      if (personClient === undefined) {
        throw new Error('person service not configured');
      }
      await personClient.createApp({ appId, displayName, description });
      try {
        const result = await client.provisionApp({ appId });
        await refreshUsers();
        return result;
      } finally {
        // Refresh the registry even if minting the publisher token throws, so
        // the just-registered app still appears in the table (and is thus
        // recoverable via Re-mint) rather than staying invisible until an
        // unrelated action happens to refresh apps.
        await refreshApps();
      }
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

  // Re-mint / rotate the publisher token. Returns the reveal-once result for
  // the edit form; nothing in the tables changes, so no refresh is needed.
  const handleRemintToken = useCallback(
    ({ appId, rotate }: { appId: string; rotate: boolean }): Promise<ProvisionAppResult> =>
      client.provisionApp({ appId, rotate }),
    [client],
  );

  // Remove app = full cascade (server-side): publisher identity + every
  // subscriber grant + registry row. Refresh apps (row gone) and users (grants
  // gone).
  const handleRemoveApp = useCallback(
    ({ appId }: { appId: string }) => {
      void client
        .deprovisionApp({ appId })
        .then(() => {
          setError(null);
          return Promise.all([refreshApps(), refreshUsers()]);
        })
        .then(() => undefined)
        .catch((rejection: unknown) => {
          setError(rejection instanceof ApiError ? rejection.message : strings.genericError);
        });
    },
    [client, refreshApps, refreshUsers],
  );

  return (
    <div className="app">
      <header className="app__header">
        <h1>{strings.appTitle}</h1>
        <span className="app__badge">{strings.headerBadge}</span>
      </header>
      <main className="app__main">
        {error !== null && (
          <p role="alert" className="app__error">
            {error}
          </p>
        )}
        <div className="app__forms">
          <ProvisionForm onProvision={handleProvision} apps={apps} />
          {appsSectionEnabled && <AddAppForm onAddApp={handleAddApp} />}
        </div>
        <UsersTable
          users={users}
          loading={loading}
          emailByPersonHash={emailByPersonHash}
          onDeprovision={handleDeprovision}
          onDelete={handleDelete}
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
      </main>
    </div>
  );
}

export default App;
