import type { JSX, Ref } from 'react';

import { strings } from '../strings';
import { describedBy, fieldErrorId } from './fieldErrors';
import './DestinationForm.css';

interface ProbeCredentialsFieldProps {
  idSuffix: string;
  clientId: string;
  clientSecret: string;
  onClientIdChange: (value: string) => void;
  onClientSecretChange: (value: string) => void;
  clientIdRef: Ref<HTMLInputElement>;
  clientSecretRef: Ref<HTMLInputElement>;
  clientIdError: string | undefined;
  clientSecretError: string | undefined;
  hint: string;
  /** The form's save-in-flight flag; the inputs are read-only while it is set. */
  inFlight: boolean;
}

/**
 * The optional Cloudflare Access credential pair. Fully controlled: the form holds
 * the typed values and errors so it can validate, focus and submit them.
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
}: ProbeCredentialsFieldProps): JSX.Element {
  const captionId = `heartbeat-credentials-caption-${idSuffix}`;
  const clientIdFieldId = `heartbeat-credentials-client-id-${idSuffix}`;
  const clientSecretFieldId = `heartbeat-credentials-client-secret-${idSuffix}`;
  const hintId = `heartbeat-credentials-hint-${idSuffix}`;
  return (
    <div
      className="heartbeat-form__creds destination-form__grid"
      role="group"
      aria-labelledby={captionId}
    >
      <span id={captionId} className="destination-form__inline-label">
        {strings.heartbeatCredentialsCaption}
      </span>
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
          aria-describedby={describedBy({
            fieldId: clientIdFieldId,
            hasHint: true,
            error: clientIdError,
            hintId,
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
        <label htmlFor={clientSecretFieldId}>{strings.heartbeatCredentialsClientSecretLabel}</label>
        <input
          ref={clientSecretRef}
          id={clientSecretFieldId}
          type="password"
          autoComplete="new-password"
          readOnly={inFlight}
          value={clientSecret}
          aria-invalid={clientSecretError ? 'true' : undefined}
          aria-describedby={describedBy({
            fieldId: clientSecretFieldId,
            hasHint: true,
            error: clientSecretError,
            hintId,
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
      <span id={hintId} className="destination-form__hint">
        {hint}
      </span>
    </div>
  );
}
