import { useEffect, useRef } from 'react';
import type { JSX, RefObject } from 'react';

import { strings } from '../strings';
import { fieldErrorId } from './fieldErrors';
import './DestinationForm.css';

/** What the operator has staged for already-stored credentials (nothing is sent until Save). */
export type CredentialsStaged = 'untouched' | 'replacing' | 'clearing';

interface ProbeCredentialsFieldProps {
  idSuffix: string;
  clientId: string;
  clientSecret: string;
  onClientIdChange: (value: string) => void;
  onClientSecretChange: (value: string) => void;
  clientIdRef: RefObject<HTMLInputElement | null>;
  clientSecretRef: RefObject<HTMLInputElement | null>;
  clientIdError: string | undefined;
  clientSecretError: string | undefined;
  /** Shown in the pair-only mode (no stored credentials). */
  hint: string;
  /** The form's save-in-flight flag; the inputs are read-only and the controls inert while set. */
  inFlight: boolean;
  /** True when the server holds an Access token for this check (edit only). */
  hasStoredCredentials?: boolean;
  /** The staged mode, owned by the form. Only meaningful with `hasStoredCredentials`. */
  staged?: CredentialsStaged;
  onStagedChange?: (next: CredentialsStaged) => void;
  /** A one-off suggestion chosen by the form at mount (for example "Use Replace"). */
  stateHint?: string | null;
}

function describedByIds({
  ids,
  error,
  fieldId,
}: {
  ids: Array<string | null>;
  error: string | undefined;
  fieldId: string;
}): string | undefined {
  const all = ids.filter((id) => id !== null);
  if (error) {
    all.push(fieldErrorId({ fieldId }));
  }
  return all.length > 0 ? all.join(' ') : undefined;
}

/**
 * The optional Cloudflare Access credential pair. Fully controlled: the form holds
 * the typed values, the staged mode and the errors so it can validate, focus and submit them.
 * With stored credentials it shows Replace / Clear (and Cancel replace / Undo); the stored
 * value is never shown.
 */
