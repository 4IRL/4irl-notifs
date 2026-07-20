import { useState } from 'react';

import type { ProvisionParams, ProvisionResult } from '../api/client';
import { ApiError } from '../api/client';
import { strings } from '../strings';
import { isValidAppId, isValidEmail } from '../validation';
import './ProvisionForm.css';

/** Props for ProvisionForm. */
interface ProvisionFormProps {
  onProvision: (params: ProvisionParams) => Promise<ProvisionResult>;
}

/** Form for provisioning a user into an app, with inline validation and a token reveal on success. */
export function ProvisionForm({ onProvision }: ProvisionFormProps) {
  const [appId, setAppId] = useState('');
  const [email, setEmail] = useState('');
  const [error, setError] = useState<string | null>(null);
  const [isSubmitting, setIsSubmitting] = useState(false);
  const [result, setResult] = useState<ProvisionResult | null>(null);

  function handleSubmit(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault();

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
        <input
          id="provision-app-id"
          type="text"
          placeholder={strings.appIdPlaceholder}
          value={appId}
          onChange={(event) => setAppId(event.target.value)}
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

        {result !== null && (
          <div className="provision-form__token-reveal">
            <p>{strings.tokenRevealLead({ userId: result.userId, appId: result.appId })}</p>
            <p className="provision-form__token">{result.token}</p>
          </div>
        )}
      </form>
    </section>
  );
}
