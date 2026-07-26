import { useState } from 'react';

import type { ProvisionAppResult } from '../api/client';
import { ApiError } from '../api/client';
import type { AppSummary, UpdateAppParams } from '../api/personClient';
import { strings } from '../strings';
import { ConfirmButton } from './ConfirmButton';
import './EditAppForm.css';

/** Props for EditAppForm. */
interface EditAppFormProps {
  app: AppSummary;
  onUpdateApp: (params: UpdateAppParams) => Promise<void>;
  onRemintToken: (params: { appId: string; rotate: boolean }) => Promise<ProvisionAppResult>;
  onClose: () => void;
}

/**
 * Inline editor for a single app: edit display_name/description metadata, plus
 * the two publisher-token controls. "Re-mint" is additive (zero-downtime — the
 * old token stays valid); "Revoke & re-mint" is a hard rotation gated behind an
 * inline confirm, because it revokes the live token and breaks the app's
 * publishing until it is redeployed. Both reveal the new token inline (once).
 */
export function EditAppForm({ app, onUpdateApp, onRemintToken, onClose }: EditAppFormProps) {
  const [displayName, setDisplayName] = useState(app.displayName);
  const [description, setDescription] = useState(app.description ?? '');
  const [error, setError] = useState<string | null>(null);
  const [isSaving, setIsSaving] = useState(false);
  const [isMinting, setIsMinting] = useState(false);
  const [token, setToken] = useState<string | null>(null);

  function handleSave(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault();

    if (displayName.trim() === '') {
      setError(strings.invalidDisplayName);
      return;
    }

    setError(null);
    setIsSaving(true);
    const trimmedDescription = description.trim();
    onUpdateApp({
      appId: app.appId,
      displayName: displayName.trim(),
      description: trimmedDescription === '' ? null : trimmedDescription,
    })
      .catch((rejection: unknown) => {
        setError(rejection instanceof ApiError ? rejection.message : strings.genericError);
      })
      .finally(() => {
        setIsSaving(false);
      });
  }

  function mintToken(rotate: boolean) {
    setError(null);
    setToken(null);
    setIsMinting(true);
    onRemintToken({ appId: app.appId, rotate })
      .then((result) => {
        setToken(result.token);
      })
      .catch((rejection: unknown) => {
        setError(rejection instanceof ApiError ? rejection.message : strings.genericError);
      })
      .finally(() => {
        setIsMinting(false);
      });
  }

  return (
    <div className="edit-app-form">
      <form onSubmit={handleSave}>
        <label htmlFor={`edit-app-display-name-${app.appId}`}>{strings.appDisplayNameLabel}</label>
        <input
          id={`edit-app-display-name-${app.appId}`}
          type="text"
          value={displayName}
          onChange={(event) => setDisplayName(event.target.value)}
        />

        <label htmlFor={`edit-app-description-${app.appId}`}>{strings.appDescriptionLabel}</label>
        <input
          id={`edit-app-description-${app.appId}`}
          type="text"
          placeholder={strings.appDescriptionPlaceholder}
          value={description}
          onChange={(event) => setDescription(event.target.value)}
        />

        <div className="edit-app-form__actions">
          <button type="submit" className="edit-app-form__button" disabled={isSaving}>
            {isSaving ? strings.savingApp : strings.saveAction}
          </button>
          <button
            type="button"
            className="edit-app-form__button"
            disabled={isMinting}
            onClick={() => mintToken(false)}
          >
            {isMinting ? strings.minting : strings.remintTokenAction}
          </button>
          <ConfirmButton
            triggerLabel={strings.rotateTokenAction}
            confirmLabel={strings.confirmRotateTokenAction}
            className="edit-app-form__button edit-app-form__button--danger"
            onConfirm={() => mintToken(true)}
          />
          <button
            type="button"
            className="edit-app-form__button edit-app-form__button--ghost"
            onClick={onClose}
          >
            {strings.closeEditAction}
          </button>
        </div>

        <p className="edit-app-form__hint">{strings.rotateTokenWarning}</p>

        {error !== null && (
          <p role="alert" className="edit-app-form__error">
            {error}
          </p>
        )}

        {token !== null && (
          <div className="edit-app-form__token-reveal">
            <p>{strings.publisherTokenRevealLead({ appId: app.appId })}</p>
            <p className="edit-app-form__token">{token}</p>
          </div>
        )}
      </form>
    </div>
  );
}
