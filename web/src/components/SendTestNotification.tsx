import { useState } from 'react';
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

  // `targetApp` holds only an explicit admin choice; it starts empty and stays
  // empty until the admin picks an option. The app actually in effect is derived
  // at render time (see `effectiveTargetApp` below), so there is no reconcile
  // effect and no state to sync as `users` loads asynchronously.
  const [targetApp, setTargetApp] = useState('');
  const [channel, setChannel] = useState<string>(strings.sendTestDefaultChannel);
  const [message, setMessage] = useState<string>(strings.sendTestDefaultMessage);
  const [selected, setSelected] = useState<Set<string>>(new Set());
  const [isSending, setIsSending] = useState(false);
  const [results, setResults] = useState<TestNotifyRecipientResult[] | null>(null);
  const [error, setError] = useState<string | null>(null);

  // Derived at render time: fall back to the first sorted option whenever the
  // admin hasn't chosen a still-valid app (initial empty state, or the chosen
  // app disappearing after a deprovision). This replaces the old default-app
  // effect and is what the <select> displays.
  const effectiveTargetApp = appOptions.includes(targetApp) ? targetApp : (appOptions[0] ?? '');

  const scopedUsers = users.filter(
    (user) => user.userId.startsWith('u_') && user.apps.includes(effectiveTargetApp),
  );

  // Derived at render time: the selected ids that are still scoped to the
  // effective app. This replaces the old stale-selection pruning effect — a
  // selection whose owner is no longer scoped (full removal or per-app
  // deprovision, or simply an app switch) is filtered out here rather than
  // mutated out of `selected` state.
  const effectivelySelected = new Set(
    scopedUsers.map((user) => user.userId).filter((userId) => selected.has(userId)),
  );

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
    if (effectivelySelected.size === 0) {
      return;
    }

    if (!isValidChannel(channel)) {
      setError(strings.sendTestInvalidChannel);
      return;
    }

    setError(null);
    setIsSending(true);
    const recipients = Array.from(effectivelySelected).map((userId) => userId.slice(2));
    onSendTest({ appId: effectiveTargetApp, recipients, channel, message })
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
              value={effectiveTargetApp}
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
                    <th
                      scope="col"
                      className="send-test__th-select"
                      aria-label={strings.sendTestSelectColumnAria}
                    />
                    <th scope="col">{strings.sendTestColumnUser}</th>
                    <th scope="col">{strings.sendTestColumnApp}</th>
                    <th scope="col">{strings.sendTestColumnTopic}</th>
                  </tr>
                </thead>
                <tbody>
                  {scopedUsers.map(({ userId }) => {
                    const personHash = userId.slice(2);
                    const displayName = emailByPersonHash.get(personHash) ?? userId;
                    const topic = `${effectiveTargetApp}-${personHash}-${channel}`;
                    const isSelected = effectivelySelected.has(userId);
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
                          <span className="send-test__chip">{effectiveTargetApp}</span>
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
                {strings.sendTestSelectedCount({ count: effectivelySelected.size })}
              </span>
              <span className="send-test__spacer" />
              <button
                type="submit"
                className="send-test__submit"
                disabled={isSending || effectivelySelected.size === 0}
              >
                {isSending ? strings.sendTestSending : strings.sendTestAction}
              </button>
            </div>

            {effectivelySelected.size === 0 && (
              <p className="send-test__hint">{strings.sendTestSelectHint}</p>
            )}
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
