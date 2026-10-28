import { useState } from 'react';

import type { ProvisionParams, ProvisionResult } from '../api/client';
import { ApiError } from '../api/client';
import type { AppSummary } from '../api/personClient';
import { useCopyStatus } from '../hooks/useCopyStatus';
import { strings } from '../strings';
import { isValidAppId, isValidEmail } from '../validation';
import { AppCombobox } from './AppCombobox';
import './ProvisionForm.css';

/** Props for ProvisionForm. `apps` feeds the App-ID combobox's suggestions;
 *  it defaults to empty so the field degrades to free text when the registry
 *  is unavailable (flag off / no personClient). */
interface ProvisionFormProps {
  onProvision: (params: ProvisionParams) => Promise<ProvisionResult>;
  apps?: AppSummary[];
}

/** Form for provisioning a user into an app, with inline validation and a token reveal on success. */
export function ProvisionForm({ onProvision, apps = [] }: ProvisionFormProps) {
  const [rawAppId, setAppId] = useState('');
  const [email, setEmail] = useState('');
  const [error, setError] = useState<string | null>(null);
  const [isSubmitting, setIsSubmitting] = useState(false);
  const [result, setResult] = useState<ProvisionResult | null>(null);
  const { status: copyStatus, copy: handleCopy, clear: clearCopyStatus } = useCopyStatus();
  const password = result?.password;

  function handleSubmit(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault();

    // Clear the previous password, token and copy status on every submit
    // attempt (including validation failures) and invalidate in-flight copies.
    setResult(null);
    clearCopyStatus();

    // Enter/Save mid-composition: submit what the field will show.
    const appId = rawAppId.toLowerCase();
    if (appId !== rawAppId) {
      setAppId(appId);
    }
    if (!isValidAppId(appId)) {
      setError(strings.invalidAppId);
      return;
    }

    if (!isValidEmail(email)) {
      setError(strings.invalidEmail);
      return;
    }

    setError(null);
    setIsSubmitting(true);
    onProvision({ appId, email })
      .then((provisionResult) => {
        setResult(provisionResult);
      })
      .catch((rejection: unknown) => {
        setResult(null);
        setError(rejection instanceof ApiError ? rejection.message : strings.genericError);
      })
      .finally(() => {
        setIsSubmitting(false);
      });
  }

  return (
    <section className="provision-form">
      <h2>{strings.provisionHeading}</h2>
      <form onSubmit={handleSubmit}>
        <label htmlFor="provision-app-id">{strings.appIdLabel}</label>
        <AppCombobox
          id="provision-app-id"
          value={rawAppId}
          onChange={setAppId}
          apps={apps}
          placeholder={strings.appIdPlaceholder}
          lowercase
        />

        <label htmlFor="provision-email">{strings.emailLabel}</label>
        <input
          id="provision-email"
          type="text"
          placeholder={strings.emailPlaceholder}
          value={email}
          onChange={(event) => setEmail(event.target.value)}
        />

        <button type="submit" disabled={isSubmitting}>
          {isSubmitting ? strings.provisioning : strings.provisionAction}
        </button>

        {error !== null && (
          <p role="alert" className="provision-form__error">
            {error}
          </p>
        )}

        <div role="status" aria-atomic="false" className="provision-form__status">
          {result !== null && result.password !== undefined && <p>{strings.passwordRevealLead}</p>}
          {result !== null && result.password === undefined && (
            <>
              <p>{strings.noPasswordReturned}</p>
              <p>{strings.noPasswordRecovery}</p>
            </>
          )}
          {copyStatus !== null && (
            <p
              key={copyStatus.count}
              className={
                copyStatus.kind === 'copied'
                  ? 'provision-form__copy-ok'
                  : 'provision-form__copy-fallback'
              }
            >
              {copyStatus.kind === 'copied'
                ? strings.passwordCopied
                : strings.passwordCopyUnavailable}
            </p>
          )}
        </div>

        {result !== null && (
          <div className="provision-form__token-reveal">
            {password !== undefined && (
              <div
                role="group"
                aria-labelledby="provision-password-heading"
                className="provision-form__password-group"
              >
                <h3 id="provision-password-heading">{strings.passwordGroupHeading}</h3>
                <label htmlFor="provision-password">{strings.passwordLabel}</label>
                <div className="provision-form__password-row">
                  <input
                    id="provision-password"
                    type="text"
                    readOnly
                    autoComplete="off"
                    spellCheck={false}
                    className="provision-form__password"
                    value={password}
                    onFocus={(event) => event.currentTarget.select()}
                  />
                  <button
                    type="button"
                    className="provision-form__copy"
                    onClick={() => handleCopy(password)}
                  >
                    {strings.copyPassword}
                  </button>
                </div>
              </div>
            )}
            <div
              role="group"
              aria-labelledby="provision-token-heading"
              className="provision-form__token-group"
            >
              <h3 id="provision-token-heading">{strings.tokenGroupHeading}</h3>
              <p>{strings.tokenRevealLead({ userId: result.userId, appId: result.appId })}</p>
              <p className="provision-form__token">{result.token}</p>
            </div>
          </div>
        )}
      </form>
    </section>
  );
}
