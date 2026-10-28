import { useEffect, useRef, useState } from 'react';
import type { RefObject } from 'react';

import type { ProvisionAppResult } from '../api/client';
import { ApiError } from '../api/client';
import type { AppSummary, UpdateAppParams } from '../api/personClient';
import { strings } from '../strings';
import type { ConfirmStep } from './EditModal';
import './EditAppForm.css';

/** Props for EditAppForm. */
interface EditAppFormProps {
  app: AppSummary;
  onUpdateApp: (params: UpdateAppParams) => Promise<void>;
  onRemintToken: (params: { appId: string; rotate: boolean }) => Promise<ProvisionAppResult>;
  /** The dialog's guarded close (Close button, or an untouched Save). */
  onClose: () => void;
  /** Asks the dialog to show its in-dialog confirm step (Revoke & re-mint). */
  onRequestConfirm: (step: ConfirmStep) => void;
  /** Reports whether the form holds unsaved edits (false again on unmount). */
  onDirtyChange?: (dirty: boolean) => void;
  /** True while a Save or a (re-)mint is in flight; false again on unmount. */
  onBusyChange?: (busy: boolean) => void;
  /** True while a one-time token is displayed; false once cleared and on unmount. */
  onTokenShownChange?: (shown: boolean) => void;
  /** The dialog's initial-focus target: the display-name input. */
  firstFieldRef?: RefObject<HTMLInputElement | null>;
}

interface Baseline {
  displayName: string;
  description: string | null;
}

function normalised({
  displayName,
  description,
}: {
  displayName: string;
  description: string;
}): Baseline {
  const trimmedDescription = description.trim();
  return {
    displayName: displayName.trim(),
    description: trimmedDescription === '' ? null : trimmedDescription,
  };
}

/**
 * Edit form for a single app, rendered inside the shared edit dialog: edit
 * display_name/description metadata, plus the two publisher-token controls.
 * "Re-mint" is additive (zero-downtime — the old token stays valid); "Revoke &
 * re-mint" is a hard rotation gated behind the dialog's confirm step, because it
 * revokes the live token and breaks the app's publishing until it is redeployed.
 * Both reveal the new token inline (once).
 */
export function EditAppForm({
  app,
  onUpdateApp,
  onRemintToken,
  onClose,
  onRequestConfirm,
  onDirtyChange,
  onBusyChange,
  onTokenShownChange,
  firstFieldRef,
}: EditAppFormProps) {
  const [baseline, setBaseline] = useState<Baseline>(() =>
    normalised({ displayName: app.displayName, description: app.description ?? '' }),
  );
  const [displayName, setDisplayName] = useState(app.displayName);
  const [description, setDescription] = useState(app.description ?? '');
  const [error, setError] = useState<string | null>(null);
  const [savedMessage, setSavedMessage] = useState<string | null>(null);
  const [isSaving, setIsSaving] = useState(false);
  const [isMinting, setIsMinting] = useState(false);
  const [token, setToken] = useState<string | null>(null);
  // Read when a save resolves: whether the token is on screen then, not when Save was clicked.
  const tokenRef = useRef<string | null>(null);

  const current = normalised({ displayName, description });
  const dirty =
    current.displayName !== baseline.displayName || current.description !== baseline.description;
  const busy = isSaving || isMinting;

  const callbacks = useRef({ onDirtyChange, onBusyChange, onTokenShownChange });
  useEffect(() => {
    callbacks.current = { onDirtyChange, onBusyChange, onTokenShownChange };
  });
  useEffect(() => {
    callbacks.current.onDirtyChange?.(dirty);
  }, [dirty]);
  useEffect(() => {
    callbacks.current.onBusyChange?.(busy);
  }, [busy]);
  useEffect(
    () => () => {
      callbacks.current.onDirtyChange?.(false);
      callbacks.current.onBusyChange?.(false);
      callbacks.current.onTokenShownChange?.(false);
    },
    [],
  );

  function showToken(next: string | null) {
    tokenRef.current = next;
    setToken(next);
    onTokenShownChange?.(next !== null);
  }

  function handleSave(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault();
    // A save and a mint never overlap: either would close or replace the other's result.
    if (busy) {
      return;
    }

    if (displayName.trim() === '') {
      setError(strings.invalidDisplayName);
      return;
    }

    setError(null);
    setSavedMessage(null);
    // Nothing changed: close like the other forms, with no request and no result.
    if (!dirty) {
      onClose();
      return;
    }
    setIsSaving(true);
    const saved = normalised({ displayName, description });
    onUpdateApp({
      appId: app.appId,
      displayName: saved.displayName,
      description: saved.description,
    })
      .then(() => {
        // The saved values are the new baseline, so a dialog that stays open is clean again.
        setBaseline(saved);
        if (tokenRef.current !== null) {
          setSavedMessage(strings.appSavedResult({ appId: app.appId }));
        }
      })
      .catch((rejection: unknown) => {
        setError(rejection instanceof ApiError ? rejection.message : strings.genericError);
      })
      .finally(() => {
        setIsSaving(false);
      });
  }

  function mintToken(rotate: boolean) {
    if (busy) {
      return;
    }
    setError(null);
    setSavedMessage(null);
    showToken(null);
    setIsMinting(true);
    onRemintToken({ appId: app.appId, rotate })
      .then((result) => {
        showToken(result.token);
      })
      .catch((rejection: unknown) => {
        setError(rejection instanceof ApiError ? rejection.message : strings.genericError);
      })
      .finally(() => {
        setIsMinting(false);
      });
  }

  function requestRotate() {
    if (busy) {
      return;
    }
    onRequestConfirm({
      message: strings.rotateTokenWarning,
      confirmLabel: strings.confirmRotateTokenAction,
      cancelLabel: strings.cancelAction,
      onConfirm: () => mintToken(true),
      onCancel: () => undefined,
    });
  }

  return (
    <div className="edit-app-form">
      <form onSubmit={handleSave}>
        <label htmlFor={`edit-app-display-name-${app.appId}`}>{strings.appDisplayNameLabel}</label>
        <input
          ref={firstFieldRef}
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
          <button
            type="submit"
            className="edit-app-form__button"
            aria-disabled={busy ? 'true' : undefined}
          >
            {isSaving ? strings.savingApp : strings.saveAction}
          </button>
          <button
            type="button"
            className="edit-app-form__button"
            aria-disabled={busy ? 'true' : undefined}
            onClick={() => mintToken(false)}
          >
            {isMinting ? strings.minting : strings.remintTokenAction}
          </button>
          <button
            type="button"
            className="edit-app-form__button edit-app-form__button--danger"
            aria-disabled={busy ? 'true' : undefined}
            onClick={requestRotate}
          >
            {strings.rotateTokenAction}
          </button>
          <button
            type="button"
            className="edit-app-form__button edit-app-form__button--ghost"
            aria-disabled={busy ? 'true' : undefined}
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

        {savedMessage !== null && (
          <p role="status" className="edit-app-form__saved">
            {savedMessage}
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
