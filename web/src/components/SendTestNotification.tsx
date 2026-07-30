import { useEffect, useState } from 'react';
import type { JSX } from 'react';

import { ApiError } from '../api/client';
import type {
  TestNotifyParams,
  TestNotifyRecipientResult,
  TestNotifyResult,
  UserSummary,
} from '../api/client';
import { strings } from '../strings';
import { isValidChannel } from '../validation';
import './SendTestNotification.css';

interface SendTestNotificationProps {
  users: UserSummary[];
  loading: boolean;
  /** personHash (ntfy userId minus its "u_" prefix) -> email, for display only. */
  emailByPersonHash: Map<string, string>;
  onSendTest: (params: TestNotifyParams) => Promise<TestNotifyResult>;
}

/**
 * Self-contained "Send test notification" section: pick a target app, select
 * one or more provisioned subscribers on that app, optionally edit the channel
 * and message, and dispatch a real ntfy notification to each. Selection, send
 * state, and per-recipient results are kept entirely local (mirroring how
 * ProvisionForm owns its own error/result state rather than the shared banner).
 */
export function SendTestNotification({
  users,
  loading,
  emailByPersonHash,
  onSendTest,
}: SendTestNotificationProps): JSX.Element {
  // Subscribers only (publisher identities don't start with "u_"); one option
  // per distinct app they subscribe to, sorted for stable display.
  const appOptions = Array.from(
    new Set(users.filter((user) => user.userId.startsWith('u_')).flatMap((user) => user.apps)),
  ).sort();

  // targetApp starts empty and is defaulted to appOptions[0] by the reconcile
  // effect below, once `users` has loaded (it may be empty at first render).
  const [targetApp, setTargetApp] = useState('');
  const [channel, setChannel] = useState<string>(strings.sendTestDefaultChannel);
  const [message, setMessage] = useState<string>(strings.sendTestDefaultMessage);
  const [selected, setSelected] = useState<Set<string>>(new Set());
  const [isSending, setIsSending] = useState(false);
  const [results, setResults] = useState<TestNotifyRecipientResult[] | null>(null);
  const [error, setError] = useState<string | null>(null);

  const scopedUsers = users.filter(
    (user) => user.userId.startsWith('u_') && user.apps.includes(targetApp),
  );

  // Single reconcile effect with two responsibilities against the latest
  // `users`/`targetApp`: (1) drop any selected id whose owner is no longer
  // scoped to the current app (covers both full removal and a per-app
  // deprovision); (2) default `targetApp` to the first option once the async
  // users load resolves, and re-default if the current app later disappears.
  // This is a synchronous derived-state adjustment (not external-system sync),
  // so it must run before paint: the default-app and stale-drop have to apply
  // within the same commit as the users prop arriving. Both setState calls use a
  // bail-out form (functional updater / equality guard) so an already-consistent
  // state never re-renders. set-state-in-effect is disabled only on the
  // selection-drop updater — that rule targets external-sync effects, and
  // App.tsx's async-only microtask workaround is unusable here (it would defer
  // the default app past first paint); the guarded setTargetApp is already
  // accepted. exhaustive-deps is disabled because appOptions is derived wholly
  // from `users` (already a dep) — a fresh array each render.
  useEffect(() => {
    const scopedIds = new Set(
      users
        .filter((user) => user.userId.startsWith('u_') && user.apps.includes(targetApp))
        .map((user) => user.userId),
    );
    // eslint-disable-next-line react-hooks/set-state-in-effect
    setSelected((previous) => {
      let changed = false;
      const next = new Set<string>();
      for (const id of previous) {
        if (scopedIds.has(id)) {
          next.add(id);
        } else {
          changed = true;
        }
      }
      return changed ? next : previous;
    });
    if (!appOptions.includes(targetApp)) {
      setTargetApp(appOptions[0] ?? '');
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [users, targetApp]);

  function toggleRecipient(userId: string) {
    setSelected((previous) => {
      const next = new Set(previous);
      if (next.has(userId)) {
        next.delete(userId);
      } else {
        next.add(userId);
      }
      return next;
    });
  }

  // Changing the target app resets the selection, any prior results, and any
  // stale error so the admin never sends stale selections or reads results/
  // errors from another app.
  function handleAppChange(event: React.ChangeEvent<HTMLSelectElement>) {
    setTargetApp(event.target.value);
    setSelected(new Set());
    setResults(null);
    setError(null);
  }

  // Validate-on-submit, promise-chain shape mirroring ProvisionForm.handleSubmit:
  // preventDefault → channel guard (early return before any sending state is
  // touched) → clear error → set sending → dispatch → then/catch/finally.
  function handleSend(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault();

    // The channel/message inputs live inside the <form> in every gating branch,
    // so Enter can submit even when the Send button is disabled. Guard the
    // no-selection case here so an Enter press never dispatches an empty batch.
    if (selected.size === 0) {
      return;
    }

    if (!isValidChannel(channel)) {
      setError(strings.sendTestInvalidChannel);
      return;
    }

    setError(null);
    setIsSending(true);
    const recipients = Array.from(selected).map((userId) => userId.slice(2));
    onSendTest({ appId: targetApp, recipients, channel, message })
      .then((result) => {
        setResults(result.results);
      })
      .catch((rejection: unknown) => {
        setError(rejection instanceof ApiError ? rejection.message : strings.genericError);
      })
      .finally(() => {
        setIsSending(false);
      });
  }

  return (
    <section className="send-test">
      <h2>{strings.sendTestHeading}</h2>
      <form onSubmit={handleSend}>
        <div className="send-test__picker-row">
          <div className="send-test__field send-test__field--app">
            <label htmlFor="send-test-app">{strings.sendTestTargetAppLabel}</label>
            <select
              id="send-test-app"
              className="send-test__control"
              value={targetApp}
              onChange={handleAppChange}
              disabled={isSending}
            >
              {appOptions.map((app) => (
                <option key={app} value={app}>
                  {app}
                </option>
              ))}
            </select>
          </div>
          <div className="send-test__field send-test__field--channel">
            <label htmlFor="send-test-channel">{strings.sendTestChannelLabel}</label>
            <input
              id="send-test-channel"
              className="send-test__control"
              type="text"
              value={channel}
              onChange={(event) => setChannel(event.target.value)}
              disabled={isSending}
            />
          </div>
          <div className="send-test__field send-test__field--message">
            <label htmlFor="send-test-message">{strings.sendTestMessageLabel}</label>
            <input
              id="send-test-message"
              className="send-test__control"
              type="text"
              value={message}
              onChange={(event) => setMessage(event.target.value)}
              disabled={isSending}
            />
          </div>
        </div>

        {/* Single error surface for both the invalid-channel client gate and an
            onSendTest rejection. Sits below the picker row and above the Send
            button, and renders whenever error !== null — independent of the
            loading/empty/populated gating — so a rejection stays visible even if
            the scoped-user set empties mid-send. */}
        {error !== null && (
          <p role="alert" className="send-test__error">
            {error}
          </p>
        )}

        {loading && <p className="send-test__status">{strings.sendTestLoading}</p>}
        {!loading && scopedUsers.length === 0 && (
          <p className="send-test__status">{strings.sendTestNoUsersForApp}</p>
        )}
        {!loading && scopedUsers.length > 0 && (
          <>
            <div className="send-test__table-wrap">
              <table className="send-test__table">
                <thead>
                  <tr>
                    <th scope="col" className="send-test__th-select" aria-label="Select" />
                    <th scope="col">{strings.sendTestColumnUser}</th>
                    <th scope="col">{strings.sendTestColumnApp}</th>
                    <th scope="col">{strings.sendTestColumnTopic}</th>
                  </tr>
                </thead>
                <tbody>
                  {scopedUsers.map(({ userId }) => {
                    const personHash = userId.slice(2);
                    const displayName = emailByPersonHash.get(personHash) ?? userId;
                    const topic = `${targetApp}-${personHash}-${channel}`;
                    const isSelected = selected.has(userId);
                    return (
                      <tr
                        key={userId}
                        className={
                          isSelected ? 'send-test__row send-test__row--selected' : 'send-test__row'
                        }
                      >
                        <td className="send-test__td-select">
                          <input
                            type="checkbox"
                            checked={isSelected}
                            disabled={isSending}
                            aria-label={strings.sendTestSelectAria({ who: displayName })}
                            onChange={() => toggleRecipient(userId)}
                          />
                        </td>
                        <td className="send-test__user">{displayName}</td>
                        <td>
                          <span className="send-test__chip">{targetApp}</span>
                        </td>
                        <td className="send-test__topic">{topic}</td>
                      </tr>
                    );
                  })}
                </tbody>
              </table>
            </div>

            <div className="send-test__bar">
              <span className="send-test__count">
                {strings.sendTestSelectedCount({ count: selected.size })}
              </span>
              <span className="send-test__spacer" />
              <button
                type="submit"
                className="send-test__submit"
                disabled={isSending || selected.size === 0}
              >
                {isSending ? strings.sendTestSending : strings.sendTestAction}
              </button>
            </div>

            {selected.size === 0 && <p className="send-test__hint">{strings.sendTestSelectHint}</p>}
          </>
        )}
      </form>

      {results !== null && (
        <div className="send-test__results" role="status" aria-live="polite">
          <p className="send-test__summary">
            {strings.sendTestResultsSummary({
              sent: results.filter((result) => result.ok).length,
              total: results.length,
              failed: results.filter((result) => !result.ok).length,
            })}
          </p>
          {results.map((result) => {
            const who =
              emailByPersonHash.get(result.userId.slice(2)) ?? result.recipient ?? result.userId;
            return (
              <div key={result.recipient} className="send-test__result-row">
                <span
                  className={
                    result.ok
                      ? 'send-test__pill send-test__pill--ok'
                      : 'send-test__pill send-test__pill--fail'
                  }
                >
                  {result.ok ? strings.sendTestDelivered : strings.sendTestFailed}
                </span>
                <span className="send-test__who">{who}</span>
                <span className="send-test__spacer" />
                <span className="send-test__detail">
                  {result.ok
                    ? strings.sendTestResultDetail({
                        messageId: result.messageId,
                        topic: result.topic,
                      })
                    : result.error}
                </span>
              </div>
            );
          })}
        </div>
      )}
    </section>
  );
}
