import { useEffect, useRef, useState } from 'react';
import type { FormEvent, JSX } from 'react';

import { ApiError } from '../api/client';
import type { DestinationSummary, RotateSecretResult } from '../api/deliveryClient';
import { strings } from '../strings';
import { ConfirmButton } from './ConfirmButton';
import { fieldsFor, validateField } from './destinationFields';
import './DestinationForm.css';

const OVERLAP_HOURS = [1, 24, 168] as const;
const DEFAULT_OVERLAP_HOURS = 24;
const PREVIOUS_EXPIRES_KEY = 'previous_secret_expires_at';
const SIGNING_SECRET_KEY = 'signing_secret';

/** The changed fields an edit sends; empty/omitted sections are not sent. */
export interface EditDestinationSubmission {
  config?: Record<string, string>;
  secrets?: Record<string, string>;
}

interface EditDestinationFormProps {
  destination: DestinationSummary;
  saving: boolean;
  rotating: boolean;
  onSave: (submission: EditDestinationSubmission) => Promise<void>;
  onRotate: (overlapHours: number) => Promise<RotateSecretResult>;
  /** Close the editor (Close button or an untouched Save). */
  onClose: () => void;
}

function previousSecretStillValid(expiresAt: string | undefined): boolean {
  if (expiresAt === undefined) {
    return false;
  }
  const parsed = Date.parse(expiresAt);
  return !Number.isNaN(parsed) && parsed > Date.now();
}

/**
 * Inline editor for one destination. Config fields are prefilled; secrets are
 * write-only (blank = keep, masked value only in the placeholder). The baseline
 * the changed-keys diff is computed against is snapshotted when the editor
 * opens and a list refresh never touches typed values. The single exception is
 * a successful Rotate: only untouched config fields and the baseline re-sync
 * from the returned destination, and the typed signing secret is cleared so a
 * later Save can never overwrite the freshly rotated secret.
 */
