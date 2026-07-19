import { useCallback, useEffect, useState } from 'react';

import { ApiError } from './api/client';
import type {
  ApiClient,
  AppUserPair,
  ProvisionParams,
  ProvisionResult,
  UserSummary,
} from './api/client';
import type { PersonApiClient, PersonSummary } from './api/personClient';
import { ProvisionForm } from './components/ProvisionForm';
import { UsersTable } from './components/UsersTable';
import { PeopleTable } from './components/PeopleTable';
import { strings } from './strings';
import './App.css';

/**
 * Props for App. The API client is injected so tests can supply a double.
 * personClient is optional: locally there is no person-service Worker, so the
 * people view must not render at all when it is absent.
 */
interface AppProps {
  client: ApiClient;
  personClient?: PersonApiClient;
}

/**
 * App is the admin shell: it owns the user list (loaded on mount and refreshed
 * after every mutation) and wires the provision form and users table to the
 * injected API client. When a personClient is supplied it also owns the
 * people list (loaded on mount and refreshed after a successful provision,
 * since provisioning dual-writes a person record on the Go side).
 */
function App({ client, personClient }: AppProps) {
  const [users, setUsers] = useState<UserSummary[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [people, setPeople] = useState<PersonSummary[]>([]);
  const [peopleLoading, setPeopleLoading] = useState(personClient !== undefined);
  const [peopleError, setPeopleError] = useState<string | null>(null);

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
        <ProvisionForm onProvision={handleProvision} />
        <UsersTable
          users={users}
          loading={loading}
          onDeprovision={handleDeprovision}
          onDelete={handleDelete}
        />
        {personClient !== undefined && (
          <PeopleTable people={people} loading={peopleLoading} error={peopleError} />
        )}
      </main>
    </div>
  );
}

export default App;
