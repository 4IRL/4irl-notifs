import { useState } from 'react';

import type { ProvisionAppResult } from '../api/client';
import { ApiError } from '../api/client';
import type { CreateAppParams } from '../api/personClient';
import { useLowercaseInput } from '../hooks/useLowercaseInput';
import { strings } from '../strings';
import { isValidAppId } from '../validation';
import './AddAppForm.css';

/** Props for AddAppForm. onAddApp mints the app's publisher token (never
 *  returned) and registers the app. */
interface AddAppFormProps {
  onAddApp: (params: CreateAppParams) => Promise<AddAppResult>;
}

/** ProvisionAppResult plus a flag set when the app row already existed. */
export type AddAppResult = ProvisionAppResult & { alreadyRegistered?: boolean };

/**
 * Form for registering a new app. On success it shows an inline confirmation
 * that the publisher token was minted (the token itself is never shown).
 * Adding an app mints the publisher identity first, then writes the registry
 * row (see App.handleAddApp).
 */
export function AddAppForm({ onAddApp }: AddAppFormProps) {
  const [rawAppId, setAppId] = useState('');
  const appIdInput = useLowercaseInput({ setValue: setAppId });
  const [displayName, setDisplayName] = useState('');
  const [description, setDescription] = useState('');
  const [error, setError] = useState<string | null>(null);
  const [isSubmitting, setIsSubmitting] = useState(false);
  const [result, setResult] = useState<AddAppResult | null>(null);

  function handleSubmit(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault();
    // A validation error must never sit beside a previous success.
    setResult(null);

    // Enter/Save mid-composition: submit what the field will show.
    const appId = rawAppId.toLowerCase();
    if (appId !== rawAppId) {
      setAppId(appId);
    }
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
          value={rawAppId}
          autoCapitalize="none"
          autoCorrect="off"
          spellCheck={false}
          {...appIdInput}
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

        {/* Always mounted so screen readers announce the text when it is filled. */}
        <div role="status" aria-atomic="false" className="add-app-form__status">
          {result !== null && <p>{strings.publisherMintedResult({ appId: result.appId })}</p>}
          {result?.alreadyRegistered === true && <p>{strings.appAlreadyRegisteredNote}</p>}
        </div>
      </form>
    </section>
  );
}