export function EditDestinationForm({
  destination,
  saving,
  rotating,
  onSave,
  onRotate,
  onClose,
}: EditDestinationFormProps): JSX.Element {
  const fields = fieldsFor(destination.type);
  const [baselineConfig, setBaselineConfig] = useState<Record<string, string>>(
    () => destination.config,
  );
  const [configValues, setConfigValues] = useState<Record<string, string>>(() => ({
    ...destination.config,
  }));
  const [secretValues, setSecretValues] = useState<Record<string, string>>({});
  const [overlapHours, setOverlapHours] = useState<number>(DEFAULT_OVERLAP_HOURS);
  const [error, setError] = useState<string | null>(null);
  const touchedConfig = useRef(new Set<string>());
  const firstFieldRef = useRef<HTMLInputElement | null>(null);

  useEffect(() => {
    firstFieldRef.current?.focus();
  }, []);

  function handleSave(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (saving) {
      return;
    }

    const changedConfig: Record<string, string> = {};
    const changedSecrets: Record<string, string> = {};
    for (const field of fields) {
      if (field.section === 'config') {
        const value = configValues[field.key] ?? '';
        if (value.trim() === (baselineConfig[field.key] ?? '')) {
          continue;
        }
        const problem = validateField({ field, value, required: true });
        if (problem !== null) {
          setError(problem);
          return;
        }
        changedConfig[field.key] = value.trim();
      } else {
        const value = secretValues[field.key] ?? '';
        if (value === '') {
          continue;
        }
        const problem = validateField({ field, value, required: false });
        if (problem !== null) {
          setError(problem);
          return;
        }
        changedSecrets[field.key] = value;
      }
    }

    const hasConfig = Object.keys(changedConfig).length > 0;
    const hasSecrets = Object.keys(changedSecrets).length > 0;
    setError(null);
    if (!hasConfig && !hasSecrets) {
      onClose();
      return;
    }
    onSave({
      config: hasConfig ? changedConfig : undefined,
      secrets: hasSecrets ? changedSecrets : undefined,
    }).catch((rejection: unknown) => {
      setError(rejection instanceof ApiError ? rejection.message : strings.genericError);
    });
  }

  function handleRotate() {
    setError(null);
    onRotate(overlapHours)
      .then((result) => {
        const nextConfig = result.destination.config;
        setBaselineConfig(nextConfig);
        setConfigValues((current) => {
          const resynced = { ...current };
          for (const field of fields) {
            if (field.section === 'config' && !touchedConfig.current.has(field.key)) {
              resynced[field.key] = nextConfig[field.key] ?? '';
            }
          }
          return resynced;
        });
        setSecretValues((current) => ({ ...current, [SIGNING_SECRET_KEY]: '' }));
      })
      .catch((rejection: unknown) => {
        setError(rejection instanceof ApiError ? rejection.message : strings.genericError);
      });
  }

  const isWebhook = destination.type === 'webhook';
  const invalidatesPrevious = previousSecretStillValid(baselineConfig[PREVIOUS_EXPIRES_KEY]);

  return (
    <div className="destination-form">
      <form
        aria-label={strings.editDestination({ name: destination.name })}
        onSubmit={handleSave}
        noValidate
      >
        <div className="destination-form__grid">
          {fields.map((field, index) => {
            const inputId = `edit-dest-${field.key}-${destination.name}`;
            const isSecret = field.section === 'secret';
            const masked = destination.secrets[field.key];
            return (
              <div key={field.key} className="destination-form__field">
                <label htmlFor={inputId}>{field.label}</label>
                <input
                  ref={index === 0 ? firstFieldRef : undefined}
                  id={inputId}
                  type={isSecret ? 'password' : 'text'}
                  autoComplete={isSecret ? 'new-password' : 'off'}
                  placeholder={
                    isSecret && masked !== undefined
                      ? `${masked} ${strings.destLeaveBlank}`
                      : undefined
                  }
                  value={
                    isSecret ? (secretValues[field.key] ?? '') : (configValues[field.key] ?? '')
                  }
                  onChange={(event) => {
                    const { value } = event.target;
                    if (isSecret) {
                      setSecretValues((current) => ({ ...current, [field.key]: value }));
                    } else {
                      touchedConfig.current.add(field.key);
                      setConfigValues((current) => ({ ...current, [field.key]: value }));
                    }
                  }}
                />
              </div>
            );
          })}
        </div>
        <p className="destination-form__hint">{strings.destEditSecretsHint}</p>

        <div className="destination-form__actions">
          <button
            type="submit"
            className="destination-form__button destination-form__button--primary"
            aria-disabled={saving ? 'true' : undefined}
          >
            {saving
              ? strings.savingDestination
              : strings.saveDestination({ name: destination.name })}
          </button>
          {isWebhook && (
            <>
              <label
                htmlFor={`rotate-overlap-${destination.name}`}
                className="destination-form__inline-label"
              >
                {strings.rotateOverlapLabel}
              </label>
              <select
                id={`rotate-overlap-${destination.name}`}
                className="destination-form__select"
                value={overlapHours}
                onChange={(event) => setOverlapHours(Number(event.target.value))}
              >
                {OVERLAP_HOURS.map((hours) => (
                  <option key={hours} value={hours}>
                    {strings.rotateOverlapOptions[hours]}
                  </option>
                ))}
              </select>
              <ConfirmButton
                triggerLabel={strings.rotateDestination({ name: destination.name })}
                confirmLabel={strings.confirmRotateDestination({ name: destination.name })}
                className="destination-form__button destination-form__button--danger"
                busy={rotating}
                busyLabel={strings.rotatingDestination}
                confirmNotice={invalidatesPrevious ? strings.rotateInvalidatesPrevious : undefined}
                onConfirm={handleRotate}
              />
            </>
          )}
          <button
            type="button"
            className="destination-form__button destination-form__button--ghost"
            onClick={onClose}
          >
            {strings.closeEditAction}
          </button>
        </div>
        {isWebhook && <p className="destination-form__hint">{strings.rotateHint}</p>}

        {error !== null && (
          <p role="alert" className="destination-form__error">
            {error}
          </p>
        )}
      </form>
    </div>
  );
}