export function ProbeCredentialsField({
  idSuffix,
  clientId,
  clientSecret,
  onClientIdChange,
  onClientSecretChange,
  clientIdRef,
  clientSecretRef,
  clientIdError,
  clientSecretError,
  hint,
  inFlight,
  hasStoredCredentials = false,
  staged = 'untouched',
  onStagedChange,
  stateHint = null,
}: ProbeCredentialsFieldProps): JSX.Element {
  const captionId = `heartbeat-credentials-caption-${idSuffix}`;
  const statusId = `heartbeat-credentials-status-${idSuffix}`;
  const clientIdFieldId = `heartbeat-credentials-client-id-${idSuffix}`;
  const clientSecretFieldId = `heartbeat-credentials-client-secret-${idSuffix}`;
  const hintId = `heartbeat-credentials-hint-${idSuffix}`;
  const stateHintId = `heartbeat-credentials-state-hint-${idSuffix}`;
  const replaceRef = useRef<HTMLButtonElement | null>(null);
  const clearRef = useRef<HTMLButtonElement | null>(null);
  const undoRef = useRef<HTMLButtonElement | null>(null);
  const previousStaged = useRef<CredentialsStaged>(staged);

  // Focus follows a transition only; the mount (and any re-render) leaves focus alone.
  useEffect(() => {
    const previous = previousStaged.current;
    previousStaged.current = staged;
    if (!hasStoredCredentials || previous === staged) {
      return;
    }
    if (staged === 'replacing') {
      clientIdRef.current?.focus();
    } else if (staged === 'clearing') {
      undoRef.current?.focus();
    } else if (previous === 'replacing') {
      replaceRef.current?.focus();
    } else {
      clearRef.current?.focus();
    }
  }, [staged, hasStoredCredentials, clientIdRef]);

  function stage(next: CredentialsStaged) {
    if (inFlight) {
      return;
    }
    onStagedChange?.(next);
  }

  const showInputs = !hasStoredCredentials || staged === 'replacing';
  const showStateHint = stateHint !== null && staged !== 'clearing';
  const describedIds = [!hasStoredCredentials ? hintId : null, showStateHint ? stateHintId : null];
  const disabledAttr = inFlight ? 'true' : undefined;

  let statusText: string = strings.heartbeatCredentialsStored;
  if (staged === 'replacing') {
    statusText = strings.heartbeatCredentialsReplacing;
  } else if (staged === 'clearing') {
    statusText = strings.heartbeatCredentialsClearing;
  }

  return (
    <div
      className="heartbeat-form__creds destination-form__grid"
      role="group"
      aria-labelledby={hasStoredCredentials ? statusId : captionId}
    >
      {hasStoredCredentials ? (
        <>
          <p
            id={statusId}
            role="status"
            className={
              staged === 'clearing'
                ? 'heartbeat-form__creds-state heartbeat-form__creds-state--clearing'
                : 'heartbeat-form__creds-state'
            }
          >
            {statusText}
          </p>
          <div className="heartbeat-form__creds-actions">
            {staged === 'untouched' && (
              <>
                <button
                  ref={replaceRef}
                  type="button"
                  className="destination-form__button"
                  aria-disabled={disabledAttr}
                  onClick={() => stage('replacing')}
                >
                  {strings.heartbeatCredentialsReplace}
                </button>
                <button
                  ref={clearRef}
                  type="button"
                  className="destination-form__button destination-form__button--danger"
                  aria-disabled={disabledAttr}
                  onClick={() => stage('clearing')}
                >
                  {strings.heartbeatCredentialsClear}
                </button>
              </>
            )}
            {staged === 'replacing' && (
              <button
                type="button"
                className="destination-form__button destination-form__button--ghost"
                aria-disabled={disabledAttr}
                onClick={() => stage('untouched')}
              >
                {strings.heartbeatCredentialsCancelReplace}
              </button>
            )}
            {staged === 'clearing' && (
              <button
                ref={undoRef}
                type="button"
                className="destination-form__button destination-form__button--ghost"
                aria-disabled={disabledAttr}
                onClick={() => stage('untouched')}
              >
                {strings.heartbeatCredentialsUndo}
              </button>
            )}
          </div>
        </>
      ) : (
        <span id={captionId} className="destination-form__inline-label">
          {strings.heartbeatCredentialsCaption}
        </span>
      )}
      {showInputs && (
        <>
          <div className="destination-form__field">
            <label htmlFor={clientIdFieldId}>{strings.heartbeatCredentialsClientIdLabel}</label>
            <input
              ref={clientIdRef}
              id={clientIdFieldId}
              type="text"
              autoComplete="off"
              readOnly={inFlight}
              value={clientId}
              aria-invalid={clientIdError ? 'true' : undefined}
              aria-describedby={describedByIds({
                ids: describedIds,
                error: clientIdError,
                fieldId: clientIdFieldId,
              })}
              onChange={(event) => onClientIdChange(event.target.value)}
            />
            {clientIdError && (
              <span
                id={fieldErrorId({ fieldId: clientIdFieldId })}
                className="destination-form__field-error"
              >
                {clientIdError}
              </span>
            )}
          </div>
          <div className="destination-form__field">
            <label htmlFor={clientSecretFieldId}>
              {strings.heartbeatCredentialsClientSecretLabel}
            </label>
            <input
              ref={clientSecretRef}
              id={clientSecretFieldId}
              type="password"
              autoComplete="new-password"
              readOnly={inFlight}
              value={clientSecret}
              aria-invalid={clientSecretError ? 'true' : undefined}
              aria-describedby={describedByIds({
                ids: describedIds,
                error: clientSecretError,
                fieldId: clientSecretFieldId,
              })}
              onChange={(event) => onClientSecretChange(event.target.value)}
            />
            {clientSecretError && (
              <span
                id={fieldErrorId({ fieldId: clientSecretFieldId })}
                className="destination-form__field-error"
              >
                {clientSecretError}
              </span>
            )}
          </div>
        </>
      )}
      {!hasStoredCredentials && (
        <span id={hintId} className="destination-form__hint">
          {hint}
        </span>
      )}
      {showStateHint && (
        <span id={stateHintId} className="destination-form__hint">
          {stateHint}
        </span>
      )}
    </div>
  );
}
