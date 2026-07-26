import { useState } from 'react';

import type { ProvisionAppResult } from '../api/client';
import { ApiError } from '../api/client';
import type { CreateAppParams } from '../api/personClient';
import { strings } from '../strings';
import { isValidAppId } from '../validation';
import './AddAppForm.css';

/** Props for AddAppForm. onAddApp registers the app and mints its publisher
 *  token, returning the reveal-once result. */
interface AddAppFormProps {
  onAddApp: (params: CreateAppParams) => Promise<ProvisionAppResult>;
}

/**
 * Form for registering a new app. On success it reveals the app's publisher
 * token inline (shown once) — the same reveal pattern as ProvisionForm, no
 * modal. Registering an app both writes the registry row and mints the
 * publisher identity (see App.handleAddApp).
 */
export function AddAppForm({ onAddApp }: AddAppFormProps) {
  const [appId, setAppId] = useState('');
  const [displayName, setDisplayName] = useState('');
  const [description, setDescription] = useState('');
  const [error, setError] = useState<string | null>(null);
  const [isSubmitting, setIsSubmitting] = useState(false);
  const [result, setResult] = useState<ProvisionAppResult | null>(null);

  function handleSubmit(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault();

    if (!isValidAppId(appId)) {
      setError(strings.invalidAppId);
      return;
    }
    if (displayName.trim() === '') {
      setError(strings.invalidDisplayName);
      return;
    }

    setError(null);
    setIsSubmitting(true);
    const trimmedDescription = description.trim();
    onAddApp({
      appId,
      displayName: displayName.trim(),
      description: trimmedDescription === '' ? undefined : trimmedDescription,
    })
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
    <section className="add-app-form">
      <h2>{strings.addAppHeading}</h2>
      <form onSubmit={handleSubmit}>
        <label htmlFor="add-app-id">{strings.appIdLabel}</label>
        <input
          id="add-app-id"
          type="text"
          placeholder={strings.appIdPlaceholder}
          value={appId}
          onChange={(event) => setAppId(event.target.value)}
        />

        <label htmlFor="add-app-display-name">{strings.appDisplayNameLabel}</label>
        <input
          id="add-app-display-name"
          type="text"
          placeholder={strings.appDisplayNamePlaceholder}
          value={displayName}
          onChange={(event) => setDisplayName(event.target.value)}
        />

        <label htmlFor="add-app-description">{strings.appDescriptionLabel}</label>
        <input
          id="add-app-description"
          type="text"
          placeholder={strings.appDescriptionPlaceholder}
          value={description}
          onChange={(event) => setDescription(event.target.value)}
        />

        <button type="submit" disabled={isSubmitting}>
          {isSubmitting ? strings.addingApp : strings.addAppAction}
        </button>

        {error !== null && (
          <p role="alert" className="add-app-form__error">
            {error}
          </p>
        )}

        {result !== null && (
          <div className="add-app-form__token-reveal">
            <p>{strings.publisherTokenRevealLead({ appId: result.appId })}</p>
            <p className="add-app-form__token">{result.token}</p>
          </div>
        )}
      </form>
    </section>
  );
}
