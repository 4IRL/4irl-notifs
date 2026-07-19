import { useCallback, useEffect, useState } from 'react';

import { ApiError } from './api/client';
import type {
  ApiClient,
  AppUserPair,
  ProvisionParams,
  ProvisionResult,
  UserSummary,
} from './api/client';
import { ProvisionForm } from './components/ProvisionForm';
import { UsersTable } from './components/UsersTable';
import { strings } from './strings';
import './App.css';

/** Props for App. The API client is injected so tests can supply a double. */
interface AppProps {
  client: ApiClient;
}

/**
 * App is the admin shell: it owns the user list (loaded on mount and refreshed
 * after every mutation) and wires the provision form and users table to the
 * injected API client.
 */
function App({ client }: AppProps) {
  const [users, setUsers] = useState<UserSummary[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

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

  const handleProvision = useCallback(
    async (params: ProvisionParams): Promise<ProvisionResult> => {
      const result = await client.provision(params);
      await refreshUsers();
      return result;
    },
    [client, refreshUsers],
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
      </main>
    </div>
  );
}

export default App;
